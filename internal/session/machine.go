package session

import "errors"

// StateMachine is what a state is handed with every event: the machine it
// belongs to. The state decides whether to switch and to which state, by
// setting it; an event that sets nothing leaves the machine where it was.
type StateMachine[S any] interface {
	SetState(state S)
	// Abort marks the machine aborted: it takes no event any more. No state
	// calls it; the control that holds the machine does, when an event fails.
	Abort()
}

// StateMachineImpl holds the current state and is the one place it changes.
type StateMachineImpl[S any] struct {
	state   S
	aborted bool
}

func (sm *StateMachineImpl[S]) SetState(state S) { sm.state = state }

func (sm *StateMachineImpl[S]) Abort() { sm.aborted = true }

// errAborted is an event handed to a machine an earlier event failed on. A
// failed event may have left the level's data half changed, so nothing more
// is decided from it.
var errAborted = errors.New("session: the state machine was aborted by an earlier error")

// fire hands an event to the current state, unless the machine was aborted,
// and aborts it when the event fails.
func fire[S, R any](sm *StateMachineImpl[S], event func(S) (R, error)) (R, error) {
	var zero R
	if sm.aborted {
		return zero, errAborted
	}
	r, err := event(sm.state)
	if err != nil {
		sm.Abort()
	}
	return r, err
}
