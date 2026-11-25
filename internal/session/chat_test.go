package session

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/agent"
)

const (
	web    = testLocalHome + "/web"
	webR   = testRemoteHome + "/web"
	fresh  = testLocalHome + "/new"
	freshR = testRemoteHome + "/new"
)

// hs is how a reply carries testHandshake.
const hs = `v1 {"loadSession":true}`

// step is one thing that happens to the router: a message arrives from the
// client or from the adapter of a directory's session, a call is answered or
// a request cancelled on one of those sides, or an adapter's connection ends.
type step struct {
	side        string  // "" is the client; otherwise an adapter's directory
	send        fakeMsg // arrives from side
	ask         bool    // what arrives is a request
	answer      string  // side answers its call of this name
	result      fakeMsg // with this result
	fail        bool    // or with an error
	cancel      string  // side cancels its request of this name
	ends        bool    // the adapter's connection ends
	workersFail bool    // tmux cannot list its sessions from this step on
}

func client(m fakeMsg) step           { return step{send: m, ask: true} }
func clientNote(m fakeMsg) step       { return step{send: m} }
func from(dir string, m fakeMsg) step { return step{side: dir, send: m, ask: true} }
func fromNote(dir string, m fakeMsg) step {
	return step{side: dir, send: m}
}
func answers(side, name string, result fakeMsg) step {
	return step{side: side, answer: name, result: result}
}
func refuses(side, name string) step { return step{side: side, answer: name, fail: true} }
func cancels(side, name string) step { return step{side: side, cancel: name} }
func endsAt(dir string) step         { return step{side: dir, ends: true} }

func conv(id, dir string, state agent.State) agent.Conversation {
	return agent.Conversation{ID: id, Dir: dir, State: state}
}

// The lines a side receives, as fakeChannel records them.
func call(m fakeMsg) string                 { return "call " + m.String() }
func notify(m fakeMsg) string               { return "notify " + m.String() }
func returns(name string, m fakeMsg) string { return "return " + name + ": " + m.String() }
func replies(name, result string) string    { return "reply " + name + ": " + result }
func fails(name string) string              { return "fail " + name }
func cancelled(name string) string          { return "cancelled " + name }

// TestChatEvents runs every row of the Events table through fake channels,
// and checks what the client and each adapter received, what changed on the
// two workstations, which sessions remain and how many chats each waits on,
// and what was logged.
func TestChatEvents(t *testing.T) {
	cases := []struct {
		name         string
		running      []string // directories whose adapter runs already
		refuse       []string // directories whose attach fails
		convs        []agent.Conversation
		queryFails   bool
		queryGarbled bool
		clientFails  bool
		steps        []step
		wantErr      bool
		wantClient   []string
		wantAdapters map[string][]string
		wantChanges  []string
		wantSessions map[string]int // directory → chats waited on
		wantLog      []string       // parts of lines logged, in order
		forbidLog    []string       // parts of no line logged
	}{
		{
			name:       "a connect is answered from the agent and attaches nothing",
			steps:      []step{client(connectReq("0"))},
			wantClient: []string{replies("0", hs)},
		},
		{
			name:         "an open in a new directory attaches it and its new adapter is called with the kept connect first",
			steps:        []step{client(connectReq("0")), client(openIn("1", api))},
			wantClient:   []string{replies("0", hs)},
			wantAdapters: map[string][]string{api: {call(connectReq("0"))}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
		},
		{
			name: "what waited for the kept connect is called after its answer",
			steps: []step{client(connectReq("0")), client(openIn("1", api)),
				answers(api, "0", handshakeAnswer(1, `{"loadSession":true}`))},
			wantClient:   []string{replies("0", hs)},
			wantAdapters: map[string][]string{api: {call(connectReq("0")), call(openIn("1", apiR))}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
			forbidLog:    []string{"protocol version", "capabilities", "no handshake", "refused"},
		},
		{
			name:         "an adapter that runs already gets no connect",
			running:      []string{api},
			steps:        []step{client(connectReq("0")), client(openIn("1", api))},
			wantClient:   []string{replies("0", hs)},
			wantAdapters: map[string][]string{api: {call(openIn("1", apiR))}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
		},
		{
			name: "the adapter's answer to the kept connect is dropped, its other version logged",
			steps: []step{client(connectReq("0")), client(openIn("1", api)),
				answers(api, "0", handshakeAnswer(2, `{"loadSession":true}`))},
			wantClient:   []string{replies("0", hs)},
			wantAdapters: map[string][]string{api: {call(connectReq("0")), call(openIn("1", apiR))}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
			wantLog:      []string{"protocol version 2"},
		},
		{
			name: "the adapter's answer to the kept connect is dropped, its other capabilities logged",
			steps: []step{client(connectReq("0")), client(openIn("1", api)),
				answers(api, "0", handshakeAnswer(1, `{"loadSession":false}`))},
			wantClient:   []string{replies("0", hs)},
			wantAdapters: map[string][]string{api: {call(connectReq("0")), call(openIn("1", apiR))}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
			wantLog:      []string{"offers capabilities"},
		},
		{
			name:         "a kept connect the adapter refuses is logged",
			steps:        []step{client(connectReq("0")), client(openIn("1", api)), refuses(api, "0")},
			wantClient:   []string{replies("0", hs)},
			wantAdapters: map[string][]string{api: {call(connectReq("0")), call(openIn("1", apiR))}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
			wantLog:      []string{"refused the connection start"},
		},
		{
			name:    "the answer to an open names the chat, by which later messages are routed",
			running: []string{api},
			steps: []step{client(openIn("1", api)), answers(api, "1", msg("r1", "c1")),
				client(msg("2", "c1")), answers(api, "2", msg("r2", "")), fromNote(api, msg("u", "c1"))},
			wantClient:   []string{returns("1", msg("r1", "c1")), returns("2", msg("r2", "")), notify(msg("u", "c1"))},
			wantAdapters: map[string][]string{api: {call(openIn("1", apiR)), call(msg("2", "c1"))}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
		},
		{
			name:    "two adapters' questions are each called on the client and answered back",
			running: []string{api, web},
			steps: []step{client(openIn("1", api)), client(openIn("2", web)),
				answers(api, "1", msg("r1", "c1")), answers(web, "2", msg("r2", "c2")),
				from(api, msg("qa", "c1")), from(web, msg("qw", "c2")),
				answers("", "qw", msg("aw", "")), answers("", "qa", msg("aa", ""))},
			wantClient: []string{returns("1", msg("r1", "c1")), returns("2", msg("r2", "c2")),
				call(msg("qa", "c1")), call(msg("qw", "c2"))},
			wantAdapters: map[string][]string{
				api: {call(openIn("1", apiR)), returns("qa", msg("aa", ""))},
				web: {call(openIn("2", webR)), returns("qw", msg("aw", ""))},
			},
			wantChanges:  []string{"create " + api, "flush " + api, "create " + web, "flush " + web},
			wantSessions: map[string]int{api: 1, web: 1},
		},
		{
			name:    "a suspend is answered and not passed on, and the last one detaches",
			running: []string{api},
			steps: []step{client(openIn("1", api)), answers(api, "1", msg("r1", "c1")), client(suspendReq("2", "c1")),
				fromNote(api, msg("u", "c1"))},
			wantClient:   []string{returns("1", msg("r1", "c1")), replies("2", "empty")},
			wantAdapters: map[string][]string{api: {call(openIn("1", apiR))}},
			wantChanges:  []string{"create " + api, "flush " + api, "pause " + api},
		},
		{
			name:    "a suspend that leaves a chat waited on keeps the session",
			running: []string{api},
			steps: []step{client(openIn("1", api)), answers(api, "1", msg("r1", "c1")),
				client(openIn("2", api)), answers(api, "2", msg("r2", "c2")), client(suspendReq("3", "c1"))},
			wantClient:   []string{returns("1", msg("r1", "c1")), returns("2", msg("r2", "c2")), replies("3", "empty")},
			wantAdapters: map[string][]string{api: {call(openIn("1", apiR)), call(openIn("2", apiR))}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
		},
		{
			name:    "a question held while its chat is suspended is called on the client after the answer that opens it again",
			running: []string{api},
			steps: []step{client(openIn("1", api)), answers(api, "1", msg("r1", "c1")),
				client(openIn("2", api)), answers(api, "2", msg("r2", "c2")), client(suspendReq("3", "c1")),
				from(api, msg("q", "c1")), fromNote(api, msg("u", "c1")), client(reopen("4", "c1")),
				answers(api, "4", msg("r4", "")), answers("", "q", msg("a", ""))},
			wantClient: []string{returns("1", msg("r1", "c1")), returns("2", msg("r2", "c2")), replies("3", "empty"),
				returns("4", msg("r4", "")), call(msg("q", "c1"))},
			wantAdapters: map[string][]string{api: {call(openIn("1", apiR)), call(openIn("2", apiR)), call(reopen("4", "c1")),
				returns("q", msg("a", ""))}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 2},
		},
		{
			name:    "the answer to an open of a chat suspended meanwhile returns to its request, and the chat stays suspended",
			running: []string{api},
			steps: []step{client(openIn("1", api)), answers(api, "1", msg("r1", "c1")),
				client(openIn("2", api)), answers(api, "2", msg("r2", "c2")), client(suspendReq("3", "c1")),
				client(reopen("4", "c1")), client(suspendReq("5", "c1")), answers(api, "4", msg("r4", "")),
				from(api, msg("q", "c1"))},
			wantClient: []string{returns("1", msg("r1", "c1")), returns("2", msg("r2", "c2")), replies("3", "empty"),
				replies("5", "empty"), returns("4", msg("r4", ""))},
			wantAdapters: map[string][]string{api: {call(openIn("1", apiR)), call(openIn("2", apiR)), call(reopen("4", "c1"))}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
		},
		{
			name:    "each of two opens of a chat, a suspend between them, gets its own call's answer",
			running: []string{api},
			steps: []step{client(openIn("1", api)), answers(api, "1", msg("r1", "c1")),
				client(openIn("2", api)), answers(api, "2", msg("r2", "c2")), client(suspendReq("3", "c1")),
				client(reopen("4", "c1")), client(suspendReq("5", "c1")), client(reopen("6", "c1")),
				answers(api, "4", msg("r4", "")), answers(api, "6", msg("r6", "")), fromNote(api, msg("u", "c1"))},
			wantClient: []string{returns("1", msg("r1", "c1")), returns("2", msg("r2", "c2")), replies("3", "empty"),
				replies("5", "empty"), returns("4", msg("r4", "")), returns("6", msg("r6", "")), notify(msg("u", "c1"))},
			wantAdapters: map[string][]string{api: {call(openIn("1", apiR)), call(openIn("2", apiR)), call(reopen("4", "c1")),
				call(reopen("6", "c1"))}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 2},
		},
		{
			name:    "the answer to an open of a chat closed meanwhile returns to its request",
			running: []string{api},
			steps: []step{client(openIn("1", api)), answers(api, "1", msg("r1", "c1")),
				client(openIn("2", api)), answers(api, "2", msg("r2", "c2")), client(suspendReq("3", "c1")),
				client(reopen("4", "c1")), client(closeReq("5", "c1")), answers(api, "4", msg("r4", "")),
				answers(api, "5", msg("r5", ""))},
			wantClient: []string{returns("1", msg("r1", "c1")), returns("2", msg("r2", "c2")), replies("3", "empty"),
				returns("4", msg("r4", "")), returns("5", msg("r5", ""))},
			wantAdapters: map[string][]string{api: {call(openIn("1", apiR)), call(openIn("2", apiR)), call(reopen("4", "c1")),
				call(closeReq("5", "c1"))}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
		},
		{
			name:    "a closed chat's output reaches the client as a message of no chat",
			running: []string{api},
			steps: []step{client(openIn("1", api)), answers(api, "1", msg("r1", "c1")),
				client(openIn("2", api)), answers(api, "2", msg("r2", "c2")), client(closeReq("3", "c1")),
				fromNote(api, msg("u", "c1")), answers(api, "3", msg("r3", ""))},
			wantClient: []string{returns("1", msg("r1", "c1")), returns("2", msg("r2", "c2")), notify(msg("u", "c1")),
				returns("3", msg("r3", ""))},
			wantAdapters: map[string][]string{api: {call(openIn("1", apiR)), call(openIn("2", apiR)), call(closeReq("3", "c1"))}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
		},
		{
			name:    "the last close with a conversation still running in the session's directory detaches",
			running: []string{api},
			convs:   []agent.Conversation{conv("c1", apiR, agent.Stopped), conv("c3", apiR, agent.Running)},
			steps: []step{client(openIn("1", api)), answers(api, "1", msg("r1", "c1")), client(closeReq("2", "c1")),
				answers(api, "2", msg("r2", ""))},
			wantClient:   []string{returns("1", msg("r1", "c1")), returns("2", msg("r2", ""))},
			wantAdapters: map[string][]string{api: {call(openIn("1", apiR)), call(closeReq("2", "c1"))}},
			wantChanges:  []string{"create " + api, "flush " + api, "pause " + api},
		},
		{
			name:    "the last close with a conversation running only in a subdirectory of the workspace detaches",
			running: []string{api},
			convs:   []agent.Conversation{conv("c1", apiR, agent.Stopped), conv("c3", apiR+"/sub", agent.Running)},
			steps: []step{client(openIn("1", api)), answers(api, "1", msg("r1", "c1")), client(closeReq("2", "c1")),
				answers(api, "2", msg("r2", ""))},
			wantClient:   []string{returns("1", msg("r1", "c1")), returns("2", msg("r2", ""))},
			wantAdapters: map[string][]string{api: {call(openIn("1", apiR)), call(closeReq("2", "c1"))}},
			wantChanges:  []string{"create " + api, "flush " + api, "pause " + api},
		},
		{
			name:    "the last close with no conversation running closes the adapter worker",
			running: []string{api},
			convs:   []agent.Conversation{conv("c1", apiR, agent.Stopped), conv("c4", webR, agent.Running)},
			steps: []step{client(openIn("1", api)), answers(api, "1", msg("r1", "c1")), client(closeReq("2", "c1")),
				answers(api, "2", msg("r2", ""))},
			wantClient:   []string{returns("1", msg("r1", "c1")), returns("2", msg("r2", ""))},
			wantAdapters: map[string][]string{api: {call(openIn("1", apiR)), call(closeReq("2", "c1"))}},
			wantChanges:  []string{"create " + api, "flush " + api, "close acp:" + api, "flush " + api, "terminate " + api},
		},
		{
			name:       "the last close keeps the adapter when the agent program cannot list",
			running:    []string{api},
			queryFails: true,
			steps: []step{client(openIn("1", api)), answers(api, "1", msg("r1", "c1")), client(closeReq("2", "c1")),
				answers(api, "2", msg("r2", ""))},
			wantClient:   []string{returns("1", msg("r1", "c1")), returns("2", msg("r2", ""))},
			wantAdapters: map[string][]string{api: {call(openIn("1", apiR)), call(closeReq("2", "c1"))}},
			wantChanges:  []string{"create " + api, "flush " + api, "pause " + api},
			wantLog:      []string{"keeping the adapter"},
		},
		{
			name:         "the last close keeps the adapter when the agent program's listing cannot be read",
			running:      []string{api},
			queryGarbled: true,
			steps: []step{client(openIn("1", api)), answers(api, "1", msg("r1", "c1")), client(closeReq("2", "c1")),
				answers(api, "2", msg("r2", ""))},
			wantClient:   []string{returns("1", msg("r1", "c1")), returns("2", msg("r2", ""))},
			wantAdapters: map[string][]string{api: {call(openIn("1", apiR)), call(closeReq("2", "c1"))}},
			wantChanges:  []string{"create " + api, "flush " + api, "pause " + api},
			wantLog:      []string{"keeping the adapter"},
		},
		{
			name:    "an open before the adapter's answer to the last close keeps the session",
			running: []string{api},
			steps: []step{client(openIn("1", api)), answers(api, "1", msg("r1", "c1")), client(closeReq("2", "c1")),
				client(openIn("3", api)), answers(api, "2", msg("r2", ""))},
			wantClient:   []string{returns("1", msg("r1", "c1")), returns("2", msg("r2", ""))},
			wantAdapters: map[string][]string{api: {call(openIn("1", apiR)), call(closeReq("2", "c1")), call(openIn("3", apiR))}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
		},
		{
			name:    "a suspend between the last close and its answer leaves the end to the close's answer",
			running: []string{api},
			convs:   []agent.Conversation{conv("c1", apiR, agent.Stopped)},
			steps: []step{client(openIn("1", api)), answers(api, "1", msg("r1", "c1")), client(closeReq("2", "c1")),
				client(suspendReq("3", "c1")), answers(api, "2", msg("r2", ""))},
			wantClient:   []string{returns("1", msg("r1", "c1")), replies("3", "empty"), returns("2", msg("r2", ""))},
			wantAdapters: map[string][]string{api: {call(openIn("1", apiR)), call(closeReq("2", "c1"))}},
			wantChanges:  []string{"create " + api, "flush " + api, "close acp:" + api, "flush " + api, "terminate " + api},
		},
		{
			name:    "the last close sent as a notification ends the session at once",
			running: []string{api},
			steps: []step{client(openIn("1", api)), answers(api, "1", msg("r1", "c1")), clientNote(closeReq("2", "c1")),
				fromNote(api, msg("u", "c1"))},
			wantClient:   []string{returns("1", msg("r1", "c1"))},
			wantAdapters: map[string][]string{api: {call(openIn("1", apiR)), notify(closeReq("2", "c1"))}},
			wantChanges:  []string{"create " + api, "flush " + api, "close acp:" + api, "flush " + api, "terminate " + api},
		},
		{
			name:    "a new chat whose open fails is waited on no more",
			running: []string{api},
			steps: []step{client(openIn("1", api)), client(openIn("2", api)), refuses(api, "1"),
				answers(api, "2", msg("r2", "c2")), client(suspendReq("3", "c2"))},
			wantClient:   []string{fails("1"), returns("2", msg("r2", "c2")), replies("3", "empty")},
			wantAdapters: map[string][]string{api: {call(openIn("1", apiR)), call(openIn("2", apiR))}},
			wantChanges:  []string{"create " + api, "flush " + api, "pause " + api},
			wantLog:      []string{"names no chat"},
		},
		{
			name:    "a question answered after its session detached is answered to its adapter",
			running: []string{api},
			steps: []step{client(openIn("1", api)), answers(api, "1", msg("r1", "c1")), from(api, msg("q", "c1")),
				client(suspendReq("2", "c1")), answers("", "q", msg("a", ""))},
			wantClient:   []string{returns("1", msg("r1", "c1")), call(msg("q", "c1")), replies("2", "empty")},
			wantAdapters: map[string][]string{api: {call(openIn("1", apiR)), returns("q", msg("a", ""))}},
			wantChanges:  []string{"create " + api, "flush " + api, "pause " + api},
		},
		{
			name: "a list for a directory no relay covers answers its conversations and none under it",
			convs: []agent.Conversation{conv("n1", freshR, agent.Running), conv("n2", freshR+"/sub", agent.Stopped),
				conv("o1", apiR, agent.Running)},
			steps:      []step{client(listReq("1", fresh))},
			wantClient: []string{replies("1", "list n1@"+fresh+":running")},
		},
		{
			name:       "a list the agent program cannot answer fails",
			queryFails: true,
			steps:      []step{client(listReq("1", fresh))},
			wantClient: []string{fails("1")},
		},
		{
			name:         "a list whose answer cannot be read fails",
			queryGarbled: true,
			steps:        []step{client(listReq("1", fresh))},
			wantClient:   []string{fails("1")},
		},
		{
			name:    "the client's cancel ends the call that passed its request on",
			running: []string{api},
			steps: []step{client(openIn("1", api)), answers(api, "1", msg("r1", "c1")), client(msg("2", "c1")),
				cancels("", "2")},
			wantClient:   []string{returns("1", msg("r1", "c1")), fails("2")},
			wantAdapters: map[string][]string{api: {call(openIn("1", apiR)), call(msg("2", "c1")), cancelled("2")}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
		},
		{
			name:    "an adapter's cancel ends the call that passed its question on",
			running: []string{api},
			steps: []step{client(openIn("1", api)), answers(api, "1", msg("r1", "c1")), from(api, msg("q", "c1")),
				cancels(api, "q")},
			wantClient:   []string{returns("1", msg("r1", "c1")), call(msg("q", "c1")), cancelled("q")},
			wantAdapters: map[string][]string{api: {call(openIn("1", apiR)), fails("q")}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
		},
		{
			name:         "a located running chat is opened again in the session attached for it",
			running:      []string{api},
			convs:        []agent.Conversation{conv("c9", apiR, agent.Running)},
			steps:        []step{client(reopen("1", "c9")), answers(api, "1", msg("r1", "")), client(msg("2", "c9"))},
			wantClient:   []string{returns("1", msg("r1", ""))},
			wantAdapters: map[string][]string{api: {call(reopen("1", "c9")), call(msg("2", "c9"))}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
		},
		{
			name:         "a located stopped chat is opened in the session attached for it",
			running:      []string{api},
			convs:        []agent.Conversation{conv("c9", apiR, agent.Stopped)},
			steps:        []step{client(reopen("1", "c9")), answers(api, "1", msg("r1", ""))},
			wantClient:   []string{returns("1", msg("r1", ""))},
			wantAdapters: map[string][]string{api: {call(reopen("1", "c9"))}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
		},
		{
			name:         "a located chat's close reaches its adapter, which is then closed",
			running:      []string{api},
			convs:        []agent.Conversation{conv("c9", apiR, agent.Stopped)},
			steps:        []step{client(closeReq("1", "c9")), answers(api, "1", msg("r1", ""))},
			wantClient:   []string{returns("1", msg("r1", ""))},
			wantAdapters: map[string][]string{api: {call(closeReq("1", "c9"))}},
			wantChanges:  []string{"create " + api, "flush " + api, "close acp:" + api, "flush " + api, "terminate " + api},
		},
		{
			name:         "a located chat's suspend detaches the session attached for it",
			running:      []string{api},
			convs:        []agent.Conversation{conv("c9", apiR, agent.Running)},
			steps:        []step{client(suspendReq("1", "c9"))},
			wantClient:   []string{replies("1", "empty")},
			wantChanges:  []string{"create " + api, "flush " + api, "pause " + api},
			wantAdapters: map[string][]string{api: nil},
		},
		{
			name:       "a located stopped chat's suspend is answered without attaching",
			convs:      []agent.Conversation{conv("c9", apiR, agent.Stopped)},
			steps:      []step{client(suspendReq("1", "c9"))},
			wantClient: []string{replies("1", "empty")},
		},
		{
			name:       "a chat the agent program does not have is answered with an error",
			convs:      []agent.Conversation{conv("c9", apiR, agent.Running)},
			steps:      []step{client(suspendReq("1", "zz")), client(reopen("2", "zz"))},
			wantClient: []string{fails("1"), fails("2")},
		},
		{
			name:       "a lookup that fails is answered with an error",
			queryFails: true,
			steps:      []step{client(closeReq("1", "c9"))},
			wantClient: []string{fails("1")},
		},
		{
			name:         "a lookup whose answer cannot be read is answered with an error",
			queryGarbled: true,
			steps:        []step{client(closeReq("1", "c9"))},
			wantClient:   []string{fails("1")},
		},
		{
			name:    "a notification for a chat the agent program does not have is dropped",
			convs:   []agent.Conversation{conv("c9", apiR, agent.Running)},
			steps:   []step{clientNote(closeReq("1", "zz")), clientNote(suspendReq("2", "zz"))},
			wantLog: []string{"dropped a close", "dropped a suspend"},
		},
		{
			name:       "a pass for no chat fails a request and drops a notification",
			steps:      []step{client(msg("1", "zz")), clientNote(msg("u", "zz")), client(msg("2", ""))},
			wantClient: []string{fails("1"), fails("2")},
		},
		{
			name:       "an open that names neither a chat nor a directory fails",
			steps:      []step{client(openIn("1", ""))},
			wantClient: []string{fails("1")},
		},
		{
			name:    "an event the chat refuses ends the chat",
			running: []string{api},
			steps: []step{client(openIn("1", api)), answers(api, "1", msg("r1", "c1")), client(reopen("2", "c1")),
				client(msg("3", "c1"))},
			wantErr:      true,
			wantClient:   []string{returns("1", msg("r1", "c1"))},
			wantAdapters: map[string][]string{api: {call(openIn("1", apiR))}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
		},
		{
			name:        "an answer the client's connection cannot take ends the chat",
			clientFails: true,
			steps:       []step{client(connectReq("0"))},
			wantErr:     true,
			wantClient:  []string{replies("0", hs)},
		},
		{
			name:    "the adapter's connection ending detaches the session, and its chats go with it",
			running: []string{api},
			steps: []step{client(openIn("1", api)), answers(api, "1", msg("r1", "c1")), endsAt(api),
				client(msg("2", "c1"))},
			wantClient:   []string{returns("1", msg("r1", "c1")), fails("2")},
			wantAdapters: map[string][]string{api: {call(openIn("1", apiR))}},
			wantChanges:  []string{"create " + api, "flush " + api, "pause " + api},
		},
		{
			name:         "a call under way when the adapter's connection ends fails its request",
			running:      []string{api},
			steps:        []step{client(openIn("1", api)), endsAt(api)},
			wantClient:   []string{fails("1")},
			wantAdapters: map[string][]string{api: {call(openIn("1", apiR))}},
			wantChanges:  []string{"create " + api, "flush " + api, "pause " + api},
		},
		{
			name:         "a request waiting for the kept connect fails when the adapter's connection ends",
			steps:        []step{client(connectReq("0")), client(openIn("1", api)), endsAt(api)},
			wantClient:   []string{replies("0", hs), fails("1")},
			wantAdapters: map[string][]string{api: {call(connectReq("0"))}},
			wantChanges:  []string{"create " + api, "flush " + api, "pause " + api},
		},
		{
			name:    "a workspace that cannot be attached ends the chat",
			refuse:  []string{api},
			steps:   []step{client(openIn("1", api))},
			wantErr: true,
		},
		{
			name:    "a failed detach keeps the session attached and ends the chat",
			running: []string{api},
			steps: []step{client(openIn("1", api)), answers(api, "1", msg("r1", "c1")),
				{send: suspendReq("2", "c1"), ask: true, workersFail: true}},
			wantErr:      true,
			wantClient:   []string{returns("1", msg("r1", "c1")), replies("2", "empty")},
			wantAdapters: map[string][]string{api: {call(openIn("1", apiR))}},
			wantChanges:  []string{"create " + api, "flush " + api, "pause " + api},
			wantSessions: map[string]int{api: 0},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newChatEnv(t)
			e.f.convs, e.f.queryFails, e.f.queryGarbled = tc.convs, tc.queryFails, tc.queryGarbled
			e.client.fails = tc.clientFails
			for _, d := range tc.running {
				e.running[d] = true
			}
			for _, d := range tc.refuse {
				e.refuse[d] = true
			}
			ctx, stop := context.WithCancel(context.Background())
			r := newRouter(ctx, e.request())
			t.Cleanup(func() { _ = r.close(context.Background(), stop) })

			var err error
			for _, s := range tc.steps {
				if err = e.run(t, r, s); err != nil {
					break
				}
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("error %v, want one: %v", err, tc.wantErr)
			}
			if got := e.client.recorded(); !sameLines(got, tc.wantClient) {
				t.Errorf("client received\n%q\nwant\n%q", got, tc.wantClient)
			}
			for dir, c := range e.adapters {
				if got := c.recorded(); !sameLines(got, tc.wantAdapters[string(dir)]) {
					t.Errorf("adapter of %s received\n%q\nwant\n%q", dir, got, tc.wantAdapters[string(dir)])
				}
			}
			for dir := range tc.wantAdapters {
				if _, ok := e.adapters[Path(dir)]; !ok {
					t.Errorf("no adapter of %s was attached", dir)
				}
			}
			if got := e.changes(); !sameLines(got, tc.wantChanges) {
				t.Errorf("changes\n%q\nwant\n%q", got, tc.wantChanges)
			}
			if got := waiting(r); !reflect.DeepEqual(got, tc.wantSessions) && len(got)+len(tc.wantSessions) > 0 {
				t.Errorf("sessions %v, want %v", got, tc.wantSessions)
			}
			if got := e.logLines(); !logged(got, tc.wantLog, tc.forbidLog) {
				t.Errorf("logged %q, want lines with %q and none with %q", got, tc.wantLog, tc.forbidLog)
			}
		})
	}
}

// run hands the router one step and runs what the calls it returned bring
// back.
func (e *chatEnv) run(t *testing.T, r *router, s step) error {
	t.Helper()
	e.f.mu.Lock()
	e.f.workersFail = e.f.workersFail || s.workersFail
	e.f.mu.Unlock()
	side := e.client
	if s.side != "" {
		side = e.adapter(Path(s.side))
	}
	var err error
	returns := 0
	switch {
	case s.answer != "":
		result := callResult{msg: s.result}
		if s.fail {
			result = callResult{err: errors.New("refused")}
		}
		side.answer(t, s.answer, result)
		returns = 1
	case s.cancel != "":
		side.cancel(s.cancel)
		returns = 1
	case s.ends:
		dir := Path(s.side)
		if err = r.fromAdapter(r.running, dir, r.sessions[dir], Inbound{}); err == nil {
			returns = side.end()
		}
	case s.side == "":
		err = r.fromClient(r.running, side.inbound(s.send, s.ask))
	default:
		dir := Path(s.side)
		err = r.fromAdapter(r.running, dir, r.sessions[dir], side.inbound(s.send, s.ask))
	}
	if err != nil {
		return err
	}
	return e.settle(r, returns)
}

// waiting is how many chats each session the router holds waits on; -1 for a
// session that is not Attached.
func waiting(r *router) map[string]int {
	got := map[string]int{}
	for dir, s := range r.sessions {
		st, ok := s.sm.state.(sessionAttached)
		if !ok {
			got[string(dir)] = -1
			continue
		}
		got[string(dir)] = st.waiting
	}
	return got
}

func sameLines(got, want []string) bool {
	if len(got)+len(want) == 0 {
		return true
	}
	return reflect.DeepEqual(got, want)
}

// logged reports whether lines hold each of want, in order, and none of
// forbid.
func logged(lines, want, forbid []string) bool {
	next := 0
	for _, line := range lines {
		for _, f := range forbid {
			if strings.Contains(line, f) {
				return false
			}
		}
		if next < len(want) && strings.Contains(line, want[next]) {
			next++
		}
	}
	return next == len(want)
}

// loopStep is one thing a test does to a running Chat.
type loopStep struct {
	adapter string  // "" is the client's connection
	msg     fakeMsg // arrives, from the client as a request and from an adapter as a notification; none ends that connection
	note    bool    // what arrives from the client is a notification
	// answer is the call of this name, which the connection returns msg to
	// once it is made, where the connection is read, as acp.Conn does.
	answer string
	await  string // then waits until the client records this line
}

// TestChatLoop runs Chat itself: messages reach it through the client's
// connection and each adapter's forwarder, and its end detaches every session
// it holds.
func TestChatLoop(t *testing.T) {
	errDropped := errors.New("connection dropped")
	cases := []struct {
		name        string
		clientErr   error
		steps       []loopStep
		wantErr     error
		wantClient  []string
		wantAdapter []string // what the adapter of api received; nil is unchecked
		wantChanges []string
	}{
		{
			name:        "the client's connection ending detaches every session",
			steps:       []loopStep{{msg: openIn("1", api)}, {msg: openIn("2", web)}, {}},
			wantChanges: []string{"create " + api, "flush " + api, "create " + web, "flush " + web, "pause " + api, "pause " + web},
		},
		{
			name: "an adapter's messages reach the client through its forwarder, and its end detaches",
			steps: []loopStep{{msg: openIn("1", api)}, {adapter: api, msg: msg("u", ""), await: notify(msg("u", ""))},
				{adapter: api}, {}},
			wantClient:  []string{notify(msg("u", ""))},
			wantChanges: []string{"create " + api, "flush " + api, "pause " + api},
		},
		{
			name: "an adapter's answer reaches the client after the messages the adapter sent before it",
			steps: []loopStep{{msg: openIn("1", api)}, {adapter: api, answer: "1", msg: msg("r1", "c1"), await: returns("1", msg("r1", "c1"))},
				{msg: msg("p", "c1")}, {adapter: api, msg: msg("u1", "c1")}, {adapter: api, msg: msg("u2", "c1")},
				{adapter: api, answer: "p", msg: msg("rp", "c1"), await: returns("p", msg("rp", "c1"))}, {}},
			wantClient: []string{returns("1", msg("r1", "c1")), notify(msg("u1", "c1")), notify(msg("u2", "c1")),
				returns("p", msg("rp", "c1"))},
			wantAdapter: []string{call(openIn("1", apiR)), call(msg("p", "c1"))},
			wantChanges: []string{"create " + api, "flush " + api, "pause " + api},
		},
		{
			name: "a client's request and the notification after it reach the adapter in that order",
			steps: []loopStep{{msg: openIn("1", api)}, {adapter: api, answer: "1", msg: msg("r1", "c1"), await: returns("1", msg("r1", "c1"))},
				{msg: msg("p", "c1")}, {msg: msg("n", "c1"), note: true}, {}},
			wantClient:  []string{returns("1", msg("r1", "c1"))},
			wantAdapter: []string{call(openIn("1", apiR)), call(msg("p", "c1")), notify(msg("n", "c1")), cancelled("p")},
			wantChanges: []string{"create " + api, "flush " + api, "pause " + api},
		},
		{
			name:        "the client's connection failing returns why",
			clientErr:   errDropped,
			steps:       []loopStep{{msg: openIn("1", api)}, {}},
			wantErr:     errDropped,
			wantChanges: []string{"create " + api, "flush " + api, "pause " + api},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newChatEnv(t)
			e.client.err = tc.clientErr
			result := make(chan error, 1)
			go func() { result <- Chat(context.Background(), e.request()) }()
			for _, s := range tc.steps {
				c := e.client
				if s.adapter != "" {
					c = e.adapter(Path(s.adapter))
				}
				switch {
				case s.answer != "":
					c.called(s.answer)(s.msg, nil)
				case s.msg.name == "":
					close(c.in)
				default:
					c.in <- c.inbound(s.msg, s.adapter == "" && !s.note)
				}
				if s.await != "" {
					e.client.await(s.await)
				}
			}
			if err := <-result; !errors.Is(err, tc.wantErr) || (err == nil) != (tc.wantErr == nil) {
				t.Errorf("Chat: %v, want %v", err, tc.wantErr)
			}
			if got := e.client.recorded(); !sameLines(got, tc.wantClient) {
				t.Errorf("client received %q, want %q", got, tc.wantClient)
			}
			if got := e.adapter(api).recorded(); tc.wantAdapter != nil && !reflect.DeepEqual(got, tc.wantAdapter) {
				t.Errorf("adapter received %q, want %q", got, tc.wantAdapter)
			}
			if got := e.changes(); !reflect.DeepEqual(got, tc.wantChanges) {
				t.Errorf("changes\n%q\nwant\n%q", got, tc.wantChanges)
			}
		})
	}
}
