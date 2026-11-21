package sandbox

import (
	"context"
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/shell"
)

func TestRootLiveness(t *testing.T) {
	const tagged = "home=/home/u\ndir=dev/api\ndir=/srv/web/app\ndir=\nend\n"
	// One captured ssh call to the sandbox, whatever the answer.
	oneLook := []look{{via: []string{"ssh", "prod"}, capture: true}}
	cases := []struct {
		name      string
		out       string
		err       error
		root      string
		want      bool
		wantErr   bool
		wantLooks []look
	}{
		{"a session in the root", tagged, nil, "dev/api", true, false, oneLook},
		{"a session under the root", tagged, nil, "dev", true, false, oneLook},
		{"an absolute root against a home-relative tag", tagged, nil, "/home/u/dev/api", true, false, oneLook},
		{"a home-relative root against an absolute tag", "home=/home/u\ndir=/home/u/dev/api\nend\n", nil, "dev/api", true, false, oneLook},
		{"an absolute tag", tagged, nil, "/srv/web", true, false, oneLook},
		{"a session above the root does not keep it", tagged, nil, "dev/api/cmd", false, false, oneLook},
		{"a session beside the root does not keep it", tagged, nil, "dev/ap", false, false, oneLook},
		{"untagged sessions keep nothing", "home=/home/u\ndir=\ndir=\nend\n", nil, "dev", false, false, oneLook},
		{"no sessions", "home=/home/u\nend\n", nil, "dev/api", false, false, oneLook},
		{"a root with a trailing slash", tagged, nil, "dev/api/", true, false, oneLook},
		{"an incomplete look is unknown", "home=/home/u\ndir=dev/api\n", nil, "dev/api", false, true, oneLook},
		{"a look without the home is unknown", "dir=dev/api\nend\n", nil, "dev/api", false, true, oneLook},
		{"a failed look is unknown", "", exitErr{code: 255}, "dev/api", false, true, oneLook},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leaf := &scriptedShell{ret: shell.Result{Stdout: tc.out}, err: tc.err}
			got, err := NewLiveness(leaf, "prod").Root(tc.root).IsActive(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("IsActive() error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("IsActive() = %v, want %v", got, tc.want)
			}
			if got := looks(leaf.calls); !reflect.DeepEqual(got, tc.wantLooks) {
				t.Errorf("looks = %+v, want %+v", got, tc.wantLooks)
			}
		})
	}
}

// look is the shape of one call the liveness look made: the transport it went
// through and whether its output was captured.
type look struct {
	via     []string
	capture bool
}

func looks(calls []leafCall) []look {
	var got []look
	for _, c := range calls {
		got = append(got, look{via: c.cmd.Argv[:min(2, len(c.cmd.Argv))], capture: c.cmd.Capture})
	}
	return got
}

// TestLivenessLooksOnce asserts the mirrors of one run settle on one look.
func TestLivenessLooksOnce(t *testing.T) {
	cases := []struct {
		name      string
		out       string
		roots     []string
		want      []bool
		wantLooks int
	}{
		{"project and transcript roots", "home=/home/u\ndir=dev/api\nend\n", []string{"dev", "dev/api", "web"}, []bool{true, true, false}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leaf := &scriptedShell{ret: shell.Result{Stdout: tc.out}}
			live := NewLiveness(leaf, "prod")
			for i, root := range tc.roots {
				got, err := live.Root(root).IsActive(context.Background())
				if err != nil || got != tc.want[i] {
					t.Errorf("root %s: IsActive() = %v, %v, want %v", root, got, err, tc.want[i])
				}
			}
			if len(leaf.calls) != tc.wantLooks {
				t.Errorf("looked %d times, want %d", len(leaf.calls), tc.wantLooks)
			}
		})
	}
}

// TestLivenessScript runs the script against a stand-in tmux that answers as
// the real one does.
func TestLivenessScript(t *testing.T) {
	cases := []struct {
		name       string
		tmux       string // the stand-in tmux's body
		wantOut    string
		wantStatus int
	}{
		{"tagged and untagged sessions", "printf 'dir=dev/api\\ndir=\\n'", "home={home}\ndir=dev/api\ndir=\nend\n", 0},
		{"no server running", "echo 'no server running on /tmp/tmux-1000/default' >&2; exit 1", "home={home}\nend\n", 0},
		{"no socket", "echo 'error connecting to /tmp/tmux-1000/default (No such file or directory)' >&2; exit 1", "home={home}\nend\n", 0},
		{"any other failure is unknown", "echo 'lost server' >&2; exit 1", "home={home}\n", 5},
	}
	sh := lookSh(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			bin := filepath.Join(home, "bin")
			writeExecutable(t, filepath.Join(bin, "tmux"), "#!/bin/sh\n"+tc.tmux+"\n")
			cmd := osexec.Command(sh, "-c", livenessScript)
			cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":" + os.Getenv("PATH")}
			out, err := cmd.Output()
			status := 0
			if err != nil {
				var exit *osexec.ExitError
				if !errors.As(err, &exit) {
					t.Fatalf("liveness script: %v", err)
				}
				status = exit.ExitCode()
			}
			want := strings.ReplaceAll(tc.wantOut, "{home}", home)
			if string(out) != want || status != tc.wantStatus {
				t.Errorf("output, status = %q, %d, want %q, %d", out, status, want, tc.wantStatus)
			}
		})
	}
}
