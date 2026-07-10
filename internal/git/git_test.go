package git

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/zerlok/hermod/internal/execx"
	"github.com/zerlok/hermod/internal/shell"
)

// fakeShell answers git-config reads keyed on the requested config key (the last
// argv element), and records every argv it was asked to run.
type fakeShell struct {
	out  map[string]string
	err  map[string]error
	seen [][]string
}

func (f *fakeShell) Run(_ context.Context, cmd execx.Command) (execx.Result, error) {
	f.seen = append(f.seen, cmd.Argv)
	key := cmd.Argv[len(cmd.Argv)-1]
	return execx.Result{Stdout: f.out[key]}, f.err[key]
}

// factoryOf serves a fixed shell as the Real (probe) leaf git reads from.
type factoryOf struct{ sh shell.Shell }

func (f factoryOf) Real() shell.Shell      { return f.sh }
func (f factoryOf) Effective() shell.Shell { return f.sh }

func TestRead(t *testing.T) {
	readErr := errors.New("exit status 1")
	cases := []struct {
		name string
		out  map[string]string
		err  map[string]error
		want Identity
	}{
		{"full", map[string]string{"user.name": "Jane Doe\n", "user.email": "jane@x.io\n"}, nil, Identity{Name: "Jane Doe", Email: "jane@x.io"}},
		{"name only", map[string]string{"user.name": "Jane Doe\n"}, nil, Identity{Name: "Jane Doe"}},
		{"email only", map[string]string{"user.email": "jane@x.io\n"}, nil, Identity{Email: "jane@x.io"}},
		{"empty", nil, nil, Identity{}},
		{"read failure non-fatal", nil, map[string]error{"user.name": readErr, "user.email": readErr}, Identity{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := New(factoryOf{sh: &fakeShell{out: tc.out, err: tc.err}}, "").Read(context.Background())
			if got != tc.want {
				t.Errorf("Read() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestReadIsReadOnly(t *testing.T) {
	cases := []struct {
		name string
		want [][]string
	}{
		{"only --get probes", [][]string{
			{"git", "config", "--get", "user.name"},
			{"git", "config", "--get", "user.email"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeShell{}
			New(factoryOf{sh: f}, "").Read(context.Background())
			if !reflect.DeepEqual(f.seen, tc.want) {
				t.Errorf("issued %v, want %v", f.seen, tc.want)
			}
		})
	}
}

func TestIdentityIsZero(t *testing.T) {
	cases := []struct {
		name string
		id   Identity
		want bool
	}{
		{"empty", Identity{}, true},
		{"name set", Identity{Name: "Jane Doe"}, false},
		{"email set", Identity{Email: "jane@x.io"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.id.IsZero(); got != tc.want {
				t.Errorf("IsZero() = %v, want %v", got, tc.want)
			}
		})
	}
}
