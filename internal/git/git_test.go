package git

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/shell"
)

// reply is what the fake prints for one git command, or the error it ends with.
type reply struct {
	out string
	err error
}

// fakeExec answers each command by its arguments after "git" and records every
// command it was asked to run, with the directory it ran in.
type fakeExec struct {
	replies map[string]reply
	ran     []shell.Command
}

func (f *fakeExec) Exec(_ context.Context, cmd shell.Command) shell.Execution {
	f.ran = append(f.ran, shell.Command{Args: cmd.Args, Dir: cmd.Dir})
	r, known := f.replies[strings.Join(cmd.Args[1:], " ")]
	if !known {
		r.err = errNotRepo
	}
	if cmd.Stdout != nil {
		_, _ = io.WriteString(cmd.Stdout, r.out)
	}
	return shell.Finished(cmd, r.err)
}

var errNotRepo = errors.New("exit status 128")

func gitCmd(dir string, args ...string) shell.Command {
	return shell.Command{Args: append([]string{"git"}, args...), Dir: dir}
}

func TestRead(t *testing.T) {
	const dir = "/home/dev/project"
	probes := []shell.Command{
		gitCmd(dir, "config", "--get", "user.name"),
		gitCmd(dir, "config", "--get", "user.email"),
	}
	cases := []struct {
		name    string
		replies map[string]reply
		want    Identity
		wantRan []shell.Command
	}{
		{
			name: "present",
			replies: map[string]reply{
				"config --get user.name":  {out: "Jane Doe\n"},
				"config --get user.email": {out: "jane@x.io\n"},
			},
			want:    Identity{Name: "Jane Doe", Email: "jane@x.io"},
			wantRan: probes,
		},
		{
			name:    "name only",
			replies: map[string]reply{"config --get user.name": {out: "Jane Doe\n"}},
			want:    Identity{Name: "Jane Doe"},
			wantRan: probes,
		},
		{
			name:    "email only",
			replies: map[string]reply{"config --get user.email": {out: "jane@x.io\n"}},
			want:    Identity{Email: "jane@x.io"},
			wantRan: probes,
		},
		{
			name:    "missing",
			replies: nil,
			want:    Identity{},
			wantRan: probes,
		},
		{
			name: "output of a failed read ignored",
			replies: map[string]reply{
				"config --get user.name":  {out: "partial", err: errNotRepo},
				"config --get user.email": {out: "partial", err: errNotRepo},
			},
			want:    Identity{},
			wantRan: probes,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeExec{replies: tc.replies}
			got := Read(context.Background(), f, dir)
			if got != tc.want {
				t.Errorf("Read() = %+v, want %+v", got, tc.want)
			}
			if !reflect.DeepEqual(f.ran, tc.wantRan) {
				t.Errorf("ran %v, want %v", f.ran, tc.wantRan)
			}
		})
	}
}

func TestMainWorktree(t *testing.T) {
	const (
		dir      = "/home/dev/feature"
		subdir   = "/home/dev/feature/sub"
		revParse = "rev-parse --path-format=absolute --git-dir --git-common-dir --show-toplevel"
		list     = "worktree list --porcelain"
		both     = "worktree /home/dev/project\nHEAD abc\nbranch refs/heads/main\n\nworktree /home/dev/feature\nHEAD def\nbranch refs/heads/feature\n"
		linked   = "/home/dev/project/.git/worktrees/feature\n/home/dev/project/.git\n/home/dev/feature\n"
	)
	probe := func(dir string) shell.Command {
		return gitCmd(dir, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir", "--show-toplevel")
	}
	listing := func(dir string) shell.Command { return gitCmd(dir, "worktree", "list", "--porcelain") }
	cases := []struct {
		name     string
		dir      string
		replies  map[string]reply
		wantMain string
		wantRoot string
		wantOK   bool
		wantRan  []shell.Command
	}{
		{
			name: "main tree",
			dir:  "/home/dev/project",
			replies: map[string]reply{
				revParse: {out: "/home/dev/project/.git\n/home/dev/project/.git\n/home/dev/project\n"},
				list:     {out: both},
			},
			wantRan: []shell.Command{probe("/home/dev/project")},
		},
		{
			name: "submodule",
			dir:  "/home/dev/project/lib",
			replies: map[string]reply{
				revParse: {out: "/home/dev/project/.git/modules/lib\n/home/dev/project/.git/modules/lib\n/home/dev/project/lib\n"},
				list:     {out: "worktree /home/dev/project/.git/modules/lib\nHEAD abc\nbranch refs/heads/main\n"},
			},
			wantRan: []shell.Command{probe("/home/dev/project/lib")},
		},
		{
			name: "linked worktree of a submodule",
			dir:  dir,
			replies: map[string]reply{
				revParse: {out: "/home/dev/project/.git/modules/lib/worktrees/feature\n/home/dev/project/.git/modules/lib\n/home/dev/feature\n"},
				list:     {out: "worktree /home/dev/project/.git/modules/lib\nHEAD abc\nbranch refs/heads/main\n\nworktree /home/dev/feature\nHEAD def\nbranch refs/heads/feature\n"},
			},
			wantRan: []shell.Command{probe(dir), listing(dir)},
		},
		{
			name:     "linked worktree",
			dir:      dir,
			replies:  map[string]reply{revParse: {out: linked}, list: {out: both}},
			wantMain: "/home/dev/project",
			wantRoot: "/home/dev/feature",
			wantOK:   true,
			wantRan:  []shell.Command{probe(dir), listing(dir)},
		},
		{
			name:     "subdirectory of a linked worktree",
			dir:      subdir,
			replies:  map[string]reply{revParse: {out: linked}, list: {out: both}},
			wantMain: "/home/dev/project",
			wantRoot: "/home/dev/feature",
			wantOK:   true,
			wantRan:  []shell.Command{probe(subdir), listing(subdir)},
		},
		{
			name: "linked worktree of a bare repository",
			dir:  dir,
			replies: map[string]reply{
				revParse: {out: "/home/dev/project.git/worktrees/feature\n/home/dev/project.git\n/home/dev/feature\n"},
				list:     {out: "worktree /home/dev/project.git\nbare\n\nworktree /home/dev/feature\nHEAD def\nbranch refs/heads/feature\n"},
			},
			wantRan: []shell.Command{probe(dir), listing(dir)},
		},
		{
			name:    "outside any repository",
			dir:     "/home/dev/plain",
			replies: nil,
			wantRan: []shell.Command{probe("/home/dev/plain")},
		},
		{
			name:    "inside a git dir, without a working tree",
			dir:     "/home/dev/project/.git",
			replies: map[string]reply{revParse: {out: "/home/dev/project/.git\n/home/dev/project/.git\n"}},
			wantRan: []shell.Command{probe("/home/dev/project/.git")},
		},
		{
			name:    "unreadable worktree list",
			dir:     dir,
			replies: map[string]reply{revParse: {out: linked}},
			wantRan: []shell.Command{probe(dir), listing(dir)},
		},
		{
			name:    "empty worktree list",
			dir:     dir,
			replies: map[string]reply{revParse: {out: linked}, list: {out: ""}},
			wantRan: []shell.Command{probe(dir), listing(dir)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeExec{replies: tc.replies}
			main, root, ok := MainWorktree(context.Background(), f, tc.dir)
			if main != tc.wantMain || root != tc.wantRoot || ok != tc.wantOK {
				t.Errorf("MainWorktree() = %q, %q, %v, want %q, %q, %v", main, root, ok, tc.wantMain, tc.wantRoot, tc.wantOK)
			}
			if !reflect.DeepEqual(f.ran, tc.wantRan) {
				t.Errorf("ran %v, want %v", f.ran, tc.wantRan)
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
