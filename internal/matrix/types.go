// Package matrix archives joined Matrix rooms through the built-in Go client.
package matrix

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Account identifies one homeserver login, independently of its device.
type Account struct {
	Homeserver string `json:"homeserver"`
	UserID     string `json:"user_id"`
}

func (a Account) validate() error {
	u, err := url.Parse(a.Homeserver)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("matrix homeserver must be an HTTP(S) URL without credentials, query, or fragment")
	}
	if !validID(a.UserID, '@') {
		return errors.New("matrix account must contain a full user ID")
	}
	return nil
}

// Identifier is the stable archive source identity, independent of device ID.
func (a Account) Identifier() string {
	return a.UserID + " at " + strings.TrimRight(a.Homeserver, "/")
}

func validID(id string, sigil byte) bool {
	if len(id) < 4 || id[0] != sigil || strings.ContainsAny(id, "\r\n\t ") {
		return false
	}
	local, server, found := strings.Cut(id[1:], ":")
	return found && local != "" && server != ""
}

// PageRequest is the Matrix client's history pagination contract.
type PageRequest struct {
	RoomID    string `json:"room_id"`
	From      string `json:"from,omitempty"`
	Direction string `json:"direction"`
	Limit     int    `json:"limit"`
}

// Page contains decrypted timeline evidence and opaque pagination tokens.
type Page struct {
	Account Account          `json:"account"`
	RoomID  string           `json:"room_id"`
	Name    string           `json:"name"`
	Start   string           `json:"start"`
	End     string           `json:"end"`
	Events  []jsontext.Value `json:"events"`
}

type messageEvent struct {
	Type      string `json:"type"`
	ID        string `json:"event_id"`
	Sender    string `json:"sender"`
	Timestamp *int64 `json:"origin_server_ts"`
	Content   struct {
		MessageType string  `json:"msgtype"`
		Body        *string `json:"body"`
		Relation    struct {
			Type string `json:"rel_type"`
		} `json:"m.relates_to"`
	} `json:"content"`
}

func parseEvent(raw jsontext.Value) (messageEvent, bool, error) {
	var e messageEvent
	if err := json.Unmarshal(raw, &e); err != nil {
		return e, false, fmt.Errorf("decode Matrix event: %w", err)
	}
	if e.Type == "m.room.encrypted" {
		return e, false, errors.New("matrix page contains undecryptable events; recover encryption keys and retry")
	}
	if e.Type == "" {
		return e, false, errors.New("matrix event has no type")
	}
	if e.Type != "m.room.message" || e.Content.Relation.Type == "m.replace" {
		return e, false, nil
	}
	switch e.Content.MessageType {
	case "m.text", "m.notice", "m.emote":
	default:
		return e, false, nil
	}
	if len(e.ID) < 2 || !strings.HasPrefix(e.ID, "$") || strings.ContainsAny(e.ID, "\r\n\t ") || !validID(e.Sender, '@') || e.Timestamp == nil || *e.Timestamp < 0 || e.Content.Body == nil {
		return e, false, errors.New("matrix text event has invalid identity, timestamp, or body")
	}
	return e, true, nil
}
