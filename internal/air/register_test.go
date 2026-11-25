package air

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/acp"
	"github.com/zerlok/hermod/internal/session"
)

// JSON-RPC's codes for a method with no handler and for params that do not
// read.
const (
	methodNotFound = -32601
	invalidParams  = -32602
)

// TestRegister reads each of Air's own messages, and session/close, which
// Air sends for every closed tab, through a client's connection whose
// registry Air's methods were added to, and through one with ACP's standard
// methods only. A message is delivered with its kind and chat, or answered
// with the error code of its registry's row.
func TestRegister(t *testing.T) {
	cases := []struct {
		name         string
		line         string
		kind         session.Kind
		chat         session.ChatID
		standardCode int // the error the standard registry answers; 0 delivers it
		airCode      int // the error Air's registry answers; 0 delivers it
	}{
		{"_session/archive", `{"jsonrpc":"2.0","id":1,"method":"_session/archive","params":{"sessionId":"s1"}}`,
			session.Close, "s1", methodNotFound, 0},
		{"_session/unarchive", `{"jsonrpc":"2.0","id":2,"method":"_session/unarchive","params":{"sessionId":"s1"}}`,
			session.Pass, "s1", methodNotFound, 0},
		{"_session/rename", `{"jsonrpc":"2.0","id":3,"method":"_session/rename","params":{"sessionId":"s1","title":"Fix it"}}`,
			session.Pass, "s1", methodNotFound, 0},
		{"_session/rename without a title", `{"jsonrpc":"2.0","id":4,"method":"_session/rename","params":{"sessionId":"s1"}}`,
			0, "", methodNotFound, invalidParams},
		{"a _session/* method with no handler", `{"jsonrpc":"2.0","id":5,"method":"_session/steering","params":{}}`,
			0, "", methodNotFound, methodNotFound},
		{"session/close", `{"jsonrpc":"2.0","id":6,"method":"session/close","params":{"sessionId":"s1"}}`,
			session.Suspend, "s1", 0, 0},
	}
	registries := []struct {
		name string
		air  bool
	}{
		{"Air's", true},
		{"the standard", false},
	}
	for _, r := range registries {
		for _, tc := range cases {
			t.Run(r.name+"/"+tc.name, func(t *testing.T) {
				reg := acp.Standard()
				wantCode := tc.standardCode
				if r.air {
					Register(reg)
					wantCode = tc.airCode
				}
				var out bytes.Buffer
				c := acp.NewConn(context.Background(), strings.NewReader(tc.line+"\n"), &out, reg, acp.Client, log.New(io.Discard, "", 0))
				var got []session.Message
				for in := range c.Receive() {
					got = append(got, in.Message)
				}
				if wantCode != 0 {
					var answer struct {
						Error *acp.Error `json:"error"`
					}
					if err := json.Unmarshal(out.Bytes(), &answer); err != nil || answer.Error == nil || answer.Error.Code != wantCode || len(got) != 0 {
						t.Errorf("delivered %v and wrote %s, want only an error answer %d", got, out.String(), wantCode)
					}
					return
				}
				if len(got) != 1 || got[0].Kind() != tc.kind || got[0].Chat() != tc.chat {
					t.Fatalf("delivered %v, want one %s of chat %q", got, tc.kind, tc.chat)
				}
			})
		}
	}
}
