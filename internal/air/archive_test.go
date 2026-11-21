package air

import "testing"

func TestMarked(t *testing.T) {
	const id = "0b7e5f3c-2a41-4c8e-9d6f-1e2a3b4c5d6e"
	cases := []struct {
		name     string
		line     string
		wantID   string
		wantMark Mark
		wantOK   bool
	}{
		{"mark done, a request", `{"jsonrpc":"2.0","id":7,"method":"_session/archive","params":{"sessionId":"` + id + `"}}` + "\n", id, MarkDone, true},
		{"mark done, a notification, more params", `{"jsonrpc":"2.0","method":"_session/archive","params":{"cwd":"/home/me/dev/api","sessionId":"` + id + `"}}`, id, MarkDone, true},
		{"reopen", `{"jsonrpc":"2.0","id":8,"method":"_session/unarchive","params":{"sessionId":"` + id + `"}}`, id, MarkReopened, true},
		{"reopen, the other method named in its params", `{"jsonrpc":"2.0","id":8,"method":"_session/unarchive","params":{"sessionId":"` + id + `","note":"_session/archive"}}`, id, MarkReopened, true},
		{"the method named in a prompt", `{"jsonrpc":"2.0","id":9,"method":"session/prompt","params":{"sessionId":"` + id + `","prompt":[{"type":"text","text":"_session/archive"}]}}`, "", 0, false},
		{"no session", `{"jsonrpc":"2.0","id":7,"method":"_session/archive","params":{}}`, "", 0, false},
		{"a session that is not a string", `{"jsonrpc":"2.0","id":7,"method":"_session/archive","params":{"sessionId":7}}`, "", 0, false},
		{"not JSON", `_session/archive ` + id, "", 0, false},
		{"a response", `{"jsonrpc":"2.0","id":7,"result":null}`, "", 0, false},
		{"empty", "", "", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, mark, ok := Marked([]byte(tc.line))
			if id != tc.wantID || mark != tc.wantMark || ok != tc.wantOK {
				t.Errorf("Marked() = %q, %v, %v, want %q, %v, %v", id, mark, ok, tc.wantID, tc.wantMark, tc.wantOK)
			}
		})
	}
}
