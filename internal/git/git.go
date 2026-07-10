// Package git reads the local git author identity. The read is a read-only
// probe and never a hard failure: a missing or unreadable identity yields the
// zero value.
package git

import (
	"context"
	"strings"

	"github.com/zerlok/hermod/internal/shell"
)

// Identity is the local git author identity. Any field may be empty: the
// identity may be complete, partial, or absent.
type Identity struct {
	Name  string
	Email string
}

// IsZero reports whether no identity was found.
func (i Identity) IsZero() bool { return i.Name == "" && i.Email == "" }

// Git discovers the local identity over a Shell.
type Git struct {
	shell shell.Shell
	dir   string
}

// New returns a Git that reads identity from dir over sh. Callers pass a
// real-executor shell so the read reflects true identity even under dry-run, and
// the mirrored directory so a repo-local user.name/email is honored.
func New(sh shell.Shell, dir string) Git {
	return Git{shell: sh, dir: dir}
}

// Read returns the local identity. Any per-key read failure (git absent, key
// unset) leaves that field empty; it is never surfaced as an error.
func (r Git) Read(ctx context.Context) Identity {
	return Identity{
		Name:  r.get(ctx, "user.name"),
		Email: r.get(ctx, "user.email"),
	}
}

func (r Git) get(ctx context.Context, key string) string {
	res, err := r.shell.Run(ctx, shell.Command{
		Argv:    []string{"git", "config", "--get", key},
		Dir:     r.dir,
		Capture: true,
	})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(res.Stdout)
}
