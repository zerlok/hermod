package acp

import (
	"errors"
	"testing"
	"time"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/session"
)

func TestIDs(t *testing.T) {
	cases := []struct {
		name   string
		from   Peer
		asked  string
		line   string
		wantID session.MessageID
		newID  session.MessageID
		want   string // the message under newID
	}{
		{"a number", Agent, "",
			`{"jsonrpc":"2.0","id":7,"method":"terminal/kill","params":{"sessionId":"s1","terminalId":"t"}}`, num(7), text("hermod-1"),
			`{"jsonrpc":"2.0","id":"hermod-1","method":"terminal/kill","params":{"sessionId":"s1","terminalId":"t"}}`},
		{"zero", Agent, "",
			`{"jsonrpc":"2.0","id":0,"method":"terminal/kill","params":{"sessionId":"s1","terminalId":"t"}}`, num(0), num(0),
			`{"jsonrpc":"2.0","id":0,"method":"terminal/kill","params":{"sessionId":"s1","terminalId":"t"}}`},
		{"a negative number", Agent, "",
			`{"jsonrpc":"2.0","id":-3,"method":"terminal/kill","params":{"sessionId":"s1","terminalId":"t"}}`, num(-3), num(-3),
			`{"jsonrpc":"2.0","id":-3,"method":"terminal/kill","params":{"sessionId":"s1","terminalId":"t"}}`},
		{"a string back to a number", Client, "fs/write_text_file",
			`{"jsonrpc":"2.0","id":"hermod-1","result":{}}`, text("hermod-1"), num(7),
			`{"jsonrpc":"2.0","id":7,"result":{}}`},
		{"a string of digits is no number", Agent, "",
			`{"jsonrpc":"2.0","id":"7","method":"terminal/kill","params":{"sessionId":"s1","terminalId":"t"}}`, text("7"), text("7"),
			`{"jsonrpc":"2.0","id":"7","method":"terminal/kill","params":{"sessionId":"s1","terminalId":"t"}}`},
		{"a string reading as JSON stays a string", Agent, "",
			`{"jsonrpc":"2.0","id":"null","method":"terminal/kill","params":{"sessionId":"s1","terminalId":"t"}}`, text("null"), text("\"x\""),
			`{"jsonrpc":"2.0","id":"\"x\"","method":"terminal/kill","params":{"sessionId":"s1","terminalId":"t"}}`},
		{"an empty string is an ID", Client, "",
			`{"jsonrpc":"2.0","id":"","method":"session/list","params":{}}`, text(""), text("hermod-3"),
			`{"jsonrpc":"2.0","id":"hermod-3","method":"session/list","params":{}}`},
		{"a cancel renames the request it cancels", Agent, "",
			`{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":3}}`, num(3), text("hermod-2"),
			`{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":"hermod-2"}}`},
		{"a notification has none", Client, "",
			`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s1"}}`, session.MessageID{}, text("x"),
			`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s1"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := read(t, tc.from, tc.asked, tc.line)
			if msg.ID() != tc.wantID {
				t.Errorf("ID = %s, want %s", msg.ID(), tc.wantID)
			}
			renamed := msg.WithID(tc.newID)
			if got := written(t, renamed); !sameJSON(t, got, tc.want) {
				t.Errorf("under %s wrote %s, want %s", tc.newID, got, tc.want)
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
		from    Peer
		asked   string
		path    func(session.Path) session.Path
		line    string
		want    string
		wantDir session.Path
	}{
		{"session/new's directories and an MCP server's command", Client, "", toRemote,
			`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/home/me/p","additionalDirectories":["/home/me/p/a","/elsewhere"],"mcpServers":[{"name":"io","command":"/home/me/p/bin/mcp","args":["/home/me/p"],"env":[]}]}}`,
			`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/home/u/p","additionalDirectories":["/home/u/p/a","/elsewhere"],"mcpServers":[{"name":"io","command":"/home/u/p/bin/mcp","args":["/home/me/p"],"env":[]}]}}`, "/home/u/p"},
		{"session/load's directories", Client, "", toRemote,
			`{"jsonrpc":"2.0","id":2,"method":"session/load","params":{"sessionId":"s1","cwd":"/home/me/p/sub","additionalDirectories":["/home/me/p/a"],"mcpServers":[]}}`,
			`{"jsonrpc":"2.0","id":2,"method":"session/load","params":{"sessionId":"s1","cwd":"/home/u/p/sub","additionalDirectories":["/home/u/p/a"],"mcpServers":[]}}`, "/home/u/p/sub"},
		{"session/resume's directories", Client, "", toRemote,
			`{"jsonrpc":"2.0","id":3,"method":"session/resume","params":{"sessionId":"s1","cwd":"/home/me/p","additionalDirectories":["/home/me/p/a"]}}`,
			`{"jsonrpc":"2.0","id":3,"method":"session/resume","params":{"sessionId":"s1","cwd":"/home/u/p","additionalDirectories":["/home/u/p/a"]}}`, "/home/u/p"},
		{"session/list's directory", Client, "", toRemote,
			`{"jsonrpc":"2.0","id":7,"method":"session/list","params":{"cwd":"/home/me/p"}}`,
			`{"jsonrpc":"2.0","id":7,"method":"session/list","params":{"cwd":"/home/u/p"}}`, "/home/u/p"},
		{"a prompt's file URIs, its prose left as written", Client, "", toRemote,
			`{"jsonrpc":"2.0","id":9,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"text","text":"/home/me/p"},{"type":"image","data":"x","mimeType":"image/png","uri":"file:///home/me/p/a.png"},{"type":"resource_link","uri":"file:///home/me/p/a.go","name":"/home/me/p/a.go"},{"type":"resource","resource":{"uri":"file:///home/me/p/my%20b.go","text":"/home/me/p stays in prose"}},{"type":"resource","resource":{"uri":"file://localhost/home/me/p/c.bin","blob":"YQ=="}}]}}`,
			`{"jsonrpc":"2.0","id":9,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"text","text":"/home/me/p"},{"type":"image","data":"x","mimeType":"image/png","uri":"file:///home/u/p/a.png"},{"type":"resource_link","uri":"file:///home/u/p/a.go","name":"/home/me/p/a.go"},{"type":"resource","resource":{"uri":"file:///home/u/p/my%20b.go","text":"/home/me/p stays in prose"}},{"type":"resource","resource":{"uri":"file://localhost/home/u/p/c.bin","blob":"YQ=="}}]}}`, ""},
		{"URIs under no root, of another host, of another scheme or unparsed stay", Client, "", toRemote,
			`{"jsonrpc":"2.0","id":9,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"resource_link","uri":"file:///elsewhere/a.go","name":"a"},{"type":"resource_link","uri":"file://box/home/me/p/a.go","name":"b"},{"type":"resource_link","uri":"file:///home/me/p/%zz","name":"c"},{"type":"resource_link","uri":"https://x/home/me/p","name":"d"}]}}`,
			`{"jsonrpc":"2.0","id":9,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"resource_link","uri":"file:///elsewhere/a.go","name":"a"},{"type":"resource_link","uri":"file://box/home/me/p/a.go","name":"b"},{"type":"resource_link","uri":"file:///home/me/p/%zz","name":"c"},{"type":"resource_link","uri":"https://x/home/me/p","name":"d"}]}}`, ""},
		{"a tool call's locations, diff, raw input and output", Agent, "", toLocal,
			`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"t1","title":"/home/u/p/a.go","locations":[{"path":"/home/u/p/a.go","line":3}],"content":[{"type":"diff","path":"/home/u/p/a.go","oldText":"/home/u/p","newText":"b"},{"type":"content","content":{"type":"resource_link","uri":"file:///home/u/p/a.go","name":"a.go"}}],"rawInput":{"planFilePath":"/home/u/p/plan.md","n":[1,"/home/u/p/b"]},"rawOutput":"/home/u/p/c"}}}`,
			`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"t1","title":"/home/u/p/a.go","locations":[{"path":"/home/me/p/a.go","line":3}],"content":[{"type":"diff","path":"/home/me/p/a.go","oldText":"/home/u/p","newText":"b"},{"type":"content","content":{"type":"resource_link","uri":"file:///home/me/p/a.go","name":"a.go"}}],"rawInput":{"planFilePath":"/home/me/p/plan.md","n":[1,"/home/me/p/b"]},"rawOutput":"/home/me/p/c"}}}`, ""},
		{"a plan's file", Agent, "", toLocal,
			`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"plan_update","plan":{"type":"markdown","planId":"p1","content":"x","path":"/home/u/p/plan.md"}}}}`,
			`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"plan_update","plan":{"type":"markdown","planId":"p1","content":"x","path":"/home/me/p/plan.md"}}}}`, ""},
		{"a child session's directories, given or cleared", Agent, "", toLocal,
			`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"subagent_update","sessionId":"c1","cwd":"/home/u/p/sub","additionalDirectories":null}}}`,
			`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"subagent_update","sessionId":"c1","cwd":"/home/me/p/sub","additionalDirectories":null}}}`, ""},
		{"a child session's directories in a list", Agent, "", toLocal,
			`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"subagent_update","sessionId":"c1","additionalDirectories":["/home/u/p/a"]}}}`,
			`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"subagent_update","sessionId":"c1","additionalDirectories":["/home/me/p/a"]}}}`, ""},
		{"a question's file and a terminal's directory", Agent, "", toRemote,
			`{"jsonrpc":"2.0","id":0,"method":"terminal/create","params":{"sessionId":"s1","command":"/home/me/p/run","cwd":"/home/me/p"}}`,
			`{"jsonrpc":"2.0","id":0,"method":"terminal/create","params":{"sessionId":"s1","command":"/home/me/p/run","cwd":"/home/u/p"}}`, ""},
		{"a question's path", Agent, "", toRemote,
			`{"jsonrpc":"2.0","id":0,"method":"fs/read_text_file","params":{"sessionId":"s1","path":"/home/me/p/a.go"}}`,
			`{"jsonrpc":"2.0","id":0,"method":"fs/read_text_file","params":{"sessionId":"s1","path":"/home/u/p/a.go"}}`, ""},
		{"an answer's sessions", Agent, "session/list", toLocal,
			`{"jsonrpc":"2.0","id":7,"result":{"sessions":[{"sessionId":"s1","cwd":"/home/u/p","additionalDirectories":["/home/u/p/a"]}]}}`,
			`{"jsonrpc":"2.0","id":7,"result":{"sessions":[{"sessionId":"s1","cwd":"/home/me/p","additionalDirectories":["/home/me/p/a"]}]}}`, ""},
		{"an error's data", Client, "fs/read_text_file", toRemote,
			`{"jsonrpc":"2.0","id":"hermod-1","error":{"code":-32603,"message":"/home/me/p/a.go","data":{"path":"/home/me/p/a.go"}}}`,
			`{"jsonrpc":"2.0","id":"hermod-1","error":{"code":-32603,"message":"/home/me/p/a.go","data":{"path":"/home/u/p/a.go"}}}`, ""},
		{"every string of _meta, at any depth, and no key", Client, "", toRemote,
			`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":1,"_meta":{"jetbrains":{"air":{"/home/me/p":"/home/me/p/x","views":["file:///home/me/p/v",2,true,null]}}}}}`,
			`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":1,"_meta":{"jetbrains":{"air":{"/home/me/p":"/home/u/p/x","views":["file:///home/u/p/v",2,true,null]}}}}}`, ""},
		{"a nested _meta", Agent, "", toLocal,
			`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"x","_meta":{"file":"/home/u/p/a"}}}}}`,
			`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"x","_meta":{"file":"/home/me/p/a"}}}}}`, ""},
		{"a written file's path, and an ID, a session ID and content that look like paths", Agent, "", toRemote,
			`{"jsonrpc":"2.0","id":"/home/me/p","method":"fs/write_text_file","params":{"sessionId":"/home/me/p","path":"/home/me/p/w","content":"/home/me/p"}}`,
			`{"jsonrpc":"2.0","id":"/home/me/p","method":"fs/write_text_file","params":{"sessionId":"/home/me/p","path":"/home/u/p/w","content":"/home/me/p"}}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := read(t, tc.from, tc.asked, tc.line)
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

func TestReplyAndFail(t *testing.T) {
	updated := time.Date(2026, 11, 24, 10, 30, 0, 0, time.FixedZone("CET", 3600))
	handshake, err := agent.NewHandshake(1, []byte(`{"loadSession":true,"sessionCapabilities":{"list":{}},"_meta":{"k":"v"}}`))
	if err != nil {
		t.Fatal(err)
	}
	// local takes the remote's /r to the client's /l.
	local := func(p session.Path) session.Path {
		if p == "/r" {
			return "/l"
		}
		return p
	}
	cases := []struct {
		name    string
		request string
		result  session.Result // nil to Fail with failure
		failure error
		path    func(session.Path) session.Path // what the answer is Mapped with; nil for none
		want    string
	}{
		{"a handshake", `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":1}}`, session.HandshakeResult{Handshake: handshake}, nil, nil,
			`{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":true,"sessionCapabilities":{"list":{}},"_meta":{"k":"v"}},"authMethods":[]}}`},
		{"the zero handshake", `{"jsonrpc":"2.0","id":"i","method":"initialize","params":{"protocolVersion":1}}`, session.HandshakeResult{}, nil, nil,
			`{"jsonrpc":"2.0","id":"i","result":{"protocolVersion":0,"agentCapabilities":{},"authMethods":[]}}`},
		{"a list", `{"jsonrpc":"2.0","id":7,"method":"session/list","params":{"cwd":"/p"}}`, session.ListResult{Conversations: []agent.Conversation{
			{ID: "s1", Dir: "/p", State: agent.Running, Title: "fix it", Updated: updated},
			{ID: "s2", Dir: "/p", State: agent.Stopped},
		}}, nil, nil,
			`{"jsonrpc":"2.0","id":7,"result":{"sessions":[{"sessionId":"s1","cwd":"/p","title":"fix it","updatedAt":"2026-11-24T09:30:00Z"},{"sessionId":"s2","cwd":"/p"}]}}`},
		{"an empty list", `{"jsonrpc":"2.0","id":7,"method":"session/list","params":{"cwd":"/p"}}`, session.ListResult{}, nil, nil,
			`{"jsonrpc":"2.0","id":7,"result":{"sessions":[]}}`},
		{"a result of another request's", `{"jsonrpc":"2.0","id":7,"method":"session/list","params":{"cwd":"/p"}}`, session.EmptyResult{}, nil, nil,
			`{"jsonrpc":"2.0","id":7,"error":{"code":-32603,"message":"hermod: session/list is not answered with a session.EmptyResult"}}`},
		{"a request hermod does not answer", `{"jsonrpc":"2.0","id":8,"method":"session/close","params":{"sessionId":"s1"}}`, session.EmptyResult{}, nil, nil,
			`{"jsonrpc":"2.0","id":8,"error":{"code":-32603,"message":"hermod: session/close is not answered by hermod"}}`},
		{"a list's directories mapped", `{"jsonrpc":"2.0","id":7,"method":"session/list","params":{"cwd":"/l"}}`, session.ListResult{Conversations: []agent.Conversation{
			{ID: "s1", Dir: "/r", State: agent.Running},
		}}, nil, local,
			`{"jsonrpc":"2.0","id":7,"result":{"sessions":[{"sessionId":"s1","cwd":"/l"}]}}`},
		{"a failure", `{"jsonrpc":"2.0","id":9,"method":"session/prompt","params":{"sessionId":"s1","prompt":[]}}`, nil, errors.New("no chat"), nil,
			`{"jsonrpc":"2.0","id":9,"error":{"code":-32603,"message":"no chat"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := read(t, Client, "", tc.request)
			var answer session.Message
			if tc.result != nil {
				answer = msg.Reply(tc.result)
			} else {
				answer = msg.Fail(tc.failure)
			}
			if tc.path != nil {
				answer = answer.Mapped(tc.path)
			}
			_, method := msg.(requester).asks()
			if got := answering(t, method, answer); !sameJSON(t, got, tc.want) {
				t.Errorf("wrote %s, want %s", got, tc.want)
			}
		})
	}
}

// TestNoAnswer answers what is no request: there is nothing to write.
func TestNoAnswer(t *testing.T) {
	cases := []struct {
		name  string
		from  Peer
		asked string
		line  string
		fail  bool // Fail rather than Reply
	}{
		{"a reply to a notification", Client, "", `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s1"}}`, false},
		{"a failure of a notification", Client, "", `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s1"}}`, true},
		{"a reply to an answer", Client, "fs/write_text_file", `{"jsonrpc":"2.0","id":"hermod-1","result":{}}`, false},
		{"a failure of an answer", Client, "fs/write_text_file", `{"jsonrpc":"2.0","id":"hermod-1","result":{}}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := read(t, tc.from, tc.asked, tc.line)
			answer := msg.Reply(session.EmptyResult{})
			if tc.fail {
				answer = msg.Fail(errors.New("no"))
			}
			if line, err := NewStandard().Encode(answer, asked(tc.asked)); err == nil {
				t.Errorf("Encode = %s, want an error", line)
			}
		})
	}
}

func TestResult(t *testing.T) {
	cases := []struct {
		name    string
		from    Peer
		asked   string
		line    string
		want    bool // the message carries a HandshakeResult
		version int
		caps    string
	}{
		{"the answer to initialize", Agent, "initialize",
			`{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":true,"_meta":{"k":"v"}},"authMethods":[]}}`, true, 1, `{"loadSession":true,"_meta":{"k":"v"}}`},
		{"capabilities left out", Agent, "initialize",
			`{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":2}}`, true, 2, `{}`},
		{"an error answer to initialize", Agent, "initialize",
			`{"jsonrpc":"2.0","id":0,"error":{"code":-32603,"message":"no"}}`, false, 0, ``},
		{"the answer to another request", Agent, "session/new",
			`{"jsonrpc":"2.0","id":0,"result":{"sessionId":"s1"}}`, false, 0, ``},
		{"a request", Client, "",
			`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":1}}`, false, 0, ``},
		{"a notification", Client, "",
			`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s1"}}`, false, 0, ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := read(t, tc.from, tc.asked, tc.line).Result()
			h, ok := result.(session.HandshakeResult)
			if ok != tc.want || (!tc.want && result != nil) {
				t.Fatalf("Result = %#v, want a handshake: %v", result, tc.want)
			}
			if tc.want && (h.Handshake.Version() != tc.version || !sameJSON(t, string(h.Handshake.Capabilities()), tc.caps)) {
				t.Errorf("Result = v%d %s, want v%d %s", h.Handshake.Version(), h.Handshake.Capabilities(), tc.version, tc.caps)
			}
		})
	}
}
