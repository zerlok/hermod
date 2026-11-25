package acp

import (
	"bytes"
	"errors"
	"io"
	"log"
	"slices"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/zerlok/hermod/internal/session"
)

// receive is every message a connection reads from in, what it writes back,
// what it logs, and why it ended.
func receive(peer Peer, in io.Reader) (got []session.Message, out, logged string, err error) {
	var w, l bytes.Buffer
	c := newConn(in, &w, peer, NewStandard(), newRequests(), log.New(&l, "", 0))
	for msg := range c.Receive() {
		got = append(got, msg)
	}
	return got, w.String(), l.String(), c.Err()
}

func TestConnRejects(t *testing.T) {
	cases := []struct {
		name     string
		peer     Peer
		in       string
		received []session.Kind
		written  []string
		logged   string
	}{
		{"an unknown request is answered and nothing received", Client,
			`{"jsonrpc":"2.0","id":1,"method":"session/fork","params":{}}`,
			nil,
			[]string{`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found: no request \"session/fork\" from the client"}}`},
			`"session/fork"`},
		{"an unknown notification is logged and dropped", Agent,
			`{"jsonrpc":"2.0","method":"_session/list/changes","params":{}}`,
			nil, nil, `"_session/list/changes"`},
		{"a malformed request is answered with invalid params", Client,
			`{"jsonrpc":"2.0","id":"a","method":"session/new","params":{"cwd":7,"mcpServers":[]}}`,
			nil,
			[]string{`{"jsonrpc":"2.0","id":"a","error":{"code":-32602,"message":"invalid params of \"session/new\": json: cannot unmarshal number into Go struct field NewSessionParams.cwd of type string"}}`},
			"session/new"},
		{"a line that is no JSON is dropped and the next read", Client,
			"hello\n" + `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s1"}}`,
			[]session.Kind{session.Pass}, nil, "not a JSON-RPC message"},
		{"an answer this side did not ask for is dropped", Agent,
			`{"jsonrpc":"2.0","id":3,"result":{}}`,
			nil, nil, "did not ask"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, out, logged, err := receive(tc.peer, strings.NewReader(tc.in+"\n"))
			if err != nil {
				t.Fatalf("Err = %v", err)
			}
			var kinds []session.Kind
			for _, msg := range got {
				kinds = append(kinds, msg.Kind())
			}
			if !slices.Equal(kinds, tc.received) {
				t.Errorf("received %v, want %v", kinds, tc.received)
			}
			gotLines := lines(out)
			if len(gotLines) != len(tc.written) {
				t.Fatalf("wrote %q, want %q", gotLines, tc.written)
			}
			for i := range gotLines {
				if !sameJSON(t, gotLines[i], tc.written[i]) {
					t.Errorf("wrote %s, want %s", gotLines[i], tc.written[i])
				}
			}
			if !strings.Contains(logged, tc.logged) {
				t.Errorf("logged %q, want it to name %q", logged, tc.logged)
			}
		})
	}
}

// TestAnswerByRequest sends a request on a connection and reads the answer by
// the type of what was asked: once, as a request has one answer, and only for
// a request that was written.
func TestAnswerByRequest(t *testing.T) {
	cases := []struct {
		name       string
		peer       Peer   // the side the connection reads
		from       Peer   // the side the request came from
		request    string // what the connection sends
		writeFails bool   // the request is not written
		answers    string // what it then reads
		kinds      []session.Kind
		chats      []session.ChatID
		received   []string // each message received, as written
	}{
		{"the adapter answers the client's session/new", Agent, Client,
			`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/p","mcpServers":[]}}`, false,
			`{"jsonrpc":"2.0","id":1,"result":{"sessionId":"s2"}}`,
			[]session.Kind{session.Pass}, []session.ChatID{"s2"},
			[]string{`{"jsonrpc":"2.0","id":1,"result":{"sessionId":"s2"}}`}},
		{"the client answers the adapter's question under the router's ID", Client, Agent,
			`{"jsonrpc":"2.0","id":"hermod-1","method":"fs/read_text_file","params":{"sessionId":"s1","path":"/p"}}`, false,
			`{"jsonrpc":"2.0","id":"hermod-1","result":{"content":"x"}}`,
			[]session.Kind{session.Answer}, []session.ChatID{""},
			[]string{`{"jsonrpc":"2.0","id":"hermod-1","result":{"content":"x"}}`}},
		{"a second answer to the same request is dropped", Agent, Client,
			`{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"s1","prompt":[]}}`, false,
			`{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn"}}` + "\n" + `{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn"}}`,
			[]session.Kind{session.Pass}, []session.ChatID{""},
			[]string{`{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn"}}`}},
		{"an answer under the ID of another type is dropped", Agent, Client,
			`{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"s1","prompt":[]}}`, false,
			`{"jsonrpc":"2.0","id":"1","result":{"stopReason":"end_turn"}}`,
			nil, nil, nil},
		{"an adapter's answer that does not parse is dropped", Agent, Client,
			`{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"s1","prompt":[]}}`, false,
			`{"jsonrpc":"2.0","id":1,"result":{"stopReason":"tired"}}`,
			nil, nil, nil},
		{"the client's answer that does not parse is dropped", Client, Agent,
			`{"jsonrpc":"2.0","id":"hermod-1","method":"fs/read_text_file","params":{"sessionId":"s1","path":"/p"}}`, false,
			`{"jsonrpc":"2.0","id":"hermod-1","result":{}}`,
			nil, nil, nil},
		{"an answer to a request whose write failed is dropped", Agent, Client,
			`{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"s1","prompt":[]}}`, true,
			`{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn"}}`,
			nil, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, feed := io.Pipe()
			var out io.Writer = io.Discard
			if tc.writeFails {
				out = failingWriter{}
			}
			c := newConn(in, out, tc.peer, NewStandard(), newRequests(), discard())
			if err := c.Send(read(t, tc.from, "", tc.request)); (err != nil) != tc.writeFails {
				t.Fatalf("Send: %v, want an error: %v", err, tc.writeFails)
			}
			go func() {
				_, _ = io.WriteString(feed, tc.answers+"\n")
				_ = feed.Close()
			}()
			var kinds []session.Kind
			var chats []session.ChatID
			var received []string
			for msg := range c.Receive() {
				kinds = append(kinds, msg.Kind())
				chats = append(chats, msg.Chat())
				received = append(received, written(t, msg))
			}
			if !slices.Equal(kinds, tc.kinds) || !slices.Equal(chats, tc.chats) || len(received) != len(tc.received) {
				t.Fatalf("received %v %q %q, want %v %q %q", kinds, chats, received, tc.kinds, tc.chats, tc.received)
			}
			for i := range received {
				if !sameJSON(t, received[i], tc.received[i]) {
					t.Errorf("received %s, want %s", received[i], tc.received[i])
				}
			}
		})
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// TestAdaptersConnect sends a request on one connection to an adapter and
// reads the answer on a later one: the adapter of the same key that runs on
// answers there, and a new one, or another adapter, answers nothing asked
// before.
func TestAdaptersConnect(t *testing.T) {
	request := `{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"s1","prompt":[]}}`
	answer := `{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn"}}`
	cases := []struct {
		name     string
		key      string // the later connection's adapter; the first is "a"
		started  bool   // the later connection is to an adapter just started
		received int
	}{
		{"the adapter runs on", "a", false, 1},
		{"the adapter started again", "a", true, 0},
		{"another adapter", "b", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adapters := NewAdapters[string](NewStandard(), discard())
			first := adapters.Connect("a", true, strings.NewReader(""), io.Discard)
			if err := first.Send(read(t, Client, "", request)); err != nil {
				t.Fatalf("Send: %v", err)
			}
			for range first.Receive() {
			}
			later := adapters.Connect(tc.key, tc.started, strings.NewReader(answer+"\n"), io.Discard)
			got := 0
			for range later.Receive() {
				got++
			}
			if got != tc.received {
				t.Errorf("received %d answers, want %d", got, tc.received)
			}
		})
	}
}

// TestAdapterInitialize writes the client's initialize to an adapter without
// the client capabilities that act on the client's workstation.
func TestAdapterInitialize(t *testing.T) {
	cases := []struct {
		name string
		peer Peer
		line string
		want string
	}{
		{"fs and terminal dropped for an adapter, Air's _meta kept", Agent,
			`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true,"writeTextFile":true},"terminal":true,"plan":{},"_meta":{"jetbrains":{"air":true}}},"_meta":{"jetbrains":{"air":{"loadReplayEnd":true}}}}}`,
			`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{"plan":{},"_meta":{"jetbrains":{"air":true}}},"_meta":{"jetbrains":{"air":{"loadReplayEnd":true}}}}}`},
		{"no client capabilities", Agent,
			`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":1}}`,
			`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":1}}`},
		{"kept for a client", Client,
			`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true},"terminal":true}}}`,
			`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true},"terminal":true}}}`},
		{"another message kept for an adapter", Agent,
			`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/p","mcpServers":[]}}`,
			`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/p","mcpServers":[]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := read(t, Client, "", tc.line)
			var out bytes.Buffer
			c := newConn(strings.NewReader(""), &out, tc.peer, NewStandard(), newRequests(), discard())
			if err := c.Send(msg); err != nil {
				t.Fatalf("Send: %v", err)
			}
			got := lines(out.String())
			if len(got) != 1 || !sameJSON(t, got[0], tc.want) {
				t.Errorf("wrote %q, want %s", got, tc.want)
			}
			if got := written(t, msg); !sameJSON(t, got, tc.line) {
				t.Errorf("the message changed: %s, want %s", got, tc.line)
			}
		})
	}
}

func TestConnectionEnds(t *testing.T) {
	broken := errors.New("broken pipe")
	request := `{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"s1","prompt":[]}}`
	cases := []struct {
		name    string
		in      func() io.Reader
		closed  bool // Close is called before anything is received
		want    int  // messages received
		wantErr string
	}{
		{"input ends after a line", func() io.Reader { return strings.NewReader(request + "\n" + request + "\n") }, false, 2, ""},
		{"a whole message on a partial final line", func() io.Reader { return strings.NewReader(request + "\n" + request) }, false, 2, ""},
		{"a message cut on a partial final line", func() io.Reader { return strings.NewReader(request + "\n" + request[:20]) }, false, 1, "ended inside a message"},
		{"blank lines between messages", func() io.Reader { return strings.NewReader("\n" + request + "\n \r\n" + request + "\n\n") }, false, 2, ""},
		{"empty input", func() io.Reader { return strings.NewReader("") }, false, 0, ""},
		{"a read error", func() io.Reader { return io.MultiReader(strings.NewReader(request+"\n"), iotest.ErrReader(broken)) }, false, 1, broken.Error()},
		{"closed by its owner", func() io.Reader { return strings.NewReader(request + "\n") }, true, 0, ""},
		{"closed by its owner, then its input fails", func() io.Reader { return iotest.ErrReader(broken) }, true, 0, ""},
		{"closed by its owner, then its input is cut", func() io.Reader { return strings.NewReader(request[:20]) }, true, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClient(tc.in(), io.Discard, NewStandard(), discard())
			if tc.closed {
				if err := c.Close(); err != nil {
					t.Fatalf("Close: %v", err)
				}
			}
			got := 0
			for range c.Receive() {
				got++
			}
			err := c.Err()
			switch {
			case got != tc.want:
				t.Errorf("received %d messages, want %d", got, tc.want)
			case tc.wantErr == "" && err != nil:
				t.Errorf("Err = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("Err = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

// foreign is a message of no acp serializer: a session's view of one.
type foreign struct {
	kind   session.Kind
	id     session.MessageID
	result session.Result
}

func (f foreign) Kind() session.Kind                                     { return f.kind }
func (f foreign) ID() session.MessageID                                  { return f.id }
func (foreign) Chat() session.ChatID                                     { return "" }
func (foreign) Dir() session.Path                                        { return "" }
func (f foreign) WithID(id session.MessageID) session.Message            { f.id = id; return f }
func (f foreign) Mapped(func(session.Path) session.Path) session.Message { return f }
func (f foreign) Reply(session.Result) session.Message                   { return f }
func (f foreign) Result() session.Result                                 { return f.result }
func (f foreign) Fail(error) session.Message                             { return f }

// TestSend writes messages no acp serializer made, and those that have no
// form on the wire, on a connection that has received request 7, a
// session/list.
func TestSend(t *testing.T) {
	received := `{"jsonrpc":"2.0","id":7,"method":"session/list","params":{"cwd":"/p"}}`
	notification := `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s1"}}`
	cases := []struct {
		name string
		msg  func(t *testing.T) session.Message
		want string // the line written; "" for an error and nothing written
	}{
		{"another implementation's answer, by its result", func(*testing.T) session.Message {
			return foreign{kind: session.Pass, id: num(7), result: session.ListResult{}}
		}, `{"jsonrpc":"2.0","id":7,"result":{"sessions":[]}}`},
		{"another implementation's answer that its method cannot hold", func(*testing.T) session.Message {
			return foreign{kind: session.Pass, id: num(7), result: session.EmptyResult{}}
		}, `{"jsonrpc":"2.0","id":7,"error":{"code":-32603,"message":"hermod: session/list is not answered with a session.EmptyResult"}}`},
		{"another implementation's answer without a result", func(*testing.T) session.Message {
			return foreign{kind: session.Answer, id: num(7)}
		}, `{"jsonrpc":"2.0","id":7,"error":{"code":-32603,"message":"hermod: the answer holds no result"}}`},
		{"another implementation's answer to no request received", func(*testing.T) session.Message {
			return foreign{kind: session.Pass, id: num(8), result: session.ListResult{}}
		}, ""},
		{"another implementation's request", func(*testing.T) session.Message {
			return foreign{kind: session.Open, id: num(7)}
		}, ""},
		{"another implementation's cancel", func(*testing.T) session.Message {
			return foreign{kind: session.Cancel, id: num(7)}
		}, ""},
		{"nil", func(*testing.T) session.Message { return nil }, ""},
		{"the answer to a notification", func(t *testing.T) session.Message {
			return read(t, Client, "", notification).Reply(session.EmptyResult{})
		}, ""},
		{"a request under no ID", func(*testing.T) session.Message {
			return NewRequest[PromptParams, PromptResult](session.MessageID{}, PromptParams{SessionID: "s1", Prompt: []ContentBlock{}})
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			c := NewClient(strings.NewReader(received+"\n"), &out, NewStandard(), discard())
			for range c.Receive() {
			}
			err := c.Send(tc.msg(t))
			got := lines(out.String())
			switch {
			case tc.want == "" && (err == nil || len(got) != 0):
				t.Errorf("Send = %v and wrote %q, want an error and nothing written", err, got)
			case tc.want != "" && (err != nil || len(got) != 1 || !sameJSON(t, got[0], tc.want)):
				t.Errorf("Send = %v and wrote %q, want %s", err, got, tc.want)
			}
		})
	}
}
