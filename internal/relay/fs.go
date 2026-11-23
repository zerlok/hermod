package relay

import (
	"bufio"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/zerlok/hermod/internal/shell"
)

// Label is a Mutagen label an FS relay carries, so the relays of a remote or a
// workspace are listed by a label selector rather than recorded in a file.
type Label struct {
	Key   string
	Value string
}

func (l Label) String() string { return l.Key + "=" + l.Value }

// FS is an FS relay: a Mutagen two-way-safe session between its two ends.
type FS struct {
	exec   shell.Executor // changes a state; the dry-run leaf in a dry run
	probe  shell.Executor // only reads a state; always a real leaf
	ends   Ends
	name   string
	labels []Label
	leases leases
}

// NewFS is the FS relay named name between ends, carrying labels. Commands
// that change a state run on exec and commands that read one on probe; the
// lock files of its leases are in locks.
func NewFS(exec, probe shell.Executor, locks string, ends Ends, name string, labels ...Label) *FS {
	f := &FS{exec: exec, probe: probe, ends: ends, name: name, labels: labels}
	f.leases = leases{name: name, locks: locks, mech: f}
	return f
}

// Connect leases the relay: it creates the Mutagen session when there is
// none, resumes and flushes a paused one so what each side buffered is
// delivered before the caller works on the files, and uses a running one as it
// is. A session found under the name with another remote end is neither
// resumed nor used.
func (f *FS) Connect(ctx context.Context) (Lease, error) { return f.leases.connect(ctx) }

// Flush waits until both ends are consistent.
func (f *FS) Flush(ctx context.Context) error { return f.verb(ctx, "flush") }

// EndpointMismatchError reports a session found under the relay's name that
// syncs to another remote end. Using it would send the files somewhere other
// than where the caller works, so the connect stops instead. It may be another
// relay's working session, since two aliases or two paths can reduce to one
// name (`box.a` and `box-a`), so the error does not suggest removing it.
type EndpointMismatchError struct {
	Name string
	Want string // the remote end this relay joins
	Got  string // the remote end the found session syncs to
}

func (e *EndpointMismatchError) Error() string {
	return fmt.Sprintf("relay %s syncs to %s, not %s: another relay already has this name, and Hermod leaves it alone",
		e.Name, e.Got, e.Want)
}

func (f *FS) make(ctx context.Context) error {
	found, err := f.find(ctx)
	if err != nil {
		return err
	}
	if found == nil {
		return f.create(ctx)
	}
	if found.ends.endpoint() != f.ends.endpoint() {
		return &EndpointMismatchError{Name: f.name, Want: f.ends.endpoint(), Got: found.ends.endpoint()}
	}
	if !found.paused {
		return nil
	}
	if err := f.verb(ctx, "resume"); err != nil {
		return err
	}
	return f.Flush(ctx)
}

func (f *FS) serve(context.Context) (*serving, error) { return nil, nil }

func (f *FS) settle(ctx context.Context, how giveUp) error {
	switch how {
	case suspend:
		return f.verb(ctx, "pause")
	case release:
		if err := f.Flush(ctx); err != nil {
			return err
		}
		return f.verb(ctx, "terminate")
	}
	return fmt.Errorf("relay %s: unhandled way to give a lease up: %d", f.name, how)
}

// create makes both roots and the Mutagen session between them. Mutagen
// creates a sync root but not its parents, and a root with an absent parent
// stalls the sync.
func (f *FS) create(ctx context.Context) error {
	// Through shell's ssh, which quotes the path for the remote's shell: ssh
	// joins its arguments for that shell to parse again.
	if err := f.run(ctx, shell.NewSSH(f.exec, f.ends.Host), "mkdir", "-p", "--", f.ends.Remote); err != nil {
		return err
	}
	if err := f.run(ctx, f.exec, "mkdir", "-p", "--", f.ends.Local); err != nil {
		return err
	}
	args := []string{"mutagen", "sync", "create", "--name", f.name}
	for _, l := range f.labels {
		args = append(args, "--label", l.String())
	}
	args = append(args, "--mode", "two-way-safe", f.ends.Local, f.ends.endpoint())
	return f.run(ctx, f.exec, args...)
}

func (f *FS) verb(ctx context.Context, verb string) error {
	return f.run(ctx, f.exec, "mutagen", "sync", verb, f.name)
}

// run runs one command to its end. It reads no input: this process's stdin
// belongs to what Hermod attaches, and an ssh would otherwise consume it.
func (f *FS) run(ctx context.Context, ex shell.Executor, args ...string) error {
	out, err := shell.Run(ctx, ex, shell.Command{Args: args})
	return failed(args, out, err)
}

// failed is a command's error with what it wrote to stderr, nil when it
// succeeded.
func failed(args []string, out shell.Output, err error) error {
	if err == nil {
		return nil
	}
	if msg := strings.TrimSpace(out.Stderr); msg != "" {
		return fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return fmt.Errorf("%s: %w", strings.Join(args, " "), err)
}

// notFoundMarker is what `mutagen sync list <name>` reports when no session
// matches the name: the positive signal that the relay is Absent, read from
// the output rather than inferred from the exit status.
const notFoundMarker = "unable to locate requested sessions"

// find is the session under the relay's name, nil when Mutagen reports none.
// Any other failure is returned, so an ambiguous reading stops the connect
// rather than creating a second session over an existing one.
func (f *FS) find(ctx context.Context) (*listed, error) {
	args := []string{"mutagen", "sync", "list", f.name}
	out, err := shell.Run(ctx, f.probe, shell.Command{Args: args})
	if strings.Contains(out.Stdout, notFoundMarker) || strings.Contains(out.Stderr, notFoundMarker) {
		return nil, nil
	}
	if err := failed(args, out, err); err != nil {
		return nil, err
	}
	for _, s := range parseList(out.Stdout) {
		if s.name == f.name {
			return &s, nil
		}
	}
	// A listing of the name always holds that session; one that does not is
	// output this parser does not understand, which is no proof of absence.
	return nil, fmt.Errorf("%s: the session is not in its output", strings.Join(args, " "))
}

// Listed is one FS relay as Mutagen lists it.
type Listed struct {
	Name   string
	Ends   Ends
	Labels map[string]string
	State  State // Disconnected or Connected: a listed relay exists
}

// List reads the FS relays that carry label, with their ends and states, from
// Mutagen on probe and the lock files in locks.
func List(ctx context.Context, probe shell.Executor, locks string, label Label) ([]Listed, error) {
	args := []string{"mutagen", "sync", "list", "--label-selector", label.String()}
	out, err := shell.Run(ctx, probe, shell.Command{Args: args})
	if err := failed(args, out, err); err != nil {
		return nil, err
	}
	var relays []Listed
	for _, s := range parseList(out.Stdout) {
		held, err := leased(locks, s.name)
		if err != nil {
			return nil, err
		}
		state := Disconnected
		if held {
			state = Connected
		}
		relays = append(relays, Listed{Name: s.name, Ends: s.ends, Labels: s.labels, State: state})
	}
	sort.Slice(relays, func(i, j int) bool { return relays[i].Name < relays[j].Name })
	return relays, nil
}

// Unlabeled is the names of the Mutagen sessions on probe that sync the local
// root local and carry no label key: sessions made under earlier names, or by
// hand, which no listing by label finds. Resuming one could send the files to
// a remote other than the caller's, and terminating one could drop changes it
// still holds, so they are only named, for the user to remove.
func Unlabeled(ctx context.Context, probe shell.Executor, local, key string) ([]string, error) {
	args := []string{"mutagen", "sync", "list"}
	out, err := shell.Run(ctx, probe, shell.Command{Args: args})
	if err := failed(args, out, err); err != nil {
		return nil, err
	}
	var names []string
	for _, s := range parseList(out.Stdout) {
		if _, labelled := s.labels[key]; s.ends.Local == local && !labelled {
			names = append(names, s.name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// listed is one session as `mutagen sync list` prints it.
type listed struct {
	name   string
	ends   Ends
	paused bool
	labels map[string]string // nil when it has none
}

// parseList reads the sessions from `mutagen sync list` output. Each session
// starts at a top-level `Name:` line; its ends are the indented `URL:` lines
// under the top-level `Alpha:` (local) and `Beta:` (remote) headers, its
// labels the indented `key: value` lines under a `Labels:` header, and a
// paused session says so in its status.
func parseList(out string) []listed {
	var (
		sessions []listed
		section  string
	)
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if line != "" && line[0] != ' ' && line[0] != '\t' {
			section = ""
			switch {
			case strings.HasPrefix(trimmed, "Name:"):
				sessions = append(sessions, listed{name: strings.TrimSpace(strings.TrimPrefix(trimmed, "Name:"))})
				continue
			case trimmed == "Alpha:" || trimmed == "Beta:" || trimmed == "Labels:":
				section = trimmed
				continue
			}
		}
		if len(sessions) == 0 {
			continue
		}
		cur := &sessions[len(sessions)-1]
		if section == "Labels:" {
			if key, value, ok := strings.Cut(trimmed, ": "); ok {
				if cur.labels == nil {
					cur.labels = map[string]string{}
				}
				cur.labels[key] = value
			}
			continue
		}
		url, isURL := strings.CutPrefix(trimmed, "URL:")
		url = strings.TrimSpace(url)
		switch {
		case isURL && section == "Alpha:":
			cur.ends.Local = url
		case isURL && section == "Beta:":
			// A beta end with no host is no relay of Hermod's: read as a host
			// with an empty remote root, it matches no relay's remote end.
			cur.ends.Host, cur.ends.Remote, _ = strings.Cut(url, ":")
		case !isURL && strings.Contains(trimmed, "Paused"):
			cur.paused = true
		}
	}
	return sessions
}
