package air

import (
	"errors"
	"testing"

	"github.com/zerlok/hermod/internal/acp"
	"github.com/zerlok/hermod/internal/session"
)

// asked is the asked lookup of a side that sent one request, method.
func asked(method string) func(session.MessageID) (string, bool) {
	return func(session.MessageID) (string, bool) { return method, method != "" }
}

// TestKinds reads every row of Air's messages with Air's serializer and with
// ACP's standard one, which rejects Air's own.
func TestKinds(t *testing.T) {
	cases := []struct {
		name     string
		from     acp.Peer
		asked    string
		line     string
		withAir  session.Kind
		standard session.Kind
		rejected bool // the standard serializer rejects it
		chat     session.ChatID
	}{
		{"session/new", acp.Client, "", `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/p","mcpServers":[]}}`, session.Open, session.Open, false, ""},
		{"session/load", acp.Client, "", `{"jsonrpc":"2.0","id":2,"method":"session/load","params":{"sessionId":"s1","cwd":"/p","mcpServers":[]}}`, session.Open, session.Open, false, "s1"},
		{"session/resume", acp.Client, "", `{"jsonrpc":"2.0","id":3,"method":"session/resume","params":{"sessionId":"s1","cwd":"/p"}}`, session.Open, session.Open, false, "s1"},
		{"session/close", acp.Client, "", `{"jsonrpc":"2.0","id":4,"method":"session/close","params":{"sessionId":"s1"}}`, session.Suspend, session.Pass, false, "s1"},
		{"_session/archive", acp.Client, "", `{"jsonrpc":"2.0","id":5,"method":"_session/archive","params":{"sessionId":"s1"}}`, session.Close, 0, true, "s1"},
		{"_session/unarchive", acp.Client, "", `{"jsonrpc":"2.0","id":6,"method":"_session/unarchive","params":{"sessionId":"s1"}}`, session.Pass, 0, true, "s1"},
		{"_session/rename", acp.Client, "", `{"jsonrpc":"2.0","id":7,"method":"_session/rename","params":{"sessionId":"s1","title":"Fix it"}}`, session.Pass, 0, true, "s1"},
		{"initialize", acp.Client, "", `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":1}}`, session.Connect, session.Connect, false, ""},
		{"session/list", acp.Client, "", `{"jsonrpc":"2.0","id":8,"method":"session/list","params":{"cwd":"/p"}}`, session.List, session.List, false, ""},
		{"$/cancel_request", acp.Client, "", `{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":9}}`, session.Cancel, session.Cancel, false, ""},
		{"a request from the adapter", acp.Agent, "", `{"jsonrpc":"2.0","id":0,"method":"session/request_permission","params":{"sessionId":"s1","toolCall":{"toolCallId":"t1"},"options":[]}}`, session.Question, session.Question, false, "s1"},
		{"the client's answer to it", acp.Client, "session/request_permission", `{"jsonrpc":"2.0","id":"hermod-1","result":{"outcome":{"outcome":"cancelled"}}}`, session.Answer, session.Answer, false, ""},
		{"another message of ACP's", acp.Client, "", `{"jsonrpc":"2.0","id":10,"method":"session/prompt","params":{"sessionId":"s1","prompt":[]}}`, session.Pass, session.Pass, false, "s1"},
		{"the adapter's answer to Air's own request", acp.Agent, "_session/archive", `{"jsonrpc":"2.0","id":5,"result":{}}`, session.Pass, 0, true, ""},
	}
	serializers := []struct {
		name       string
		serializer acp.Serializer
		standard   bool
	}{
		{"Air's", NewSerializer(), false},
		{"the standard", acp.NewStandard(), true},
	}
	for _, s := range serializers {
		for _, tc := range cases {
			t.Run(s.name+"/"+tc.name, func(t *testing.T) {
				msg, err := s.serializer.Decode([]byte(tc.line), tc.from, asked(tc.asked))
				var rejection *acp.Rejection
				if s.standard && tc.rejected {
					if !errors.As(err, &rejection) {
						t.Fatalf("Decode = %v, %v, want a rejection", msg, err)
					}
					return
				}
				if err != nil {
					t.Fatalf("Decode: %v", err)
				}
				want := tc.withAir
				if s.standard {
					want = tc.standard
				}
				if msg.Kind() != want || msg.Chat() != tc.chat {
					t.Errorf("Kind, Chat = %s, %q, want %s, %q", msg.Kind(), msg.Chat(), want, tc.chat)
				}
				line, err := s.serializer.Encode(msg, asked(""))
				if err != nil || string(line) != tc.line {
					t.Errorf("Encode = %s, %v, want %s", line, err, tc.line)
				}
			})
		}
	}
}

// TestReply answers Air's own requests as session does.
func TestReply(t *testing.T) {
	cases := []struct {
		name    string
		request string
		method  string // the request's method, as the connection recorded it
		result  session.Result
		want    string
	}{
		{"session/close, which session answers", `{"jsonrpc":"2.0","id":4,"method":"session/close","params":{"sessionId":"s1"}}`, "session/close", session.EmptyResult{},
			`{"jsonrpc":"2.0","id":4,"result":{}}`},
		{"session/close with another result", `{"jsonrpc":"2.0","id":4,"method":"session/close","params":{"sessionId":"s1"}}`, "session/close", session.ListResult{},
			`{"jsonrpc":"2.0","id":4,"error":{"code":-32603,"message":"hermod: session/close is not answered with a session.ListResult"}}`},
		{"_session/archive, which the adapter answers", `{"jsonrpc":"2.0","id":5,"method":"_session/archive","params":{"sessionId":"s1"}}`, "_session/archive", session.EmptyResult{},
			`{"jsonrpc":"2.0","id":5,"error":{"code":-32603,"message":"hermod: _session/archive is not answered by hermod"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSerializer()
			msg, err := s.Decode([]byte(tc.request), acp.Client, asked(""))
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			line, err := s.Encode(msg.Reply(tc.result), asked(tc.method))
			if err != nil || string(line) != tc.want {
				t.Errorf("Encode = %s, %v, want %s", line, err, tc.want)
			}
		})
	}
}
