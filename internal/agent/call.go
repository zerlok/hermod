package agent

import "errors"

// form is what a call asks for. It is closed: a Call is built only by the
// constructors below.
type form int

const (
	// command is the zero form, so an unparsed call is never taken for a
	// conversation: the agent does not run it, and it runs as given.
	command form = iota
	oneShot
	conversation
)

// Call is what an agent read from a command's arguments: a conversation and
// its ID, a one-shot call, or a command the agent does not run.
type Call struct {
	form form
	id   string
	args []string
}

// NewConversation is a call that runs the conversation the agent program
// stores under id.
func NewConversation(id string, args []string) (Call, error) {
	if id == "" {
		return Call{}, errors.New("agent: a conversation needs an ID")
	}
	return Call{form: conversation, id: id, args: clone(args)}, nil
}

// NewOneShot is a call that runs to completion without a conversation, such as
// a subcommand or a version query.
func NewOneShot(args []string) Call { return Call{form: oneShot, args: clone(args)} }

// NewCommand is a command the agent does not run: it runs as given, with no
// agent.
func NewCommand(args []string) Call { return Call{form: command, args: clone(args)} }

// ID is the conversation's ID; ok is false for every other call.
func (c Call) ID() (id string, ok bool) { return c.id, c.form == conversation }

// OneShot reports whether the call runs to completion without a conversation.
func (c Call) OneShot() bool { return c.form == oneShot }

// Args is the command as given, program first.
func (c Call) Args() []string { return clone(c.args) }

func clone[T any](s []T) []T {
	if s == nil {
		return nil
	}
	return append([]T(nil), s...)
}
