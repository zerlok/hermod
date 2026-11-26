package session

import (
	"context"
	"fmt"
	"io"

	"github.com/zerlok/hermod/internal/shell"
	"github.com/zerlok/hermod/internal/workspace"
)

// inDir runs its arguments after the first in the directory the first names,
// or in the remote user's home when that directory does not exist, so a call
// that reads the project's settings sees them wherever the project is.
const inDir = `cd -- "$1" 2>/dev/null || cd; shift; exec "$@"`

// oneShot runs a call that names no conversation on the remote, as given,
// under the remote user's login shell, with the caller's streams; a failure
// that carries the program's exit code is that ExitStatus. No workspace,
// worker or relay takes part: the project's remote directory is only read from
// the relays already there, and a read that fails leaves the call in the home.
//
// No variable of the caller's reaches the program: a secret must not go on a
// command line, and the call's stdin is the caller's, so with no worker's
// directory there is no other way to it.
func oneShot(ctx context.Context, req Request) error {
	dir := req.Remote.Home
	paths, err := workspace.Map(ctx, req.Remote, req.Local, req.Dir)
	if err != nil {
		req.logger().Printf("running in the remote home: %v", err)
	} else {
		dir = paths.ToRemote(req.Dir)
	}
	in := req.Streams.In
	if in == nil {
		in = shell.NoInput
	}
	cmd := shell.Command{
		Args:   append([]string{"sh", "-c", inDir, "sh", dir}, req.Args...),
		Stdin:  in,
		Stdout: orDiscard(req.Streams.Out),
		Stderr: orDiscard(req.Streams.Err),
	}
	err = shell.NewLogin(shell.NewSSH(req.Remote.Exec, req.Remote.Host)).Exec(ctx, cmd).Wait()
	if err == nil {
		return nil
	}
	// A negative code is a process a signal ended: there is no code to pass on.
	if code, ok := shell.ExitCode(err); ok && code > 0 {
		return ExitStatus(code)
	}
	return fmt.Errorf("run %q on %s: %w", req.Args, req.Remote.Host, err)
}

func orDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}
