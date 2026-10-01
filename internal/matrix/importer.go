package matrix

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"time"

	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/textutil"
)

// ImportOptions bounds an explicitly selected set of joined rooms.
type ImportOptions struct {
	RoomIDs  []string
	MaxPages int // Across this run; zero is unlimited.
	Full     bool
}

// ImportSummary describes persisted work, including bounded backfills.
type ImportSummary struct {
	SourceID          int64
	MessagesProcessed int64
	MessagesAdded     int64
	MessagesUpdated   int64
	EventsSkipped     int64
	PagesProcessed    int
	HistoryComplete   bool
}

type roomState struct {
	Oldest string `json:"oldest"`
	Head   string `json:"head"`
	Done   bool   `json:"done"`
}

type syncState struct {
	Rooms    map[string]*roomState `json:"rooms"`
	NextRoom string                `json:"next_room,omitempty"`
}

// Import backfills history, then reads forward from the original head token.
// Each page is validated before writes; its cursor advances only after every
// supported event is durably archived. Partial writes replay safely by event ID.
func Import(ctx context.Context, st *store.Store, account Account,
	readPage func(context.Context, PageRequest) (Page, error), opts ImportOptions,
) (sum *ImportSummary, err error) {
	if err = account.validate(); err != nil {
		return nil, err
	}
	if len(opts.RoomIDs) == 0 || opts.MaxPages < 0 || readPage == nil {
		return nil, errors.New("matrix import needs rooms, a reader, and a non-negative page limit")
	}
	for _, id := range opts.RoomIDs {
		if !validID(id, '!') {
			return nil, fmt.Errorf("invalid Matrix room ID %q", id)
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	source, err := st.GetOrCreateSource("matrix", account.Identifier())
	if err != nil {
		return nil, err
	}
	sum = &ImportSummary{SourceID: source.ID}
	syncID, err := st.StartSync(source.ID, "matrix")
	if err != nil {
		return sum, err
	}
	st = st.ScopedToSync(source.ID, syncID)
	state := syncState{Rooms: make(map[string]*roomState)}
	stateLoaded := false
	defer func() {
		if err != nil {
			if !stateLoaded {
				err = errors.Join(err, st.FailSync(syncID, err.Error()))
				return
			}
			cp, cpErr := checkpoint(&state, sum)
			if cpErr == nil {
				cpErr = st.FailSyncWithCheckpoint(syncID, err.Error(), cp)
			}
			err = errors.Join(err, cpErr)
		}
	}()
	if err = loadState(st, source.ID, &state); err != nil {
		return sum, err
	}
	stateLoaded = true
	rooms := slices.Compact(slices.Sorted(slices.Values(opts.RoomIDs)))
	if !opts.Full {
		if offset := slices.Index(rooms, state.NextRoom); offset > 0 {
			rooms = append(slices.Clone(rooms[offset:]), rooms[:offset]...)
		}
	}
	for _, roomID := range rooms {
		if opts.Full || state.Rooms[roomID] == nil {
			state.Rooms[roomID] = &roomState{}
		}
	}
	for roomIndex, roomID := range rooms {
		rs := state.Rooms[roomID]
		// Finish one backfill before starting its incremental path on a later
		// invocation. Head remains the initial history boundary throughout.
		backfill := !rs.Done
		for opts.MaxPages == 0 || sum.PagesProcessed < opts.MaxPages {
			if err = ctx.Err(); err != nil {
				return sum, err
			}
			request := PageRequest{RoomID: roomID, From: rs.Head, Direction: "f", Limit: 100}
			if backfill {
				request.From, request.Direction = rs.Oldest, "b"
			}
			var page Page
			page, err = readPage(ctx, request)
			if err != nil {
				return sum, fmt.Errorf("read Matrix room %s: %w", roomID, err)
			}
			if err = validatePage(page, request, account); err != nil {
				return sum, err
			}
			if err = persistPage(ctx, st, page, sum); err != nil {
				return sum, err
			}
			exhausted := page.End == "" || page.End == request.From
			if backfill {
				if rs.Head == "" {
					rs.Head = page.Start
				}
				rs.Oldest, rs.Done = page.End, exhausted
			} else if page.End != "" {
				rs.Head = page.End
			}
			sum.PagesProcessed++
			state.NextRoom = rooms[(roomIndex+1)%len(rooms)]
			var cp *store.Checkpoint
			cp, err = checkpoint(&state, sum)
			if err == nil {
				err = st.UpdateSyncCheckpointContext(ctx, syncID, cp)
			}
			if err != nil {
				return sum, err
			}
			if exhausted {
				break
			}
		}
	}
	sum.HistoryComplete = true
	for _, id := range rooms {
		sum.HistoryComplete = sum.HistoryComplete && state.Rooms[id].Done
	}
	if err = st.RecomputeConversationStatsContext(ctx, source.ID); err != nil {
		return sum, err
	}
	cp, err := checkpoint(&state, sum)
	if err != nil {
		return sum, err
	}
	err = st.CompleteSyncAndUpdateSourceCursorContext(ctx, syncID, source.ID, cp.PageToken)
	return sum, err
}

func loadState(st *store.Store, sourceID int64, state *syncState) error {
	var blob string
	run, err := st.GetLastSuccessfulSync(sourceID)
	if err != nil && !errors.Is(err, store.ErrSyncRunNotFound) {
		return err
	}
	if run != nil {
		blob = run.CursorAfter.String
	}
	run, err = st.GetLatestCheckpointedSync(sourceID)
	if err != nil && !errors.Is(err, store.ErrSyncRunNotFound) {
		return err
	}
	if run != nil {
		blob = run.CursorBefore.String
	}
	if blob != "" {
		if err := json.Unmarshal([]byte(blob), state); err != nil {
			return fmt.Errorf("decode Matrix sync checkpoint: %w", err)
		}
	}
	if state.Rooms == nil {
		state.Rooms = make(map[string]*roomState)
	}
	return nil
}

func checkpoint(state *syncState, sum *ImportSummary) (*store.Checkpoint, error) {
	data, err := json.Marshal(state, json.Deterministic(true))
	if err != nil {
		return nil, fmt.Errorf("encode Matrix sync checkpoint: %w", err)
	}
	return &store.Checkpoint{PageToken: string(data), MessagesProcessed: sum.MessagesProcessed,
		MessagesAdded: sum.MessagesAdded, MessagesUpdated: sum.MessagesUpdated}, nil
}

func validatePage(page Page, request PageRequest, account Account) error {
	if page.Account != account || page.RoomID != request.RoomID || page.Start == "" {
		return errors.New("matrix reader returned a different account, room, or missing page boundary")
	}
	if request.From != "" && page.Start != request.From {
		return errors.New("matrix reader returned a different pagination boundary")
	}
	if len(page.Events) > 0 && page.End != "" && (page.End == request.From || page.End == page.Start) {
		return errors.New("matrix history cursor did not advance")
	}
	for _, raw := range page.Events {
		if _, _, err := parseEvent(raw); err != nil {
			return err
		}
	}
	return nil
}

func persistPage(ctx context.Context, st *store.Store, page Page, sum *ImportSummary) error {
	for _, raw := range page.Events {
		e, supported, err := parseEvent(raw)
		if err != nil {
			return err
		}
		if !supported {
			sum.EventsSkipped++
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		existing, err := st.MessageExistsBatch(sum.SourceID, []string{e.ID})
		if err != nil {
			return err
		}
		pid, err := st.EnsureParticipantByIdentifier("matrix", e.Sender, e.Sender)
		if err != nil {
			return err
		}
		body := textutil.SanitizeTerminalMultiline(*e.Content.Body)
		snippet := []rune(body)
		if len(snippet) > 100 {
			snippet = snippet[:100]
		}
		sentAt := sql.NullTime{Time: time.UnixMilli(*e.Timestamp).UTC(), Valid: true}
		_, err = st.PersistMessageContext(ctx, &store.MessagePersistData{
			Message: &store.Message{SourceID: sum.SourceID, SourceMessageID: e.ID,
				MessageType: "matrix", SenderID: sql.NullInt64{Int64: pid, Valid: true},
				SentAt: sentAt, ReceivedAt: sentAt, IsFromMe: e.Sender == page.Account.UserID,
				Snippet: sql.NullString{String: string(snippet), Valid: true}, SizeEstimate: int64(len(body))},
			Conversation: &store.ConversationPersistData{SourceConversationID: page.RoomID,
				ConversationType: "group_chat", Title: page.Name,
				Participants:                 []store.ConversationParticipantRef{{ParticipantID: pid, Role: "member"}},
				PreserveExistingParticipants: true},
			BodyText: sql.NullString{String: body, Valid: true}, RawMIME: raw, RawFormat: "matrix_json",
			Recipients: []store.RecipientSet{{Type: "from", ParticipantIDs: []int64{pid}, DisplayNames: []string{e.Sender}}},
			FTS:        &store.FTSDoc{Body: body, FromAddr: e.Sender},
		})
		if err != nil {
			return fmt.Errorf("persist Matrix event %s: %w", e.ID, err)
		}
		sum.MessagesProcessed++
		if _, found := existing[e.ID]; found {
			sum.MessagesUpdated++
		} else {
			sum.MessagesAdded++
		}
	}
	return nil
}
