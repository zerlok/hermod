package acp

import (
	"encoding/json"

	"github.com/zerlok/hermod/internal/session"
)

// JSON-RPC lets a request's ID be a number or a string, and a side matches an
// answer to its request by the ID as it wrote it. session carries an ID as a
// string and hands it back through WithID, so the string must say which of
// the two the ID was:
//
//   - a number is its literal, 7;
//   - a string is itself, hermod-1, unless it reads as a JSON number or
//     string, 7 or "x", or is empty, which is then quoted, "7", "\"x\"" or
//     "".
//
// rawID undoes it, and an ID session makes of its own, which reads as neither,
// is written as a string.

// idOf is the ID of a raw id member; "" for none.
func idOf(raw json.RawMessage) session.MessageID {
	if !present(raw) {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return session.MessageID(raw) // a number, its literal as written
	}
	// The empty string is quoted too, because session's "" means no ID.
	if s == "" || readsAsID(s) {
		return session.MessageID(raw)
	}
	return session.MessageID(s)
}

// rawID is the id member that id names; none for "".
func rawID(id session.MessageID) json.RawMessage {
	if id == "" {
		return nil
	}
	if readsAsID(string(id)) {
		return json.RawMessage(id)
	}
	// NOTE: a string always encodes.
	raw, _ := json.Marshal(string(id))
	return raw
}

// readsAsID reports whether s is the literal of a JSON number or string.
func readsAsID(s string) bool {
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return false
	}
	switch v.(type) {
	case float64, string:
		return true
	}
	return false
}
