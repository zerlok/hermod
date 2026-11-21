package air

import (
	"bytes"
	"encoding/json"
)

// Mark is what Air does to a chat with a lifecycle message.
type Mark int

const (
	// MarkDone is `_session/archive`, sent when the user marks a chat done. It
	// reaches only the adapter, after the chat's claude has exited, so the
	// process wrapping the adapter is the one place that can act on it.
	MarkDone Mark = iota + 1
	// MarkReopened is `_session/unarchive`, sent when the user reopens a chat
	// marked done, before the `session/resume` that brings it back.
	MarkReopened
)

// marks are the methods of the lifecycle messages.
var marks = map[string]Mark{
	"_session/archive":   MarkDone,
	"_session/unarchive": MarkReopened,
}

// Marked reads one ACP message, a line from Air to the adapter, and reports
// the chat it marks done or reopens, as ACP names it. Any other message, or one
// that is not JSON, marks nothing.
func Marked(line []byte) (sessionID string, mark Mark, ok bool) {
	if !namesAMark(line) {
		return "", 0, false
	}
	var msg struct {
		Method string `json:"method"`
		Params struct {
			SessionID string `json:"sessionId"`
		} `json:"params"`
	}
	if err := json.Unmarshal(line, &msg); err != nil || msg.Params.SessionID == "" {
		return "", 0, false
	}
	mark, ok = marks[msg.Method]
	if !ok {
		return "", 0, false
	}
	return msg.Params.SessionID, mark, true
}

// namesAMark reports whether the line names a lifecycle method anywhere. Most
// lines are prompts and tool results; only one naming a method is worth
// decoding.
func namesAMark(line []byte) bool {
	for method := range marks {
		if bytes.Contains(line, []byte(method)) {
			return true
		}
	}
	return false
}
