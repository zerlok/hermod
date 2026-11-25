package acp

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/session"
)

// TestMessages reads each of ACP's messages, the params of a request or a
// notification or the result of an answer, into its type, checks what it
// means to a session, and writes it back as it was.
func TestMessages(t *testing.T) {
	cases := []struct {
		name   string
		method string
		result bool // doc is the result of method's answer, not its params
		doc    string
		kind   session.Kind
		chat   session.ChatID
		dir    session.Path
	}{
		{"initialize", "initialize", false,
			`{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true,"writeTextFile":false},"terminal":true,"session":{"compaction":{},"configOptions":{"boolean":{}},"notices":{}},"subagents":{},"plan":{},"auth":{"terminal":true},"elicitation":{"form":{},"url":{}},"_meta":{"jetbrains":{"air":{"sessionIndex":true}}}},"clientInfo":{"name":"air","title":"Air","version":"1.0"},"_meta":{"n":12345678901234567890}}`,
			session.Connect, "", ""},
		{"authenticate", "authenticate", false,
			`{"methodId":"claude-login"}`,
			session.Pass, "", ""},
		{"logout", "logout", false,
			`{}`,
			session.Pass, "", ""},
		{"session/new with every kind of MCP server", "session/new", false,
			`{"cwd":"/p","additionalDirectories":["/q"],"mcpServers":[{"type":"http","name":"h","url":"http://127.0.0.1:1/mcp","headers":[{"name":"Authorization","value":"t"}]},{"type":"sse","name":"s","url":"http://x/sse","headers":[]},{"name":"io","command":"/bin/mcp","args":["-v"],"env":[{"name":"K","value":"V"}]}],"systemPrompt":{"mode":"append","content":[{"type":"text","text":"be brief"}]}}`,
			session.Open, "", "/p"},
		{"session/load", "session/load", false,
			`{"sessionId":"s1","cwd":"/p","mcpServers":[]}`,
			session.Open, "s1", "/p"},
		{"session/resume", "session/resume", false,
			`{"sessionId":"s1","cwd":"/p","additionalDirectories":[]}`,
			session.Open, "s1", "/p"},
		{"session/list of a directory", "session/list", false,
			`{"cwd":"/p","cursor":"c"}`,
			session.List, "", "/p"},
		{"session/list of none", "session/list", false,
			`{}`,
			session.List, "", ""},
		{"session/delete", "session/delete", false,
			`{"sessionId":"s1"}`,
			session.Pass, "s1", ""},
		{"session/close", "session/close", false,
			`{"sessionId":"s1"}`,
			session.Suspend, "s1", ""},
		{"session/set_mode", "session/set_mode", false,
			`{"sessionId":"s1","modeId":"plan"}`,
			session.Pass, "s1", ""},
		{"session/set_config_option of a value", "session/set_config_option", false,
			`{"sessionId":"s1","configId":"model","value":"opus"}`,
			session.Pass, "s1", ""},
		{"session/set_config_option of a boolean", "session/set_config_option", false,
			`{"sessionId":"s1","configId":"fast","type":"boolean","value":true,"_meta":{"k":"v"}}`,
			session.Pass, "s1", ""},
		{"session/prompt with every kind of content", "session/prompt", false,
			`{"sessionId":"s1","prompt":[{"type":"text","text":"a < b & c","annotations":{"audience":["user","assistant"],"lastModified":"2026-11-24","priority":0.5}},{"type":"image","data":"aW1n","mimeType":"image/png","uri":"file:///p/a.png"},{"type":"audio","data":"YXU=","mimeType":"audio/wav"},{"type":"resource_link","uri":"file:///p/a.go","name":"a.go","title":"A","description":"d","mimeType":"text/x-go","size":12},{"type":"resource","resource":{"uri":"file:///p/b.go","text":"package b","mimeType":"text/x-go"}},{"type":"resource","resource":{"uri":"file:///p/c.bin","blob":"YmxvYg=="}}]}`,
			session.Pass, "s1", ""},
		{"session/cancel", "session/cancel", false,
			`{"sessionId":"s1"}`,
			session.Pass, "s1", ""},
		{"fs/read_text_file", "fs/read_text_file", false,
			`{"sessionId":"s1","path":"/p/a.go","line":3,"limit":10}`,
			session.Pass, "s1", ""},
		{"fs/write_text_file", "fs/write_text_file", false,
			`{"sessionId":"s1","path":"/p/a.go","content":"x"}`,
			session.Pass, "s1", ""},
		{"session/request_permission", "session/request_permission", false,
			`{"sessionId":"s1","toolCall":{"toolCallId":"t1","title":"Run","kind":"execute","status":"pending","rawInput":{"command":"ls"}},"options":[{"optionId":"a","name":"Allow","kind":"allow_once"},{"optionId":"b","name":"Always","kind":"allow_always"},{"optionId":"c","name":"Reject","kind":"reject_once"},{"optionId":"d","name":"Never","kind":"reject_always"}]}`,
			session.Pass, "s1", ""},
		{"terminal/create", "terminal/create", false,
			`{"sessionId":"s1","command":"ls","args":["-l"],"env":[{"name":"K","value":"V"}],"cwd":"/p","outputByteLimit":1024}`,
			session.Pass, "s1", ""},
		{"terminal/output", "terminal/output", false,
			`{"sessionId":"s1","terminalId":"term1"}`,
			session.Pass, "s1", ""},
		{"terminal/release", "terminal/release", false,
			`{"sessionId":"s1","terminalId":"term1"}`,
			session.Pass, "s1", ""},
		{"terminal/wait_for_exit", "terminal/wait_for_exit", false,
			`{"sessionId":"s1","terminalId":"term1"}`,
			session.Pass, "s1", ""},
		{"terminal/kill", "terminal/kill", false,
			`{"sessionId":"s1","terminalId":"term1"}`,
			session.Pass, "s1", ""},
		{"elicitation/create by a form of every kind of property", "elicitation/create", false,
			`{"message":"Pick","mode":"form","sessionId":"s1","toolCallId":"t1","requestedSchema":{"type":"object","title":"T","description":"D","properties":{"a":{"type":"string","minLength":1,"maxLength":9,"pattern":"x+","format":"email","default":"x","enum":["x"],"oneOf":[{"const":"x","title":"X","description":"the x"}]},"b":{"type":"number","minimum":0.5,"maximum":9.5,"default":1.5},"c":{"type":"integer","minimum":1,"maximum":3,"default":2},"d":{"type":"boolean","default":true},"e":{"type":"array","minItems":1,"maxItems":2,"items":{"type":"string","enum":["p","q"]},"default":["p"]},"f":{"type":"array","items":{"anyOf":[{"const":"p","title":"P"}]}}},"required":["a"]}}`,
			session.Pass, "s1", ""},
		{"elicitation/create at a URL for a request", "elicitation/create", false,
			`{"message":"Sign in","mode":"url","elicitationId":"e1","url":"https://example.com/login","requestId":3}`,
			session.Pass, "", ""},
		{"elicitation/complete", "elicitation/complete", false,
			`{"elicitationId":"e1"}`,
			session.Pass, "", ""},
		{"user_message_chunk", "session/update", false,
			`{"sessionId":"s1","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"hi"},"messageId":"m1"}}`,
			session.Pass, "s1", ""},
		{"agent_message_chunk", "session/update", false,
			`{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello"}}}`,
			session.Pass, "s1", ""},
		{"agent_thought_chunk", "session/update", false,
			`{"sessionId":"s1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"hmm"}}}`,
			session.Pass, "s1", ""},
		{"tool_call with every kind of content", "session/update", false,
			`{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"t1","title":"Edit","name":"Edit","kind":"edit","status":"in_progress","content":[{"type":"content","content":{"type":"text","text":"x"}},{"type":"diff","path":"/p/a.go","oldText":"a","newText":"b"},{"type":"terminal","terminalId":"term1"},{"type":"session","sessionId":"s2"}],"locations":[{"path":"/p/a.go","line":3}],"rawInput":{"file_path":"/p/a.go","n":12345678901234567890},"rawOutput":null}}`,
			session.Pass, "s1", ""},
		{"tool_call_update clearing its content", "session/update", false,
			`{"sessionId":"s1","update":{"sessionUpdate":"tool_call_update","toolCallId":"t1","status":"completed","title":"done","content":[],"locations":[]}}`,
			session.Pass, "s1", ""},
		{"plan", "session/update", false,
			`{"sessionId":"s1","update":{"sessionUpdate":"plan","entries":[{"content":"one","priority":"high","status":"pending"},{"content":"two","priority":"low","status":"completed"}]}}`,
			session.Pass, "s1", ""},
		{"plan_update of entries", "session/update", false,
			`{"sessionId":"s1","update":{"sessionUpdate":"plan_update","plan":{"type":"items","planId":"p1","entries":[{"content":"one","priority":"medium","status":"in_progress"}]}}}`,
			session.Pass, "s1", ""},
		{"plan_update of markdown", "session/update", false,
			`{"sessionId":"s1","update":{"sessionUpdate":"plan_update","plan":{"type":"markdown","planId":"p1","content":"# plan","path":"/p/plan.md"}}}`,
			session.Pass, "s1", ""},
		{"plan_removed", "session/update", false,
			`{"sessionId":"s1","update":{"sessionUpdate":"plan_removed","planId":"p1"}}`,
			session.Pass, "s1", ""},
		{"available_commands_update", "session/update", false,
			`{"sessionId":"s1","update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"review","description":"Review","input":{"hint":"a branch"}},{"name":"init","description":"Init"}]}}`,
			session.Pass, "s1", ""},
		{"current_mode_update", "session/update", false,
			`{"sessionId":"s1","update":{"sessionUpdate":"current_mode_update","currentModeId":"plan"}}`,
			session.Pass, "s1", ""},
		{"config_option_update", "session/update", false,
			`{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"fast","name":"Fast","type":"boolean","currentValue":true}]}}`,
			session.Pass, "s1", ""},
		{"session_info_update", "session/update", false,
			`{"sessionId":"s1","update":{"sessionUpdate":"session_info_update","title":"Fix it","updatedAt":"2026-11-24T09:30:00Z","_meta":{"jetbrains":{"air":{"loadReplayEnd":true}}}}}`,
			session.Pass, "s1", ""},
		{"session_info_update clearing its title and time", "session/update", false,
			`{"sessionId":"s1","update":{"sessionUpdate":"session_info_update","title":null,"updatedAt":null}}`,
			session.Pass, "s1", ""},
		{"usage_update", "session/update", false,
			`{"sessionId":"s1","update":{"sessionUpdate":"usage_update","used":1000,"size":200000,"cost":{"amount":1.25,"currency":"USD"}}}`,
			session.Pass, "s1", ""},
		{"notice", "session/update", false,
			`{"sessionId":"s1","update":{"sessionUpdate":"notice","severity":"warning","title":"Slow","description":"the model is busy"}}`,
			session.Pass, "s1", ""},
		{"compaction_update", "session/update", false,
			`{"sessionId":"s1","update":{"sessionUpdate":"compaction_update","compactionId":"k1","status":"failed","summary":[{"type":"text","text":"so far"}],"error":"too long"}}`,
			session.Pass, "s1", ""},
		{"compaction_update clearing its summary, error and _meta", "session/update", false,
			`{"sessionId":"s1","update":{"sessionUpdate":"compaction_update","compactionId":"k1","status":"completed","summary":null,"error":null,"_meta":null}}`,
			session.Pass, "s1", ""},
		{"compaction_update patching its _meta", "session/update", false,
			`{"sessionId":"s1","update":{"sessionUpdate":"compaction_update","compactionId":"k1","status":"completed","_meta":{"k":"v"}}}`,
			session.Pass, "s1", ""},
		{"compaction_summary_chunk", "session/update", false,
			`{"sessionId":"s1","update":{"sessionUpdate":"compaction_summary_chunk","compactionId":"k1","content":{"type":"text","text":"so"}}}`,
			session.Pass, "s1", ""},
		{"subagent_update with members cleared", "session/update", false,
			`{"sessionId":"s1","update":{"sessionUpdate":"subagent_update","sessionId":"c1","title":null,"description":"helper","capabilities":{"cancel":{}},"state":{"state":"idle","stopReason":"error","error":{"code":-32603,"message":"boom"}},"cwd":"/p/sub","additionalDirectories":null}}`,
			session.Pass, "s1", ""},
		{"subagent_update of every other state", "session/update", false,
			`{"sessionId":"s1","update":{"sessionUpdate":"subagent_update","sessionId":"c1","state":{"state":"running","_meta":{"k":"v"}}}}`,
			session.Pass, "s1", ""},
		{"session_message", "session/update", false,
			`{"sessionId":"s1","update":{"sessionUpdate":"session_message","messageId":"m2","senderSessionId":"s1","recipientSessionId":null,"senderMessageId":"m1","content":[{"type":"text","text":"x"}]}}`,
			session.Pass, "s1", ""},
		{"session_message removing its _meta", "session/update", false,
			`{"sessionId":"s1","update":{"sessionUpdate":"session_message","messageId":"m2","content":null,"_meta":null}}`,
			session.Pass, "s1", ""},
		{"session_message_chunk", "session/update", false,
			`{"sessionId":"s1","update":{"sessionUpdate":"session_message_chunk","messageId":"m2","content":{"type":"text","text":"x"}}}`,
			session.Pass, "s1", ""},
		{"the answer to initialize", "initialize", true,
			`{"protocolVersion":1,"agentCapabilities":{"loadSession":true,"promptCapabilities":{"image":true,"audio":false,"embeddedContext":true},"mcpCapabilities":{"http":true,"sse":true},"sessionCapabilities":{"list":{},"delete":{},"additionalDirectories":{},"systemPrompt":{"mode":{"override":{}}},"resume":{},"close":{}},"auth":{"logout":{}}},"authMethods":[{"id":"a","name":"Agent"},{"type":"terminal","id":"t","name":"Terminal","description":"d","args":["login"],"env":{"K":"V"}}],"agentInfo":{"name":"claude-agent-acp","version":"0.109.0"},"_meta":{"jetbrains":{"air":{"sessionIndex":true}}}}`,
			session.Pass, "", ""},
		{"the answer to authenticate", "authenticate", true,
			`{}`,
			session.Pass, "", ""},
		{"the answer to logout", "logout", true,
			`{}`,
			session.Pass, "", ""},
		{"the answer to session/new naming its chat", "session/new", true,
			`{"sessionId":"s2","modes":{"currentModeId":"default","availableModes":[{"id":"default","name":"Default","description":"d"}]},"configOptions":[{"id":"model","name":"Model","category":"model","type":"select","currentValue":"opus","options":[{"value":"opus","name":"Opus"}]},{"id":"effort","name":"Effort","description":"how hard","type":"select","currentValue":"high","options":[{"group":"g","name":"G","options":[{"value":"high","name":"High"}]}]},{"id":"fast","name":"Fast","type":"boolean","currentValue":false}]}`,
			session.Pass, "s2", ""},
		{"the answer to session/load", "session/load", true,
			`{"configOptions":[]}`,
			session.Pass, "", ""},
		{"the answer to session/resume", "session/resume", true,
			`{"modes":{"currentModeId":"a","availableModes":[]}}`,
			session.Pass, "", ""},
		{"the answer to session/list", "session/list", true,
			`{"sessions":[{"sessionId":"s1","cwd":"/p","additionalDirectories":["/q"],"title":"t","updatedAt":"2026-11-24T09:30:00Z"}],"nextCursor":"n"}`,
			session.Pass, "", ""},
		{"the answer to session/delete", "session/delete", true,
			`{}`,
			session.Pass, "", ""},
		{"the answer to session/close", "session/close", true,
			`{}`,
			session.Pass, "", ""},
		{"the answer to session/set_mode", "session/set_mode", true,
			`{}`,
			session.Pass, "", ""},
		{"the answer to session/set_config_option", "session/set_config_option", true,
			`{"configOptions":[]}`,
			session.Pass, "", ""},
		{"the answer to session/prompt", "session/prompt", true,
			`{"stopReason":"end_turn"}`,
			session.Pass, "", ""},
		{"the answer to fs/read_text_file", "fs/read_text_file", true,
			`{"content":"text"}`,
			session.Pass, "", ""},
		{"the answer to fs/write_text_file", "fs/write_text_file", true,
			`{}`,
			session.Pass, "", ""},
		{"a cancelled permission", "session/request_permission", true,
			`{"outcome":{"outcome":"cancelled"}}`,
			session.Pass, "", ""},
		{"a selected permission", "session/request_permission", true,
			`{"outcome":{"outcome":"selected","optionId":"a"}}`,
			session.Pass, "", ""},
		{"the answer to terminal/create", "terminal/create", true,
			`{"terminalId":"term1"}`,
			session.Pass, "", ""},
		{"the answer to terminal/output", "terminal/output", true,
			`{"output":"o","truncated":false,"exitStatus":{"exitCode":0}}`,
			session.Pass, "", ""},
		{"the answer to terminal/release", "terminal/release", true,
			`{}`,
			session.Pass, "", ""},
		{"the answer to terminal/wait_for_exit", "terminal/wait_for_exit", true,
			`{"exitCode":1,"signal":"TERM"}`,
			session.Pass, "", ""},
		{"the answer to terminal/kill", "terminal/kill", true,
			`{}`,
			session.Pass, "", ""},
		{"an accepted elicitation", "elicitation/create", true,
			`{"action":"accept","content":{"a":"x","b":1.5,"d":true,"e":["p"]}}`,
			session.Pass, "", ""},
		{"a declined elicitation", "elicitation/create", true,
			`{"action":"decline"}`,
			session.Pass, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := read(t, tc.method, tc.result, tc.doc)
			if msg.Kind() != tc.kind || msg.Chat() != tc.chat || msg.Dir() != tc.dir {
				t.Errorf("Kind, Chat, Dir = %s, %q, %q, want %s, %q, %q", msg.Kind(), msg.Chat(), msg.Dir(), tc.kind, tc.chat, tc.dir)
			}
			if got := written(t, msg); !sameJSON(t, got, tc.doc) {
				t.Errorf("wrote %s, want %s", got, tc.doc)
			}
		})
	}
}

// TestInvalid reads params and results that do not decode, or that their
// type's Validate rejects.
func TestInvalid(t *testing.T) {
	cases := []struct {
		name   string
		method string
		result bool
		doc    string
	}{
		{"a cwd that is no string", "session/new", false, `{"cwd":7,"mcpServers":[]}`},
		{"a required member left out", "session/new", false, `{"cwd":"/p"}`},
		{"a required member of null", "session/load", false, `{"sessionId":null,"cwd":"/p","mcpServers":[]}`},
		{"no params where some are required", "session/prompt", false, `{}`},
		{"a protocol version left out", "initialize", false, `{}`},
		{"a required member deep in a list", "session/new", false, `{"cwd":"/p","mcpServers":[{"name":"io","command":"/bin/mcp","args":[]}]}`},
		{"a content block of an unknown type", "session/prompt", false, `{"sessionId":"s1","prompt":[{"type":"video","data":"x"}]}`},
		{"a content block of no type", "session/prompt", false, `{"sessionId":"s1","prompt":[{"text":"x"}]}`},
		{"a boolean option whose value is no boolean", "session/set_config_option", false, `{"sessionId":"s1","configId":"fast","type":"boolean","value":"yes"}`},
		{"an option with no value", "session/set_config_option", false, `{"sessionId":"s1","configId":"fast"}`},
		{"a form elicitation without its form", "elicitation/create", false, `{"message":"m","mode":"form","sessionId":"s1"}`},
		{"a URL elicitation without its URL", "elicitation/create", false, `{"message":"m","mode":"url","elicitationId":"e1","sessionId":"s1"}`},
		{"an elicitation tied to nothing", "elicitation/create", false, `{"message":"m","mode":"url","elicitationId":"e1","url":"https://x"}`},
		{"an elicitation's schema that is no object", "elicitation/create", false, `{"message":"m","mode":"form","sessionId":"s1","requestedSchema":{"type":"array"}}`},
		{"an update of an unknown kind", "session/update", false, `{"sessionId":"s1","update":{"sessionUpdate":"cursor_update"}}`},
		{"an update left out", "session/update", false, `{"sessionId":"s1"}`},
		{"a tool call of an unknown kind", "session/update", false, `{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"t1","title":"x","kind":"dance"}}`},
		{"a tool call's location with no path", "session/update", false, `{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"t1","title":"x","locations":[{"line":1}]}}`},
		{"a permission whose outcome is left out", "session/request_permission", true, `{}`},
		{"an answer of the wrong type", "session/new", true, `{"stopReason":"end_turn"}`},
		{"a declined elicitation with content", "elicitation/create", true, `{"action":"decline","content":{"a":"x"}}`},
		{"a form value that is an object", "elicitation/create", true, `{"action":"accept","content":{"a":{"b":1}}}`},
		{"a select with no options", "session/set_config_option", true, `{"configOptions":[{"id":"m","name":"M","type":"select","currentValue":"a"}]}`},
		{"a select whose value is a boolean", "session/set_config_option", true, `{"configOptions":[{"id":"m","name":"M","type":"select","currentValue":true,"options":[]}]}`},
		{"a boolean with options", "session/set_config_option", true, `{"configOptions":[{"id":"f","name":"F","type":"boolean","currentValue":true,"options":[]}]}`},
		{"an option of a type Hermod does not read", "session/set_config_option", true, `{"configOptions":[{"id":"f","name":"F","type":"slider","currentValue":1}]}`},
		{"a value of a type Hermod does not read", "session/set_config_option", false, `{"sessionId":"s1","configId":"f","type":"slider","value":"a"}`},
		{"a select's value that is a boolean", "session/set_config_option", false, `{"sessionId":"s1","configId":"f","type":"select","value":true}`},
		{"null", "session/list", false, `null`},
	}
	reg := Standard()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := reg.methods[tc.method]
			readDoc := m.params
			if tc.result {
				readDoc = m.result
			}
			if msg, err := readDoc([]byte(tc.doc)); err == nil {
				t.Errorf("read %v, want an error", msg)
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
		method  string
		result  bool
		path    func(session.Path) session.Path
		doc     string
		want    string
		wantDir session.Path
	}{
		{"session/new's directories and an MCP server's command", "session/new", false, toRemote,
			`{"cwd":"/home/me/p","additionalDirectories":["/home/me/p/a","/elsewhere"],"mcpServers":[{"name":"io","command":"/home/me/p/bin/mcp","args":["/home/me/p"],"env":[]}]}`,
			`{"cwd":"/home/u/p","additionalDirectories":["/home/u/p/a","/elsewhere"],"mcpServers":[{"name":"io","command":"/home/u/p/bin/mcp","args":["/home/me/p"],"env":[]}]}`, "/home/u/p"},
		{"session/load's directories", "session/load", false, toRemote,
			`{"sessionId":"s1","cwd":"/home/me/p/sub","additionalDirectories":["/home/me/p/a"],"mcpServers":[]}`,
			`{"sessionId":"s1","cwd":"/home/u/p/sub","additionalDirectories":["/home/u/p/a"],"mcpServers":[]}`, "/home/u/p/sub"},
		{"session/resume's directories", "session/resume", false, toRemote,
			`{"sessionId":"s1","cwd":"/home/me/p","additionalDirectories":["/home/me/p/a"]}`,
			`{"sessionId":"s1","cwd":"/home/u/p","additionalDirectories":["/home/u/p/a"]}`, "/home/u/p"},
		{"session/list's directory", "session/list", false, toRemote,
			`{"cwd":"/home/me/p"}`,
			`{"cwd":"/home/u/p"}`, "/home/u/p"},
		{"a prompt's file URIs, its prose left as written", "session/prompt", false, toRemote,
			`{"sessionId":"s1","prompt":[{"type":"text","text":"/home/me/p"},{"type":"image","data":"x","mimeType":"image/png","uri":"file:///home/me/p/a.png"},{"type":"resource_link","uri":"file:///home/me/p/a.go","name":"/home/me/p/a.go"},{"type":"resource","resource":{"uri":"file:///home/me/p/my%20b.go","text":"/home/me/p stays in prose"}},{"type":"resource","resource":{"uri":"file://localhost/home/me/p/c.bin","blob":"YQ=="}}]}`,
			`{"sessionId":"s1","prompt":[{"type":"text","text":"/home/me/p"},{"type":"image","data":"x","mimeType":"image/png","uri":"file:///home/u/p/a.png"},{"type":"resource_link","uri":"file:///home/u/p/a.go","name":"/home/me/p/a.go"},{"type":"resource","resource":{"uri":"file:///home/u/p/my%20b.go","text":"/home/me/p stays in prose"}},{"type":"resource","resource":{"uri":"file://localhost/home/u/p/c.bin","blob":"YQ=="}}]}`, ""},
		{"URIs under no root, of another host, of another scheme or unparsed stay", "session/prompt", false, toRemote,
			`{"sessionId":"s1","prompt":[{"type":"resource_link","uri":"file:///elsewhere/a.go","name":"a"},{"type":"resource_link","uri":"file://box/home/me/p/a.go","name":"b"},{"type":"resource_link","uri":"file:///home/me/p/%zz","name":"c"},{"type":"resource_link","uri":"https://x/home/me/p","name":"d"}]}`,
			`{"sessionId":"s1","prompt":[{"type":"resource_link","uri":"file:///elsewhere/a.go","name":"a"},{"type":"resource_link","uri":"file://box/home/me/p/a.go","name":"b"},{"type":"resource_link","uri":"file:///home/me/p/%zz","name":"c"},{"type":"resource_link","uri":"https://x/home/me/p","name":"d"}]}`, ""},
		{"a tool call's locations, diff, raw input and output", "session/update", false, toLocal,
			`{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"t1","title":"/home/u/p/a.go","locations":[{"path":"/home/u/p/a.go","line":3}],"content":[{"type":"diff","path":"/home/u/p/a.go","oldText":"/home/u/p","newText":"b"},{"type":"content","content":{"type":"resource_link","uri":"file:///home/u/p/a.go","name":"a.go"}}],"rawInput":{"planFilePath":"/home/u/p/plan.md","n":[1,"/home/u/p/b"]},"rawOutput":"/home/u/p/c"}}`,
			`{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"t1","title":"/home/u/p/a.go","locations":[{"path":"/home/me/p/a.go","line":3}],"content":[{"type":"diff","path":"/home/me/p/a.go","oldText":"/home/u/p","newText":"b"},{"type":"content","content":{"type":"resource_link","uri":"file:///home/me/p/a.go","name":"a.go"}}],"rawInput":{"planFilePath":"/home/me/p/plan.md","n":[1,"/home/me/p/b"]},"rawOutput":"/home/me/p/c"}}`, ""},
		{"a plan's file", "session/update", false, toLocal,
			`{"sessionId":"s1","update":{"sessionUpdate":"plan_update","plan":{"type":"markdown","planId":"p1","content":"x","path":"/home/u/p/plan.md"}}}`,
			`{"sessionId":"s1","update":{"sessionUpdate":"plan_update","plan":{"type":"markdown","planId":"p1","content":"x","path":"/home/me/p/plan.md"}}}`, ""},
		{"a child session's directories, given or cleared", "session/update", false, toLocal,
			`{"sessionId":"s1","update":{"sessionUpdate":"subagent_update","sessionId":"c1","cwd":"/home/u/p/sub","additionalDirectories":null}}`,
			`{"sessionId":"s1","update":{"sessionUpdate":"subagent_update","sessionId":"c1","cwd":"/home/me/p/sub","additionalDirectories":null}}`, ""},
		{"a child session's directories in a list", "session/update", false, toLocal,
			`{"sessionId":"s1","update":{"sessionUpdate":"subagent_update","sessionId":"c1","additionalDirectories":["/home/u/p/a"]}}`,
			`{"sessionId":"s1","update":{"sessionUpdate":"subagent_update","sessionId":"c1","additionalDirectories":["/home/me/p/a"]}}`, ""},
		{"a terminal's directory, its command and arguments left as written", "terminal/create", false, toRemote,
			`{"sessionId":"s1","command":"/home/me/p/run","args":["/home/me/p"],"cwd":"/home/me/p"}`,
			`{"sessionId":"s1","command":"/home/me/p/run","args":["/home/me/p"],"cwd":"/home/u/p"}`, ""},
		{"a permission request's tool call and the _meta of its options", "session/request_permission", false, toLocal,
			`{"sessionId":"s1","toolCall":{"toolCallId":"t1","locations":[{"path":"/home/u/p/a"}],"rawInput":{"file":"/home/u/p/b"}},"options":[{"optionId":"o","name":"/home/u/p","kind":"allow_once","_meta":{"f":"/home/u/p/c"}}]}`,
			`{"sessionId":"s1","toolCall":{"toolCallId":"t1","locations":[{"path":"/home/me/p/a"}],"rawInput":{"file":"/home/me/p/b"}},"options":[{"optionId":"o","name":"/home/u/p","kind":"allow_once","_meta":{"f":"/home/me/p/c"}}]}`, ""},
		{"a capability's _meta", "initialize", false, toRemote,
			`{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true,"_meta":{"root":"/home/me/p"}},"session":{"configOptions":{"boolean":{"_meta":{"f":"/home/me/p/a"}}}}}}`,
			`{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true,"_meta":{"root":"/home/u/p"}},"session":{"configOptions":{"boolean":{"_meta":{"f":"/home/u/p/a"}}}}}}`, ""},
		{"the _meta of a message that names only a chat", "session/cancel", false, toRemote,
			`{"sessionId":"s1","_meta":{"f":"/home/me/p/a"}}`,
			`{"sessionId":"s1","_meta":{"f":"/home/u/p/a"}}`, ""},
		{"a config option's value, an ID, left as written", "session/set_config_option", false, toRemote,
			`{"sessionId":"s1","configId":"dir","value":"/home/me/p"}`,
			`{"sessionId":"s1","configId":"dir","value":"/home/me/p"}`, ""},
		{"a compaction's summary and _meta, given or cleared", "session/update", false, toLocal,
			`{"sessionId":"s1","update":{"sessionUpdate":"compaction_update","compactionId":"k","status":"done","summary":[{"type":"resource_link","uri":"file:///home/u/p/a","name":"a"}],"_meta":null}}`,
			`{"sessionId":"s1","update":{"sessionUpdate":"compaction_update","compactionId":"k","status":"done","summary":[{"type":"resource_link","uri":"file:///home/me/p/a","name":"a"}],"_meta":null}}`, ""},
		{"an elicitation's form properties", "elicitation/create", false, toLocal,
			`{"sessionId":"s1","message":"m","mode":"form","requestedSchema":{"properties":{"p":{"type":"string","_meta":{"f":"/home/u/p/a"}}}}}`,
			`{"sessionId":"s1","message":"m","mode":"form","requestedSchema":{"properties":{"p":{"type":"string","_meta":{"f":"/home/me/p/a"}}}}}`, ""},
		{"a question's path", "fs/read_text_file", false, toRemote,
			`{"sessionId":"s1","path":"/home/me/p/a.go"}`,
			`{"sessionId":"s1","path":"/home/u/p/a.go"}`, ""},
		{"an answer's sessions", "session/list", true, toLocal,
			`{"sessions":[{"sessionId":"s1","cwd":"/home/u/p","additionalDirectories":["/home/u/p/a"]}]}`,
			`{"sessions":[{"sessionId":"s1","cwd":"/home/me/p","additionalDirectories":["/home/me/p/a"]}]}`, ""},
		{"every string of _meta, at any depth, and no key", "initialize", false, toRemote,
			`{"protocolVersion":1,"_meta":{"jetbrains":{"air":{"/home/me/p":"/home/me/p/x","views":["file:///home/me/p/v",2,true,null]}}}}`,
			`{"protocolVersion":1,"_meta":{"jetbrains":{"air":{"/home/me/p":"/home/u/p/x","views":["file:///home/u/p/v",2,true,null]}}}}`, ""},
		{"a nested _meta", "session/update", false, toLocal,
			`{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"x","_meta":{"file":"/home/u/p/a"}}}}`,
			`{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"x","_meta":{"file":"/home/me/p/a"}}}}`, ""},
		{"a written file's path, and an ID, a session ID and content that look like paths", "fs/write_text_file", false, toRemote,
			`{"sessionId":"/home/me/p","path":"/home/me/p/w","content":"/home/me/p"}`,
			`{"sessionId":"/home/me/p","path":"/home/u/p/w","content":"/home/me/p"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := read(t, tc.method, tc.result, tc.doc)
			mapped := msg.Mapped(tc.path)
			if got := written(t, mapped); !sameJSON(t, got, tc.want) {
				t.Errorf("mapped to %s, want %s", got, tc.want)
			}
			if mapped.Dir() != tc.wantDir {
				t.Errorf("Dir = %q, want %q", mapped.Dir(), tc.wantDir)
			}
			if got := written(t, msg); !sameJSON(t, got, tc.doc) {
				t.Errorf("the original changed: wrote %s, want %s", got, tc.doc)
			}
		})
	}
}

// TestErrorMapped maps the data of an error answer, as free-form as any
// _meta, and leaves the error it was copied from as it was.
func TestErrorMapped(t *testing.T) {
	m, err := pathmap.New(pathmap.Place{Local: "/home/me/p", Remote: "/home/u/p"})
	if err != nil {
		t.Fatal(err)
	}
	toLocal := func(p session.Path) session.Path { return session.Path(m.ToLocal(string(p))) }
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{"data naming a path and a file URI", `{"code":-32603,"message":"/home/u/p/a","data":{"file":"/home/u/p/a","uri":"file:///home/u/p/b","n":[1,"/home/u/p/c"]}}`,
			`{"code":-32603,"message":"/home/u/p/a","data":{"file":"/home/me/p/a","uri":"file:///home/me/p/b","n":[1,"/home/me/p/c"]}}`},
		{"data left out", `{"code":-32603,"message":"m"}`, `{"code":-32603,"message":"m"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var e Error
			if err := json.Unmarshal([]byte(tc.doc), &e); err != nil {
				t.Fatal(err)
			}
			got, err := marshal(e.Mapped(toLocal))
			if err != nil || !sameJSON(t, string(got), tc.want) {
				t.Errorf("mapped to %s, %v, want %s", got, err, tc.want)
			}
			if orig, err := marshal(&e); err != nil || !sameJSON(t, string(orig), tc.doc) {
				t.Errorf("the original changed: wrote %s, want %s", orig, tc.doc)
			}
		})
	}
}

// TestFromSession writes each of session's Results as the answer of ACP's
// that carries it.
func TestFromSession(t *testing.T) {
	updated := time.Date(2026, 11, 24, 10, 30, 0, 0, time.FixedZone("CET", 3600))
	handshake, err := agent.NewHandshake(1, []byte(`{"loadSession":true,"sessionCapabilities":{"list":{}},"_meta":{"k":"v"}}`))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		result  session.Result
		want    string
		wantErr bool
	}{
		{"nothing", session.EmptyResult{}, `{}`, false},
		{"a handshake", session.HandshakeResult{Handshake: handshake},
			`{"protocolVersion":1,"agentCapabilities":{"loadSession":true,"sessionCapabilities":{"list":{}},"_meta":{"k":"v"}},"authMethods":[]}`, false},
		{"the zero handshake", session.HandshakeResult{}, `{"protocolVersion":0,"agentCapabilities":{},"authMethods":[]}`, false},
		{"a list", session.ListResult{Conversations: []agent.Conversation{
			{ID: "s1", Dir: "/p", State: agent.Running, Title: "fix it", Updated: updated},
			{ID: "s2", Dir: "/p", State: agent.Stopped},
		}}, `{"sessions":[{"sessionId":"s1","cwd":"/p","title":"fix it","updatedAt":"2026-11-24T09:30:00Z"},{"sessionId":"s2","cwd":"/p"}]}`, false},
		{"an empty list", session.ListResult{}, `{"sessions":[]}`, false},
		{"none", nil, ``, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, err := fromSession(tc.result)
			if (err != nil) != tc.wantErr {
				t.Fatalf("fromSession: %v, want an error: %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			got, err := marshal(v)
			if err != nil || !sameJSON(t, string(got), tc.want) {
				t.Errorf("wrote %s, %v, want %s", got, err, tc.want)
			}
		})
	}
}

// TestResult reads the session Result an answer carries.
func TestResult(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		result  bool
		doc     string
		want    bool // the message carries a HandshakeResult
		version int
		caps    string
	}{
		{"the answer to initialize", "initialize", true,
			`{"protocolVersion":1,"agentCapabilities":{"loadSession":true,"_meta":{"k":"v"}},"authMethods":[]}`, true, 1, `{"loadSession":true,"_meta":{"k":"v"}}`},
		{"capabilities left out", "initialize", true, `{"protocolVersion":2}`, true, 2, `{}`},
		{"the answer to another request", "session/new", true, `{"sessionId":"s1"}`, false, 0, ``},
		{"a request", "initialize", false, `{"protocolVersion":1}`, false, 0, ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := read(t, tc.method, tc.result, tc.doc).Result()
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
