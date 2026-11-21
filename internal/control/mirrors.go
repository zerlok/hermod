package control

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path"

	"github.com/zerlok/hermod/internal/mirror"
	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/sandbox"
	"github.com/zerlok/hermod/internal/shell"
)

// lease is this run's share in a mirror (see mirror.Lease).
type lease interface {
	// Release gives the share up and reports whether this run was the last
	// holder, which settles the mirror.
	Release() (last bool, err error)
	// Close drops whatever the lease still holds.
	Close() error
}

// soleLease is the lease of a run that shares no mirror, a dry run or one
// without a lease directory: it is always the last holder.
type soleLease struct{}

func (soleLease) Release() (bool, error) { return true, nil }
func (soleLease) Close() error           { return nil }

// heldMirror is a mirror the run opened under a lease.
type heldMirror struct {
	name    string
	session mirror.Session
	lease   lease
	root    string // the sandbox directory whose tmux sessions keep the mirror alive
	last    bool   // the run was the mirror's last holder
	settled bool   // the mirror needs nothing more from this run
}

// runMirrors are the mirrors one run uses. Each is opened under a lease and
// settled when the run is over by the run that leaves it last, by the liveness
// of its root: one rule, for the project and transcript mirrors alike.
type runMirrors struct {
	o         Options
	effective shell.Shell // side effects, dry-run-aware
	real      shell.Shell // probes, always real
	live      *sandbox.Liveness
	logger    *log.Logger
	held      []*heldMirror
}

func newRunMirrors(o Options, effective, real shell.Shell, logger *log.Logger) *runMirrors {
	// Liveness rides the effective shell with every other step of the settle;
	// a dry run never reads it (see dryRunSettle).
	return &runMirrors{o: o, effective: effective, real: real, live: sandbox.NewLiveness(effective, o.Sandbox), logger: logger}
}

// dryRunSettle is why a dry run plans a mirror's settle as a pause: liveness
// decides what follows a real attach, which a dry run only prints, so no look
// at the sandbox's sessions would tell what the detach will find.
const dryRunSettle = "dry run: the sessions left at detach decide; planned as the pause a running session gets"

// place lists the sandbox's mirrors from Mutagen and places the run's
// directory among their roots.
func place(ctx context.Context, o Options, real shell.Shell) (mirror.Placement, error) {
	listed, err := mirror.List(ctx, real, o.Sandbox)
	if err != nil {
		return mirror.Placement{}, err
	}
	p := mirror.Place(listed, o.LocalDir, o.RemoteDir)
	// Not step logging: -q must not hide that files will be synced twice.
	for _, r := range p.Held {
		fmt.Fprintf(o.Stdio.Err, "hermod: %s is already mirrored on its own and will also be synced through %s\n", r.Local, p.Root.Local)
	}
	return p, nil
}

// projectMirror is the project mirror of the placement's root.
func projectMirror(host string, p mirror.Placement) mirror.Config {
	return mirror.Config{Kind: mirror.Project, Host: host, RemotePath: p.Root.Remote, LocalPath: p.Root.Local}
}

// open takes a lease on the mirror and then opens it (creates or resumes),
// so it is never opened while its last holder settles it. root is the sandbox
// directory whose sessions keep it alive. Its commands print to ownOut.
func (m *runMirrors) open(ctx context.Context, cfg mirror.Config, root string) (mirror.Session, error) {
	cfg.Output = m.o.ownOut()
	name := mirror.Name(cfg)
	l, err := m.take(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("open mirror: %w", err)
	}
	session, earlier, err := mirror.NewMutagenSession(ctx, m.effective, m.real, cfg)
	if err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("open mirror: %w", err)
	}
	// Not step logging: -q must not hide a second sync of the same directory.
	for _, e := range earlier {
		fmt.Fprintf(m.o.Stdio.Err, "hermod: another Mutagen session also syncs this directory: %s\n", e)
	}
	m.held = append(m.held, &heldMirror{name: name, session: session, lease: l, root: root})
	return session, nil
}

func (m *runMirrors) take(ctx context.Context, name string) (lease, error) {
	if m.o.DryRun || m.o.LeaseDir == "" {
		return soleLease{}, nil
	}
	l, err := mirror.TakeLease(ctx, m.o.LeaseDir, name)
	if err != nil {
		return nil, err
	}
	return l, nil
}

// settleRule is how a run settles a mirror it is the last holder of: by its
// root's liveness, or by a pause when Claude may still run though liveness
// would not show it, as after a failed hang-up.
type settleRule struct {
	pauseReason string // why the mirror is paused; "" settles it by liveness
}

// byLiveness settles a mirror by its root's liveness, as a detach does.
var byLiveness = settleRule{}

// pausing pauses a mirror, for reason, which the log line gives.
func pausing(reason string) settleRule { return settleRule{pauseReason: reason} }

// afterFailedHangUp pauses the mirrors of a session whose hang-up failed: its
// tmux session may be gone while its Claude still runs and writes.
var afterFailedHangUp = pausing("the hang-up failed, and its Claude may still run")

// settle releases the run's leases and settles each mirror it was the last
// holder of by rule. A mirror another run still holds is left as it is.
func (m *runMirrors) settle(ctx context.Context, rule settleRule) error {
	var errs []error
	for _, h := range m.held {
		if h.settled {
			continue
		}
		last, err := h.lease.Release()
		switch {
		case err != nil:
			// Whether another run holds it is unknown: leaving the mirror as it
			// is never pulls it from under one.
			m.logger.Printf("mirror %s: %v; leaving it as it is", h.name, err)
			h.settled = true
			continue
		case !last:
			m.logger.Printf("mirror %s: another run still uses it → leaving it running", h.name)
			h.settled = true
			continue
		}
		h.last = true
		if rule.pauseReason == "" && m.o.DryRun {
			rule = pausing(dryRunSettle)
		}
		if rule.pauseReason == "" {
			err = teardown(ctx, h.name, h.session, m.live.Root(h.root), m.logger)
		} else {
			err = pauseMirror(ctx, h.name, h.session, rule.pauseReason, m.logger)
		}
		h.settled = err == nil
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// pauseUnsettled pauses every mirror the run was the last holder of and has
// not settled yet.
func (m *runMirrors) pauseUnsettled(ctx context.Context) error {
	var errs []error
	for _, h := range m.held {
		if h.last && !h.settled {
			err := h.session.Pause(ctx)
			h.settled = err == nil
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// close drops the leases, once the mirrors are settled.
func (m *runMirrors) close() {
	for _, h := range m.held {
		if err := h.lease.Close(); err != nil {
			m.logger.Printf("mirror %s: %v", h.name, err)
		}
	}
}

// runRoots is the run's path map: every project root of the sandbox, a
// home-relative sandbox root placed under home. A pair whose root another pair
// already maps on either side is left out, the run's own root coming first,
// since one path cannot map two ways.
func runRoots(p mirror.Placement, home string) (pathmap.Roots, error) {
	var (
		pairs  []pathmap.Pair
		local  = map[string]bool{}
		remote = map[string]bool{}
	)
	for _, r := range p.Roots {
		s := pathmap.Absolute(home, r.Remote)
		l := path.Clean(r.Local)
		if local[l] || remote[s] {
			continue
		}
		local[l], remote[s] = true, true
		pairs = append(pairs, pathmap.Pair{Local: l, Sandbox: s})
	}
	return pathmap.New(pairs...)
}
