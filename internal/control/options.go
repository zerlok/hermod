// Package control resolves one invocation into an immutable Options and runs the
// sync → attach → pause/teardown flow. It is the composition layer: it wires the
// git, mirror, and sandbox collaborators together and owns the single decision
// that gives hermod its reason to exist — pause the mirror when the sandbox
// session outlived the detach, tear it down when it did not.
package control

import (
	"path/filepath"
	"strings"
)

// Options is the fully-resolved intent of one invocation. Run builds it by
// applying the options it is given; it is not mutated afterward.
type Options struct {
	Sandbox   string   // ssh host alias (the mirror's sandbox host and the ssh target)
	Session   string   // shared tmux + mutagen session name
	LocalDir  string   // absolute local directory to mirror
	RemoteDir string   // home-relative sandbox path the mirror and session use
	Command   []string // passthrough sandbox command; nil runs the default shell
	DryRun    bool     // print side-effecting commands instead of executing them
	Quiet     bool     // suppress step logging
}

// Option applies one setting to the Options being built. Run applies each in
// turn; most just copy a value, while WithWorkdir carries the derivation.
type Option func(*Options)

// WithWorkdir selects the directory to mirror and derives the sandbox path from
// it. This is the option with extended logic: it resolves dir (defaulting to
// cwd, expanding a leading ~, making it absolute against cwd), maps it to the
// home-relative sandbox path, and — unless a session name is already set —
// defaults the session to a dash-joined slug of that path so distinct projects
// sharing a base name do not collide.
func WithWorkdir(dir, cwd, home string) Option {
	return func(o *Options) {
		if dir == "" {
			dir = cwd
		}
		dir = expandHome(dir, home)
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(cwd, dir)
		}
		o.LocalDir = filepath.Clean(dir)
		o.RemoteDir = remoteDir(o.LocalDir, home)
		if o.Session == "" {
			o.Session = strings.ReplaceAll(o.RemoteDir, "/", "-")
		}
	}
}

// WithSession sets an explicit session name; an empty name leaves the
// directory-derived default in place, regardless of option order.
func WithSession(name string) Option {
	return func(o *Options) {
		if name != "" {
			o.Session = name
		}
	}
}

// WithCommand sets the verbatim passthrough command run in the sandbox session.
func WithCommand(cmd []string) Option { return func(o *Options) { o.Command = cmd } }

// WithDryRun marks the run as a dry-run.
func WithDryRun(on bool) Option { return func(o *Options) { o.DryRun = on } }

// WithQuiet suppresses step logging.
func WithQuiet(on bool) Option { return func(o *Options) { o.Quiet = on } }

// remoteDir maps a local project directory to a home-relative path on the
// sandbox. A directory under the local home mirrors its home-relative path
// (/home/u/dev/api → "dev/api"); a directory outside home is slugified and
// placed directly under home (/opt/work/api → "opt-work-api"). Mutagen and tmux
// both resolve a non-absolute path relative to the sandbox home, so no ~
// expansion is needed.
func remoteDir(localDir, home string) string {
	localDir = filepath.Clean(localDir)
	if home != "" {
		home = filepath.Clean(home)
		if rel, err := filepath.Rel(home, localDir); err == nil && isDescendant(rel) {
			if rel == "." {
				rel = filepath.Base(localDir)
			}
			return filepath.ToSlash(rel)
		}
	}
	return slug(localDir)
}

// isDescendant reports whether a filepath.Rel result stays within the base
// (i.e. does not escape via "..").
func isDescendant(rel string) bool {
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// slug flattens an absolute path into a single home-relative segment by dropping
// the leading separator and joining components with dashes.
func slug(p string) string {
	p = strings.Trim(filepath.ToSlash(filepath.Clean(p)), "/")
	return strings.ReplaceAll(p, "/", "-")
}

func expandHome(p, home string) string {
	switch {
	case p == "~":
		return home
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(home, p[2:])
	default:
		return p
	}
}
