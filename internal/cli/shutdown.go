package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/zerlok/hermod/internal/workspace"
)

// defaultTeardownTimeout bounds a teardown when neither --teardown-timeout nor
// HERMOD_TEARDOWN_TIMEOUT is given.
const defaultTeardownTimeout = 10 * time.Second

// shutdown is the contexts a command runs and tears down under: the run ends
// at the first SIGINT or SIGTERM, and every teardown at its deadline or at a
// second signal.
type shutdown struct {
	run    context.Context
	forced context.Context // ended by a second signal
	now    func() time.Time

	mu        sync.Mutex
	signalled time.Time // the first signal's time; zero before it
}

// newShutdown watches signals until stop is called.
func newShutdown(signals <-chan os.Signal, now func() time.Time) (s *shutdown, stop func()) {
	run, endRun := context.WithCancel(context.Background())
	forced, force := context.WithCancel(context.Background())
	s = &shutdown{run: run, forced: forced, now: now}
	stopped := make(chan struct{})
	go func() {
		for first := true; ; first = false {
			select {
			case <-signals:
			case <-stopped:
				return
			}
			if !first {
				force()
				return
			}
			s.mu.Lock()
			s.signalled = now()
			s.mu.Unlock()
			endRun()
		}
	}()
	return s, func() {
		close(stopped)
		endRun()
		force()
	}
}

// teardown starts a teardown context whose deadline is timeout after the
// start or after the first signal, whichever came first: a teardown that
// follows a signal has had its time counted since the user asked to stop.
func (s *shutdown) teardown(timeout time.Duration) workspace.Teardown {
	return func() (context.Context, context.CancelFunc) {
		start := s.now()
		s.mu.Lock()
		if !s.signalled.IsZero() && s.signalled.Before(start) {
			start = s.signalled
		}
		s.mu.Unlock()
		return context.WithDeadline(s.forced, start.Add(timeout))
	}
}

// teardownTimeout is the teardown's deadline as cmd's flag gives it, else
// the environment.
func teardownTimeout(cmd *cobra.Command, lookupEnv func(string) (string, bool)) (time.Duration, error) {
	if !cmd.Flags().Changed(teardownTimeoutFlag) {
		return resolveTeardownTimeout(nil, lookupEnv)
	}
	flag, err := cmd.Flags().GetDuration(teardownTimeoutFlag)
	if err != nil {
		return 0, err
	}
	return resolveTeardownTimeout(&flag, lookupEnv)
}

// resolveTeardownTimeout is the teardown's deadline: the flag when it was
// given, else the environment's, else the default. A blank variable counts as
// unset. The stand-in passes no flag, since all its arguments are Claude's.
func resolveTeardownTimeout(flag *time.Duration, lookupEnv func(string) (string, bool)) (time.Duration, error) {
	timeout, source := defaultTeardownTimeout, ""
	switch v, _ := lookupEnv(envTeardownTimeout); {
	case flag != nil:
		timeout, source = *flag, "--"+teardownTimeoutFlag
	case strings.TrimSpace(v) != "":
		d, err := time.ParseDuration(v)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", envTeardownTimeout, err)
		}
		timeout, source = d, envTeardownTimeout
	}
	if timeout <= 0 {
		return 0, fmt.Errorf("%s: %s is no time to tear down in", source, timeout)
	}
	return timeout, nil
}
