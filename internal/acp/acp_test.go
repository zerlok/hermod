package acp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/session"
)

// airTable is the table air gives for Air's own methods.
var airTable = map[string]session.Kind{
	"session/close":    session.Suspend,
	"_session/archive": session.Close,
}

type side int

const (
	client side = iota
	adapter
)

func (s side) conn(in io.Reader, out io.Writer, methods map[string]session.Kind) *Conn {
	if s == adapter {
		return NewAdapter(in, out)
	}
	return NewClient(in, out, methods)
}

// receive is every message a connection reads from in, and why it ended.
func receive(s side, methods map[string]session.Kind, in io.Reader) ([]session.Message, error) {
	c := s.conn(in, io.Discard, methods)
	var got []session.Message
	for msg := range c.Receive() {
		got = append(got, msg)
	}
	return got, c.Err()
}

// one is the one message a line reads as.
func one(t *testing.T, s side, methods map[string]session.Kind, line string) session.Message {
	t.Helper()
	got, err := receive(s, methods, strings.NewReader(line+"\n"))
	if err != nil || len(got) != 1 {
		t.Fatalf("reading %s: %d messages, error %v", line, len(got), err)
	}
	return got[0]
}

// written is what a connection writes for msg.
func written(t *testing.T, msg session.Message) string {
	t.Helper()
	var out bytes.Buffer
	if err := NewClient(strings.NewReader(""), &out, nil).Send(msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	return out.String()
}

// sameJSON reports whether two documents say the same, however spaced.
func sameJSON(t *testing.T, got, want string) bool {
	t.Helper()
	var a, b any
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatalf("the expected document is no JSON: %v", err)
	}
	return json.Unmarshal([]byte(got), &a) == nil && reflect.DeepEqual(a, b)
}

func TestKind(t *testing.T) {
	cases := []struct {
		name     string
		side     side
		line     string
		withAir  session.Kind
		without  session.Kind
		wantID   session.MessageID
		wantChat session.ChatID
		wantDir  session.Path
	}{
		{"session/new", client, `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/p","mcpServers":[]}}`, session.Open, session.Open, "1", "", "/p"},
		{"session/load", client, `{"jsonrpc":"2.0","id":2,"method":"session/load","params":{"sessionId":"s1","cwd":"/p","mcpServers":[]}}`, session.Open, session.Open, "2", "s1", "/p"},
		{"session/resume", client, `{"jsonrpc":"2.0","id":3,"method":"session/resume","params":{"sessionId":"s1","cwd":"/p"}}`, session.Open, session.Open, "3", "s1", "/p"},
		{"session/close", client, `{"jsonrpc":"2.0","id":4,"method":"session/close","params":{"sessionId":"s1"}}`, session.Suspend, session.Pass, "4", "s1", ""},
		{"_session/archive", client, `{"jsonrpc":"2.0","id":5,"method":"_session/archive","params":{"sessionId":"s1"}}`, session.Close, session.Pass, "5", "s1", ""},
		{"_session/unarchive", client, `{"jsonrpc":"2.0","id":6,"method":"_session/unarchive","params":{"sessionId":"s1"}}`, session.Pass, session.Pass, "6", "s1", ""},
		{"initialize", client, `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":1}}`, session.Connect, session.Connect, "0", "", ""},
		{"session/list", client, `{"jsonrpc":"2.0","id":7,"method":"session/list","params":{"cwd":"/p"}}`, session.List, session.List, "7", "", "/p"},
		{"$/cancel_request of a number", client, `{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":8}}`, session.Cancel, session.Cancel, "8", "", ""},
		{"$/cancel_request of a string", client, `{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":"hermod-1"}}`, session.Cancel, session.Cancel, "hermod-1", "", ""},
		{"a question from the adapter", adapter, `{"jsonrpc":"2.0","id":0,"method":"session/request_permission","params":{"sessionId":"s1"}}`, session.Question, session.Question, "0", "s1", ""},
		{"the client's answer to it", client, `{"jsonrpc":"2.0","id":"hermod-1","result":{"outcome":{"outcome":"cancelled"}}}`, session.Answer, session.Answer, "hermod-1", "", ""},
		{"the client's error answer", client, `{"jsonrpc":"2.0","id":"hermod-2","error":{"code":-32603,"message":"no"}}`, session.Answer, session.Answer, "hermod-2", "", ""},
		{"a prompt", client, `{"jsonrpc":"2.0","id":9,"method":"session/prompt","params":{"sessionId":"s1","prompt":[]}}`, session.Pass, session.Pass, "9", "s1", ""},
		{"a notification of the client", client, `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s1"}}`, session.Pass, session.Pass, "", "s1", ""},
		{"a standard method the table names", client, `{"jsonrpc":"2.0","id":10,"method":"initialize","params":{}}`, session.Connect, session.Connect, "10", "", ""},
		{"an update from the adapter", adapter, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{}}}`, session.Pass, session.Pass, "", "s1", ""},
		{"the adapter's answer naming a new chat", adapter, `{"jsonrpc":"2.0","id":1,"result":{"sessionId":"s2"}}`, session.Pass, session.Pass, "1", "s2", ""},
		{"the adapter's cancel of its question", adapter, `{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":0}}`, session.Cancel, session.Cancel, "0", "", ""},
		{"a method the table names, from the adapter", adapter, `{"jsonrpc":"2.0","method":"session/close","params":{"sessionId":"s1"}}`, session.Pass, session.Pass, "", "s1", ""},
	}
	tables := []struct {
		name    string
		methods map[string]session.Kind
		want    func(withAir, without session.Kind) session.Kind
	}{
		{"with Air's table", withStandardOverridden(airTable), func(withAir, _ session.Kind) session.Kind { return withAir }},
		{"without a table", nil, func(_, without session.Kind) session.Kind { return without }},
	}
	for _, table := range tables {
		for _, tc := range cases {
			t.Run(table.name+"/"+tc.name, func(t *testing.T) {
				msg := one(t, tc.side, table.methods, tc.line)
				if got, want := msg.Kind(), table.want(tc.withAir, tc.without); got != want {
					t.Errorf("Kind = %s, want %s", got, want)
				}
				if msg.ID() != tc.wantID || msg.Chat() != tc.wantChat || msg.Dir() != tc.wantDir {
					t.Errorf("ID, Chat, Dir = %q, %q, %q, want %q, %q, %q", msg.ID(), msg.Chat(), msg.Dir(), tc.wantID, tc.wantChat, tc.wantDir)
				}
			})
		}
	}
}

// withStandardOverridden is table with a standard method given another Kind,
// which acp's own must win over.
func withStandardOverridden(table map[string]session.Kind) map[string]session.Kind {
	out := map[string]session.Kind{"initialize": session.Close}
	for method, kind := range table {
		out[method] = kind
	}
	return out
}

func TestReplyAndFail(t *testing.T) {
	updated := time.Date(2026, 11, 24, 10, 30, 0, 0, time.FixedZone("CET", 3600))
	handshake, err := agent.NewHandshake(1, []byte(`{"loadSession":true}`))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		request string
		result  session.Result // nil to Fail with failure
		failure error
		want    string // "" for nothing written
	}{
		{"empty", `{"jsonrpc":"2.0","id":4,"method":"session/close","params":{"sessionId":"s1"}}`, session.EmptyResult{}, nil,
			`{"jsonrpc":"2.0","id":4,"result":{}}`},
		{"empty under a string ID", `{"jsonrpc":"2.0","id":"a","method":"session/close","params":{"sessionId":"s1"}}`, session.EmptyResult{}, nil,
			`{"jsonrpc":"2.0","id":"a","result":{}}`},
		{"a handshake", `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":1}}`, session.HandshakeResult{Handshake: handshake}, nil,
			`{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":true},"authMethods":[]}}`},
		{"the zero handshake", `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{}}`, session.HandshakeResult{}, nil,
			`{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":0,"agentCapabilities":{},"authMethods":[]}}`},
		{"a list", `{"jsonrpc":"2.0","id":7,"method":"session/list","params":{"cwd":"/p"}}`, session.ListResult{Conversations: []agent.Conversation{
			{ID: "s1", Dir: "/p", State: agent.Running, Title: "fix it", Updated: updated},
			{ID: "s2", Dir: "/p", State: agent.Stopped},
		}}, nil,
			`{"jsonrpc":"2.0","id":7,"result":{"sessions":[{"sessionId":"s1","cwd":"/p","title":"fix it","updatedAt":"2026-11-24T09:30:00Z"},{"sessionId":"s2","cwd":"/p"}]}}`},
		{"an empty list", `{"jsonrpc":"2.0","id":7,"method":"session/list","params":{"cwd":"/p"}}`, session.ListResult{}, nil,
			`{"jsonrpc":"2.0","id":7,"result":{"sessions":[]}}`},
		{"a failure", `{"jsonrpc":"2.0","id":9,"method":"session/prompt","params":{"sessionId":"s1"}}`, nil, errors.New("no chat"),
			`{"jsonrpc":"2.0","id":9,"error":{"code":-32603,"message":"no chat"}}`},
		{"a reply to a notification", `{"jsonrpc":"2.0","method":"session/close","params":{"sessionId":"s1"}}`, session.EmptyResult{}, nil, ""},
		{"a failure of a notification", `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s1"}}`, nil, errors.New("no chat"), ""},
		{"a reply to an answer", `{"jsonrpc":"2.0","id":"hermod-1","result":{}}`, session.EmptyResult{}, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := one(t, client, airTable, tc.request)
			var answer session.Message
			if tc.result != nil {
				answer = msg.Reply(tc.result)
			} else {
				answer = msg.Fail(tc.failure)
			}
			got := written(t, answer)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("wrote %q, want nothing", got)
				}
				return
			}
			if strings.Count(got, "\n") != 1 || !strings.HasSuffix(got, "\n") || !sameJSON(t, got, tc.want) {
				t.Errorf("wrote %q, want %s on one line", got, tc.want)
			}
		})
	}
}

func TestIDs(t *testing.T) {
	cases := []struct {
		name   string
		side   side
		line   string
		wantID session.MessageID
		newID  session.MessageID
		want   string // the message under newID
	}{
		{"a number", adapter, `{"jsonrpc":"2.0","id":7,"method":"fs/read_text_file","params":{}}`, "7", "hermod-1",
			`{"jsonrpc":"2.0","id":"hermod-1","method":"fs/read_text_file","params":{}}`},
		{"zero", adapter, `{"jsonrpc":"2.0","id":0,"method":"fs/read_text_file","params":{}}`, "0", "0",
			`{"jsonrpc":"2.0","id":0,"method":"fs/read_text_file","params":{}}`},
		{"a string", client, `{"jsonrpc":"2.0","id":"hermod-1","result":{}}`, "hermod-1", "7",
			`{"jsonrpc":"2.0","id":7,"result":{}}`},
		{"a string of digits keeps its type", adapter, `{"jsonrpc":"2.0","id":"7","method":"x","params":{}}`, `"7"`, `"7"`,
			`{"jsonrpc":"2.0","id":"7","method":"x","params":{}}`},
		{"a string of a quoted string keeps its quotes", adapter, `{"jsonrpc":"2.0","id":"\"x\"","method":"x","params":{}}`, `"\"x\""`, `"\"x\""`,
			`{"jsonrpc":"2.0","id":"\"x\"","method":"x","params":{}}`},
		{"a string reading as another JSON value", adapter, `{"jsonrpc":"2.0","id":"null","method":"x","params":{}}`, "null", "null",
			`{"jsonrpc":"2.0","id":"null","method":"x","params":{}}`},
		{"an empty string is an ID, not none", adapter, `{"jsonrpc":"2.0","id":"","method":"session/request_permission","params":{}}`, `""`, `""`,
			`{"jsonrpc":"2.0","id":"","method":"session/request_permission","params":{}}`},
		{"an empty string renamed", client, `{"jsonrpc":"2.0","id":"","method":"session/prompt","params":{}}`, `""`, "hermod-3",
			`{"jsonrpc":"2.0","id":"hermod-3","method":"session/prompt","params":{}}`},
		{"a cancel renames the request it cancels", adapter, `{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":3}}`, "3", "hermod-2",
			`{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":"hermod-2"}}`},
		{"a notification has none", client, `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s1"}}`, "", "",
			`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s1"}}`},
		{"a null ID is none", client, `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse"}}`, "", "",
			`{"jsonrpc":"2.0","error":{"code":-32700,"message":"parse"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := one(t, tc.side, nil, tc.line)
			if msg.ID() != tc.wantID {
				t.Errorf("ID = %q, want %q", msg.ID(), tc.wantID)
			}
			renamed := msg.WithID(tc.newID)
			if got := written(t, renamed); !sameJSON(t, got, tc.want) {
				t.Errorf("under %q wrote %s, want %s", tc.newID, got, tc.want)
			}
			if renamed.ID() != tc.newID || renamed.Kind() != msg.Kind() {
				t.Errorf("renamed: ID %q kind %s, want %q kind %s", renamed.ID(), renamed.Kind(), tc.newID, msg.Kind())
			}
			if got := written(t, msg); !sameJSON(t, got, tc.line) {
				t.Errorf("the original changed: wrote %s, want %s", got, tc.line)
			}
		})
	}
}

func TestMapped(t *testing.T) {
	m, err := pathmap.New(pathmap.Place{Local: "/home/me/p", Remote: "/home/u/p"})
	if err != nil {
		t.Fatal(err)
	}
	toRemote := func(p session.Path) session.Path { return session.Path(m.ToRemote(string(p))) }
	toLocal := func(p session.Path) session.Path { return session.Path(m.ToLocal(string(p))) }
	cases := []struct {
		name    string
		side    side
		path    func(session.Path) session.Path
		line    string
		want    string
		wantDir session.Path
	}{
		{"session/new cwd", client, toRemote,
			`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/home/me/p","mcpServers":[]}}`,
			`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/home/u/p","mcpServers":[]}}`, "/home/u/p"},
		{"session/load cwd", client, toRemote,
			`{"jsonrpc":"2.0","id":2,"method":"session/load","params":{"sessionId":"s1","cwd":"/home/me/p/sub","mcpServers":[]}}`,
			`{"jsonrpc":"2.0","id":2,"method":"session/load","params":{"sessionId":"s1","cwd":"/home/u/p/sub","mcpServers":[]}}`, "/home/u/p/sub"},
		{"session/resume additional directories", client, toRemote,
			`{"jsonrpc":"2.0","id":3,"method":"session/resume","params":{"sessionId":"s1","cwd":"/home/me/p","additionalDirectories":["/home/me/p/a","/elsewhere"]}}`,
			`{"jsonrpc":"2.0","id":3,"method":"session/resume","params":{"sessionId":"s1","cwd":"/home/u/p","additionalDirectories":["/home/u/p/a","/elsewhere"]}}`, "/home/u/p"},
		{"session/list cwd", client, toRemote,
			`{"jsonrpc":"2.0","id":7,"method":"session/list","params":{"cwd":"/home/me/p"}}`,
			`{"jsonrpc":"2.0","id":7,"method":"session/list","params":{"cwd":"/home/u/p"}}`, "/home/u/p"},
		{"a prompt's resource link", client, toRemote,
			`{"jsonrpc":"2.0","id":9,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"resource_link","uri":"file:///home/me/p/a.go","name":"a.go"}]}}`,
			`{"jsonrpc":"2.0","id":9,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"resource_link","uri":"file:///home/u/p/a.go","name":"a.go"}]}}`, ""},
		{"a prompt's embedded resource", client, toRemote,
			`{"jsonrpc":"2.0","id":9,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"resource","resource":{"uri":"file:///home/me/p/my%20b.go","text":"/home/me/p stays in prose"}}]}}`,
			`{"jsonrpc":"2.0","id":9,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"resource","resource":{"uri":"file:///home/u/p/my%20b.go","text":"/home/me/p stays in prose"}}]}}`, ""},
		{"a file URI of localhost", client, toRemote,
			`{"jsonrpc":"2.0","id":9,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"resource_link","uri":"file://localhost/home/me/p/a.go","name":"a.go"}]}}`,
			`{"jsonrpc":"2.0","id":9,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"resource_link","uri":"file://localhost/home/u/p/a.go","name":"a.go"}]}}`, ""},
		{"file URIs under no root, of another host or unparsed stay", client, toRemote,
			`{"jsonrpc":"2.0","id":9,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"resource_link","uri":"file:///elsewhere/a.go","name":"a"},{"type":"resource_link","uri":"file://box/home/me/p/a.go","name":"b"},{"type":"resource_link","uri":"file:///home/me/p/%zz","name":"c"}]}}`,
			`{"jsonrpc":"2.0","id":9,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"resource_link","uri":"file:///elsewhere/a.go","name":"a"},{"type":"resource_link","uri":"file://box/home/me/p/a.go","name":"b"},{"type":"resource_link","uri":"file:///home/me/p/%zz","name":"c"}]}}`, ""},
		{"an update's file URI to the client", adapter, toLocal,
			`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","content":[{"type":"content","content":{"type":"resource_link","uri":"file:///home/u/p/a.go","name":"a.go"}}]}}}`,
			`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","content":[{"type":"content","content":{"type":"resource_link","uri":"file:///home/me/p/a.go","name":"a.go"}}]}}}`, ""},
		{"a question's path", adapter, toRemote,
			`{"jsonrpc":"2.0","id":0,"method":"fs/read_text_file","params":{"sessionId":"s1","path":"/home/me/p/a.go"}}`,
			`{"jsonrpc":"2.0","id":0,"method":"fs/read_text_file","params":{"sessionId":"s1","path":"/home/u/p/a.go"}}`, ""},
		{"an update's tool call locations", adapter, toRemote,
			`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","locations":[{"path":"/home/me/p/a.go","line":3}],"content":[{"type":"diff","path":"/home/me/p/a.go","oldText":"a","newText":"b"}]}}}`,
			`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","locations":[{"path":"/home/u/p/a.go","line":3}],"content":[{"type":"diff","path":"/home/u/p/a.go","oldText":"a","newText":"b"}]}}}`, ""},
		{"an answer's result", adapter, toRemote,
			`{"jsonrpc":"2.0","id":7,"result":{"sessions":[{"sessionId":"s1","cwd":"/home/me/p"}]}}`,
			`{"jsonrpc":"2.0","id":7,"result":{"sessions":[{"sessionId":"s1","cwd":"/home/u/p"}]}}`, ""},
		{"an error's data", client, toRemote,
			`{"jsonrpc":"2.0","id":"hermod-1","error":{"code":-32603,"message":"failed","data":{"path":"/home/me/p/a.go"}}}`,
			`{"jsonrpc":"2.0","id":"hermod-1","error":{"code":-32603,"message":"failed","data":{"path":"/home/u/p/a.go"}}}`, ""},
		{"a method and an ID that look like paths stay", adapter, toRemote,
			`{"jsonrpc":"2.0","id":"/home/me/p","method":"/home/me/p","params":{}}`,
			`{"jsonrpc":"2.0","id":"/home/me/p","method":"/home/me/p","params":{}}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := one(t, tc.side, nil, tc.line)
			mapped := msg.Mapped(tc.path)
			if got := written(t, mapped); !sameJSON(t, got, tc.want) {
				t.Errorf("mapped to %s, want %s", got, tc.want)
			}
			if mapped.Dir() != tc.wantDir {
				t.Errorf("Dir = %q, want %q", mapped.Dir(), tc.wantDir)
			}
			if got := written(t, msg); !sameJSON(t, got, tc.line) {
				t.Errorf("the original changed: wrote %s, want %s", got, tc.line)
			}
		})
	}
}

func TestWithoutClientCapabilities(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{"fs and terminal dropped, the rest kept",
			`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true,"writeTextFile":true},"terminal":true,"_meta":{"jetbrains":{"air":true}}}}}`,
			`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{"_meta":{"jetbrains":{"air":true}}}}}`},
		{"no client capabilities",
			`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":1}}`,
			`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":1}}`},
		{"another message is kept",
			`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/p","clientCapabilities":{"fs":{}}}}`,
			`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/p","clientCapabilities":{"fs":{}}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := one(t, client, nil, tc.line)
			if got := written(t, msg.WithoutClientCapabilities()); !sameJSON(t, got, tc.want) {
				t.Errorf("wrote %s, want %s", got, tc.want)
			}
			if got := written(t, msg); !sameJSON(t, got, tc.line) {
				t.Errorf("the original changed: wrote %s, want %s", got, tc.line)
			}
		})
	}
}

func TestHandshake(t *testing.T) {
	cases := []struct {
		name        string
		line        string
		wantOK      bool
		wantVersion int
		wantCaps    string
	}{
		{"an answer to initialize", `{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":true},"authMethods":[]}}`, true, 1, `{"loadSession":true}`},
		{"capabilities left out", `{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":2}}`, true, 2, `{}`},
		{"no version", `{"jsonrpc":"2.0","id":0,"result":{"agentCapabilities":{}}}`, false, 0, `{}`},
		{"capabilities that are no object", `{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":1,"agentCapabilities":[]}}`, false, 0, `{}`},
		{"an error answer", `{"jsonrpc":"2.0","id":0,"error":{"code":-32603,"message":"no"}}`, false, 0, `{}`},
		{"a request", `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":1}}`, false, 0, `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, ok := one(t, adapter, nil, tc.line).Handshake()
			if ok != tc.wantOK || h.Version() != tc.wantVersion || !sameJSON(t, string(h.Capabilities()), tc.wantCaps) {
				t.Errorf("Handshake = %d %s %v, want %d %s %v", h.Version(), h.Capabilities(), ok, tc.wantVersion, tc.wantCaps, tc.wantOK)
			}
		})
	}
}

func TestConnectionEnds(t *testing.T) {
	broken := errors.New("broken pipe")
	request := `{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"s1"}}`
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
		{"a line that is no JSON", func() io.Reader { return strings.NewReader(request + "\nhello\n" + request + "\n") }, false, 1, "not a message"},
		{"a member that is no message", func() io.Reader { return strings.NewReader(`{"jsonrpc":"2.0","id":1}` + "\n") }, false, 0, "neither"},
		{"closed by its owner", func() io.Reader { return strings.NewReader(request + "\n") }, true, 0, ""},
		{"closed by its owner, then its input fails", func() io.Reader { return iotest.ErrReader(broken) }, true, 0, ""},
		{"closed by its owner, then its input is cut", func() io.Reader { return strings.NewReader(request[:20]) }, true, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClient(tc.in(), io.Discard, nil)
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

// foreign is a message no Conn made.
type foreign struct{ session.Message }

func TestSendRefusesForeignMessages(t *testing.T) {
	cases := []struct {
		name string
		msg  session.Message
	}{
		{"another implementation", foreign{}},
		{"nil", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := NewClient(strings.NewReader(""), &out, nil).Send(tc.msg); err == nil || out.Len() != 0 {
				t.Errorf("Send = %v and wrote %q, want an error and nothing written", err, out.String())
			}
		})
	}
}
