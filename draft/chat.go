package draft

import "fmt"

// Three things nest, and each owns its own state:
//
//	Chat (the router)   one client connection: which session a message is for
//	  session           one workspace held attached: its adapter and its chats
//	    chat            one conversation: whether the client shows it
//
// The router decides nothing about a chat and a chat knows nothing about
// relays. Each level hands an event down. A session and a chat are state
// machines in the state pattern: a state is a type, an event is a method,
// and a state switches by setting the next one on its machine.

// ---- chat: one conversation, as the client sees it

// chatContext is what a chat's states work on besides the state itself: the
// chat's identity and the way to the client.
type chatContext struct {
	id     ChatID // empty for a new chat until the adapter names it
	sender Sender[Message]
}

// hands a message to the client
func (c *chatContext) send(msg Message) error {
	return c.sender.Send(msg)
}

// ChatSM is the machine a chat's state is handed with every event.
type ChatSM = StateMachine[ChatState]

// ChatState is one state of a chat, with the chat's events as its methods. A
// state handles the events that mean something in it, sends the client what
// is due, and switches by setting a new value of the state it moves to.
// Nothing asks a state what it holds or what it is.
type ChatState interface {
	Open(sm ChatSM, chat *chatContext, request MessageID) error
	Answered(sm ChatSM, chat *chatContext, msg Message) (bool, error)
	Output(sm ChatSM, chat *chatContext, msg Message) error
	Suspend(sm ChatSM, chat *chatContext) error
	Close(sm ChatSM, chat *chatContext) (bool, error)
}

// ChatControl is one conversation: its machine and its context. Whoever
// holds it fires an event by calling one of its methods and reads the
// outcome from what the method returns.
type ChatControl struct {
	sm   StateMachineImpl[ChatState]
	chat chatContext
}

type ChatSubscriber interface {
	NotifyOpened()
	NotifyAnswered()
	NotifySuspended()
	NotifyClosed()
}

func NewChatControl(id ChatID, sender Sender[Message]) *ChatControl {
	return &ChatControl{sm: StateMachineImpl[ChatState]{state: newChatUnopened()}, chat: chatContext{id: id, sender: sender}}
}

// Open: the client asked for the chat with this request. On success the
// client waits on the chat.
func (c *ChatControl) Open(request MessageID) error { return c.sm.state.Open(&c.sm, &c.chat, request) }

// Answered: the adapter answered a request. It reports whether that was the
// answer this chat waited for.
func (c *ChatControl) Answered(msg Message) (bool, error) {
	return c.sm.state.Answered(&c.sm, &c.chat, msg)
}

// Output: the adapter sent a message of this chat.
func (c *ChatControl) Output(msg Message) error { return c.sm.state.Output(&c.sm, &c.chat, msg) }

// Suspend: the client closed the chat's tab. On success the client no longer
// waits on the chat.
func (c *ChatControl) Suspend() error { return c.sm.state.Suspend(&c.sm, &c.chat) }

// Close: the client ended the chat. It reports whether the client was waiting
// on it until now.
func (c *ChatControl) Close() (bool, error) { return c.sm.state.Close(&c.sm, &c.chat) }

// refused is embedded by every state: an event the state does not handle is
// an error, and the chat stays where it was. An answer nobody waited for is
// not an error: it is simply not this chat's.
type refused struct{ name string }

func (r refused) Answered(ChatSM, *chatContext, Message) (bool, error) { return false, nil }
func (r refused) Open(ChatSM, *chatContext, MessageID) error {
	return fmt.Errorf("a chat that is %s cannot be opened", r.name)
}
func (r refused) Suspend(ChatSM, *chatContext) error {
	return fmt.Errorf("a chat that is %s cannot be suspended", r.name)
}
func (r refused) Close(ChatSM, *chatContext) (bool, error) {
	return false, fmt.Errorf("a chat that is %s cannot be closed", r.name)
}

// unwatched is embedded by the states the client does not wait in: opening
// the chat is the same from each of them.
type unwatched struct {
	refused
	held []Message // adapter requests nobody could answer yet
}

func (s unwatched) Open(sm ChatSM, _ *chatContext, request MessageID) error {
	sm.SetState(newChatOpening(request, s.held))
	return nil
}

// watched is embedded by the states the client waits in: output reaches it,
// and suspending or closing the chat is the same from each of them.
type watched struct {
	refused
	held []Message
}

func (s watched) Output(_ ChatSM, chat *chatContext, msg Message) error { return chat.send(msg) }
func (s watched) Suspend(sm ChatSM, _ *chatContext) error {
	sm.SetState(newChatSuspended(s.held))
	return nil
}
func (s watched) Close(sm ChatSM, _ *chatContext) (bool, error) {
	sm.SetState(newChatClosed())
	return true, nil
}

// chatUnopened: the chat is known and the client has not asked for it yet.
type chatUnopened struct{ unwatched }

func newChatUnopened() chatUnopened {
	return chatUnopened{unwatched{refused: refused{"unopened"}}}
}

func (s chatUnopened) Output(ChatSM, *chatContext, Message) error { return nil }

// chatOpening: the client asked for the chat and the adapter has not
// answered.
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
	// The answer first, then what waited for the client's return.
	for _, due := range append([]Message{msg}, s.held...) {
		if err := chat.send(due); err != nil {
			return true, err
		}
	}
	chat.id = msg.Chat() // the answer names a new chat
	sm.SetState(newChatOpen())
	return true, nil
}

// chatOpen: the client shows the chat.
type chatOpen struct{ watched }

func newChatOpen() chatOpen { return chatOpen{watched{refused: refused{"open"}}} }

// chatSuspended: the client does not show the chat, which runs on in the
// adapter.
type chatSuspended struct{ unwatched }

func newChatSuspended(held []Message) chatSuspended {
	return chatSuspended{unwatched{refused{"suspended"}, held}}
}

func (s chatSuspended) Output(sm ChatSM, _ *chatContext, msg Message) error {
	if msg.Kind() == Question {
		// Nobody can answer now, and a request is not replayed: it waits here.
		s.held = append(s.held, msg)
	}
	return nil // other output is replayed when the chat is reopened
}
func (s chatSuspended) Close(sm ChatSM, _ *chatContext) (bool, error) {
	sm.SetState(newChatClosed())
	return false, nil
}

// chatClosed: the client closed the chat. Nothing happens to it any more.
type chatClosed struct{ refused }

func newChatClosed() chatClosed { return chatClosed{refused{"closed"}} }

func (s chatClosed) Output(ChatSM, *chatContext, Message) error { return nil }
