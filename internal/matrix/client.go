package matrix

import (
	"context"
	"crypto/rand"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/rs/zerolog"
	"go.kenn.io/msgvault/internal/fileutil"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type savedSession struct {
	Homeserver  string `json:"homeserver"`
	UserID      string `json:"user_id"`
	DeviceID    string `json:"device_id"`
	AccessToken string `json:"access_token"`
}

// Client owns the account's dedicated device and persistent encryption state.
// Archive pagination remains in the importer, independently of crypto sync.
type Client struct {
	account Account
	sdk     *mautrix.Client
	crypto  *cryptohelper.CryptoHelper
	lock    *flock.Flock
}

func lockState(dataDir string) (*flock.Flock, error) {
	if err := fileutil.SecureMkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create Matrix state directory: %w", err)
	}
	if err := fileutil.SecureChmod(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("protect Matrix state directory: %w", err)
	}
	lock := flock.New(filepath.Join(dataDir, "session.lock"), flock.SetPermissions(0o600))
	locked, err := lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("lock Matrix state: %w", err)
	}
	if !locked {
		return nil, errors.New("matrix session is already in use; retry after the current command finishes")
	}
	return lock, nil
}

func loadSession(dataDir string) (savedSession, error) {
	data, err := os.ReadFile(filepath.Join(dataDir, "session.json"))
	if err != nil {
		return savedSession{}, fmt.Errorf("load Matrix session (run add-matrix first): %w", err)
	}
	var saved savedSession
	if err := json.Unmarshal(data, &saved); err != nil {
		return savedSession{}, fmt.Errorf("decode Matrix session: %w", err)
	}
	if err := (Account{Homeserver: saved.Homeserver, UserID: saved.UserID}).validate(); err != nil {
		return savedSession{}, err
	}
	if saved.DeviceID == "" || saved.AccessToken == "" {
		return savedSession{}, errors.New("matrix session has no device ID or access token")
	}
	return saved, nil
}

func newSDK(account Account, token string) (*mautrix.Client, error) {
	if err := account.validate(); err != nil {
		return nil, err
	}
	sdk, err := mautrix.NewClient(account.Homeserver, id.UserID(account.UserID), token)
	if err != nil {
		return nil, fmt.Errorf("create Matrix client: %w", err)
	}
	sdk.Client = &http.Client{Timeout: 45 * time.Second}
	sdk.DefaultHTTPRetries = 0
	sdk.Log = zerolog.Nop()
	return sdk, nil
}

func NewClient(ctx context.Context, dataDir string) (client *Client, err error) {
	lock, err := lockState(dataDir)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, lock.Unlock())
		}
	}()
	if err := fileutil.SecureChmod(filepath.Join(dataDir, "session.json"), 0o600); err != nil {
		return nil, fmt.Errorf("protect Matrix session (run add-matrix first): %w", err)
	}
	saved, err := loadSession(dataDir)
	if err != nil {
		return nil, err
	}
	account := Account{Homeserver: saved.Homeserver, UserID: saved.UserID}
	sdk, err := newSDK(account, saved.AccessToken)
	if err != nil {
		return nil, err
	}
	sdk.DeviceID = id.DeviceID(saved.DeviceID)
	client, err = initializeCrypto(ctx, dataDir, account, sdk)
	if err == nil {
		client.lock = lock
	}
	return client, err
}

func initializeCrypto(ctx context.Context, dataDir string, account Account, sdk *mautrix.Client) (*Client, error) {
	if err := fileutil.SecureMkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create Matrix state directory: %w", err)
	}
	keyPath := filepath.Join(dataDir, "store.key")
	key, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err = rand.Read(key); err == nil {
			err = fileutil.SecureReplaceFile(keyPath, key, 0o600)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("load Matrix crypto-store key: %w", err)
	}
	if len(key) != 32 {
		return nil, errors.New("matrix crypto-store key has invalid length")
	}
	if err := fileutil.SecureChmod(keyPath, 0o600); err != nil {
		return nil, fmt.Errorf("protect Matrix crypto-store key: %w", err)
	}
	dbPath := filepath.Join(dataDir, "crypto.db")
	file, err := fileutil.SecureOpenFile(dbPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("protect Matrix crypto store: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close Matrix crypto-store file: %w", err)
	}
	if err := fileutil.SecureChmod(dbPath, 0o600); err != nil {
		return nil, fmt.Errorf("protect Matrix crypto store: %w", err)
	}
	helper, err := cryptohelper.NewCryptoHelper(sdk, key, dbPath)
	if err != nil {
		return nil, fmt.Errorf("create Matrix encryption client: %w", err)
	}
	if err := helper.Init(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("initialize Matrix encryption: %w", err), helper.Close())
	}
	return &Client{account: account, sdk: sdk, crypto: helper}, nil
}

func (c *Client) Close() error {
	if err := errors.Join(c.crypto.Close(), c.lock.Unlock()); err != nil {
		return fmt.Errorf("close Matrix crypto store: %w", err)
	}
	return nil
}

func (c *Client) Account() Account {
	return c.account
}

// Login reuses the dedicated device on reauthentication. Switching account
// identity in one crypto directory is forbidden; use another archive instead.
func Login(ctx context.Context, dataDir string, account Account, password string) (client *Client, err error) {
	account.Homeserver = strings.TrimRight(account.Homeserver, "/")
	sdk, err := newSDK(account, "")
	if err != nil {
		return nil, err
	}
	if password == "" {
		return nil, errors.New("matrix password is empty")
	}
	lock, err := lockState(dataDir)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, lock.Unlock())
		}
	}()
	request := mautrix.ReqLogin{Type: mautrix.AuthTypePassword,
		Identifier: mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: account.UserID},
		Password:   password, InitialDeviceDisplayName: "msgvault Matrix archive", StoreCredentials: true}
	previous, err := loadSession(dataDir)
	if err == nil {
		if previous.Homeserver != account.Homeserver || previous.UserID != account.UserID {
			return nil, errors.New("another Matrix account already owns this session directory")
		}
		request.DeviceID = id.DeviceID(previous.DeviceID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	response, err := sdk.Login(ctx, &request)
	if err != nil {
		return nil, fmt.Errorf("log in to Matrix: %w", err)
	}
	if response.UserID.String() != account.UserID || response.DeviceID == "" || response.AccessToken == "" {
		return nil, errors.New("matrix login returned a different user or incomplete device session")
	}
	saved := savedSession{Homeserver: account.Homeserver, UserID: account.UserID,
		DeviceID: response.DeviceID.String(), AccessToken: response.AccessToken}
	data, err := json.Marshal(saved)
	if err != nil {
		return nil, fmt.Errorf("encode Matrix session: %w", err)
	}
	if err := fileutil.SecureMkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create Matrix session directory: %w", err)
	}
	if err := fileutil.SecureReplaceFile(filepath.Join(dataDir, "session.json"), data, 0o600); err != nil {
		return nil, fmt.Errorf("save Matrix session: %w", err)
	}
	client, err = initializeCrypto(ctx, dataDir, account, sdk)
	if err == nil {
		client.lock = lock
	}
	return client, err
}

// SyncKeys receives to-device keys and state before acknowledging their token.
// Message history uses /messages and cannot be lost through a limited /sync.
func (c *Client) SyncKeys(ctx context.Context) error {
	since, err := c.sdk.Store.LoadNextBatch(ctx, c.sdk.UserID)
	if err != nil {
		return fmt.Errorf("load Matrix crypto sync cursor: %w", err)
	}
	response, err := c.sdk.SyncRequest(ctx, 0, since, "", false, event.PresenceOffline)
	if err != nil {
		return fmt.Errorf("sync Matrix encryption state: %w", err)
	}
	if err := c.sdk.Syncer.ProcessResponse(ctx, response, since); err != nil {
		return fmt.Errorf("process Matrix encryption sync: %w", err)
	}
	if err := c.sdk.Store.SaveNextBatch(ctx, c.sdk.UserID, response.NextBatch); err != nil {
		return fmt.Errorf("save Matrix crypto sync cursor: %w", err)
	}
	return nil
}

type Room struct {
	RoomID string `json:"room_id"`
	Name   string `json:"name"`
}

func (c *Client) Rooms(ctx context.Context) ([]Room, error) {
	response, err := c.sdk.JoinedRooms(ctx)
	if err != nil {
		return nil, fmt.Errorf("list joined Matrix rooms: %w", err)
	}
	rooms := make([]Room, 0, len(response.JoinedRooms))
	for _, roomID := range response.JoinedRooms {
		rooms = append(rooms, Room{RoomID: roomID.String(), Name: c.roomName(ctx, roomID)})
	}
	return rooms, nil
}

func (c *Client) roomName(ctx context.Context, roomID id.RoomID) string {
	var name event.RoomNameEventContent
	if c.sdk.StateEvent(ctx, roomID, event.StateRoomName, "", &name) == nil && name.Name != "" {
		return name.Name
	}
	return roomID.String()
}

func (c *Client) Page(ctx context.Context, request PageRequest) (Page, error) {
	if !validID(request.RoomID, '!') || request.Limit < 1 || request.Limit > 250 || (request.Direction != "b" && request.Direction != "f") {
		return Page{}, errors.New("invalid Matrix history page request")
	}
	roomID := id.RoomID(request.RoomID)
	joined, err := c.sdk.JoinedRooms(ctx)
	if err != nil {
		return Page{}, fmt.Errorf("check joined Matrix rooms: %w", err)
	}
	if !slices.Contains(joined.JoinedRooms, roomID) {
		return Page{}, errors.New("requested Matrix room is not joined by this account")
	}
	query := map[string]string{"dir": request.Direction, "limit": strconv.Itoa(request.Limit)}
	if request.From != "" {
		query["from"] = request.From
	}
	data, err := c.sdk.MakeFullRequest(ctx, mautrix.FullRequest{
		Method:            http.MethodGet,
		URL:               c.sdk.BuildURLWithQuery(mautrix.ClientURLPath{"v3", "rooms", roomID, "messages"}, query),
		ResponseSizeLimit: 32 << 20,
	})
	if err != nil {
		return Page{}, fmt.Errorf("load Matrix history page: %w", err)
	}
	var response struct {
		Start string           `json:"start"`
		End   string           `json:"end"`
		Chunk []jsontext.Value `json:"chunk"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return Page{}, fmt.Errorf("decode Matrix history page: %w", err)
	}
	page := Page{Account: c.account, RoomID: request.RoomID, Name: c.roomName(ctx, roomID),
		Start: response.Start, End: response.End, Events: make([]jsontext.Value, 0, len(response.Chunk))}
	for _, raw := range response.Chunk {
		var ev event.Event
		if err := json.Unmarshal(raw, &ev); err != nil {
			return Page{}, fmt.Errorf("decode Matrix timeline event: %w", err)
		}
		if ev.Type == event.EventEncrypted {
			ev.RoomID = roomID
			if err := ev.Content.ParseRaw(ev.Type); err != nil {
				return Page{}, fmt.Errorf("parse encrypted Matrix event: %w", err)
			}
			decrypted, err := c.crypto.Decrypt(ctx, &ev)
			if err != nil {
				return Page{}, fmt.Errorf("matrix history page has undecryptable events (run recover-matrix): %w", err)
			}
			raw, err = decryptedEvidence(raw, decrypted)
			if err != nil {
				return Page{}, err
			}
		}
		page.Events = append(page.Events, raw)
	}
	return page, nil
}

func decryptedEvidence(raw jsontext.Value, decrypted *event.Event) (jsontext.Value, error) {
	var envelope map[string]jsontext.Value
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode Matrix encrypted envelope: %w", err)
	}
	envelope["msgvault_encrypted_event"] = raw
	content, err := json.Marshal(decrypted.Content)
	if err != nil {
		return nil, fmt.Errorf("encode Matrix decrypted content: %w", err)
	}
	envelope["content"] = content
	envelope["type"], err = json.Marshal(decrypted.Type.Type)
	if err != nil {
		return nil, fmt.Errorf("encode Matrix decrypted event type: %w", err)
	}
	result, err := json.Marshal(envelope, json.Deterministic(true))
	if err != nil {
		return nil, fmt.Errorf("encode Matrix decrypted evidence: %w", err)
	}
	return result, nil
}
