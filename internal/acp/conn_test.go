package acp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/session"
)

// TestConnRejects reads lines that are no message the registry has, or whose
// params do not read: a request is answered with JSON-RPC's error under its
// ID, anything else only logged, and nothing is delivered.
func TestConnRejects(t *testing.T) {
	cases := []struct {
		name     string
		peer     Peer
		line     string
		wantID   string // the ID of the error answer; "" for none
		wantCode int
		wantLog  string
	}{
		{"an unknown request", Client, `{"jsonrpc":"2.0","id":1,"method":"session/fork","params":{"sessionId":"s1"}}`,
			"1", codeMethodNotFound, `no request "session/fork" from the client`},
		{"an unknown request under a string ID", Client, `{"jsonrpc":"2.0","id":"x","method":"_session/steering","params":{}}`,
			`"x"`, codeMethodNotFound, "method not found"},
		{"an unknown notification", Agent, `{"jsonrpc":"2.0","method":"_session/list/changes","params":{}}`,
			"", 0, `no notification "_session/list/changes" from the agent`},
		{"the agent's request from the client", Client, `{"jsonrpc":"2.0","id":2,"method":"fs/read_text_file","params":{"sessionId":"s1","path":"/p"}}`,
			"2", codeMethodNotFound, "method not found"},
		{"the client's request from the agent", Agent, `{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/p","mcpServers":[]}}`,
			"2", codeMethodNotFound, "method not found"},
		{"a notification sent as a request", Client, `{"jsonrpc":"2.0","id":3,"method":"session/cancel","params":{"sessionId":"s1"}}`,
			"3", codeMethodNotFound, "method not found"},
		{"a cancel sent as a request", Client, `{"jsonrpc":"2.0","id":3,"method":"$/cancel_request","params":{"requestId":1}}`,
			"3", codeMethodNotFound, "method not found"},
		{"a request sent as a notification", Client, `{"jsonrpc":"2.0","method":"session/new","params":{"cwd":"/p","mcpServers":[]}}`,
			"", 0, `no notification "session/new"`},
		{"a request under a null ID", Client, `{"jsonrpc":"2.0","id":null,"method":"session/new","params":{"cwd":"/p","mcpServers":[]}}`,
			"", 0, `no notification "session/new"`},
		{"invalid params of a request", Client, `{"jsonrpc":"2.0","id":4,"method":"session/new","params":{"cwd":"/p"}}`,
			"4", codeInvalidParams, "mcpServers is required"},
		{"invalid params of a notification", Agent, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"cursor_update"}}}`,
			"", 0, "invalid params"},
		{"a cancel of no request", Agent, `{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":null}}`,
			"", 0, "a cancel from the agent of no request"},
		{"an answer to a request this side did not ask", Agent, `{"jsonrpc":"2.0","id":1,"result":{}}`,
			"", 0, "which this side does not wait for"},
		{"an ID that is no number or string", Client, `{"jsonrpc":"2.0","id":{"n":1},"method":"session/list","params":{}}`,
			"", 0, "an ID is an integer or a string"},
		{"an ID with a fraction", Client, `{"jsonrpc":"2.0","id":1.5,"method":"session/list","params":{}}`,
			"", 0, "an ID is an integer or a string"},
		{"another version of JSON-RPC", Client, `{"jsonrpc":"1.0","id":1,"method":"session/list","params":{}}`,
			"", 0, "no JSON-RPC 2.0 message"},
		{"no JSON", Client, `hello`, "", 0, "no JSON-RPC 2.0 message"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := dial(t, tc.peer)
			e.send(t, tc.line)
			if tc.wantID != "" {
				f := frameOf(t, e.next(t))
				if string(f.ID) != tc.wantID || f.Error == nil || f.Error.Code != tc.wantCode {
					t.Errorf("answered %s with %+v, want %s with code %d", f.ID, f.Error, tc.wantID, tc.wantCode)
				}
			}
			_ = e.w.Close()
			for in := range e.conn.Receive() {
				t.Errorf("delivered %v", in.Message)
			}
			if got := e.log.String(); !strings.Contains(got, tc.wantLog) {
				t.Errorf("logged %q, want a line with %q", got, tc.wantLog)
			}
		})
	}
}

// TestConnCall sends a request under this connection's own ID and returns
// what the answer under that ID says.
func TestConnCall(t *testing.T) {
	errBoth := errors.New("both")
	cases := []struct {
		name     string
		method   string
		params   string
		answer   string // the result or error members of the answer, %s its ID; "" for none
		cancel   bool   // the call's context ends before the answer
		ends     bool   // the connection ends, after the answer when there is one
		wantChat session.ChatID
		wantCode int   // the code of the error answered; 0 for none
		wantErr  error // any other error the call returns
	}{
		{"a result read as the method's answer", "session/new", `{"cwd":"/p","mcpServers":[]}`,
			`"result":{"sessionId":"s2"}`, false, false, "s2", 0, nil},
		{"an error answer", "session/prompt", `{"sessionId":"s1","prompt":[]}`,
			`"error":{"code":-32603,"message":"no"}`, false, false, "", -32603, nil},
		{"an answer of the wrong type", "session/new", `{"cwd":"/p","mcpServers":[]}`,
			`"result":{"stopReason":"end_turn"}`, false, false, "", 0, errBoth},
		{"an answer with both a result and an error", "session/prompt", `{"sessionId":"s1","prompt":[]}`,
			`"result":{"stopReason":"end_turn"},"error":{"code":1,"message":"m"}`, false, false, "", 0, errBoth},
		{"a call whose context ends is cancelled on the wire", "session/prompt", `{"sessionId":"s1","prompt":[]}`,
			"", true, false, "", 0, context.Canceled},
		{"a call whose connection ends", "session/prompt", `{"sessionId":"s1","prompt":[]}`,
			"", false, true, "", 0, errEnded},
		{"an answer the connection's end follows at once", "session/new", `{"cwd":"/p","mcpServers":[]}`,
			`"result":{"sessionId":"s2"}`, false, true, "s2", 0, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := dial(t, Agent)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := e.call(t, ctx, read(t, tc.method, false, tc.params))
			req := frameOf(t, e.next(t))
			if req.Method != tc.method || !sameJSON(t, string(req.Params), tc.params) {
				t.Errorf("wrote %s %s, want %s %s", req.Method, req.Params, tc.method, tc.params)
			}
			switch {
			case tc.cancel:
				cancel()
				f := frameOf(t, e.next(t))
				if f.Method != cancelMethod || !sameJSON(t, string(f.Params), fmt.Sprintf(`{"requestId":%s}`, req.ID)) {
					t.Errorf("wrote %s %s, want a cancel of %s", f.Method, f.Params, req.ID)
				}
			case tc.ends:
				if tc.answer != "" {
					e.send(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,%s}`, req.ID, tc.answer))
				}
				_ = e.w.Close()
			default:
				e.send(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,%s}`, req.ID, tc.answer))
			}
			got := <-done
			var answered *Error
			switch {
			case tc.wantCode != 0:
				if !errors.As(got.err, &answered) || answered.Code != tc.wantCode {
					t.Errorf("Call: %v, want the error answer %d", got.err, tc.wantCode)
				}
			case tc.wantErr == errBoth:
				if got.err == nil {
					t.Errorf("Call: %v, want an error", got.msg)
				}
			case tc.wantErr != nil:
				if !errors.Is(got.err, tc.wantErr) {
					t.Errorf("Call: %v, want %v", got.err, tc.wantErr)
				}
			default:
				if got.err != nil || got.msg.Chat() != tc.wantChat {
					t.Errorf("Call: %v, %v, want an answer of chat %q", got.msg, got.err, tc.wantChat)
				}
			}
		})
	}
}

// TestConnAnswerInOrder returns an answer where it was read: after every
// message read before it has been taken from Receive, and before any read
// after it is delivered.
func TestConnAnswerInOrder(t *testing.T) {
	update := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"x"}}}}`
	cases := []struct {
		name   string
		before int // the updates written before the answer
		after  int // and after it
		want   string
	}{
		{"two updates before, one after", 2, 1, "update update answer update"},
		{"none before, one after", 0, 1, "answer update"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := dial(t, Agent)
			answered := make(chan struct{})
			err := e.conn.Call(context.Background(), read(t, "session/prompt", false, `{"sessionId":"s1","prompt":[]}`),
				func(session.Message, error) { answered <- struct{}{} })
			if err != nil {
				t.Fatalf("Call: %v", err)
			}
			req := frameOf(t, e.next(t))
			lines := slices.Repeat([]string{update}, tc.before)
			lines = append(lines, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}`, req.ID))
			lines = append(lines, slices.Repeat([]string{update}, tc.after)...)
			go func() { _, _ = io.WriteString(e.w, strings.Join(lines, "\n")+"\n") }()
			// The reader waits for each to be taken, so the order they are
			// taken in is the order it handed them over in.
			var got []string
			for range tc.before + tc.after + 1 {
				select {
				case <-e.conn.Receive():
					got = append(got, "update")
				case <-answered:
					got = append(got, "answer")
				}
			}
			if strings.Join(got, " ") != tc.want {
				t.Errorf("handed over %q, want %q", got, tc.want)
			}
		})
	}
}

// TestConnLateAnswer drops an answer to a call that was cancelled: its ID is
// no longer waited for.
func TestConnLateAnswer(t *testing.T) {
	cases := []struct {
		name    string
		answer  string
		wantLog string
	}{
		{"a result after the cancel", `"result":{"stopReason":"cancelled"}`, "which this side does not wait for"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := dial(t, Agent)
			ctx, cancel := context.WithCancel(context.Background())
			done := e.call(t, ctx, read(t, "session/prompt", false, `{"sessionId":"s1","prompt":[]}`))
			req := frameOf(t, e.next(t))
			cancel()
			e.next(t)
			<-done
			e.send(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,%s}`, req.ID, tc.answer))
			_ = e.w.Close()
			for range e.conn.Receive() {
			}
			if got := e.log.String(); !strings.Contains(got, tc.wantLog) {
				t.Errorf("logged %q, want a line with %q", got, tc.wantLog)
			}
		})
	}
}

// TestConnServe delivers a request that arrives with its context and its
// Answer, which writes the answer under the request's own ID, a number or a
// string as the other side wrote it.
func TestConnServe(t *testing.T) {
	cases := []struct {
		name       string
		line       string
		cancel     bool // the other side cancels the request before it is answered
		answer     func(session.Answer) error
		wantKind   session.Kind
		wantChat   session.ChatID
		wantAnswer string
	}{
		{"a reply under a number ID", `{"jsonrpc":"2.0","id":7,"method":"session/list","params":{}}`, false,
			func(a session.Answer) error { return a.Reply(session.ListResult{}) },
			session.List, "", `{"jsonrpc":"2.0","id":7,"result":{"sessions":[]}}`},
		{"a reply under a string ID", `{"jsonrpc":"2.0","id":"7","method":"session/close","params":{"sessionId":"s1"}}`, false,
			func(a session.Answer) error { return a.Reply(session.EmptyResult{}) },
			session.Suspend, "s1", `{"jsonrpc":"2.0","id":"7","result":{}}`},
		{"another side's answer returned", `{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"s1","prompt":[]}}`, false,
			func(a session.Answer) error {
				return a.Return(read(t, "session/prompt", true, `{"stopReason":"end_turn"}`))
			},
			session.Pass, "s1", `{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn"}}`},
		{"another side's error answer as it was", `{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"s1","prompt":[]}}`, false,
			func(a session.Answer) error { return a.Fail(&Error{Code: -32000, Message: "auth"}) },
			session.Pass, "s1", `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"auth"}}`},
		{"a failure as an internal error", `{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"s1","prompt":[]}}`, false,
			func(a session.Answer) error { return a.Fail(errors.New("no chat")) },
			session.Pass, "s1", `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"no chat"}}`},
		{"a cancelled request still answered, as cancelled", `{"jsonrpc":"2.0","id":"q","method":"session/prompt","params":{"sessionId":"s1","prompt":[]}}`, true,
			func(a session.Answer) error { return a.Fail(context.Canceled) },
			session.Pass, "s1", `{"jsonrpc":"2.0","id":"q","error":{"code":-32800,"message":"request cancelled"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := dial(t, Client)
			e.send(t, tc.line)
			in := e.inbound(t)
			if in.Message.Kind() != tc.wantKind || in.Message.Chat() != tc.wantChat {
				t.Errorf("delivered %s of chat %q, want %s of chat %q", in.Message.Kind(), in.Message.Chat(), tc.wantKind, tc.wantChat)
			}
			if tc.cancel {
				e.send(t, fmt.Sprintf(`{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":%s}}`, frameOf(t, tc.line).ID))
				<-in.Context.Done()
			}
			if err := tc.answer(in.Answer); err != nil {
				t.Fatalf("answer: %v", err)
			}
			if got := e.next(t); !sameJSON(t, got, tc.wantAnswer) {
				t.Errorf("wrote %s, want %s", got, tc.wantAnswer)
			}
			if err := in.Answer.Fail(errors.New("again")); err == nil {
				t.Error("a request was answered twice")
			}
		})
	}
}

// TestConnNotify delivers a notification without a context or an Answer, and
// writes one with no ID.
func TestConnNotify(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		{"an update", `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"x"}}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			from, to := dial(t, Agent), dial(t, Client)
			from.send(t, tc.line)
			in := from.inbound(t)
			if in.Answer != nil || in.Context != nil {
				t.Errorf("a notification arrived with an answer or a context")
			}
			if err := to.conn.Notify(in.Message); err != nil {
				t.Fatalf("Notify: %v", err)
			}
			if got := to.next(t); !sameJSON(t, got, tc.line) {
				t.Errorf("wrote %s, want %s", got, tc.line)
			}
		})
	}
}

// TestPerHopIDs passes the questions of two adapters, under the same ID, on
// as calls of the client's connection: each goes under an ID of its own there,
// and each answer returns to its adapter under that adapter's ID.
func TestPerHopIDs(t *testing.T) {
	cases := []struct {
		name string
		id   string
	}{
		{"two questions under the ID 7", "7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := dial(t, Client)
			adapters := []*farEnd{dial(t, Agent), dial(t, Agent)}
			sent := map[string]*farEnd{}
			for i, a := range adapters {
				a.send(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"fs/read_text_file","params":{"sessionId":"s%d","path":"/p"}}`, tc.id, i))
				in := a.inbound(t)
				err := client.conn.Call(in.Context, in.Message, func(answer session.Message, err error) {
					if err != nil {
						_ = in.Answer.Fail(err)
						return
					}
					_ = in.Answer.Return(answer)
				})
				if err != nil {
					t.Fatalf("Call: %v", err)
				}
				req := frameOf(t, client.next(t))
				if _, taken := sent[string(req.ID)]; taken {
					t.Fatalf("the client's connection wrote a second request under %s", req.ID)
				}
				sent[string(req.ID)] = a
			}
			for id, a := range sent {
				client.send(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"content":"for %s"}}`, id, id))
				want := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"content":"for %s"}}`, tc.id, id)
				if got := a.next(t); !sameJSON(t, got, want) {
					t.Errorf("adapter got %s, want %s", got, want)
				}
			}
		})
	}
}

// TestAdapterInitialize writes the initialize a connection to an adapter
// sends without the client capabilities that act on the client's own
// workstation, and a client's as it is.
func TestAdapterInitialize(t *testing.T) {
	params := `{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true},"terminal":true,"session":{"notices":{}}},"_meta":{"k":"v"}}`
	cases := []struct {
		name string
		peer Peer
		want string
	}{
		{"to an adapter", Agent, `{"protocolVersion":1,"clientCapabilities":{"session":{"notices":{}}},"_meta":{"k":"v"}}`},
		{"to a client", Client, params},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := dial(t, tc.peer)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e.call(t, ctx, read(t, "initialize", false, params))
			if got := frameOf(t, e.next(t)); !sameJSON(t, string(got.Params), tc.want) {
				t.Errorf("wrote %s, want %s", got.Params, tc.want)
			}
		})
	}
}

// TestConnEnds reads to the end of the input: a cut last line is the error
// the connection ends with, and a whole one is delivered first.
func TestConnEnds(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantKinds []session.Kind
		wantErr   bool
	}{
		{"between messages", `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s1"}}` + "\n", []session.Kind{session.Pass}, false},
		{"a last line without its newline", `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s1"}}`, []session.Kind{session.Pass}, false},
		{"inside a message", `{"jsonrpc":"2.0","method":"session/can`, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewConn(context.Background(), strings.NewReader(tc.input), discardWriter{}, Standard(), Client, testLogger())
			var kinds []session.Kind
			for in := range c.Receive() {
				kinds = append(kinds, in.Message.Kind())
			}
			if fmt.Sprint(kinds) != fmt.Sprint(tc.wantKinds) || (c.Err() != nil) != tc.wantErr {
				t.Errorf("delivered %v, ended with %v, want %v and an error: %v", kinds, c.Err(), tc.wantKinds, tc.wantErr)
			}
		})
	}
}
