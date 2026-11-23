package session

import (
	"fmt"

	"github.com/zerlok/hermod/internal/agent"
)

// chatContext is what a chat's states work on besides the state itself: the
// chat's identity and its way to the client.
type chatContext struct {
	id     ChatID // empty for a new chat until the adapter names it
	client Sender[Message]
}

// ChatSM is the machine a chat's state is handed with every event.
type ChatSM = StateMachine[ChatState]

// ChatState is one state of a chat, with the chat's events as its methods. A
// state handles the events that mean something in it, sends the client what is
// due, and switches by setting the state it moves to. An event it does not
// allow is an error, and leaves it where it was.
type ChatState interface {
	// Open: the client asked for the chat with request.
	Open(sm ChatSM, chat *chatContext, request MessageID) error
	// Answered: the adapter answered a request; it reports whether that was
	// the answer that opens this chat.
	Answered(sm ChatSM, chat *chatContext, msg Message) (bool, error)
	// Output: the adapter sent a message of this chat.
	Output(sm ChatSM, chat *chatContext, msg Message) error
	// Suspend: the client stopped showing the chat; it reports whether the
	// client waited on it until now.
	Suspend(sm ChatSM, chat *chatContext) (bool, error)
	// Close: the client ended the chat; it reports whether the client waited
	// on it until now.
	Close(sm ChatSM, chat *chatContext) (bool, error)
}

// ChatControl is one chat: its machine and its context. Whoever holds it fires
// an event by calling one of its methods and reads the outcome from what the
// method returns.
type ChatControl struct {
	sm   StateMachineImpl[ChatState]
	chat chatContext
}

// newChat is a chat the client has not asked for yet: a new one, with no ID.
func newChat(client Sender[Message]) *ChatControl {
	return &ChatControl{sm: StateMachineImpl[ChatState]{state: newChatUnopened()}, chat: chatContext{client: client}}
}

// newLocatedChat is a chat the agent program reported in state: Suspended when
// it runs, since its agent runs and nobody shows it, and Unopened when it is
// Stopped.
func newLocatedChat(id ChatID, state agent.State, client Sender[Message]) (*ChatControl, error) {
	var initial ChatState
	switch state {
	case agent.Running:
		initial = newChatSuspended(nil)
	case agent.Stopped:
		initial = newChatUnopened()
	default:
		return nil, fmt.Errorf("session: chat %s located in unhandled state %s", id, state)
	}
	return &ChatControl{sm: StateMachineImpl[ChatState]{state: initial}, chat: chatContext{id: id, client: client}}, nil
}

func (c *ChatControl) Open(request MessageID) error {
	_, err := fire(&c.sm, func(s ChatState) (struct{}, error) { return struct{}{}, s.Open(&c.sm, &c.chat, request) })
	return err
}

func (c *ChatControl) Answered(msg Message) (bool, error) {
	return fire(&c.sm, func(s ChatState) (bool, error) { return s.Answered(&c.sm, &c.chat, msg) })
}

func (c *ChatControl) Output(msg Message) error {
	_, err := fire(&c.sm, func(s ChatState) (struct{}, error) { return struct{}{}, s.Output(&c.sm, &c.chat, msg) })
	return err
}

func (c *ChatControl) Suspend() (bool, error) {
	return fire(&c.sm, func(s ChatState) (bool, error) { return s.Suspend(&c.sm, &c.chat) })
}

func (c *ChatControl) Close() (bool, error) {
	return fire(&c.sm, func(s ChatState) (bool, error) { return s.Close(&c.sm, &c.chat) })
}

// refused is embedded by every state: an event the state does not handle is
// an error. An answer is not an event a chat can refuse: one it did not wait
// for is simply not this chat's.
type refused struct{ name string }

func (r refused) err(chat *chatContext, event string) error {
	return fmt.Errorf("session: chat %q is %s and cannot be %s", chat.id, r.name, event)
}

func (r refused) Open(_ ChatSM, chat *chatContext, _ MessageID) error {
	return r.err(chat, "opened")
}
func (r refused) Answered(ChatSM, *chatContext, Message) (bool, error) { return false, nil }
func (r refused) Output(_ ChatSM, chat *chatContext, _ Message) error {
	return r.err(chat, "handed output")
}
func (r refused) Suspend(_ ChatSM, chat *chatContext) (bool, error) {
	return false, r.err(chat, "suspended")
}
func (r refused) Close(_ ChatSM, chat *chatContext) (bool, error) {
	return false, r.err(chat, "closed")
}

// unwatched is embedded by the states the client does not wait in: opening,
// suspending and closing the chat are the same from each of them.
type unwatched struct {
	refused
	held []Message // the adapter's questions nobody could answer yet
}

func (s unwatched) Open(sm ChatSM, _ *chatContext, request MessageID) error {
	sm.SetState(newChatOpening(request, s.held))
	return nil
}

func (s unwatched) Close(sm ChatSM, _ *chatContext) (bool, error) {
	sm.SetState(chatClosed{refused{"closed"}})
	return false, nil
}

// watched is embedded by the states the client waits in: output reaches it,
// and suspending or closing the chat is the same from each of them.
type watched struct {
	refused
	held []Message
}

func (s watched) Output(_ ChatSM, chat *chatContext, msg Message) error { return chat.client.Send(msg) }

func (s watched) Suspend(sm ChatSM, _ *chatContext) (bool, error) {
	sm.SetState(newChatSuspended(s.held))
	return true, nil
}

func (s watched) Close(sm ChatSM, _ *chatContext) (bool, error) {
	sm.SetState(chatClosed{refused{"closed"}})
	return true, nil
}

// chatUnopened: the chat is known and the client has not asked for it.
type chatUnopened struct{ unwatched }

func newChatUnopened() chatUnopened { return chatUnopened{unwatched{refused: refused{"unopened"}}} }

// Output of a chat nobody asked for is dropped: the adapter replays it when
// the chat is opened.
func (chatUnopened) Output(ChatSM, *chatContext, Message) error { return nil }

func (chatUnopened) Suspend(ChatSM, *chatContext) (bool, error) { return false, nil }

// chatOpening: the client asked for the chat and the adapter has not answered.
type chatOpening struct {
	watched
	request MessageID // the client's request that opens the chat
}

func newChatOpening(request MessageID, held []Message) chatOpening {
	return chatOpening{watched: watched{refused{"opening"}, held}, request: request}
}

func (s chatOpening) Answered(sm ChatSM, chat *chatContext, msg Message) (bool, error) {
	if msg.ID() != s.request {
		return false, nil
	}
	// The answer first, then the questions that waited for the client's
	// return: the client asks nothing of a chat it has not opened yet.
	for _, due := range append([]Message{msg}, s.held...) {
		if err := chat.client.Send(due); err != nil {
			return true, err
		}
	}
	// The answer names a new chat; one that returns keeps the ID it had.
	if id := msg.Chat(); id != "" {
		chat.id = id
	}
	sm.SetState(chatOpen{watched{refused: refused{"open"}}})
	return true, nil
}

// chatOpen: the client shows the chat.
type chatOpen struct{ watched }

// chatSuspended: the client does not show the chat, which runs on in the
// adapter.
type chatSuspended struct{ unwatched }

func newChatSuspended(held []Message) chatSuspended {
	return chatSuspended{unwatched{refused{"suspended"}, held}}
}

// Output holds a question, since nobody can answer it now and a request is not
// replayed; anything else is dropped, and the adapter replays it when the chat
// is opened again.
func (s chatSuspended) Output(sm ChatSM, _ *chatContext, msg Message) error {
	if msg.Kind() == Question {
		sm.SetState(newChatSuspended(append(append([]Message(nil), s.held...), msg)))
	}
	return nil
}

func (chatSuspended) Suspend(ChatSM, *chatContext) (bool, error) { return false, nil }

// chatClosed: the client ended the chat. It leaves the session's chats, so it
// is handed nothing more.
type chatClosed struct{ refused }
