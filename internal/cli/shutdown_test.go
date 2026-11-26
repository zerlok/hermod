package cli

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestShutdown checks the run context against the signals, and the deadline
// and end of a teardown started before or after them.
func TestShutdown(t *testing.T) {
	const timeout = 10 * time.Second
	cases := []struct {
		name string
		// signalAt and teardownAt are offsets from the test's start; a
		// teardown that starts first is started before any signal.
		signals       int
		signalAt      time.Duration
		teardownAt    time.Duration
		teardownFirst bool
		wantRunEnded  bool
		wantDeadline  time.Duration // offset from the test's start
		wantErr       error
	}{
		{"a teardown without a signal is bounded from its start", 0, 0, 2 * time.Second, false, false, 12 * time.Second, nil},
		{"a teardown within the deadline after a signal", 1, 0, 2 * time.Second, false, true, timeout, nil},
		{"a teardown cut by the deadline counted from the signal", 1, -time.Hour, 0, false, true, timeout - time.Hour, context.DeadlineExceeded},
		{"a signal during a teardown keeps its deadline", 1, 2 * time.Second, 0, true, true, timeout, nil},
		{"a second signal ends the teardown", 2, 0, time.Second, false, true, timeout, context.Canceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			var clock time.Time
			signals := make(chan os.Signal)
			s, stop := newShutdown(signals, func() time.Time { return clock })
			defer stop()
			startTeardown := func() (context.Context, context.CancelFunc) {
				clock = start.Add(tc.teardownAt)
				return s.teardown(timeout)()
			}
			var (
				ctx    context.Context
				cancel context.CancelFunc
			)
			if tc.teardownFirst {
				ctx, cancel = startTeardown()
			}
			clock = start.Add(tc.signalAt)
			for range tc.signals {
				// An unbuffered send returns once the watcher took the signal.
				signals <- syscall.SIGTERM
			}
			if tc.signals > 0 {
				<-s.run.Done()
			}
			if !tc.teardownFirst {
				ctx, cancel = startTeardown()
			}
			defer cancel()

			if got := s.run.Err() != nil; got != tc.wantRunEnded {
				t.Errorf("run ended %v, want %v", got, tc.wantRunEnded)
			}
			if got, _ := ctx.Deadline(); !got.Equal(start.Add(tc.wantDeadline)) {
				t.Errorf("deadline %v after the start, want %v", got.Sub(start), tc.wantDeadline)
			}
			if tc.wantErr != nil {
				<-ctx.Done()
			}
			if err := ctx.Err(); !errors.Is(err, tc.wantErr) || (err == nil) != (tc.wantErr == nil) {
				t.Errorf("teardown ended with %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestTeardownTimeout checks the deadline every command hands its teardown:
// from the flag, else the variable, else the default, the stand-in reading
// the variable only.
func TestTeardownTimeout(t *testing.T) {
	cases := []struct {
		name    string
		argv    string
		env     vars
		want    []time.Duration
		wantErr string
	}{
		{"the default", "hermod attach prod-box", vars{}, []time.Duration{defaultTeardownTimeout}, ""},
		{"the flag", "hermod attach prod-box --teardown-timeout 30s", vars{}, []time.Duration{30 * time.Second}, ""},
		{"the variable", "hermod attach prod-box", vars{envTeardownTimeout: "30s"}, []time.Duration{30 * time.Second}, ""},
		{"the flag wins over the variable", "hermod attach prod-box --teardown-timeout 5s", vars{envTeardownTimeout: "30s"}, []time.Duration{5 * time.Second}, ""},
		{"a blank variable is unset", "hermod attach prod-box", vars{envTeardownTimeout: " "}, []time.Duration{defaultTeardownTimeout}, ""},
		{"a chat's flag", "hermod air acp claude prod-box --teardown-timeout 1m", vars{}, []time.Duration{time.Minute}, ""},
		{"a chat's variable", "hermod air acp claude prod-box", vars{envTeardownTimeout: "1m"}, []time.Duration{time.Minute}, ""},
		{"the stand-in's variable", "claude --resume x", vars{envRemote: "prod-box", envTeardownTimeout: "30s"}, []time.Duration{30 * time.Second}, ""},
		{"the stand-in's default", "claude --resume x", vars{envRemote: "prod-box"}, []time.Duration{defaultTeardownTimeout}, ""},
		{"a variable that is no duration", "hermod attach prod-box", vars{envTeardownTimeout: "soon"}, nil, envTeardownTimeout},
		{"a flag that is no duration", "hermod attach prod-box --teardown-timeout soon", vars{}, nil, teardownTimeoutFlag},
		{"no time at all", "hermod attach prod-box --teardown-timeout 0s", vars{}, nil, "no time to tear down"},
		{"a negative variable", "hermod air acp claude prod-box", vars{envTeardownTimeout: "-1s"}, nil, "no time to tear down"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, r := newApp(t, setup{env: tc.env, terminal: true})
			err := execute(context.Background(), a, strings.Fields(tc.argv))
			if (err != nil) != (tc.wantErr != "") || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("execute: %v, want an error holding %q", err, tc.wantErr)
			}
			if !slices.Equal(r.teardowns, tc.want) {
				t.Errorf("teardown deadlines %v, want %v", r.teardowns, tc.want)
			}
			if tc.wantErr != "" && r.called() != 0 {
				t.Errorf("the core was called %d times, want none", r.called())
			}
		})
	}
}
