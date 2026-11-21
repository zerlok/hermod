// Package control resolves one invocation into an immutable Options and runs the
// sync → attach → pause/teardown flow. It is the composition layer: it wires the
// git, mirror, and sandbox collaborators together and owns the single decision
// that gives hermod its reason to exist — pause the mirror when the sandbox
// session outlived the detach, tear it down when it did not.
package control

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/zerlok/hermod/internal/claude"
	"github.com/zerlok/hermod/internal/sandbox"
	"github.com/zerlok/hermod/internal/secret"
)

// Options is the fully-resolved intent of one invocation. Run builds it by
// applying the options it is given; it is not mutated afterward.
type Options struct {
	Sandbox   string          // ssh host alias (the mirror's sandbox host and the ssh target)
	Session   string          // tmux session name
	LocalDir  string          // absolute local directory to mirror
	RemoteDir string          // home-relative sandbox path the mirror and session use
	Command   sandbox.Command // what a new sandbox session runs; the zero value runs the default shell
	Origin    string          // the name this machine is given on the sandbox; "" names none
	DryRun    bool            // print side-effecting commands instead of executing them
	Quiet     bool            // suppress step logging

	// Environ is the caller's environment, from which an agent session carries
	// Claude's variables to the sandbox. It holds credentials, so it is masked.
	Environ secret.Env
	// ClaudeConfigDir is the local Claude config directory, where Claude keeps
	// the transcripts of this machine's sessions.
	ClaudeConfigDir string

	// Stdio is the caller's streams: a utility call runs with them, and Hermod
	// writes its own lines to Stdio.Err and its dry-run plan and the output of
	// its setup commands to Stdio.Out, unless the caller owns stdout (ownOut).
	Stdio Streams
	// StandIn is set when this process stands in for the program it runs, whose
	// caller owns stdout.
	StandIn bool
	// SessionLogDir, when set, receives a log per agent session, <uuid>.log.
	SessionLogDir string
	// LeaseDir holds a lock file per mirror, through which the processes using
	// a mirror share it (see mirror.Lease). "" shares nothing: the run takes
	// itself for every mirror's only user.
	LeaseDir string
}

// resolve builds the Options of one invocation over the sandbox alias, with
// the process's own streams unless an option says otherwise.
func resolve(host string, opts ...Option) Options {
	o := Options{
		Sandbox: host,
		Stdio:   Streams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr},
	}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// ownOut is where Hermod's own stdout output goes, the dry-run plan and what its
// setup commands print: stdout, unless that belongs to the caller, as it does
// for the stand-in and for a chat session, whose stream is stdout whoever
// started it.
func (o Options) ownOut() io.Writer {
	if o.StandIn || claude.Classify(o.Command.Argv()).Kind() == claude.ChatSession {
		return o.Stdio.Err
	}
	return o.Stdio.Out
}

// Option applies one setting to the Options being built.
type Option func(*Options)

// WithWorkdir selects the directory to mirror and derives the sandbox path from
// it. When no session name is set, it defaults the session to a dash-joined slug
// of that path so distinct projects sharing a base name do not collide on one
// tmux session.
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

// WithCommand sets the verbatim argv run in a new sandbox session; an empty argv
// runs the default shell.
func WithCommand(argv []string) Option {
	return func(o *Options) { o.Command = sandbox.Exec(argv) }
}

// WithCommandLine sets a command line that the sandbox user's shell parses and
// runs in a new sandbox session; an empty line runs the default shell.
func WithCommandLine(line string) Option {
	return func(o *Options) { o.Command = sandbox.ShellLine(line) }
}

// WithOrigin names this machine on the sandbox. The name is reduced to letters,
// digits, dots and dashes, each other character becoming a dash, so it is safe
// in a tmux option and an environment value; an empty name names none.
func WithOrigin(name string) Option {
	return func(o *Options) { o.Origin = originSafe(name) }
}

// WithEnvironment hands an agent session the caller's environment.
func WithEnvironment(environ []string) Option {
	return func(o *Options) { o.Environ = secret.NewEnv(environ) }
}

// WithClaudeConfigDir sets the local Claude config directory.
func WithClaudeConfigDir(dir string) Option {
	return func(o *Options) { o.ClaudeConfigDir = dir }
}

// WithLeaseDir sets the directory of the mirrors' lease files.
func WithLeaseDir(dir string) Option {
	return func(o *Options) { o.LeaseDir = dir }
}

// WithDryRun prints side-effecting commands instead of executing them.
func WithDryRun(on bool) Option { return func(o *Options) { o.DryRun = on } }

// WithQuiet suppresses step logging.
func WithQuiet(on bool) Option { return func(o *Options) { o.Quiet = on } }

// WithStandIn marks the run as standing in for the program it runs: stdout
// carries nothing of Hermod's, and each agent session is logged under logDir.
func WithStandIn(logDir string) Option {
	return func(o *Options) {
		o.StandIn = true
		o.SessionLogDir = logDir
	}
}

func originSafe(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
			return r
		default:
			return '-'
		}
	}, name)
}

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
