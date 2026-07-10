package shell

import (
	"context"

	"github.com/zerlok/hermod/internal/execx"
)

// local is the leaf Shell: it runs the command on this machine, delegating to
// the embedded Executor (which chooses real-vs-printed, so a dry-run is a leaf
// swap).
type local struct{ execx.Executor }

// NewLocal returns the leaf Shell that executes on this machine.
func NewLocal(exec execx.Executor) Shell {
	return local{exec}
}

func (l local) Run(ctx context.Context, cmd execx.Command) (execx.Result, error) {
	return l.Execute(ctx, cmd)
}
