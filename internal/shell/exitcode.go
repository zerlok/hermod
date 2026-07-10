package shell

import "errors"

// ExitCode extracts a process exit code from err. It matches any error carrying
// an ExitCode() method — *exec.ExitError does, so real subprocess failures work,
// and fakes can implement it too. ok is false when err carries no exit code
// (e.g. the process never started, or a non-process error).
func ExitCode(err error) (code int, ok bool) {
	var e interface{ ExitCode() int }
	if errors.As(err, &e) {
		return e.ExitCode(), true
	}
	return 0, false
}
