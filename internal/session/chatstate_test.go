package session

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/zerlok/hermod/internal/agent"
)

// chatEvent is one event handed to a chat.
type chatEvent int

const (
	evOpen chatEvent = iota
	evAnswered
	evFailed
	evOutput
	evQuestion
	evSuspend
	evClose
)

// chatStep is an event with its message: the answer for Answered, the
// adapter's message for Output, or its question. req names the client's
// request an Open is for and an answer returns to; "open" when empty.
type chatStep struct {
	event chatEvent
	msg   fakeMsg
	req   string
}

func (s chatStep) fire(c *ChatControl, client *fakeChannel) (bool, error) {
	req := fakeAnswer{side: client, name: cmp.Or(s.req, "open")}
	switch s.event {
	case evOpen:
		return false, c.Open(req)
	case evAnswered:
		return c.Answered(req, s.msg, nil)
	case evFailed:
		return c.Answered(req, nil, errors.New("refused"))
	case evOutput:
		return false, c.Output(Inbound{Message: s.msg})
	case evQuestion:
		return false, c.Output(Inbound{Message: s.msg, Answer: fakeAnswer{side: client, name: s.msg.name}})
	case evSuspend:
		return c.Suspend()
	case evClose:
		return c.Close()
	}
	panic(fmt.Sprintf("unknown chat event %d", s.event))
}

// toClient is a chat's way to the client in a test: it records what reaches
// the client as a notification or as a question.
func toClient(client *fakeChannel) func(Inbound) error {
	return func(in Inbound) error {
		if in.Answer == nil {
			return client.Notify(in.Message)
		}
		return client.record(call(in.Message.(fakeMsg)))
	}
}

// chatStateName names a chat's state as the Chat states table does.
func chatStateName(s ChatState) string {
	switch s.(type) {
	case chatUnopened:
		return "unopened"
	case chatOpening:
		return "opening"
	case chatOpen:
		return "open"
	case chatSuspended:
		return "suspended"
	case chatClosed:
		return "closed"
	}
	return fmt.Sprintf("%T", s)
}

// chatStart is how a test's chat is made.
type chatStart int

const (
	startNew chatStart = iota
	startRunning
	startStopped
)

var (
	toOpening = []chatStep{{event: evOpen}}
	toOpen    = []chatStep{{event: evOpen}, {event: evAnswered, msg: msg("r", "c1")}}
	toClosed  = []chatStep{{event: evClose}}
)

// TestChatStates runs every transition of the chat diagram and every refused
// event: a chat reached its state by the setup steps, and is handed the last
// event. The messages it sent the client during that event, the bool the
// event reports, its error and the state it is left in are checked.
func TestChatStates(t *testing.T) {
	cases := []struct {
		name      string
		start     chatStart
		setup     []chatStep
		event     chatStep
		wantOK    bool
		wantErr   bool
		wantState string
		wantSent  []string
	}{
		{name: "a new chat is unopened, and an open makes it opening", event: chatStep{event: evOpen}, wantState: "opening"},
		{name: "unopened: an answer opens nothing", event: chatStep{event: evAnswered, msg: msg("r", "")}, wantState: "unopened"},
		{name: "unopened: output is dropped", event: chatStep{event: evOutput, msg: msg("u", "c1")}, wantState: "unopened"},
		{name: "unopened: a suspend keeps it, nobody waited", event: chatStep{event: evSuspend}, wantState: "unopened"},
		{name: "unopened: a close closes it, nobody waited", event: chatStep{event: evClose}, wantState: "closed"},

		{name: "opening: the answer opens it and returns to the client's request", setup: toOpening,
			event: chatStep{event: evAnswered, msg: msg("r", "c1")}, wantOK: true, wantState: "open", wantSent: []string{returns("open", msg("r", "c1"))}},
		{name: "opening: a failed answer opens it all the same and fails the client's request", setup: toOpening,
			event: chatStep{event: evFailed}, wantOK: true, wantState: "open", wantSent: []string{fails("open")}},
		{name: "opening: output goes to the client", setup: toOpening,
			event: chatStep{event: evQuestion, msg: msg("q", "c1")}, wantState: "opening", wantSent: []string{call(msg("q", "c1"))}},
		{name: "opening: a suspend suspends it, the client waited", setup: toOpening,
			event: chatStep{event: evSuspend}, wantOK: true, wantState: "suspended"},
		{name: "opening: a close closes it, the client waited", setup: toOpening,
			event: chatStep{event: evClose}, wantOK: true, wantState: "closed"},
		{name: "opening: an open is refused", setup: toOpening,
			event: chatStep{event: evOpen}, wantErr: true, wantState: "opening"},

		{name: "open: output goes to the client", setup: toOpen,
			event: chatStep{event: evOutput, msg: msg("u", "c1")}, wantState: "open", wantSent: []string{notify(msg("u", "c1"))}},
		{name: "open: an answer opens nothing", setup: toOpen,
			event: chatStep{event: evAnswered, msg: msg("r", "")}, wantState: "open"},
		{name: "open: a suspend suspends it, the client waited", setup: toOpen,
			event: chatStep{event: evSuspend}, wantOK: true, wantState: "suspended"},
		{name: "open: a close closes it, the client waited", setup: toOpen,
			event: chatStep{event: evClose}, wantOK: true, wantState: "closed"},
		{name: "open: an open is refused", setup: toOpen,
			event: chatStep{event: evOpen}, wantErr: true, wantState: "open"},

		{name: "a chat located running is suspended, and an open makes it opening", start: startRunning,
			event: chatStep{event: evOpen}, wantState: "opening"},
		{name: "a chat located stopped is unopened", start: startStopped,
			event: chatStep{event: evOutput, msg: msg("u", "c9")}, wantState: "unopened"},
		{name: "suspended: a question is held", start: startRunning,
			event: chatStep{event: evQuestion, msg: msg("q", "c9")}, wantState: "suspended"},
		{name: "suspended: a notification is dropped", start: startRunning,
			event: chatStep{event: evOutput, msg: msg("u", "c9")}, wantState: "suspended"},
		{name: "suspended: an answer opens nothing", start: startRunning,
			event: chatStep{event: evAnswered, msg: msg("r", "")}, wantState: "suspended"},
		{name: "suspended: a suspend keeps it, nobody waited", start: startRunning,
			event: chatStep{event: evSuspend}, wantState: "suspended"},
		{name: "suspended: a close closes it, nobody waited", start: startRunning,
			event: chatStep{event: evClose}, wantState: "closed"},

		{name: "closed: an open is refused", setup: toClosed, event: chatStep{event: evOpen}, wantErr: true, wantState: "closed"},
		{name: "closed: output is refused", setup: toClosed, event: chatStep{event: evOutput, msg: msg("u", "c1")}, wantErr: true, wantState: "closed"},
		{name: "closed: a suspend is refused", setup: toClosed, event: chatStep{event: evSuspend}, wantErr: true, wantState: "closed"},
		{name: "closed: a close is refused", setup: toClosed, event: chatStep{event: evClose}, wantErr: true, wantState: "closed"},
		{name: "closed: an answer opens nothing", setup: toClosed, event: chatStep{event: evAnswered, msg: msg("r", "")}, wantState: "closed"},

		{name: "a question held while suspended is called after the answer that opens the chat again", start: startRunning,
			setup: []chatStep{{event: evQuestion, msg: msg("q", "c9")}, {event: evOpen}},
			event: chatStep{event: evAnswered, msg: msg("r", "")}, wantOK: true, wantState: "open",
			wantSent: []string{returns("open", msg("r", "")), call(msg("q", "c9"))}},
		{name: "opening: the answer to an earlier open, which a suspend left behind, opens nothing", start: startRunning,
			setup: []chatStep{{event: evOpen, req: "o1"}, {event: evSuspend}, {event: evOpen, req: "o2"}},
			event: chatStep{event: evAnswered, msg: msg("r1", ""), req: "o1"}, wantState: "opening"},
		{name: "opening: the answer to the open it opens for opens it after an earlier one's", start: startRunning,
			setup: []chatStep{{event: evOpen, req: "o1"}, {event: evSuspend}, {event: evOpen, req: "o2"},
				{event: evAnswered, msg: msg("r1", ""), req: "o1"}},
			event: chatStep{event: evAnswered, msg: msg("r2", ""), req: "o2"}, wantOK: true, wantState: "open",
			wantSent: []string{returns("o2", msg("r2", ""))}},
		{name: "questions held across a suspend while opening are all called after the answer", start: startRunning,
			setup: []chatStep{{event: evQuestion, msg: msg("q5", "c9")}, {event: evOpen}, {event: evSuspend},
				{event: evQuestion, msg: msg("q6", "c9")}, {event: evOpen}},
			event: chatStep{event: evAnswered, msg: msg("r", "")}, wantOK: true, wantState: "open",
			wantSent: []string{returns("open", msg("r", "")), call(msg("q5", "c9")), call(msg("q6", "c9"))}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newFakeChannel()
			var c *ChatControl
			switch tc.start {
			case startNew:
				c = newChat(toClient(client))
			case startRunning, startStopped:
				state := agent.Stopped
				if tc.start == startRunning {
					state = agent.Running
				}
				var err error
				if c, err = newLocatedChat("c9", state, toClient(client)); err != nil {
					t.Fatal(err)
				}
			}
			for _, s := range tc.setup {
				if _, err := s.fire(c, client); err != nil {
					t.Fatalf("setup: %v", err)
				}
			}
			before := len(client.recorded())
			ok, err := tc.event.fire(c, client)
			if ok != tc.wantOK || (err != nil) != tc.wantErr {
				t.Errorf("event: %v, %v, want %v and an error: %v", ok, err, tc.wantOK, tc.wantErr)
			}
			if got := chatStateName(c.sm.state); got != tc.wantState {
				t.Errorf("state %s, want %s", got, tc.wantState)
			}
			if sent := client.recorded()[before:]; !sameLines(sent, tc.wantSent) {
				t.Errorf("sent %q, want %q", sent, tc.wantSent)
			}
		})
	}
}

// TestChatControlAborts checks that an event that failed leaves the chat
// taking nothing more.
func TestChatControlAborts(t *testing.T) {
	cases := []struct {
		name    string
		steps   []chatStep
		wantErr error
	}{
		{"an event after a refused one", []chatStep{{event: evClose}, {event: evOpen}, {event: evSuspend}}, errAborted},
		{"an event after a failed send", []chatStep{{event: evOpen}, {event: evOutput, msg: msg("u", "c1")}, {event: evSuspend}}, errAborted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newFakeChannel()
			client.fails = true
			c := newChat(toClient(client))
			var err error
			for _, s := range tc.steps {
				_, err = s.fire(c, client)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("last event: %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestDetachedSession checks that a Detached session takes nothing: a client
// message is an error, an adapter message was on its way and is dropped, and
// a detach has nothing left to give up.
func TestDetachedSession(t *testing.T) {
	cases := []struct {
		name      string
		event     func(context.Context, *SessionControl) (bool, error)
		wantEnded bool
		wantErr   bool
	}{
		{"a client message", func(ctx context.Context, s *SessionControl) (bool, error) {
			return s.HandleClientMessage(ctx, Inbound{Message: msg("1", "c1")})
		}, true, true},
		{"an adapter message", func(ctx context.Context, s *SessionControl) (bool, error) {
			return s.HandleAdapterMessage(ctx, Inbound{Message: msg("u", "c1")})
		}, true, false},
		{"a located chat", func(_ context.Context, s *SessionControl) (bool, error) {
			return false, s.Located("c1", agent.Running)
		}, false, true},
		{"a detach", func(ctx context.Context, s *SessionControl) (bool, error) {
			return false, s.Detach(ctx)
		}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &SessionControl{sm: StateMachineImpl[SessionState]{state: sessionDetached{}}, session: sessionContext{dir: api}}
			ended, err := tc.event(context.Background(), s)
			if ended != tc.wantEnded || (err != nil) != tc.wantErr {
				t.Errorf("%v, %v, want %v and an error: %v", ended, err, tc.wantEnded, tc.wantErr)
			}
			if _, ok := s.sm.state.(sessionDetached); !ok {
				t.Errorf("state %T, want detached", s.sm.state)
			}
		})
	}
}

// pathError is an error answer that names a path, as an adapter's error data
// may.
type pathError struct{ path Path }

func (e pathError) Error() string { return "failed at " + string(e.path) }

func (e pathError) Mapped(path func(Path) Path) error { return pathError{path(e.path)} }

// TestMappedCaller checks that a mapped Caller maps each message it passes on
// one way, and the answer it returns, or the error answer, the other.
func TestMappedCaller(t *testing.T) {
	out := func(p Path) Path { return "/out" + p }
	back := func(p Path) Path { return "/back" + p }
	cases := []struct {
		name       string
		notify     bool
		send       fakeMsg
		answer     callResult
		wantSent   string
		wantAnswer string // the answer, or the error, the call returns
	}{
		{"a call's message out, its answer back", false, openIn("1", "/a"), callResult{msg: fakeMsg{name: "r", dir: "/b"}},
			call(openIn("1", "/out/a")), fakeMsg{name: "r", dir: "/back/b"}.String()},
		{"an error answer that names a path back", false, openIn("1", "/a"), callResult{err: pathError{"/b"}},
			call(openIn("1", "/out/a")), "failed at /back/b"},
		{"any other error as it is", false, openIn("1", "/a"), callResult{err: errors.New("refused /b")},
			call(openIn("1", "/out/a")), "refused /b"},
		{"a notification out", true, openIn("u", "/a"), callResult{}, notify(openIn("u", "/out/a")), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			to := newFakeChannel()
			c := mappedCaller{c: to, out: out, back: back}
			if tc.notify {
				if err := c.Notify(tc.send); err != nil {
					t.Fatal(err)
				}
			} else {
				got := make(chan string, 1)
				err := c.Call(context.Background(), tc.send, func(answer Message, err error) {
					if err != nil {
						got <- err.Error()
						return
					}
					got <- answer.(fakeMsg).String()
				})
				if err != nil {
					t.Fatal(err)
				}
				to.answer(t, tc.send.name, tc.answer)
				if answer := <-got; answer != tc.wantAnswer {
					t.Errorf("answer %q, want %q", answer, tc.wantAnswer)
				}
			}
			if sent := to.recorded(); !reflect.DeepEqual(sent, []string{tc.wantSent}) {
				t.Errorf("sent %q, want %q", sent, tc.wantSent)
			}
		})
	}
}
