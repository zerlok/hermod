package shell

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// tmux wraps a command as a persistent tmux session, attaching to the named
// session if it exists and creating it otherwise (`new-session -A`). The
// command's directory and variables become tmux's own -c and -e flags, which
// take effect only when the session is created. A command with no arguments
// starts the default shell.
type tmux struct {
	inner    Executor
	session  TmuxSession
	detached bool
	options  []SessionOption
}

// SessionOption is a tmux session option, such as a user option `@name`, set on
// the session when it is created.
type SessionOption struct {
	Name  string
	Value string
}

// TmuxOption configures a tmux wrapper at construction.
type TmuxOption func(*tmux)

// Detached creates the session without attaching to it (`new-session -d`), for
// a session started by one connection and joined by another. A session that
// already exists is left as it is, its command not run again.
func Detached() TmuxOption {
	return func(t *tmux) { t.detached = true }
}

// WithSessionOptions sets options on the session. Each is set only where it is
// not set yet (`set-option -qo`), so the session keeps the values it was created
// with when a later attach joins it.
func WithSessionOptions(opts ...SessionOption) TmuxOption {
	return func(t *tmux) { t.options = append(t.options, opts...) }
}

// NewTmux wraps inner so its command runs in the named tmux session.
func NewTmux(inner Executor, session string, opts ...TmuxOption) Executor {
	t := tmux{inner: inner, session: NewTmuxSession(session)}
	for _, o := range opts {
		o(&t)
	}
	return t
}

// Exec hands the command inward as the tmux command line that makes its
// session.
func (t tmux) Exec(ctx context.Context, cmd Command) Execution {
	args, err := t.newSession(cmd)
	if err != nil {
		return Finished(cmd, err)
	}
	return t.inner.Exec(ctx, cmd.rewrapped(args))
}

// newSession is the tmux command line that makes the session running cmd. A
// variable's value is on that line, so a secret one is refused.
func (t tmux) newSession(cmd Command) ([]string, error) {
	if err := refuseSecrets("tmux", cmd); err != nil {
		return nil, err
	}
	// tmux splits each -e entry at its first '=', so a key holding one would
	// set another variable, and the session's shell reads only shell names.
	if err := refuseBadKeys("tmux", cmd); err != nil {
		return nil, err
	}
	for _, o := range t.options {
		if err := CheckSessionOption(o); err != nil {
			return nil, err
		}
	}
	args := []string{"tmux", "new-session"}
	if t.detached {
		args = append(args, "-d")
	} else {
		args = append(args, "-A")
	}
	args = append(args, "-s", t.session.name)
	if cmd.Dir != "" {
		args = append(args, "-c", tmuxWord(formatLiteral(cmd.Dir)))
	}
	for _, e := range cmd.Env.Reveal() {
		args = append(args, "-e", tmuxWord(e))
	}
	args = append(args, commandWords(cmd.Args)...)
	// A lone ";" word separates tmux commands. Each option names its session:
	// without one, tmux sets it on the session of the client's $TMUX, when
	// there is one, rather than on the session just created. -q keeps an
	// option that is already set from failing the sequence and showing an
	// error in the client.
	for _, o := range t.options {
		args = append(args, ";", "set-option", "-t", t.session.target(), "-qo", o.Name, tmuxWord(o.Value))
	}
	return args, nil
}

// CheckSessionOption checks an option before any session is made with it: its
// name a user option a listing can name, and its value free of control
// characters, since a listing prints the value as it is, one session per line
// and its fields apart by a tab.
func CheckSessionOption(o SessionOption) error {
	if !isUserOption(o.Name) {
		return fmt.Errorf("shell: %q is no tmux user option", o.Name)
	}
	if strings.ContainsFunc(o.Value, unicode.IsControl) {
		return fmt.Errorf("shell: the value %q of tmux option %s holds a control character", o.Value, o.Name)
	}
	return nil
}

// commandWords are a session's command as new-session takes it, after `--` so
// that none of the command's own flags reads as tmux's. tmux 3.0 and later
// execute several words without a shell, so each reaches the process as it is,
// but hand a single word to the shell, where it is quoted to stay one word.
func commandWords(args []string) []string {
	switch len(args) {
	case 0:
		return nil
	case 1:
		return []string{"--", tmuxWord(quote(args[0]))}
	}
	words := []string{"--"}
	for _, a := range args {
		words = append(words, tmuxWord(a))
	}
	return words
}

// tmuxWord keeps a word whole in tmux's arguments, where a word ending in ';'
// ends the command and "\;" stands for a literal one.
func tmuxWord(word string) string {
	if strings.HasSuffix(word, ";") {
		return strings.TrimSuffix(word, ";") + `\;`
	}
	return word
}

// formatLiteral is s as a tmux format that expands to s itself. tmux expands
// -c as a format, where `#(cmd)` runs cmd and other `#` sequences are
// replaced, so a directory name holding a '#' would otherwise not be the
// directory the session starts in, or would run a command; `##` is a literal
// '#'.
func formatLiteral(s string) string { return strings.ReplaceAll(s, "#", "##") }

// TmuxSession is a tmux session by its name, and the tmux commands that read
// and act on it. Each is the program and its arguments, for a caller that runs
// it or hands it to a script as one Line.
type TmuxSession struct{ name string }

// NewTmuxSession is the session of the given name.
func NewTmuxSession(name string) TmuxSession { return TmuxSession{name: name} }

// target names the session exactly (`=name:`): tmux stores a session name with
// '.' and ':' replaced by '_', and without '=' a name also matches any session
// it is the prefix of.
func (s TmuxSession) target() string {
	return "=" + strings.NewReplacer(".", "_", ":", "_").Replace(s.name) + ":"
}

// The statuses the commands that read a session or a listing exit with, other
// than 0: tmux's own exit status tells no missing session from a tmux that
// could not answer, so the commands tell them apart by what tmux says.
const (
	TmuxAbsent     = 1 // no session of that name, or no tmux server at all
	TmuxUnreadable = 2 // tmux could not tell; what it said is on stderr
)

// classifyScript runs a tmux command, passing its stdout through, and exits
// with the status of its outcome. tmux says both that a session or its server
// is missing and that it failed otherwise with status 1, so its stderr tells
// them apart; a socket directory that does not exist is a server never
// started. fd 3 carries the command's stdout past the capture of its stderr.
const classifyScript = `a=$1 u=$2
shift 2
exec 3>&1
if e=$("$@" 2>&1 >&3 3>&-); then exit 0; fi
case $e in
*"can't find session"* | *"no server running"* | *"error connecting to"*"(No such file or directory)"*) exit "$a" ;;
esac
printf '%s\n' "$e" >&2
exit "$u"`

// classified is args run by classifyScript, exiting absent for a missing
// session or server.
func classified(absent int, args ...string) []string {
	return append([]string{"sh", "-c", classifyScript, "tmux", strconv.Itoa(absent), strconv.Itoa(TmuxUnreadable)}, args...)
}

// Has exits 0 when the session exists, TmuxAbsent when it does not, and
// TmuxUnreadable when tmux cannot tell.
func (s TmuxSession) Has() []string {
	return classified(TmuxAbsent, "tmux", "has-session", "-t", s.target())
}

// PaneDead prints 1 when the session's pane is kept after its process ended,
// and 0 while the process runs; it exits as Has does. has-session comes first
// in the same tmux call, since display-message prints an empty line for a
// missing session and succeeds.
func (s TmuxSession) PaneDead() []string {
	return classified(TmuxAbsent, "tmux", "has-session", "-t", s.target(), ";",
		"display-message", "-p", "-t", s.target(), "#{pane_dead}")
}

// Kill ends the session, hanging up on its process.
func (s TmuxSession) Kill() []string { return []string{"tmux", "kill-session", "-t", s.target()} }

// Attach joins the session on the caller's tty; it creates nothing.
func (s TmuxSession) Attach() []string { return []string{"tmux", "attach-session", "-t", s.target()} }

// KeepFailedPane keeps the session's pane when its process fails, so whoever
// joins it sees the error and exit status. A tmux older than 3.2 lacks the
// value and refuses it.
func (s TmuxSession) KeepFailedPane() []string {
	return []string{"tmux", "set-option", "-w", "-t", s.target(), "remain-on-exit", "failed"}
}

// TmuxListing lists every tmux session with its pane's state and the values of
// the session options it names, one session per line.
type TmuxListing struct{ options []string }

// NewTmuxListing is the listing of the named options, each a user option
// `@name` of letters, digits, '-' and '_', which is all a format can name
// without reading as format syntax.
func NewTmuxListing(options ...string) (TmuxListing, error) {
	for _, o := range options {
		if !isUserOption(o) {
			return TmuxListing{}, fmt.Errorf("shell: %q is no tmux user option", o)
		}
	}
	return TmuxListing{options: append([]string(nil), options...)}, nil
}

func isUserOption(name string) bool {
	if len(name) < 2 || name[0] != '@' {
		return false
	}
	for _, r := range name[1:] {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// listSep separates a listing's fields. The session's name comes last, so a
// name holding one is still read whole.
const listSep = "\t"

// Command lists the sessions. With no tmux server it prints nothing and
// succeeds, since no session exists; it exits TmuxUnreadable when tmux cannot
// tell.
func (l TmuxListing) Command() []string {
	fields := []string{"#{pane_dead}"}
	for _, o := range l.options {
		fields = append(fields, "#{"+o+"}")
	}
	fields = append(fields, "#{session_name}")
	return classified(0, "tmux", "list-sessions", "-F", strings.Join(fields, listSep))
}

// TmuxListed is one session a listing found.
type TmuxListed struct {
	Name    string
	Dead    bool              // the pane is kept after its process ended
	Options map[string]string // the value of each option listed; "" for one the session lacks
}

var errUnreadableListing = errors.New("shell: unreadable tmux listing")

// Parse reads what Command printed.
func (l TmuxListing) Parse(out string) ([]TmuxListed, error) {
	var sessions []TmuxListed
	for line := range strings.Lines(out) {
		fields := strings.SplitN(strings.TrimSuffix(line, "\n"), listSep, len(l.options)+2)
		if len(fields) != len(l.options)+2 {
			return nil, fmt.Errorf("%w: line %q", errUnreadableListing, line)
		}
		s := TmuxListed{Name: fields[len(fields)-1], Options: map[string]string{}}
		switch fields[0] {
		case "0":
		case "1":
			s.Dead = true
		default:
			return nil, fmt.Errorf("%w: line %q", errUnreadableListing, line)
		}
		for i, o := range l.options {
			s.Options[o] = fields[1+i]
		}
		sessions = append(sessions, s)
	}
	return sessions, nil
}
