// Package workspace is the relays and the workers of one project on one
// remote. Its identity is the remote and the project's root, so it is neither
// created nor removed: it is Empty until something of it exists. It names and
// tags its relays and workers, so everything of one project on one remote is
// found again by its name, and it is the only package that puts relays and
// workers together: whether its relays may be removed depends on whether one
// of its workers still runs.
package workspace

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sync"
	"time"

	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/relay"
	"github.com/zerlok/hermod/internal/shell"
	"github.com/zerlok/hermod/internal/worker"
)

// State is a workspace's state, computed from its members every time it is
// read and never kept.
type State int

const (
	// Empty has no relay and no worker.
	Empty State = iota + 1
	// Detached has a worker or a relay, and no connect lease on its relays.
	Detached
	// Attached has a connect lease on its relays.
	Attached
)

func (s State) String() string {
	switch s {
	case Empty:
		return "empty"
	case Detached:
		return "detached"
	case Attached:
		return "attached"
	}
	return fmt.Sprintf("State(%d)", int(s))
}

// Members is what a workspace holds, as one read returns it: its FS relays
// with their ends and states, read from Mutagen, and its workers with their
// states, read from tmux.
type Members struct {
	Relays  []relay.Listed
	Workers []worker.Listed
}

// State is the workspace's state for these members. Only an FS relay is
// listed: an IO or a Net relay is served by the process that leased it, so a
// workspace with one also has a leased FS relay.
func (m Members) State() State {
	for _, r := range m.Relays {
		if r.State == relay.Connected {
			return Attached
		}
	}
	if len(m.Relays) == 0 && len(m.Workers) == 0 {
		return Empty
	}
	return Detached
}

// Worker is the state of the worker under key, Absent when the workspace has
// none.
func (m Members) Worker(key worker.Key) worker.State {
	for _, w := range m.Workers {
		if w.Worker.Key() == key {
			return w.State
		}
	}
	return worker.Absent
}

// Remote is the remote workstation as a workspace reaches it. The executors
// are local leaves: the workspace runs Mutagen on them and wraps them in ssh
// to reach the remote.
type Remote struct {
	Host string         // the ssh alias
	Home string         // the remote user's home, absolute
	Exec shell.Executor // changes a state; the dry-run leaf in a dry run
	Read shell.Executor // only reads a state; always a real leaf
}

// Worktree is the linked git worktree a directory lies in, as git reports it.
// Its zero value is a directory in no linked worktree.
type Worktree struct {
	Main string // the main working tree
	Root string // the linked worktree's own root, which holds its .git pointer file
}

// ErrRemoteTaken is a new FS relay refused because its remote root nests with
// a listed relay's remote root under a local root that does not match: both
// relays would sync two unrelated local trees into one remote directory, and
// each would carry the other's files back.
var ErrRemoteTaken = errors.New("workspace: the remote root is joined to another local root")

// ErrRelayGone is why an attach ended when an FS relay it holds was removed
// from outside this process.
var ErrRelayGone = errors.New("workspace: an FS relay is gone")

// fsPoll is how often an attach reads Mutagen for the FS relays it holds:
// nothing in this process serves one, so a relay terminated elsewhere is seen
// only there.
const fsPoll = 5 * time.Second

// Local is what a workspace needs of this workstation.
type Local struct {
	Home   string // the local user's home, absolute
	Locks  string // the directory of the relays' lock files
	Origin string // the machine sessions are started from; "" leaves its tag out
}

// Workspace is one project on one remote, as this process holds it: the
// leases it took on the workspace's relays. Its methods are safe for
// concurrent use.
type Workspace struct {
	remote Remote
	local  Local
	name   string        // its project FS relay's name
	root   pathmap.Place // its project FS relay's ends
	nested []pathmap.Place
	paths  pathmap.Map
	poll   time.Duration

	mu   sync.Mutex
	held []held // in the order taken
}

// held is one lease this process took through the workspace. owner is the
// workspace that made the relay: its workers decide whether the relay stays,
// which for an FS relay leased from another workspace is not this one.
type held struct {
	name  string
	owner string
	fs    bool
	lease relay.Lease
}

// found is one FS relay of the remote as Mutagen lists it, its remote end made
// absolute for comparing roots, and the ends as listed kept for leasing it,
// since a relay checks the end it finds against the one it is given.
type found struct {
	name      string
	workspace string
	place     pathmap.Place
	ends      relay.Ends
}

func (f found) project() bool { return f.name == f.workspace }

// joining is the FS relay a new workspace, or a worktree of workspace, makes
// for the local root dir: named after its ends, so a session that needs the
// same ends later finds it.
func joining(remote Remote, local Local, dir, workspace string) found {
	place := pathmap.Place{Local: dir, Remote: remoteRoot(local.Home, remote.Home, dir)}
	name := relayName(remote.Host, place)
	if workspace == "" {
		workspace = name
	}
	return found{
		name:      name,
		workspace: workspace,
		place:     place,
		ends:      relay.Ends{Local: place.Local, Host: remote.Host, Remote: place.Remote},
	}
}

// named is the relay among all under name.
func named(all []found, name string) (found, bool) {
	for _, f := range all {
		if f.name == name {
			return f, true
		}
	}
	return found{}, false
}

// absolute refuses a path that is not absolute: a path map's roots are.
func absolute(paths ...string) error {
	for _, p := range paths {
		if !path.IsAbs(p) {
			return fmt.Errorf("workspace: %q is not an absolute path", p)
		}
	}
	return nil
}

// list reads every FS relay of the remote. Two aliases can reduce to one
// label value, so a relay of another alias is left out by its ends.
func list(ctx context.Context, remote Remote, local Local) ([]found, error) {
	listed, err := relay.List(ctx, remote.Read, local.Locks, relay.Label{Key: labelRemote, Value: remoteLabel(remote.Host)})
	if err != nil {
		return nil, err
	}
	var all []found
	for _, l := range listed {
		if l.Ends.Host != remote.Host {
			continue
		}
		place := pathmap.Place{Local: path.Clean(l.Ends.Local), Remote: pathmap.Absolute(remote.Home, l.Ends.Remote)}
		// A session made or relabelled by hand may have an alpha that is no
		// local path; it cannot be a root of the path map, so it is no relay
		// of a workspace.
		if _, err := pathmap.New(place); err != nil {
			continue
		}
		all = append(all, found{
			name:      l.Name,
			workspace: l.Labels[labelWorkspace],
			place:     place,
			ends:      l.Ends,
		})
	}
	return all, nil
}

// clash is a relay among all whose remote root nests with place's, or is the
// same, while its local root does not lie at the matching place: the two
// would join one remote directory to two unrelated local trees.
func clash(all []found, place pathmap.Place) (found, bool) {
	for _, f := range all {
		if !matches(f.place, place) || !matches(place, f.place) {
			return f, true
		}
	}
	return found{}, false
}

// matches reports whether a's local root lies where b's ends put it, when a's
// remote root is b's or lies below it.
func matches(a, b pathmap.Place) bool {
	rel, under := pathmap.Under(a.Remote, b.Remote)
	return !under || path.Join(b.Local, rel) == a.Local
}

// refuseClash refuses making the relay j when its remote root clashes with one
// among all.
func refuseClash(all []found, j found) error {
	if f, ok := clash(all, j.place); ok {
		return fmt.Errorf("%w: %s → %s, where %s joins %s → %s", ErrRemoteTaken, j.place.Local, j.place.Remote, f.name, f.place.Local, f.place.Remote)
	}
	return nil
}

// covering is the relay among all that keep accepts whose local root holds
// dir, the innermost one when roots nest.
func covering(all []found, dir string, keep func(found) bool) (found, bool) {
	var (
		best found
		ok   bool
	)
	for _, f := range all {
		if !keep(f) {
			continue
		}
		if _, under := pathmap.Under(dir, f.place.Local); under && (!ok || len(f.place.Local) > len(best.place.Local)) {
			best, ok = f, true
		}
	}
	return best, ok
}

// Open is the workspace of remote that the local directory dir belongs to,
// found as the first that applies of: the workspace whose project FS relay
// holds dir; for a dir in the linked git worktree tree, the workspace that
// holds its main working tree, where the worktree's root gets an FS relay of
// its own unless one of that workspace's relays holds dir already; else a new
// workspace whose project FS relay joins dir to its remote path. A new relay
// whose remote root another relay's local root would also reach is refused
// with ErrRemoteTaken.
//
// It connects the FS relay that holds dir and flushes it, so the files are on
// both sides before anything works on them, and fixes the path map for the
// run. A relay whose two ends another workspace joined already is leased
// rather than made twice, since a relay is named after its ends.
func Open(ctx context.Context, remote Remote, local Local, dir string, tree Worktree) (*Workspace, error) {
	if err := absolute(dir, remote.Home, local.Home); err != nil {
		return nil, err
	}
	inTree := tree != Worktree{}
	if inTree {
		if err := absolute(tree.Main, tree.Root); err != nil {
			return nil, err
		}
		tree = Worktree{Main: path.Clean(tree.Main), Root: path.Clean(tree.Root)}
	}
	dir = path.Clean(dir)
	if dir == "/" {
		return nil, errors.New("workspace: the filesystem root cannot be a project root")
	}
	all, err := list(ctx, remote, local)
	if err != nil {
		return nil, err
	}
	project := func(f found) bool { return f.project() }
	w := &Workspace{remote: remote, local: local, poll: fsPoll}
	var holder found // the FS relay that holds dir
	if p, ok := covering(all, dir, project); ok {
		w.name, w.root, holder = p.name, p.place, p
	} else if p, ok := covering(all, tree.Main, project); inTree && ok {
		w.name, w.root = p.name, p.place
		holder, ok = covering(all, dir, func(f found) bool { return f.workspace == p.name })
		if !ok {
			holder = joining(remote, local, tree.Root, w.name)
			if err := refuseClash(all, holder); err != nil {
				return nil, err
			}
		}
	} else {
		holder = joining(remote, local, dir, "")
		if f, ok := named(all, holder.name); ok {
			// The same ends were joined by another workspace for one of its
			// relays, so dir belongs to that workspace.
			holder, w.name, w.root = f, f.workspace, f.place
			if p, ok := named(all, f.workspace); ok {
				w.root = p.place
			}
		} else {
			if err := refuseClash(all, holder); err != nil {
				return nil, err
			}
			w.name, w.root = holder.name, holder.place
			for _, f := range all {
				if _, inside := pathmap.Under(f.place.Local, dir); inside && f.project() && f.place.Local != dir {
					w.nested = append(w.nested, f.place)
				}
			}
		}
	}
	if err := w.addFS(ctx, holder.name, holder.workspace, holder.ends); err != nil {
		return nil, errors.Join(err, w.Detach(ctx))
	}
	places := []pathmap.Place{w.root, holder.place}
	for _, f := range all {
		if f.workspace == w.name {
			places = append(places, f.place)
		}
	}
	w.paths = buildMap(places)
	return w, nil
}

// Map is the path map of every workspace on remote: the ends of every FS
// relay Mutagen lists for it. A directory that no relay holds is mapped where
// a new workspace would join it, in either direction, so a project never
// attached here still has its remote path. Below the local home that is a
// prefix of the remote home. The home itself and a directory outside it are
// joined under a slug of their path, which cannot be read back, so each of
// those is mapped only when the caller names it in dirs; any other path under
// no root is left as it is.
func Map(ctx context.Context, remote Remote, local Local, dirs ...string) (pathmap.Map, error) {
	if err := absolute(append([]string{remote.Home, local.Home}, dirs...)...); err != nil {
		return pathmap.Map{}, err
	}
	all, err := list(ctx, remote, local)
	if err != nil {
		return pathmap.Map{}, err
	}
	places := make([]pathmap.Place, 0, len(all)+len(dirs)+1)
	for _, f := range all {
		places = append(places, f.place)
	}
	every := func(found) bool { return true }
	for _, dir := range dirs {
		dir = path.Clean(dir)
		if _, covered := covering(all, dir, every); !covered && dir != "/" {
			places = append(places, pathmap.Place{Local: dir, Remote: remoteRoot(local.Home, remote.Home, dir)})
		}
	}
	return buildMap(append(places, pathmap.Place{Local: local.Home, Remote: remote.Home})), nil
}

// buildMap is the map of places, the first of those that share a root on
// either side kept: a path under a shared root would map two ways.
func buildMap(places []pathmap.Place) pathmap.Map {
	seenLocal, seenRemote := map[string]bool{}, map[string]bool{}
	var kept []pathmap.Place
	for _, p := range places {
		if seenLocal[p.Local] || seenRemote[p.Remote] {
			continue
		}
		seenLocal[p.Local], seenRemote[p.Remote] = true, true
		kept = append(kept, p)
	}
	m, err := pathmap.New(kept...)
	if err != nil {
		// NOTE: every root is absolute and clean, and no root is kept twice,
		// which is all pathmap.New refuses.
		panic(err)
	}
	return m
}

// Name is the workspace's name: its project FS relay's.
func (w *Workspace) Name() string { return w.name }

// Root is the workspace's project root on this workstation.
func (w *Workspace) Root() string { return w.root.Local }

// PathMap is the map between local and remote paths, fixed when the workspace
// was opened.
func (w *Workspace) PathMap() pathmap.Map { return w.paths }

// Nested are the project roots of other workspaces inside a new workspace's
// root. They stay, so their files are carried twice, which the caller warns
// of.
func (w *Workspace) Nested() []pathmap.Place { return append([]pathmap.Place(nil), w.nested...) }

// AdapterKey is the key of the worker that runs the agent program's adapter
// for this workspace.
func (w *Workspace) AdapterKey() (worker.Key, error) { return adapterKey(w.name) }

// State reads the workspace's members: its FS relays from Mutagen and its
// workers from tmux.
func (w *Workspace) State(ctx context.Context) (Members, error) {
	relays, err := relay.List(ctx, w.remote.Read, w.local.Locks, relay.Label{Key: labelWorkspace, Value: w.name})
	if err != nil {
		return Members{}, err
	}
	workers, err := w.workers(ctx)
	if err != nil {
		return Members{}, err
	}
	return Members{Relays: relays, Workers: workers}, nil
}

func (w *Workspace) workers(ctx context.Context) ([]worker.Listed, error) {
	return w.workersOf(ctx, w.name)
}

// workersOf reads the workers of the workspace named workspace on the remote.
func (w *Workspace) workersOf(ctx context.Context, workspace string) ([]worker.Listed, error) {
	tag, err := worker.NewTag(tagWorkspace, workspace)
	if err != nil {
		return nil, err
	}
	return worker.List(ctx, w.workerRemote(), tag)
}

func (w *Workspace) workerRemote() worker.Remote {
	return worker.Remote{
		Exec: shell.NewSSH(w.remote.Exec, w.remote.Host),
		Read: shell.NewSSH(w.remote.Read, w.remote.Host),
	}
}

// AddFS adds the FS relay between place's two ends to the workspace, connects
// it and flushes it, so what an agent requires there is on the remote before
// it starts. A relay with the same two ends is leased as it is.
func (w *Workspace) AddFS(ctx context.Context, place pathmap.Place) error {
	if !path.IsAbs(place.Local) || !path.IsAbs(place.Remote) {
		return fmt.Errorf("workspace: FS relay %q → %q has a root that is not absolute", place.Local, place.Remote)
	}
	place = pathmap.Place{Local: path.Clean(place.Local), Remote: path.Clean(place.Remote)}
	name := relayName(w.remote.Host, place)
	if w.holds(name) {
		return nil
	}
	all, err := list(ctx, w.remote, w.local)
	if err != nil {
		return err
	}
	owner := w.name
	if f, ok := named(all, name); ok {
		owner = f.workspace
	}
	return w.addFS(ctx, name, owner, relay.Ends{Local: place.Local, Host: w.remote.Host, Remote: place.Remote})
}

// addFS leases the FS relay name of the workspace owner, making it in this
// workspace when it is Absent.
func (w *Workspace) addFS(ctx context.Context, name, owner string, ends relay.Ends) error {
	if w.holds(name) {
		return nil
	}
	fs := relay.NewFS(w.remote.Exec, w.remote.Read, w.local.Locks, ends, name, fsLabels(w.remote.Host, w.name)...)
	lease, err := fs.Connect(ctx)
	if err != nil {
		return err
	}
	w.hold(held{name: name, owner: owner, fs: true, lease: lease})
	return fs.Flush(ctx)
}

// AddNet adds the Net relay from port on the remote to the local address
// local, through filter unless it is nil, and connects it at once: a port
// another process took fails here, so the caller can go on without it.
func (w *Workspace) AddNet(ctx context.Context, port int, local string, filter relay.Filter) error {
	name := netName(w.remote.Host, port)
	if w.holds(name) {
		return nil
	}
	lease, err := relay.NewNet(w.remote.Exec, w.local.Locks, name, w.remote.Host, port, local, filter).Connect(ctx)
	if err != nil {
		return err
	}
	w.hold(held{name: name, owner: w.name, lease: lease})
	return nil
}

// Start starts spec's worker as one of the workspace's: tagged with the
// workspace, and with the origin when there is one.
func (w *Workspace) Start(ctx context.Context, spec worker.Spec) (worker.Worker, error) {
	tags, err := workerTags(w.name, w.local.Origin)
	if err != nil {
		return worker.Worker{}, err
	}
	spec.Tags = append(append([]worker.Tag(nil), spec.Tags...), tags...)
	return worker.Start(ctx, w.workerRemote(), spec)
}

// Attach connects the workspace's relays and the IO relay between the
// worker's stdio in form and streams, and returns when that relay or an FS
// relay stops serving, with why, or when ctx ends. The leases stay held for
// Detach. A line stream has one reader: while another lease holds its relay
// the attach fails with relay.ErrInUse.
func (w *Workspace) Attach(ctx context.Context, key worker.Key, form worker.Form, streams relay.Streams) error {
	wait, err := w.Join(ctx, key, form, streams)
	if err != nil {
		return err
	}
	return wait()
}

// Join is Attach for a caller that goes on while the relays serve: it returns
// once they are connected, and wait returns when Attach would. A refused or
// failed connect is Join's own error, so the caller learns it before it
// hands the streams to anyone.
func (w *Workspace) Join(ctx context.Context, key worker.Key, form worker.Form, streams relay.Streams) (wait func() error, err error) {
	if err := w.connect(ctx); err != nil {
		return nil, err
	}
	stdio, err := worker.New(w.workerRemote(), key).Stdio(form)
	if err != nil {
		return nil, err
	}
	opts := []shell.SSHOption{shell.WithoutSharedMaster()}
	var remote relay.Stdio
	switch s := stdio.(type) {
	case worker.Attach:
		remote = relay.Terminal{Attach: s.Command}
		opts = append(opts, shell.WithTty())
	case worker.Lines:
		remote = relay.Lines{In: s.In, Out: s.Out, Err: s.Err}
	default:
		return nil, fmt.Errorf("workspace: unhandled stdio %T", stdio)
	}
	name := ioName(w.remote.Host, key)
	lease, err := relay.NewIO(shell.NewSSH(w.remote.Exec, w.remote.Host, opts...), w.local.Locks, name, remote, streams).Connect(ctx)
	if err != nil {
		return nil, err
	}
	w.hold(held{name: name, owner: w.name, lease: lease})
	return func() error { return w.serve(ctx, lease) }, nil
}

// serve waits until io stops serving, or an FS relay the workspace holds is
// gone.
func (w *Workspace) serve(ctx context.Context, io relay.Lease) error {
	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()
	for {
		select {
		case <-io.Done():
			return io.Err()
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if name, gone := w.fsGone(ctx); gone {
				return fmt.Errorf("%w: %s", ErrRelayGone, name)
			}
		}
	}
}

// fsGone is an FS relay this process holds that Mutagen no longer lists. A
// read that fails finds none: the relays outlive a moment Mutagen cannot be
// read.
func (w *Workspace) fsGone(ctx context.Context) (string, bool) {
	all, err := list(ctx, w.remote, w.local)
	if err != nil {
		return "", false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, h := range w.held {
		if _, listed := named(all, h.name); h.fs && !listed {
			return h.name, true
		}
	}
	return "", false
}

// connect leases every FS relay of the workspace this process does not hold
// yet: the relays are connected together.
func (w *Workspace) connect(ctx context.Context) error {
	relays, err := relay.List(ctx, w.remote.Read, w.local.Locks, relay.Label{Key: labelWorkspace, Value: w.name})
	if err != nil {
		return err
	}
	for _, r := range relays {
		if w.holds(r.Name) {
			continue
		}
		lease, err := relay.NewFS(w.remote.Exec, w.remote.Read, w.local.Locks, r.Ends, r.Name, fsLabels(w.remote.Host, w.name)...).Connect(ctx)
		if err != nil {
			return err
		}
		w.hold(held{name: r.Name, owner: w.name, fs: true, lease: lease})
	}
	return nil
}

func (w *Workspace) holds(name string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, h := range w.held {
		if h.name == name {
			return true
		}
	}
	return false
}

func (w *Workspace) hold(h held) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.held = append(w.held, h)
}

// Detach gives up every lease this process holds on the workspace's relays:
// it suspends a lease while a worker of the relay's workspace remains, or
// while those workers cannot be read, which never removes a relay in use, and
// releases it with none left. A relay leased from another workspace is kept by
// that workspace's workers, not by this one's. The FS relays go last, each
// kind in the reverse order of taking, so what the others carried is flushed
// with them. Every error met is returned together, and the leases are given up
// all the same.
func (w *Workspace) Detach(ctx context.Context) error {
	w.mu.Lock()
	leases := w.held
	w.held = nil
	w.mu.Unlock()
	if len(leases) == 0 {
		return nil
	}
	// A detach runs when its caller ends, often because its context did; the
	// leases are given up all the same, or a relay would stay Connected with
	// nobody behind it.
	ctx = context.WithoutCancel(ctx)
	var errs []error
	remain := map[string]bool{}
	for _, h := range leases {
		if _, read := remain[h.owner]; read {
			continue
		}
		workers, err := w.workersOf(ctx, h.owner)
		errs = append(errs, err)
		remain[h.owner] = err != nil || len(workers) > 0
	}
	for _, fs := range []bool{false, true} {
		for i := len(leases) - 1; i >= 0; i-- {
			h := leases[i]
			switch {
			case h.fs != fs:
			case remain[h.owner]:
				errs = append(errs, h.lease.Suspend(ctx))
			default:
				errs = append(errs, h.lease.Release(ctx))
			}
		}
	}
	return errors.Join(errs...)
}

// Look reads in one call the state of the worker under key, the named files of
// its directory and the output of cmd run on the remote beside them, so a
// decision on the worker's and an agent's states reads both at one moment.
func (w *Workspace) Look(ctx context.Context, key worker.Key, cmd []string, files ...string) (worker.Look, error) {
	return worker.New(w.workerRemote(), key).Look(ctx, cmd, files...)
}

// FreePort picks the first of candidates free on the remote and recorded in no
// worker's file record, for a worker that listens on it.
func (w *Workspace) FreePort(ctx context.Context, record string, candidates []int) (int, error) {
	return worker.FreePort(ctx, w.workerRemote(), record, candidates)
}

// End ends the worker under key and keeps the leases this process holds, for
// a caller that starts another worker under the same key next: a Running
// worker is closed with SIGHUP, an Exited one removed, and an Absent one has
// nothing to end.
func (w *Workspace) End(ctx context.Context, key worker.Key) error {
	wk := worker.New(w.workerRemote(), key)
	state, err := wk.State(ctx)
	if err != nil {
		return err
	}
	return end(ctx, wk, state)
}

// end ends wk, read in state.
func end(ctx context.Context, wk worker.Worker, state worker.State) error {
	switch state {
	case worker.Absent:
		return nil
	case worker.Running:
		// The process may fail between the read of its state and the close,
		// which then refuses it as Exited: it is removed, as an Exited one is.
		err := wk.Close(ctx)
		if errors.Is(err, worker.ErrExited) {
			err = wk.Remove(ctx)
		}
		return err
	case worker.Exited:
		return wk.Remove(ctx)
	}
	return fmt.Errorf("workspace: unhandled worker state %s", state)
}

// Close ends the worker under key and detaches: a Running worker is closed
// with SIGHUP, an Exited one removed. The relays are connected first, so the
// detach that follows cannot race another session's, and with another worker
// left they stay, while with none they go. A worker already Absent has nothing
// to close: only the leases this process holds are given up, as Detach does.
func (w *Workspace) Close(ctx context.Context, key worker.Key) error {
	wk := worker.New(w.workerRemote(), key)
	state, err := wk.State(ctx)
	if err != nil {
		return errors.Join(err, w.Detach(ctx))
	}
	if state == worker.Absent {
		return w.Detach(ctx)
	}
	if err := w.connect(ctx); err != nil {
		return errors.Join(err, w.Detach(ctx))
	}
	return errors.Join(end(ctx, wk, state), w.Detach(ctx))
}
