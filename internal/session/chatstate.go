package session

import (
	"fmt"

	"github.com/zerlok/hermod/internal/agent"
)

// chatContext is what a chat's states work on besides the state itself: the
// chat's identity and its way to the client.
type chatContext struct {
	id ChatID // empty for a new chat until the adapter names it
	// request is the client's request that opens the chat, which the answer
	// that opens it returns to.
	request Answer
	// toClient hands the client an adapter's message: a notification, or a
	// request passed on as a call whose answer returns to the adapter.
	toClient func(in Inbound) error
}

// ChatSM is the machine a chat's state is handed with every event.
type ChatSM = StateMachine[ChatState]

// ChatState is one state of a chat, with the chat's events as its methods. A
// state handles the events that mean something in it, hands the client what
// is due, and switches by setting the state it moves to. An event it does not
// allow is an error, and leaves it where it was.
type ChatState interface {
	// Open: the client asked for the chat.
	Open(sm ChatSM, chat *chatContext) error
	// Answered: the call that passed the client's request to open the chat
	// returned answer, or failed with err; it reports whether that return
	// ended the opening.
	Answered(sm ChatSM, chat *chatContext, answer Message, err error) (bool, error)
	// Output: the adapter sent a message of this chat.
	Output(sm ChatSM, chat *chatContext, in Inbound) error
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
func newChat(toClient func(Inbound) error) *ChatControl {
	return &ChatControl{sm: StateMachineImpl[ChatState]{state: newChatUnopened()}, chat: chatContext{toClient: toClient}}
}

// newLocatedChat is a chat the agent program reported in state: Suspended when
// it runs, since its agent runs and nobody shows it, and Unopened when it is
// Stopped.
func newLocatedChat(id ChatID, state agent.State, toClient func(Inbound) error) (*ChatControl, error) {
	var initial ChatState
	switch state {
	case agent.Running:
		initial = newChatSuspended(nil)
	case agent.Stopped:
		initial = newChatUnopened()
	default:
		return nil, fmt.Errorf("session: chat %s located in unhandled state %s", id, state)
	}
	return &ChatControl{sm: StateMachineImpl[ChatState]{state: initial}, chat: chatContext{id: id, toClient: toClient}}, nil
}

// Open opens the chat for the client's request, which the answer that opens
// it returns to.
func (c *ChatControl) Open(request Answer) error {
	_, err := fire(&c.sm, func(s ChatState) (struct{}, error) {
		c.chat.request = request
		return struct{}{}, s.Open(&c.sm, &c.chat)
	})
	return err
}

// Answered hands the chat what the call that passed request on returned. Only
// the call of the request the chat opens for can open it: one of an earlier
// open, which a suspend left behind, reports false.
func (c *ChatControl) Answered(request Answer, answer Message, err error) (bool, error) {
	// NOTE: an Answer is a pointer, or a value of comparable fields, so
	// comparing two tells whether they answer the same request.
	if request != c.chat.request {
		return false, nil
	}
	return fire(&c.sm, func(s ChatState) (bool, error) { return s.Answered(&c.sm, &c.chat, answer, err) })
}

func (c *ChatControl) Output(in Inbound) error {
	_, err := fire(&c.sm, func(s ChatState) (struct{}, error) { return struct{}{}, s.Output(&c.sm, &c.chat, in) })
	return err
}

func (c *ChatControl) Suspend() (bool, error) {
	return fire(&c.sm, func(s ChatState) (bool, error) { return s.Suspend(&c.sm, &c.chat) })
}

func (c *ChatControl) Close() (bool, error) {
	return fire(&c.sm, func(s ChatState) (bool, error) { return s.Close(&c.sm, &c.chat) })
}

// refused is embedded by every state: an event the state does not handle is
// an error. An answer is not an event a chat can refuse: one that comes when
// the chat no longer waits for it opens nothing.
type refused struct{ name string }

func (r refused) err(chat *chatContext, event string) error {
	return fmt.Errorf("session: chat %q is %s and cannot be %s", chat.id, r.name, event)
}

func (r refused) Open(_ ChatSM, chat *chatContext) error                      { return r.err(chat, "opened") }
func (r refused) Answered(ChatSM, *chatContext, Message, error) (bool, error) { return false, nil }
func (r refused) Output(_ ChatSM, chat *chatContext, _ Inbound) error {
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
	held []Inbound // the adapter's questions nobody could answer yet
}

func (s unwatched) Open(sm ChatSM, _ *chatContext) error {
	sm.SetState(newChatOpening(s.held))
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
	held []Inbound
}

func (s watched) Output(_ ChatSM, chat *chatContext, in Inbound) error { return chat.toClient(in) }

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
func (chatUnopened) Output(ChatSM, *chatContext, Inbound) error { return nil }

func (chatUnopened) Suspend(ChatSM, *chatContext) (bool, error) { return false, nil }

// chatOpening: the client asked for the chat and the adapter has not answered.
type chatOpening struct{ watched }

func newChatOpening(held []Inbound) chatOpening {
	return chatOpening{watched{refused{"opening"}, held}}
}

// Answered returns the answer to the client first, then the questions that
// waited for the client's return: the client asks nothing of a chat it has
// not opened yet. A chat whose opening failed is open all the same: the client
// that returned to it shows it.
func (s chatOpening) Answered(sm ChatSM, chat *chatContext, answer Message, failed error) (bool, error) {
	if err := respond(chat.request, answer, failed); err != nil {
		return true, err
	}
	for _, in := range s.held {
		if err := chat.toClient(in); err != nil {
			return true, err
		}
	}
	// The answer names a new chat; one that returns keeps the ID it had.
	if failed == nil && answer.Chat() != "" {
		chat.id = answer.Chat()
	}
	sm.SetState(chatOpen{watched{refused: refused{"open"}}})
	return true, nil
}

// chatOpen: the client shows the chat.
type chatOpen struct{ watched }

// chatSuspended: the client does not show the chat, which runs on in the
// adapter.
type chatSuspended struct{ unwatched }

func newChatSuspended(held []Inbound) chatSuspended {
	return chatSuspended{unwatched{refused{"suspended"}, held}}
}

// Output holds a question, since nobody can answer it now and a request is not
// replayed; a notification is dropped, and the adapter replays it when the
// chat is opened again.
func (s chatSuspended) Output(sm ChatSM, _ *chatContext, in Inbound) error {
	if in.Answer != nil {
		sm.SetState(newChatSuspended(append(append([]Inbound(nil), s.held...), in)))
	}
	return nil
}

func (chatSuspended) Suspend(ChatSM, *chatContext) (bool, error) { return false, nil }

// chatClosed: the client ended the chat. It leaves the session's chats, so it
// is handed nothing more.
type chatClosed struct{ refused }
