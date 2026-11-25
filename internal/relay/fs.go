package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// Roots are an FS relay's two ends: a root on this workstation and a root on a
// remote, the remote named by its ssh alias.
type Roots struct {
	Local  string
	Host   string
	Remote string
}

// endpoint is the remote end as Mutagen writes it in a URL.
func (r Roots) endpoint() string { return r.Host + ":" + r.Remote }

// FS is an FS relay: a Mutagen two-way-safe session between its two ends.
type FS struct {
	exec   shell.Executor // changes a state, or watches one a dry run only plans; the dry-run leaf in a dry run
	probe  shell.Executor // only reads a state; always a real leaf
	roots  Roots
	name   string
	output io.Writer // nil when Mutagen's output is kept only for a failure's message
	labels []Label
	leases leases
}

// NewFS is the FS relay named name between roots, carrying labels. Commands
// that change a state, and the watch a dry run must only plan, run on exec;
// commands that read one run on probe. The lock files of its leases are in
// locks. What the Mutagen commands that
// change a state print goes to output as well, which may be nil.
func NewFS(exec, probe shell.Executor, locks string, roots Roots, name string, output io.Writer, labels ...Label) *FS {
	if output != nil {
		// Mutagen writes stdout and stderr at once, and output receives both.
		output = &lockedWriter{w: output}
	}
	f := &FS{exec: exec, probe: probe, roots: roots, name: name, output: output, labels: labels}
	f.leases = leases{name: name, locks: locks, mech: f}
	return f
}

// Connect leases the relay: it creates the Mutagen session when there is
// none, resumes and flushes a paused one so what each side buffered is
// delivered before the caller works on the files, and uses a running one as it
// is. A session found under the name with another remote end is neither
// resumed nor used. Its lease's Done is closed with Mutagen's error when the
// session disappears.
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
	if found.roots().endpoint() != f.roots.endpoint() {
		return &EndpointMismatchError{Name: f.name, Want: f.roots.endpoint(), Got: found.roots().endpoint()}
	}
	if !found.Paused {
		return nil
	}
	if err := f.verb(ctx, "resume"); err != nil {
		return err
	}
	return f.Flush(ctx)
}

// serve watches the Mutagen session while the lease is held. Mutagen ends
// `sync monitor` with an error once the session it locked on to is no longer
// listed or its daemon cannot be reached; given a --template it watches a
// selection instead and never ends on its own, so none is given. Its status
// lines are of no use here and are dropped. The watch runs on exec: a dry run
// made no session to watch, and plans the watch instead, which ends at once as
// an IO relay's attach does there.
func (f *FS) serve(ctx context.Context) (*serving, error) {
	srv, ctx := newServing(ctx)
	srv.run(func() error {
		args := []string{"mutagen", "sync", "monitor", f.name}
		out, err := shell.Run(ctx, f.exec, shell.Command{Args: args, Stdout: io.Discard})
		if err := failed(args, out, err); err != nil {
			return err
		}
		return ErrEnded
	})
	return srv, nil
}

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
	if err := f.run(ctx, shell.NewSSH(f.exec, f.roots.Host), "mkdir", "-p", "--", f.roots.Remote); err != nil {
		return err
	}
	if err := f.run(ctx, f.exec, "mkdir", "-p", "--", f.roots.Local); err != nil {
		return err
	}
	args := []string{"mutagen", "sync", "create", "--name", f.name}
	for _, l := range f.labels {
		args = append(args, "--label", l.String())
	}
	args = append(args, "--mode", "two-way-safe", f.roots.Local, f.roots.endpoint())
	return f.mutagen(ctx, args...)
}

func (f *FS) verb(ctx context.Context, verb string) error {
	return f.mutagen(ctx, "mutagen", "sync", verb, f.name)
}

// run runs one command to its end. It reads no input: this process's stdin
// belongs to what Hermod attaches, and an ssh would otherwise consume it.
func (f *FS) run(ctx context.Context, ex shell.Executor, args ...string) error {
	out, err := shell.Run(ctx, ex, shell.Command{Args: args})
	return failed(args, out, err)
}

// mutagen runs a Mutagen command that changes a state, as run does, and
// hands what it prints to the relay's output too.
func (f *FS) mutagen(ctx context.Context, args ...string) error {
	if f.output == nil {
		return f.run(ctx, f.exec, args...)
	}
	var stderr bytes.Buffer
	cmd := shell.Command{Args: args, Stdout: f.output, Stderr: io.MultiWriter(&stderr, f.output)}
	_, err := shell.Run(ctx, f.exec, cmd)
	return failed(args, shell.Output{Stderr: stderr.String()}, err)
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
func (f *FS) find(ctx context.Context) (*session, error) {
	sessions, out, err := list(ctx, f.probe, f.name)
	if strings.Contains(out.Stdout, notFoundMarker) || strings.Contains(out.Stderr, notFoundMarker) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, s := range sessions {
		if s.Name == f.name {
			return &s, nil
		}
	}
	// A listing of the name always holds that session; one that does not is
	// no proof of absence.
	return nil, fmt.Errorf("mutagen sync list %s: the session is not in its output", f.name)
}

// Listed is one FS relay as Mutagen lists it.
type Listed struct {
	Name   string
	Roots  Roots
	Labels map[string]string
	State  State // Disconnected or Connected: a listed relay exists
}

// List reads the FS relays that carry label, with their roots and states,
// from Mutagen on probe and the lock files in locks.
func List(ctx context.Context, probe shell.Executor, locks string, label Label) ([]Listed, error) {
	sessions, _, err := list(ctx, probe, "--label-selector", label.String())
	if err != nil {
		return nil, err
	}
	var relays []Listed
	for _, s := range sessions {
		if s.Name == "" {
			// Hermod names every session it makes, so an unnamed one is
			// no relay of its own, whatever labels it was given by hand.
			continue
		}
		held, err := leased(locks, s.Name)
		if err != nil {
			return nil, err
		}
		state := Disconnected
		if held {
			state = Connected
		}
		relays = append(relays, Listed{Name: s.Name, Roots: s.roots(), Labels: s.Labels, State: state})
	}
	sort.Slice(relays, func(i, j int) bool { return relays[i].Name < relays[j].Name })
	return relays, nil
}

// listTemplate has Mutagen print its sessions as one JSON array of its own
// session model; the session type declares the part of it Hermod reads.
const listTemplate = "{{json .}}"

// list runs `mutagen sync list` for selection on probe and reads its
// sessions. The output is returned beside them for a caller that reads a
// failure's message.
func list(ctx context.Context, probe shell.Executor, selection ...string) ([]session, shell.Output, error) {
	args := append(append([]string{"mutagen", "sync", "list"}, selection...), "--template", listTemplate)
	out, err := shell.Run(ctx, probe, shell.Command{Args: args})
	if err := failed(args, out, err); err != nil {
		return nil, out, err
	}
	sessions, err := parseList([]byte(out.Stdout))
	if err != nil {
		return nil, out, fmt.Errorf("%s: %w", strings.Join(args, " "), err)
	}
	return sessions, out, nil
}

// session is one Mutagen session, the fields of Mutagen's model that Hermod
// reads.
type session struct {
	Name   string            `json:"name"` // "" when it was made with no name
	Alpha  endpoint          `json:"alpha"`
	Beta   endpoint          `json:"beta"`
	Labels map[string]string `json:"labels"` // nil when it has none
	Paused bool              `json:"paused"`
}

// endpoint is one end of a Mutagen session, as Mutagen's model splits its
// URL.
type endpoint struct {
	Protocol string `json:"protocol"`
	User     string `json:"user"`
	Host     string `json:"host"`
	Port     uint16 `json:"port"`
	Path     string `json:"path"`
}

const (
	protocolLocal = "local"
	protocolSSH   = "ssh"
)

// host is the part of the end's URL before its path, as Mutagen writes it:
// "" for a local end and `[user@]host[:port]` for an ssh one. Any other
// protocol keeps its scheme, so its end matches no root of Hermod's.
func (e endpoint) host() string {
	switch e.Protocol {
	case protocolLocal:
		return ""
	case protocolSSH:
		h := e.Host
		if e.User != "" {
			h = e.User + "@" + h
		}
		if e.Port != 0 {
			h = fmt.Sprintf("%s:%d", h, e.Port)
		}
		return h
	}
	return e.Protocol + "://" + e.Host
}

// roots are the session's ends as an FS relay's roots. An alpha end that is
// not local keeps its host in its root, so it matches no local root; a beta
// end with no host matches no relay's remote end.
func (s session) roots() Roots {
	local := s.Alpha.Path
	if h := s.Alpha.host(); h != "" {
		local = h + ":" + local
	}
	return Roots{Local: local, Host: s.Beta.host(), Remote: s.Beta.Path}
}

// parseList reads the sessions of a templated `mutagen sync list`; output
// that is not its JSON array is an error, not an empty listing.
func parseList(out []byte) ([]session, error) {
	var sessions []session
	if err := json.Unmarshal(out, &sessions); err != nil {
		return nil, fmt.Errorf("read the listing: %w", err)
	}
	return sessions, nil
}
