package control

import (
	"context"
	"errors"
	"io"
	"log"
	"reflect"
	"testing"

	"github.com/zerlok/hermod/internal/git"
	"github.com/zerlok/hermod/internal/mirror"
)

// fakeMirror records the lifecycle methods invoked on it, in order.
type fakeMirror struct{ calls []string }

func (m *fakeMirror) Flush(context.Context) error { m.calls = append(m.calls, "flush"); return nil }
func (m *fakeMirror) Pause(context.Context) error { m.calls = append(m.calls, "pause"); return nil }
func (m *fakeMirror) Close(context.Context) error { m.calls = append(m.calls, "close"); return nil }
func (m *fakeMirror) Status(context.Context) (mirror.State, error) {
	return mirror.Running, nil
}

// fakeSandbox reports a canned liveness for the teardown decision.
type fakeSandbox struct {
	alive   bool
	liveErr error
}

func (s fakeSandbox) Attach(context.Context) error           { return nil }
func (s fakeSandbox) IsActive(context.Context) (bool, error) { return s.alive, s.liveErr }

// TestTeardownByLiveness asserts the one decision that gives hermod its reason
// to exist: pause when alive or when liveness is unknown, flush+close only when
// the session is confirmed gone.
func TestTeardownByLiveness(t *testing.T) {
	cases := []struct {
		name      string
		alive     bool
		liveErr   error
		wantCalls []string
	}{
		{"alive pauses", true, nil, []string{"pause"}},
		{"gone flushes then closes", false, nil, []string{"flush", "close"}},
		{"ambiguous liveness pauses", false, errors.New("unreachable"), []string{"pause"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &fakeMirror{}
			err := teardown(context.Background(), m, fakeSandbox{alive: tc.alive, liveErr: tc.liveErr}, log.New(io.Discard, "", 0))
			if err != nil {
				t.Fatalf("teardown() error: %v", err)
			}
			if !reflect.DeepEqual(m.calls, tc.wantCalls) {
				t.Errorf("mirror calls = %v, want %v", m.calls, tc.wantCalls)
			}
		})
	}
}

func TestIdentityEnv(t *testing.T) {
	cases := []struct {
		name string
		id   git.Identity
		want []string
	}{
		{"full author and committer", git.Identity{Name: "Jane Doe", Email: "jane@x.io"}, []string{"GIT_AUTHOR_NAME=Jane Doe", "GIT_COMMITTER_NAME=Jane Doe", "GIT_AUTHOR_EMAIL=jane@x.io", "GIT_COMMITTER_EMAIL=jane@x.io"}},
		{"name only", git.Identity{Name: "Jane Doe"}, []string{"GIT_AUTHOR_NAME=Jane Doe", "GIT_COMMITTER_NAME=Jane Doe"}},
		{"empty injects nothing", git.Identity{}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := identityEnv(tc.id); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("identityEnv() = %v, want %v", got, tc.want)
			}
		})
	}
}
