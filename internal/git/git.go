// Package git reads the developer's git facts on the local workstation. Every
// read is a read-only probe and never a hard failure: a fact git cannot give,
// because git is absent or the directory is in no repository, is the zero
// value, since each one only refines a choice the caller can make without it.
package git

import (
	"context"
	"path/filepath"
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

// Read returns the identity git uses in dir, so that a repository's own
// user.name and user.email win over the global ones. A key git cannot read
// leaves its field empty.
func Read(ctx context.Context, ex shell.Executor, dir string) Identity {
	return Identity{
		Name:  configValue(ctx, ex, dir, "user.name"),
		Email: configValue(ctx, ex, dir, "user.email"),
	}
}

// MainWorktree returns the main working tree of the linked worktree that holds
// dir, with that worktree's root, and false when dir is in a main working tree,
// in no repository, or in a repository whose main tree is bare and so has no
// working tree. The root is returned because a relay of the worktree must join
// its root, which holds the .git pointer file, even when dir lies below it.
//
// Only git's own split of git dir and common dir marks a linked worktree:
// comparing the first worktree entry with the top level would also flag a
// submodule, whose first entry is its git dir under the superproject's
// .git/modules rather than its working tree.
func MainWorktree(ctx context.Context, ex shell.Executor, dir string) (main, root string, ok bool) {
	out, ok := output(ctx, ex, dir, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir", "--show-toplevel")
	if !ok {
		return "", "", false
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 3 || lines[2] == "" {
		return "", "", false
	}
	gitDir, commonDir, top := filepath.Clean(lines[0]), filepath.Clean(lines[1]), lines[2]
	if gitDir == commonDir {
		return "", "", false
	}
	list, ok := output(ctx, ex, dir, "worktree", "list", "--porcelain")
	if !ok {
		return "", "", false
	}
	main, ok = firstWorktree(list)
	// A submodule's linked worktree lists the submodule's git dir as its main
	// entry, which is no working tree a workspace could cover.
	if !ok || filepath.Clean(main) == commonDir {
		return "", "", false
	}
	return main, top, true
}

// firstWorktree reads the main working tree from `git worktree list
// --porcelain`, which git always lists first; false when there is none, or
// when it is bare.
func firstWorktree(list string) (string, bool) {
	record, _, _ := strings.Cut(list, "\n\n")
	var path string
	for _, line := range strings.Split(record, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
		case line == "bare":
			return "", false
		}
	}
	return path, path != ""
}

func configValue(ctx context.Context, ex shell.Executor, dir, key string) string {
	value, _ := output(ctx, ex, dir, "config", "--get", key)
	return value
}

// output runs one git command in dir and returns what it printed, trimmed;
// false when it failed.
func output(ctx context.Context, ex shell.Executor, dir string, args ...string) (string, bool) {
	out, err := shell.Run(ctx, ex, shell.Command{Args: append([]string{"git"}, args...), Dir: dir})
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(out.Stdout), true
}
