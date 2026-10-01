package matrix

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/testutil"
	"maunium.net/go/mautrix/crypto/backup"
	"maunium.net/go/mautrix/crypto/goolm/session"
	"maunium.net/go/mautrix/crypto/ssss"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func TestClientLoginRecoveryEncryptedArchiveAndReopen(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		t.Run(fmt.Sprintf("encrypted=%t", encrypted), func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			ctx := context.Background()
			ssKey, err := ssss.NewKey("")
			require.NoError(err)
			backupKey, err := backup.NewMegolmBackupKey()
			require.NoError(err)
			outbound, err := session.NewMegolmOutboundSession()
			require.NoError(err)
			inbound, err := session.NewMegolmInboundSession([]byte(outbound.Key()))
			require.NoError(err)
			exported, err := inbound.Export(0)
			require.NoError(err)
			keyData := backup.MegolmSessionData{Algorithm: id.AlgorithmMegolmV1, SessionKey: string(exported),
				SenderKey:         id.SenderKey(base64.RawStdEncoding.EncodeToString(make([]byte, 32))),
				SenderClaimedKeys: backup.SenderClaimedKeys{Ed25519: id.Ed25519(base64.RawStdEncoding.EncodeToString(make([]byte, 32)))}}
			encryptedKey, err := backup.EncryptSessionData(backupKey, keyData)
			require.NoError(err)
			ciphertext, err := outbound.Encrypt([]byte(`{"room_id":"!chat:example.com","type":"m.room.message","content":{"msgtype":"m.text","body":"SDK archive integration"}}`))
			require.NoError(err)
			var mu sync.Mutex
			var deviceKeys any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				write := func(value any) { _ = json.MarshalWrite(w, value) }
				switch {
				case r.URL.Path == "/_matrix/client/versions":
					_, _ = fmt.Fprint(w, `{"versions":["v1.11"],"unstable_features":{}}`)
				case strings.HasSuffix(r.URL.Path, "/login"):
					_, _ = fmt.Fprint(w, `{"user_id":"@alice:example.com","device_id":"ARCHIVE_TEST","access_token":"synthetic-test-token"}`)
				case strings.HasSuffix(r.URL.Path, "/keys/upload"):
					var upload map[string]any
					if err := json.UnmarshalRead(r.Body, &upload); err != nil {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					mu.Lock()
					if upload["device_keys"] != nil {
						deviceKeys = upload["device_keys"]
					}
					mu.Unlock()
					_, _ = fmt.Fprint(w, `{"one_time_key_counts":{"signed_curve25519":50}}`)
				case strings.HasSuffix(r.URL.Path, "/keys/query"):
					mu.Lock()
					defer mu.Unlock()
					keys := map[string]any{}
					if deviceKeys != nil {
						keys[testAccount.UserID] = map[string]any{"ARCHIVE_TEST": deviceKeys}
					}
					write(map[string]any{"device_keys": keys, "failures": map[string]any{}})
				case strings.HasSuffix(r.URL.Path, "/sync"):
					_, _ = fmt.Fprint(w, `{"next_batch":"sync-next","rooms":{"join":{"!chat:example.com":{"state":{"events":[]},"timeline":{"events":[],"limited":false,"prev_batch":"head"}}}}}`)
				case strings.HasSuffix(r.URL.Path, "/joined_rooms"):
					write(map[string]any{"joined_rooms": []string{testRoom}})
				case strings.Contains(r.URL.Path, "/state/m.room.name"):
					_, _ = fmt.Fprint(w, `{"name":"Example chat"}`)
				case strings.HasSuffix(r.URL.Path, "/messages"):
					if r.URL.Query().Get("dir") == "f" {
						_, _ = fmt.Fprint(w, `{"start":"head","chunk":[],"state":[]}`)
					} else if encrypted {
						write(map[string]any{"start": "head", "chunk": []any{map[string]any{
							"type": "m.room.encrypted", "event_id": "$sdk", "sender": testAccount.UserID,
							"extra_provider_field": "preserved",
							"origin_server_ts":     1704067200123, "content": map[string]any{"algorithm": id.AlgorithmMegolmV1,
								"ciphertext": string(ciphertext), "session_id": outbound.ID(), "sender_key": keyData.SenderKey}}}})
					} else {
						_, _ = fmt.Fprintf(w, `{"start":"head","chunk":[%s],"state":[]}`, textEvent("$sdk", testAccount.UserID, "SDK archive integration"))
					}
				case strings.HasSuffix(r.URL.Path, "/account_data/m.secret_storage.default_key"):
					write(map[string]any{"key": ssKey.ID})
				case strings.Contains(r.URL.Path, "/account_data/m.secret_storage.key."):
					write(ssKey.Metadata)
				case strings.HasSuffix(r.URL.Path, "/account_data/m.megolm_backup.v1"):
					write(map[string]any{"encrypted": map[string]any{ssKey.ID: ssKey.Encrypt(event.AccountDataMegolmBackupKey.Type, backupKey.Bytes())}})
				case strings.HasSuffix(r.URL.Path, "/room_keys/version"):
					write(map[string]any{"algorithm": id.KeyBackupAlgorithmMegolmBackupV1, "version": "1", "count": 1, "etag": "test",
						"auth_data": map[string]any{"public_key": base64.RawStdEncoding.EncodeToString(backupKey.PublicKey().Bytes())}})
				case strings.HasSuffix(r.URL.Path, "/room_keys/keys"):
					write(map[string]any{"rooms": map[string]any{testRoom: map[string]any{"sessions": map[string]any{outbound.ID().String(): map[string]any{
						"first_message_index": 0, "forwarded_count": 0, "is_verified": true, "session_data": encryptedKey}}}}})
				default:
					w.WriteHeader(http.StatusNotFound)
					_, _ = fmt.Fprint(w, `{"errcode":"M_NOT_FOUND","error":"unsupported synthetic endpoint"}`)
				}
			}))
			t.Cleanup(server.Close)
			dir := t.TempDir()
			client, err := Login(ctx, dir, Account{Homeserver: server.URL, UserID: testAccount.UserID}, "synthetic-password")
			require.NoError(err)
			t.Cleanup(func() { _ = client.Close() })
			other, err := NewClient(ctx, dir)
			if other != nil {
				_ = other.Close()
			}
			require.ErrorContains(err, "already in use")
			require.NoError(client.SyncKeys(ctx))
			rooms, err := client.Rooms(ctx)
			require.NoError(err)
			assert.Equal([]Room{{RoomID: testRoom, Name: "Example chat"}}, rooms)
			st := testutil.NewTestStore(t)
			if encrypted {
				_, err := Import(ctx, st, client.Account(), client.Page, ImportOptions{RoomIDs: []string{testRoom}})
				require.ErrorContains(err, "undecryptable")
				_, err = client.Recover(ctx, "invalid recovery key")
				require.Error(err)
				count, err := client.Recover(ctx, ssKey.RecoveryKey())
				require.NoError(err)
				assert.Equal(1, count)
			}
			sum, err := Import(ctx, st, client.Account(), client.Page, ImportOptions{RoomIDs: []string{testRoom}})
			require.NoError(err)
			assert.EqualValues(1, sum.MessagesAdded)
			assert.True(sum.HistoryComplete)
			require.NoError(client.Close())
			client, err = NewClient(ctx, dir)
			require.NoError(err)
			_, err = Import(ctx, st, client.Account(), client.Page, ImportOptions{RoomIDs: []string{testRoom}, Full: true})
			require.NoError(err)
			results, total, err := st.SearchMessages("SDK archive integration", 0, 10)
			require.NoError(err)
			assert.EqualValues(1, total)
			require.Len(results, 1)
			assert.Equal("$sdk", results[0].SourceMessageID)
			raw, err := st.GetMessageRaw(results[0].ID)
			require.NoError(err)
			if encrypted {
				var evidence map[string]any
				require.NoError(json.Unmarshal(raw, &evidence))
				assert.Equal("m.room.message", evidence["type"])
				assert.Equal("preserved", evidence["extra_provider_field"])
				original, ok := evidence["msgvault_encrypted_event"].(map[string]any)
				require.True(ok)
				assert.Equal("m.room.encrypted", original["type"])
			}
			for _, file := range []string{"store.key", "session.json", "crypto.db"} {
				info, err := os.Stat(filepath.Join(dir, file))
				require.NoError(err)
				if runtime.GOOS != "windows" {
					assert.Equal(os.FileMode(0o600), info.Mode().Perm())
				}
			}
			_, err = client.Page(ctx, PageRequest{RoomID: "!other:example.com", Direction: "b", Limit: 100})
			require.ErrorContains(err, "not joined")
		})
	}
}
