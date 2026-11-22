package draft

// ---- machine: what every state machine here shares

type StateMachine[S any] interface {
	SetState(S)
	Abort()
}

// StateMachineImpl is what a state is handed with every event: the machine it belongs to.
// The state decides whether to switch, when, and to which state, by setting
// it. An event that sets nothing leaves the machine where it was.
// machine holds the current state and is the one place it changes.
type StateMachineImpl[S any] struct {
	state   S
	aborted bool
}

func (sm *StateMachineImpl[S]) SetState(state S) { sm.state = state }

func (sm *StateMachineImpl[S]) Abort() { sm.aborted = true }
