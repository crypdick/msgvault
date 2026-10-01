package matrix

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/testutil"
)

var testAccount = Account{Homeserver: "https://matrix.example.com", UserID: "@alice:example.com"}

const testRoom = "!chat:example.com"

func textEvent(id, sender, body string) jsontext.Value {
	return jsontext.Value(`{"type":"m.room.message","event_id":"` + id + `","sender":"` + sender +
		`","origin_server_ts":1704067200123,"content":{"msgtype":"m.text","body":"` + body + `"}}`)
}

func historyPage(start, end string, events ...jsontext.Value) Page {
	return Page{Account: testAccount, RoomID: testRoom, Name: "Example chat", Start: start, End: end, Events: events}
}

func TestImportBackfillResumeIncrementalAndSearch(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	ctx := context.Background()
	var requests []PageRequest
	pages := []Page{
		historyPage("head", "older", textEvent("$second", testAccount.UserID, "archive search")),
		historyPage("older", "", textEvent("$first", "@bob:example.com", "early message")),
		historyPage("head", "next", textEvent("$third", "@bob:example.com", "later message")),
		historyPage("next", ""),
	}
	read := func(_ context.Context, request PageRequest) (Page, error) {
		requests = append(requests, request)
		require.NotEmpty(pages)
		page := pages[0]
		pages = pages[1:]
		return page, nil
	}
	opts := ImportOptions{RoomIDs: []string{testRoom}, MaxPages: 1}
	sum, err := Import(ctx, st, testAccount, read, opts)
	require.NoError(err)
	assert.EqualValues(1, sum.MessagesAdded)
	assert.False(sum.HistoryComplete)
	sum, err = Import(ctx, st, testAccount, read, opts)
	require.NoError(err)
	assert.True(sum.HistoryComplete)
	opts.MaxPages = 0
	sum, err = Import(ctx, st, testAccount, read, opts)
	require.NoError(err)
	assert.EqualValues(1, sum.MessagesAdded)
	assert.Equal([]PageRequest{
		{RoomID: testRoom, Direction: "b", Limit: 100},
		{RoomID: testRoom, From: "older", Direction: "b", Limit: 100},
		{RoomID: testRoom, From: "head", Direction: "f", Limit: 100},
		{RoomID: testRoom, From: "next", Direction: "f", Limit: 100},
	}, requests)
	results, total, err := st.SearchMessages("archive search", 0, 10)
	require.NoError(err)
	assert.EqualValues(1, total)
	require.Len(results, 1)
	assert.Equal("$second", results[0].SourceMessageID)
	assert.Equal("matrix", results[0].MessageType)
	var mine bool
	var rawFormat string
	require.NoError(st.DB().QueryRow(`SELECT is_from_me FROM messages WHERE source_message_id = '$second'`).Scan(&mine))
	assert.True(mine)
	require.NoError(st.DB().QueryRow(st.Rebind(`SELECT raw_format FROM message_raw WHERE message_id = ?`), results[0].ID).Scan(&rawFormat))
	raw, err := st.GetMessageRaw(results[0].ID)
	require.NoError(err)
	assert.Equal("matrix_json", rawFormat)
	assert.JSONEq(string(textEvent("$second", testAccount.UserID, "archive search")), string(raw))
	// Re-reading the same events repairs rows without creating duplicates.
	_, err = Import(ctx, st, testAccount, func(context.Context, PageRequest) (Page, error) {
		return historyPage("fresh", "", textEvent("$second", testAccount.UserID, "archive search")), nil
	}, ImportOptions{RoomIDs: []string{testRoom}, Full: true})
	require.NoError(err)
	count, err := st.CountMessagesForSourceContext(ctx, sum.SourceID)
	require.NoError(err)
	assert.EqualValues(3, count)
}

func TestImportFailedPageDoesNotAdvanceCursor(t *testing.T) {
	for _, failure := range []string{"reader", "encrypted", "malformed", "account", "room", "cursor"} {
		t.Run(failure, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			st := testutil.NewTestStore(t)
			read := func(_ context.Context, request PageRequest) (Page, error) {
				if request.From == "" {
					return historyPage("head", "older", textEvent("$second", testAccount.UserID, "saved")), nil
				}
				page := historyPage("older", "oldest", textEvent("$first", testAccount.UserID, "earlier"))
				switch failure {
				case "reader":
					return Page{}, errors.New("reader unavailable")
				case "encrypted":
					page.Events = append(page.Events, jsontext.Value(`{"type":"m.room.encrypted"}`))
				case "malformed":
					page.Events = append(page.Events, jsontext.Value(`{"type":"m.room.message","content":{"msgtype":"m.text"}}`))
				case "account":
					page.Account.UserID = "@other:example.com"
				case "room":
					page.RoomID = "!other:example.com"
				case "cursor":
					page.End = "older"
				}
				return page, nil
			}
			_, err := Import(context.Background(), st, testAccount, read, ImportOptions{RoomIDs: []string{testRoom}})
			require.Error(err)
			var resumed string
			_, err = Import(context.Background(), st, testAccount, func(_ context.Context, request PageRequest) (Page, error) {
				resumed = request.From
				return historyPage("older", ""), nil
			}, ImportOptions{RoomIDs: []string{testRoom}, MaxPages: 1})
			require.NoError(err)
			assert.Equal("older", resumed)
		})
	}
}

func TestImportSkipsStateAndMediaButPaginates(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	pages := []Page{
		historyPage("head", "next", jsontext.Value(`{"type":"m.room.member"}`), jsontext.Value(`{"type":"m.room.message","content":{"msgtype":"m.image"}}`)),
		historyPage("next", "", textEvent("$text", "@bob:example.com", "supported text")),
	}
	sum, err := Import(context.Background(), st, testAccount, func(context.Context, PageRequest) (Page, error) {
		page := pages[0]
		pages = pages[1:]
		return page, nil
	}, ImportOptions{RoomIDs: []string{testRoom}})
	require.NoError(err)
	assert.EqualValues(1, sum.MessagesAdded)
	assert.EqualValues(2, sum.EventsSkipped)
}

func TestBoundedImportRotatesRooms(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	rooms := []string{testRoom, "!other:example.com"}
	var visited []string
	read := func(_ context.Context, request PageRequest) (Page, error) {
		visited = append(visited, request.RoomID)
		page := historyPage("head", "")
		page.RoomID = request.RoomID
		return page, nil
	}
	for range 2 {
		_, err := Import(context.Background(), st, testAccount, read, ImportOptions{RoomIDs: rooms, MaxPages: 1})
		require.NoError(err)
	}
	assert.Equal(rooms, visited)
}

func TestImportCancellationRetainsCompletedPages(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, err := Import(ctx, st, testAccount, func(_ context.Context, request PageRequest) (Page, error) {
		if request.From == "" {
			return historyPage("head", "older", textEvent("$saved", testAccount.UserID, "saved message")), nil
		}
		cancel()
		return Page{}, ctx.Err()
	}, ImportOptions{RoomIDs: []string{testRoom}})
	require.ErrorIs(err, context.Canceled)
	_, err = Import(context.Background(), st, testAccount, func(_ context.Context, request PageRequest) (Page, error) {
		assert.Equal("older", request.From)
		return historyPage("older", ""), nil
	}, ImportOptions{RoomIDs: []string{testRoom}})
	require.NoError(err)
}

func TestImportRejectsInvalidIdentifiers(t *testing.T) {
	for _, test := range []struct {
		userID string
		roomID string
	}{
		{"@alice", testRoom},
		{"@:example.com", testRoom},
		{testAccount.UserID, ""},
		{testAccount.UserID, "!"},
		{testAccount.UserID, "#chat:example.com"},
		{testAccount.UserID, "!:example.com"},
		{testAccount.UserID, "!chat:"},
		{testAccount.UserID, "!chat room"},
	} {
		t.Run(test.userID+"/"+test.roomID, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			account := testAccount
			account.UserID = test.userID
			readCalled := false
			_, err := Import(context.Background(), testutil.NewTestStore(t), account,
				func(context.Context, PageRequest) (Page, error) {
					readCalled = true
					return historyPage("head", ""), nil
				}, ImportOptions{RoomIDs: []string{test.roomID}})
			require.Error(err)
			assert.False(readCalled)
		})
	}
}
