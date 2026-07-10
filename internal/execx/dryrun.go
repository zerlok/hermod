package execx

import (
	"context"
	"fmt"
	"io"
	"strings"
)

type dryRun struct {
	out io.Writer
}

func (e dryRun) run(_ context.Context, cmd Command) (Result, error) {
	_, err := fmt.Fprintln(e.out, format(cmd))
	return Result{}, err
}

// format renders a Command as a single copy-pasteable shell line, including any
// working directory and environment so the printed form runs standalone.
func format(cmd Command) string {
	var b strings.Builder
	if cmd.Dir != "" {
		b.WriteString("cd ")
		b.WriteString(Quote(cmd.Dir))
		b.WriteString(" && ")
	}
	for _, e := range cmd.Env {
		if k, v, found := strings.Cut(e, "="); found {
			b.WriteString(k)
			b.WriteByte('=')
			b.WriteString(Quote(v))
		} else {
			b.WriteString(Quote(e))
		}
		b.WriteByte(' ')
	}
	b.WriteString(Join(cmd.Argv))
	return b.String()
}
