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

// step is one message on its way to the router: from the client, or from the
// adapter of the session of a directory.
type step struct {
	adapter     string  // "" is the client
	msg         fakeMsg // ignored when ends
	ends        bool    // the adapter's connection ends
	workersFail bool    // tmux cannot list its sessions from this step on
}

func client(msg fakeMsg) step { return step{msg: msg} }

func from(dir string, msg fakeMsg) step { return step{adapter: dir, msg: msg} }

func conv(id, dir string, state agent.State) agent.Conversation {
	return agent.Conversation{ID: id, Dir: dir, State: state}
}

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
		steps        []step
		wantErr      bool
		wantClient   []Message
		wantAdapters map[string][]Message
		wantChanges  []string
		wantSessions map[string]int // directory → chats waited on
		wantLog      []string       // parts of lines logged, in order
		forbidLog    []string       // parts of no line logged
	}{
		{
			name:       "a connect is answered from the agent and attaches nothing",
			steps:      []step{client(connectReq("0"))},
			wantClient: []Message{result("0", hs)},
		},
		{
			name:         "an open in a new directory attaches it and its new adapter gets the kept connect first",
			steps:        []step{client(connectReq("0")), client(openIn("1", api))},
			wantClient:   []Message{result("0", hs)},
			wantAdapters: map[string][]Message{api: {connectReq("0"), openIn("1", apiR)}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
		},
		{
			name:         "an adapter that runs already gets no connect",
			running:      []string{api},
			steps:        []step{client(connectReq("0")), client(openIn("1", api))},
			wantClient:   []Message{result("0", hs)},
			wantAdapters: map[string][]Message{api: {openIn("1", apiR)}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
		},
		{
			name: "the adapter's answer to the kept connect is dropped, its other version logged",
			steps: []step{client(connectReq("0")), client(openIn("1", api)),
				from(api, handshakeAnswer("0", 2, `{"loadSession":true}`))},
			wantClient:   []Message{result("0", hs)},
			wantAdapters: map[string][]Message{api: {connectReq("0"), openIn("1", apiR)}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
			wantLog:      []string{"protocol version 2"},
		},
		{
			name: "the adapter's answer to the kept connect is dropped, its other capabilities logged",
			steps: []step{client(connectReq("0")), client(openIn("1", api)),
				from(api, handshakeAnswer("0", 1, `{"loadSession":false}`))},
			wantClient:   []Message{result("0", hs)},
			wantAdapters: map[string][]Message{api: {connectReq("0"), openIn("1", apiR)}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
			wantLog:      []string{"offers capabilities"},
		},
		{
			name: "the adapter's answer to the kept connect is dropped, the same handshake not logged",
			steps: []step{client(connectReq("0")), client(openIn("1", api)),
				from(api, handshakeAnswer("0", 1, `{ "loadSession": true }`))},
			wantClient:   []Message{result("0", hs)},
			wantAdapters: map[string][]Message{api: {connectReq("0"), openIn("1", apiR)}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
			forbidLog:    []string{"protocol version", "capabilities", "no handshake"},
		},
		{
			name: "the answer to an open names the chat, by which later messages are routed",
			steps: []step{client(openIn("1", api)), from(api, prompt("1", "c1")),
				client(prompt("2", "c1")), from(api, done("2")), from(api, update("c1"))},
			wantClient:   []Message{prompt("1", "c1"), done("2"), update("c1")},
			wantAdapters: map[string][]Message{api: {openIn("1", apiR), prompt("2", "c1")}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
		},
		{
			name: "two adapters' questions under one ID are renumbered and answered back under it",
			steps: []step{client(openIn("1", api)), client(openIn("2", web)),
				from(api, prompt("1", "c1")), from(web, prompt("2", "c2")),
				from(api, ask("7", "c1")), from(web, ask("7", "c2")),
				client(reply("hermod-2")), client(reply("hermod-1"))},
			wantClient: []Message{prompt("1", "c1"), prompt("2", "c2"), ask("hermod-1", "c1"), ask("hermod-2", "c2")},
			wantAdapters: map[string][]Message{
				api: {openIn("1", apiR), reply("7")},
				web: {openIn("2", webR), reply("7")},
			},
			wantChanges:  []string{"create " + api, "flush " + api, "create " + web, "flush " + web},
			wantSessions: map[string]int{api: 1, web: 1},
		},
		{
			name: "an adapter's question under the ID of an open still waiting does not answer the open",
			steps: []step{client(openIn("1", api)), from(api, ask("1", "")), from(api, prompt("1", "c1")),
				client(prompt("2", "c1"))},
			wantClient:   []Message{ask("hermod-1", ""), prompt("1", "c1")},
			wantAdapters: map[string][]Message{api: {openIn("1", apiR), prompt("2", "c1")}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
		},
		{
			name: "a suspend is answered and not passed on, and the last one detaches",
			steps: []step{client(openIn("1", api)), from(api, prompt("1", "c1")), client(suspendReq("2", "c1")),
				from(api, update("c1"))},
			wantClient:   []Message{prompt("1", "c1"), result("2", "empty")},
			wantAdapters: map[string][]Message{api: {openIn("1", apiR)}},
			wantChanges:  []string{"create " + api, "flush " + api, "pause " + api},
		},
		{
			name: "a suspend that leaves a chat waited on keeps the session",
			steps: []step{client(openIn("1", api)), from(api, prompt("1", "c1")),
				client(openIn("2", api)), from(api, prompt("2", "c2")), client(suspendReq("3", "c1"))},
			wantClient:   []Message{prompt("1", "c1"), prompt("2", "c2"), result("3", "empty")},
			wantAdapters: map[string][]Message{api: {openIn("1", apiR), openIn("2", apiR)}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
		},
		{
			name: "a question held while its chat is suspended reaches the client after the answer that opens it again",
			steps: []step{client(openIn("1", api)), from(api, prompt("1", "c1")),
				client(openIn("2", api)), from(api, prompt("2", "c2")), client(suspendReq("3", "c1")),
				from(api, ask("5", "c1")), from(api, update("c1")), client(reopen("4", "c1")), from(api, done("4"))},
			wantClient: []Message{prompt("1", "c1"), prompt("2", "c2"), result("3", "empty"),
				done("4"), ask("hermod-1", "c1")},
			wantAdapters: map[string][]Message{api: {openIn("1", apiR), openIn("2", apiR), reopen("4", "c1")}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 2},
		},
		{
			name: "the answer to an open of a chat suspended meanwhile is dropped",
			steps: []step{client(openIn("1", api)), from(api, prompt("1", "c1")),
				client(openIn("2", api)), from(api, prompt("2", "c2")), client(suspendReq("3", "c1")),
				client(reopen("4", "c1")), client(suspendReq("5", "c1")), from(api, done("4"))},
			wantClient: []Message{prompt("1", "c1"), prompt("2", "c2"), result("3", "empty"),
				result("5", "empty")},
			wantAdapters: map[string][]Message{api: {openIn("1", apiR), openIn("2", apiR), reopen("4", "c1")}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
		},
		{
			name: "a closed chat's output reaches the client as a message of no chat",
			steps: []step{client(openIn("1", api)), from(api, prompt("1", "c1")),
				client(openIn("2", api)), from(api, prompt("2", "c2")), client(closeReq("3", "c1")),
				from(api, update("c1")), from(api, done("3"))},
			wantClient:   []Message{prompt("1", "c1"), prompt("2", "c2"), update("c1"), done("3")},
			wantAdapters: map[string][]Message{api: {openIn("1", apiR), openIn("2", apiR), closeReq("3", "c1")}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
		},
		{
			name:  "the last close with a conversation still running in the session's directory detaches",
			convs: []agent.Conversation{conv("c1", apiR, agent.Stopped), conv("c3", apiR, agent.Running)},
			steps: []step{client(openIn("1", api)), from(api, prompt("1", "c1")), client(closeReq("2", "c1")),
				from(api, done("2"))},
			wantClient:   []Message{prompt("1", "c1"), done("2")},
			wantAdapters: map[string][]Message{api: {openIn("1", apiR), closeReq("2", "c1")}},
			wantChanges:  []string{"create " + api, "flush " + api, "pause " + api},
		},
		{
			name:  "the last close with a conversation running only in a subdirectory of the workspace detaches",
			convs: []agent.Conversation{conv("c1", apiR, agent.Stopped), conv("c3", apiR+"/sub", agent.Running)},
			steps: []step{client(openIn("1", api)), from(api, prompt("1", "c1")), client(closeReq("2", "c1")),
				from(api, done("2"))},
			wantClient:   []Message{prompt("1", "c1"), done("2")},
			wantAdapters: map[string][]Message{api: {openIn("1", apiR), closeReq("2", "c1")}},
			wantChanges:  []string{"create " + api, "flush " + api, "pause " + api},
		},
		{
			name:  "the last close with no conversation running closes the adapter worker",
			convs: []agent.Conversation{conv("c1", apiR, agent.Stopped), conv("c4", webR, agent.Running)},
			steps: []step{client(openIn("1", api)), from(api, prompt("1", "c1")), client(closeReq("2", "c1")),
				from(api, done("2"))},
			wantClient:   []Message{prompt("1", "c1"), done("2")},
			wantAdapters: map[string][]Message{api: {openIn("1", apiR), closeReq("2", "c1")}},
			wantChanges:  []string{"create " + api, "flush " + api, "close acp:" + api, "flush " + api, "terminate " + api},
		},
		{
			name:       "the last close keeps the adapter when the agent program cannot list",
			queryFails: true,
			steps: []step{client(openIn("1", api)), from(api, prompt("1", "c1")), client(closeReq("2", "c1")),
				from(api, done("2"))},
			wantClient:   []Message{prompt("1", "c1"), done("2")},
			wantAdapters: map[string][]Message{api: {openIn("1", apiR), closeReq("2", "c1")}},
			wantChanges:  []string{"create " + api, "flush " + api, "pause " + api},
			wantLog:      []string{"keeping the adapter"},
		},
		{
			name:         "the last close keeps the adapter when the agent program's listing cannot be read",
			queryGarbled: true,
			steps: []step{client(openIn("1", api)), from(api, prompt("1", "c1")), client(closeReq("2", "c1")),
				from(api, done("2"))},
			wantClient:   []Message{prompt("1", "c1"), done("2")},
			wantAdapters: map[string][]Message{api: {openIn("1", apiR), closeReq("2", "c1")}},
			wantChanges:  []string{"create " + api, "flush " + api, "pause " + api},
			wantLog:      []string{"keeping the adapter"},
		},
		{
			name: "an open before the adapter's answer to the last close keeps the session",
			steps: []step{client(openIn("1", api)), from(api, prompt("1", "c1")), client(closeReq("2", "c1")),
				client(openIn("3", api)), from(api, done("2"))},
			wantClient:   []Message{prompt("1", "c1"), done("2")},
			wantAdapters: map[string][]Message{api: {openIn("1", apiR), closeReq("2", "c1"), openIn("3", apiR)}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
		},
		{
			name:  "a suspend between the last close and its answer leaves the end to the close's answer",
			convs: []agent.Conversation{conv("c1", apiR, agent.Stopped)},
			steps: []step{client(openIn("1", api)), from(api, prompt("1", "c1")), client(closeReq("2", "c1")),
				client(suspendReq("3", "c1")), from(api, done("2"))},
			wantClient:   []Message{prompt("1", "c1"), result("3", "empty"), done("2")},
			wantAdapters: map[string][]Message{api: {openIn("1", apiR), closeReq("2", "c1")}},
			wantChanges:  []string{"create " + api, "flush " + api, "close acp:" + api, "flush " + api, "terminate " + api},
		},
		{
			name: "the last close sent as a notification ends the session at once",
			steps: []step{client(openIn("1", api)), from(api, prompt("1", "c1")), client(closeReq("", "c1")),
				from(api, update("c1"))},
			wantClient:   []Message{prompt("1", "c1")},
			wantAdapters: map[string][]Message{api: {openIn("1", apiR), closeReq("", "c1")}},
			wantChanges:  []string{"create " + api, "flush " + api, "close acp:" + api, "flush " + api, "terminate " + api},
		},
		{
			name: "an open answered without a chat is waited on no more",
			steps: []step{client(openIn("1", api)), client(openIn("2", api)), from(api, failure("1")),
				from(api, prompt("2", "c2")), client(suspendReq("3", "c2"))},
			wantClient:   []Message{failure("1"), prompt("2", "c2"), result("3", "empty")},
			wantAdapters: map[string][]Message{api: {openIn("1", apiR), openIn("2", apiR)}},
			wantChanges:  []string{"create " + api, "flush " + api, "pause " + api},
			wantLog:      []string{"names no chat"},
		},
		{
			name: "an answer to a question its detached session dropped is dropped",
			steps: []step{client(openIn("1", api)), from(api, prompt("1", "c1")), from(api, ask("5", "c1")),
				client(suspendReq("2", "c1")), client(reply("hermod-1"))},
			wantClient:   []Message{prompt("1", "c1"), ask("hermod-1", "c1"), result("2", "empty")},
			wantAdapters: map[string][]Message{api: {openIn("1", apiR)}},
			wantChanges:  []string{"create " + api, "flush " + api, "pause " + api},
			wantLog:      []string{"dropped an answer"},
		},
		{
			name: "a list for a directory no relay covers answers its conversations and none under it",
			convs: []agent.Conversation{conv("n1", freshR, agent.Running), conv("n2", freshR+"/sub", agent.Stopped),
				conv("o1", apiR, agent.Running)},
			steps:      []step{client(listReq("1", fresh))},
			wantClient: []Message{result("1", "list n1@"+fresh+":running")},
		},
		{
			name:       "a list the agent program cannot answer fails",
			queryFails: true,
			steps:      []step{client(listReq("1", fresh))},
			wantClient: []Message{failure("1")},
		},
		{
			name:         "a list whose answer cannot be read fails",
			queryGarbled: true,
			steps:        []step{client(listReq("1", fresh))},
			wantClient:   []Message{failure("1")},
		},
		{
			name: "an adapter's cancel under the ID of an open still waiting does not answer the open",
			steps: []step{client(openIn("1", api)), from(api, cancelReq("1")), from(api, prompt("1", "c1")),
				client(prompt("2", "c1"))},
			wantClient:   []Message{prompt("1", "c1")},
			wantAdapters: map[string][]Message{api: {openIn("1", apiR), prompt("2", "c1")}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
			wantLog:      []string{"was not asked"},
		},
		{
			name: "an adapter's cancel of its question reaches the client under the question's renumbered ID",
			steps: []step{client(openIn("1", api)), client(openIn("2", web)),
				from(api, prompt("1", "c1")), from(web, prompt("2", "c2")),
				from(api, ask("7", "c1")), from(web, ask("7", "c2")), from(web, cancelReq("7")),
				client(reply("hermod-2"))},
			wantClient: []Message{prompt("1", "c1"), prompt("2", "c2"), ask("hermod-1", "c1"), ask("hermod-2", "c2"),
				cancelReq("hermod-2")},
			wantAdapters: map[string][]Message{
				api: {openIn("1", apiR)},
				web: {openIn("2", webR), reply("7")},
			},
			wantChanges:  []string{"create " + api, "flush " + api, "create " + web, "flush " + web},
			wantSessions: map[string]int{api: 1, web: 1},
		},
		{
			name: "a cancel reaches the session that holds the request",
			steps: []step{client(openIn("1", api)), from(api, prompt("1", "c1")), client(prompt("2", "c1")),
				client(cancelReq("2"))},
			wantClient:   []Message{prompt("1", "c1")},
			wantAdapters: map[string][]Message{api: {openIn("1", apiR), prompt("2", "c1"), cancelReq("2")}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
		},
		{
			name: "a cancel of a request already answered is dropped",
			steps: []step{client(openIn("1", api)), from(api, prompt("1", "c1")), client(prompt("2", "c1")),
				from(api, done("2")), client(cancelReq("2"))},
			wantClient:   []Message{prompt("1", "c1"), done("2")},
			wantAdapters: map[string][]Message{api: {openIn("1", apiR), prompt("2", "c1")}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
			wantLog:      []string{"dropped a cancel"},
		},
		{
			name:         "a located running chat is opened again in the session attached for it",
			running:      []string{api},
			convs:        []agent.Conversation{conv("c9", apiR, agent.Running)},
			steps:        []step{client(reopen("1", "c9")), from(api, done("1")), client(prompt("2", "c9"))},
			wantClient:   []Message{done("1")},
			wantAdapters: map[string][]Message{api: {reopen("1", "c9"), prompt("2", "c9")}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
		},
		{
			name:         "a located stopped chat is opened in the session attached for it",
			running:      []string{api},
			convs:        []agent.Conversation{conv("c9", apiR, agent.Stopped)},
			steps:        []step{client(reopen("1", "c9")), from(api, done("1"))},
			wantClient:   []Message{done("1")},
			wantAdapters: map[string][]Message{api: {reopen("1", "c9")}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
		},
		{
			name:         "a located chat's close reaches its adapter, which is then closed",
			running:      []string{api},
			convs:        []agent.Conversation{conv("c9", apiR, agent.Stopped)},
			steps:        []step{client(closeReq("1", "c9")), from(api, done("1"))},
			wantClient:   []Message{done("1")},
			wantAdapters: map[string][]Message{api: {closeReq("1", "c9")}},
			wantChanges:  []string{"create " + api, "flush " + api, "close acp:" + api, "flush " + api, "terminate " + api},
		},
		{
			name:         "a located chat's suspend detaches the session attached for it",
			running:      []string{api},
			convs:        []agent.Conversation{conv("c9", apiR, agent.Running)},
			steps:        []step{client(suspendReq("1", "c9"))},
			wantClient:   []Message{result("1", "empty")},
			wantChanges:  []string{"create " + api, "flush " + api, "pause " + api},
			wantAdapters: map[string][]Message{api: nil},
		},
		{
			name:       "a located stopped chat's suspend is answered without attaching",
			convs:      []agent.Conversation{conv("c9", apiR, agent.Stopped)},
			steps:      []step{client(suspendReq("1", "c9"))},
			wantClient: []Message{result("1", "empty")},
		},
		{
			name:       "a chat the agent program does not have is answered with an error",
			convs:      []agent.Conversation{conv("c9", apiR, agent.Running)},
			steps:      []step{client(suspendReq("1", "zz")), client(reopen("2", "zz"))},
			wantClient: []Message{failure("1"), failure("2")},
		},
		{
			name:       "a lookup that fails is answered with an error",
			queryFails: true,
			steps:      []step{client(closeReq("1", "c9"))},
			wantClient: []Message{failure("1")},
		},
		{
			name:         "a lookup whose answer cannot be read is answered with an error",
			queryGarbled: true,
			steps:        []step{client(closeReq("1", "c9"))},
			wantClient:   []Message{failure("1")},
		},
		{
			name:    "a notification for a chat the agent program does not have is dropped",
			convs:   []agent.Conversation{conv("c9", apiR, agent.Running)},
			steps:   []step{client(closeReq("", "zz")), client(suspendReq("", "zz"))},
			wantLog: []string{"dropped a close", "dropped a suspend"},
		},
		{
			name:       "a pass for no chat fails a request and drops a notification",
			steps:      []step{client(prompt("1", "zz")), client(update("zz")), client(prompt("2", ""))},
			wantClient: []Message{failure("1"), failure("2")},
		},
		{
			name:       "an open that names neither a chat nor a directory fails",
			steps:      []step{client(openIn("1", ""))},
			wantClient: []Message{failure("1")},
		},
		{
			name:    "an answer to an unknown request ends the chat",
			steps:   []step{client(reply("hermod-9"))},
			wantErr: true,
		},
		{
			name:    "a question from the client ends the chat",
			steps:   []step{client(ask("1", ""))},
			wantErr: true,
		},
		{
			name: "an event the chat refuses ends the chat",
			steps: []step{client(openIn("1", api)), from(api, prompt("1", "c1")), client(reopen("2", "c1")),
				client(prompt("3", "c1"))},
			wantErr:      true,
			wantClient:   []Message{prompt("1", "c1")},
			wantAdapters: map[string][]Message{api: {openIn("1", apiR)}},
			wantChanges:  []string{"create " + api, "flush " + api},
			wantSessions: map[string]int{api: 1},
		},
		{
			name: "the adapter's connection ending detaches the session, and its chats go with it",
			steps: []step{client(openIn("1", api)), from(api, prompt("1", "c1")), {adapter: api, ends: true},
				client(prompt("2", "c1"))},
			wantClient:   []Message{prompt("1", "c1"), failure("2")},
			wantAdapters: map[string][]Message{api: {openIn("1", apiR)}},
			wantChanges:  []string{"create " + api, "flush " + api, "pause " + api},
		},
		{
			name:    "a workspace that cannot be attached ends the chat",
			refuse:  []string{api},
			steps:   []step{client(openIn("1", api))},
			wantErr: true,
		},
		{
			name: "a failed detach keeps the session attached and ends the chat",
			steps: []step{client(openIn("1", api)), from(api, prompt("1", "c1")),
				{msg: suspendReq("2", "c1"), workersFail: true}},
			wantErr:      true,
			wantClient:   []Message{prompt("1", "c1"), result("2", "empty")},
			wantAdapters: map[string][]Message{api: {openIn("1", apiR)}},
			wantChanges:  []string{"create " + api, "flush " + api, "pause " + api},
			wantSessions: map[string]int{api: 0},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newChatEnv(t)
			e.f.convs, e.f.queryFails, e.f.queryGarbled = tc.convs, tc.queryFails, tc.queryGarbled
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
				e.f.mu.Lock()
				e.f.workersFail = e.f.workersFail || s.workersFail
				e.f.mu.Unlock()
				if s.adapter == "" {
					err = r.fromClient(ctx, s.msg)
				} else {
					in := adapterMessage{dir: Path(s.adapter), session: r.sessions[Path(s.adapter)], msg: s.msg}
					if s.ends {
						in.msg = nil
					}
					err = r.fromAdapter(ctx, in)
				}
				if err != nil {
					break
				}
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("error %v, want one: %v", err, tc.wantErr)
			}
			if got := e.client.messages(); !sameMessages(got, tc.wantClient) {
				t.Errorf("client received\n%+v\nwant\n%+v", got, tc.wantClient)
			}
			for dir, c := range e.adapters {
				if got := c.messages(); !sameMessages(got, tc.wantAdapters[string(dir)]) {
					t.Errorf("adapter of %s received\n%+v\nwant\n%+v", dir, got, tc.wantAdapters[string(dir)])
				}
			}
			for dir := range tc.wantAdapters {
				if _, ok := e.adapters[Path(dir)]; !ok {
					t.Errorf("no adapter of %s was attached", dir)
				}
			}
			if got := e.changes(); !reflect.DeepEqual(got, tc.wantChanges) && len(got)+len(tc.wantChanges) > 0 {
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

func sameMessages(got, want []Message) bool {
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
	msg     Message // nil ends that connection
	await   bool    // waits until the client receives a message
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
		wantClient  []Message
		wantChanges []string
	}{
		{
			name:        "the client's connection ending detaches every session",
			steps:       []loopStep{{msg: openIn("1", api)}, {msg: openIn("2", web)}, {}},
			wantChanges: []string{"create " + api, "flush " + api, "create " + web, "flush " + web, "pause " + api, "pause " + web},
		},
		{
			name: "an adapter's messages reach the client through its forwarder, and its end detaches",
			steps: []loopStep{{msg: openIn("1", api)}, {adapter: api, msg: prompt("1", "c1"), await: true},
				{adapter: api}, {}},
			wantClient:  []Message{prompt("1", "c1")},
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
				if s.msg == nil {
					close(c.in)
				} else {
					c.in <- s.msg
				}
				if s.await {
					<-e.client.notify
				}
			}
			if err := <-result; !errors.Is(err, tc.wantErr) || (err == nil) != (tc.wantErr == nil) {
				t.Errorf("Chat: %v, want %v", err, tc.wantErr)
			}
			if got := e.client.messages(); !sameMessages(got, tc.wantClient) {
				t.Errorf("client received %+v, want %+v", got, tc.wantClient)
			}
			if got := e.changes(); !reflect.DeepEqual(got, tc.wantChanges) {
				t.Errorf("changes\n%q\nwant\n%q", got, tc.wantChanges)
			}
		})
	}
}
