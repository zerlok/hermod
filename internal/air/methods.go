package air

import "github.com/zerlok/hermod/internal/session"

// Methods is what Air's own ACP methods mean to a session. The standard
// methods are acp's to classify and are not here.
func Methods() map[string]session.Kind {
	return map[string]session.Kind{
		// Air sends it for every closed tab and at an IDE quit, so it only
		// hides the chat; an adapter that received it would end the chat.
		"session/close": session.Suspend,
		// "Mark done": the one message that ends a chat.
		"_session/archive": session.Close,
	}
}
