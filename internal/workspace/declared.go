package workspace

import (
	"github.com/zerlok/hermod/internal/relay"
	"github.com/zerlok/hermod/internal/worker"
)

// What a caller passes to a workspace and reads from it, so that it reaches
// workers and relays through the workspace alone. Each is the worker's or the
// relay's own, named here: the workspace adds no meaning to them, and a second
// definition would drift from the one the worker or the relay acts on.

// Key identifies a worker of the workspace.
type Key = worker.Key

// NewKey checks a worker's key, as a caller that names a worker gives it.
func NewKey(name string) (Key, error) { return worker.NewKey(name) }

// Form is how a worker offers its process's stdio, fixed when it starts.
type Form = worker.Form

// The forms of a worker's stdio.
const (
	Terminal   = worker.Terminal
	LineStream = worker.LineStream
)

// WorkerState is what tmux says of a worker.
type WorkerState = worker.State

// The states of a worker.
const (
	WorkerAbsent  = worker.Absent
	WorkerRunning = worker.Running
	WorkerExited  = worker.Exited
)

// Look is a worker's state with the files its directory records, as Look
// reads them in one call.
type Look = worker.Look

// Streams are the caller's side of an attach: what the worker's stdio is
// copied from and to.
type Streams = relay.Streams

// Why an attach or a connect lease ended.
var (
	// ErrLeft is an attach whose caller's input ended.
	ErrLeft = relay.ErrLeft
	// ErrEnded is an attach whose worker's process ended.
	ErrEnded = relay.ErrEnded
	// ErrInUse is a line stream refused because another lease reads it.
	ErrInUse = relay.ErrInUse
)
