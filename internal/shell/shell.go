// Package shell answers where a command runs. A Shell rewrites an argv for a
// transport (ssh, tmux) and delegates inward; the Local leaf hands off to an
// execx.Executor. Decorators nest freely — the caller composes the stack it
// needs — which is what keeps the tool both dry-runnable and unit-testable.
package shell

import (
	"context"

	"github.com/zerlok/hermod/internal/execx"
)

// Shell runs a command in a particular place. Decorators wrap the argv and call
// an inner Shell; Local is the innermost, executing on this machine.
type Shell interface {
	Run(ctx context.Context, cmd execx.Command) (execx.Result, error)
}
