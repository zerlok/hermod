package session

import (
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
	evOutput
	evSuspend
	evClose
)

// chatStep is an event with its message: the request for an Open, the
// adapter's message for Answered and Output.
type chatStep struct {
	event chatEvent
	msg   fakeMsg
}

func (s chatStep) fire(c *ChatControl) (bool, error) {
	switch s.event {
	case evOpen:
		return false, c.Open(s.msg.ID())
	case evAnswered:
		return c.Answered(s.msg)
	case evOutput:
		return false, c.Output(s.msg)
	case evSuspend:
		return c.Suspend()
	case evClose:
		return c.Close()
	}
	panic(fmt.Sprintf("unknown chat event %d", s.event))
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
	toOpening = []chatStep{{evOpen, done("1")}}
	toOpen    = []chatStep{{evOpen, done("1")}, {evAnswered, prompt("1", "c1")}}
	toClosed  = []chatStep{{evClose, fakeMsg{}}}
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
		wantSent  []Message
	}{
		{name: "a new chat is unopened, and an open makes it opening", event: chatStep{evOpen, done("1")}, wantState: "opening"},
		{name: "unopened: an answer is not its", event: chatStep{evAnswered, done("1")}, wantState: "unopened"},
		{name: "unopened: output is dropped", event: chatStep{evOutput, update("c1")}, wantState: "unopened"},
		{name: "unopened: a suspend keeps it, nobody waited", event: chatStep{evSuspend, fakeMsg{}}, wantState: "unopened"},
		{name: "unopened: a close closes it, nobody waited", event: chatStep{evClose, fakeMsg{}}, wantState: "closed"},

		{name: "opening: the answer to its request opens it and goes to the client", setup: toOpening,
			event: chatStep{evAnswered, prompt("1", "c1")}, wantOK: true, wantState: "open", wantSent: []Message{prompt("1", "c1")}},
		{name: "opening: an answer to another request is not its", setup: toOpening,
			event: chatStep{evAnswered, done("2")}, wantState: "opening"},
		{name: "opening: output goes to the client", setup: toOpening,
			event: chatStep{evOutput, ask("5", "c1")}, wantState: "opening", wantSent: []Message{ask("5", "c1")}},
		{name: "opening: a suspend suspends it, the client waited", setup: toOpening,
			event: chatStep{evSuspend, fakeMsg{}}, wantOK: true, wantState: "suspended"},
		{name: "opening: a close closes it, the client waited", setup: toOpening,
			event: chatStep{evClose, fakeMsg{}}, wantOK: true, wantState: "closed"},
		{name: "opening: an open is refused", setup: toOpening,
			event: chatStep{evOpen, done("2")}, wantErr: true, wantState: "opening"},

		{name: "open: output goes to the client", setup: toOpen,
			event: chatStep{evOutput, update("c1")}, wantState: "open", wantSent: []Message{update("c1")}},
		{name: "open: an answer is not its", setup: toOpen,
			event: chatStep{evAnswered, done("1")}, wantState: "open"},
		{name: "open: a suspend suspends it, the client waited", setup: toOpen,
			event: chatStep{evSuspend, fakeMsg{}}, wantOK: true, wantState: "suspended"},
		{name: "open: a close closes it, the client waited", setup: toOpen,
			event: chatStep{evClose, fakeMsg{}}, wantOK: true, wantState: "closed"},
		{name: "open: an open is refused", setup: toOpen,
			event: chatStep{evOpen, done("2")}, wantErr: true, wantState: "open"},

		{name: "a chat located running is suspended, and an open makes it opening", start: startRunning,
			event: chatStep{evOpen, done("1")}, wantState: "opening"},
		{name: "a chat located stopped is unopened", start: startStopped,
			event: chatStep{evOutput, update("c9")}, wantState: "unopened"},
		{name: "suspended: a question is held", start: startRunning,
			event: chatStep{evOutput, ask("5", "c9")}, wantState: "suspended"},
		{name: "suspended: other output is dropped", start: startRunning,
			event: chatStep{evOutput, update("c9")}, wantState: "suspended"},
		{name: "suspended: an answer is not its", start: startRunning,
			event: chatStep{evAnswered, done("1")}, wantState: "suspended"},
		{name: "suspended: a suspend keeps it, nobody waited", start: startRunning,
			event: chatStep{evSuspend, fakeMsg{}}, wantState: "suspended"},
		{name: "suspended: a close closes it, nobody waited", start: startRunning,
			event: chatStep{evClose, fakeMsg{}}, wantState: "closed"},

		{name: "closed: an open is refused", setup: toClosed, event: chatStep{evOpen, done("1")}, wantErr: true, wantState: "closed"},
		{name: "closed: output is refused", setup: toClosed, event: chatStep{evOutput, update("c1")}, wantErr: true, wantState: "closed"},
		{name: "closed: a suspend is refused", setup: toClosed, event: chatStep{evSuspend, fakeMsg{}}, wantErr: true, wantState: "closed"},
		{name: "closed: a close is refused", setup: toClosed, event: chatStep{evClose, fakeMsg{}}, wantErr: true, wantState: "closed"},
		{name: "closed: an answer is not its", setup: toClosed, event: chatStep{evAnswered, done("1")}, wantState: "closed"},

		{name: "a question held while suspended is sent after the answer that opens the chat again", start: startRunning,
			setup: []chatStep{{evOutput, ask("5", "c9")}, {evOpen, done("4")}},
			event: chatStep{evAnswered, done("4")}, wantOK: true, wantState: "open", wantSent: []Message{done("4"), ask("5", "c9")}},
		{name: "questions held across a suspend while opening are all sent after the answer", start: startRunning,
			setup: []chatStep{{evOutput, ask("5", "c9")}, {evOpen, done("4")}, {evSuspend, fakeMsg{}},
				{evOutput, ask("6", "c9")}, {evOpen, done("7")}},
			event: chatStep{evAnswered, done("7")}, wantOK: true, wantState: "open",
			wantSent: []Message{done("7"), ask("5", "c9"), ask("6", "c9")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sent []Message
			client := FuncSender[Message](func(msg Message) error { sent = append(sent, msg); return nil })
			var c *ChatControl
			switch tc.start {
			case startNew:
				c = newChat(client)
			case startRunning, startStopped:
				state := agent.Stopped
				if tc.start == startRunning {
					state = agent.Running
				}
				var err error
				if c, err = newLocatedChat("c9", state, client); err != nil {
					t.Fatal(err)
				}
			}
			for _, s := range tc.setup {
				if _, err := s.fire(c); err != nil {
					t.Fatalf("setup: %v", err)
				}
			}
			sent = nil
			ok, err := tc.event.fire(c)
			if ok != tc.wantOK || (err != nil) != tc.wantErr {
				t.Errorf("event: %v, %v, want %v and an error: %v", ok, err, tc.wantOK, tc.wantErr)
			}
			if got := chatStateName(c.sm.state); got != tc.wantState {
				t.Errorf("state %s, want %s", got, tc.wantState)
			}
			if !sameMessages(sent, tc.wantSent) {
				t.Errorf("sent %+v, want %+v", sent, tc.wantSent)
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
		{"an event after a refused one", []chatStep{{evClose, fakeMsg{}}, {evOpen, done("1")}, {evSuspend, fakeMsg{}}}, errAborted},
		{"an event after a failed send", []chatStep{{evOpen, done("1")}, {evOutput, update("c1")}, {evSuspend, fakeMsg{}}}, errAborted},
	}
	errSend := errors.New("send failed")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newChat(FuncSender[Message](func(Message) error { return errSend }))
			var err error
			for _, s := range tc.steps {
				_, err = s.fire(c)
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
			return s.HandleClientMessage(ctx, prompt("1", "c1"))
		}, true, true},
		{"an adapter message", func(ctx context.Context, s *SessionControl) (bool, error) {
			return s.HandleAdapterMessage(ctx, update("c1"))
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

// TestTransformSender checks that each message is converted before it is sent
// on.
func TestTransformSender(t *testing.T) {
	cases := []struct {
		name     string
		convert  func(int) string
		in       int
		wantSent []string
	}{
		{"converted and sent on", func(n int) string { return fmt.Sprint(n * 2) }, 21, []string{"42"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sent []string
			s := TransformSender[int, string]{F: tc.convert, S: FuncSender[string](func(m string) error { sent = append(sent, m); return nil })}
			if err := s.Send(tc.in); err != nil {
				t.Errorf("Send: %v", err)
			}
			if !reflect.DeepEqual(sent, tc.wantSent) {
				t.Errorf("sent %q, want %q", sent, tc.wantSent)
			}
		})
	}
}
