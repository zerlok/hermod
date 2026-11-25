package session

import (
	"errors"
	"fmt"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/worker"
)

// action is what a session does with its worker.
type action int

const (
	join    action = iota + 1 // attach to the worker as it runs
	start                     // start the worker, then attach
	replace                   // end the worker, then start it again and attach
)

func (a action) String() string {
	switch a {
	case join:
		return "attach"
	case start:
		return "start"
	case replace:
		return "remove, then start"
	}
	return fmt.Sprintf("action(%d)", int(a))
}

// ErrRunsOutside is a conversation the agent program runs while no worker of
// Hermod's runs it: a start would run a second copy of it.
var ErrRunsOutside = errors.New("session: the conversation runs outside Hermod; end it there first")

// states are the two states a session decides from.
type states struct {
	worker    worker.State
	runsAgent bool        // the worker runs one agent's conversation
	agent     agent.State // that conversation's, when it does
}

// decide is what to do for a pair of states. A worker that runs no agent has
// only its own state to go by. Whether a Stopped conversation can be resumed
// is not a state: the caller's arguments say whether to resume, and the agent
// program refuses when there is nothing to.
func decide(s states) (action, error) {
	switch s.worker {
	case worker.Absent, worker.Running, worker.Exited:
	default:
		return 0, fmt.Errorf("session: unhandled worker state %s", s.worker)
	}
	if !s.runsAgent {
		switch s.worker {
		case worker.Running:
			return join, nil
		case worker.Exited:
			return replace, nil
		}
		return start, nil
	}
	switch s.agent {
	case agent.Running:
		// An Exited worker's process has failed, so a conversation that runs
		// all the same runs in another process, as with no worker at all.
		if s.worker == worker.Running {
			return join, nil
		}
		return 0, ErrRunsOutside
	case agent.Stopped:
		if s.worker == worker.Absent {
			return start, nil
		}
		return replace, nil
	}
	return 0, fmt.Errorf("session: unhandled agent state %s", s.agent)
}
