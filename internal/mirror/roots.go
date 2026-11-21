package mirror

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/shell"
)

// Mirror is one of Hermod's mirrors of a sandbox, as Mutagen lists it: its ends
// are the root pair it mirrors.
type Mirror struct {
	Name   string
	Kind   Kind
	Local  string // the local end
	Remote string // the sandbox end: a path on the sandbox, home-relative or absolute
}

// List lists the mirrors Hermod made for host, read from Mutagen by their
// labels: Mutagen holds which roots are mirrored and to where, so Hermod keeps
// no record of its own that could drift. A session whose labels or ends do not
// read as one of host's mirrors is left out. It is read-only.
func List(ctx context.Context, probe shell.Shell, host string) ([]Mirror, error) {
	res, err := probe.Run(ctx, shell.Command{
		Argv:    []string{"mutagen", "sync", "list", "--label-selector", Selector(host)},
		Capture: true,
	})
	// A selector that matches nothing is not an error, but read the marker as
	// for a name, should a Mutagen version report it.
	if strings.Contains(res.Stdout, notFoundMarker) || strings.Contains(res.Stderr, notFoundMarker) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list the mirrors of %s: %w", host, err)
	}
	var mirrors []Mirror
	for _, s := range parseList(res.Stdout) {
		kind, ok := parseKind(s.labels[labelKind])
		if !ok || s.labels[labelSandbox] != sandboxLabel(host) {
			continue
		}
		remote, ok := strings.CutPrefix(s.remote(kind), host+":")
		if !ok || remote == "" {
			continue
		}
		mirrors = append(mirrors, Mirror{Name: s.name, Kind: kind, Local: s.local(kind), Remote: remote})
	}
	return mirrors, nil
}

// Root is a project root pair: a local directory and the sandbox directory it
// is mirrored to.
type Root struct {
	Local  string // absolute local directory
	Remote string // sandbox directory, home-relative or absolute
}

// Placement is where a run's directory lies among a sandbox's project roots.
type Placement struct {
	// Root is the project root the run mirrors: a listed one holding the
	// directory, or a new one for the directory itself.
	Root Root
	// Dir is the run's directory on the sandbox, under Root.Remote.
	Dir string
	// Roots are every project root of the sandbox, Root first, for the run's
	// path map.
	Roots []Root
	// Held are the listed project roots inside a new Root. Their mirrors keep
	// syncing them, so their files are carried by two mirrors at once.
	Held []Root
}

// Place places a run's local directory among the sandbox's mirrors. A directory
// inside a project mirror's local root reuses that mirror, the innermost one
// when roots nest, and lies at the corresponding path under its sandbox root;
// a directory outside every root, such as a worktree beside the project,
// becomes a root of its own at remoteDir, the path it would be mirrored to.
// A new root that holds listed roots reports them in Held. Transcript mirrors
// hold no project and are passed over.
func Place(mirrors []Mirror, localDir, remoteDir string) Placement {
	localDir = path.Clean(localDir)
	var (
		roots []Root
		found = -1 // index in roots of the innermost root holding localDir
		rel   string
	)
	for _, m := range mirrors {
		if m.Kind != Project {
			continue
		}
		r := Root{Local: path.Clean(m.Local), Remote: path.Clean(m.Remote)}
		roots = append(roots, r)
		if under, ok := pathmap.Under(localDir, r.Local); ok && (found < 0 || len(r.Local) > len(roots[found].Local)) {
			found, rel = len(roots)-1, under
		}
	}
	p := Placement{Root: Root{Local: localDir, Remote: remoteDir}, Dir: remoteDir}
	if found >= 0 {
		p.Root = roots[found]
		p.Dir = path.Join(roots[found].Remote, rel)
	}
	p.Roots = []Root{p.Root}
	for _, r := range roots {
		if r == p.Root {
			continue
		}
		p.Roots = append(p.Roots, r)
		if _, inside := pathmap.Under(r.Local, localDir); found < 0 && inside {
			p.Held = append(p.Held, r)
		}
	}
	return p
}

// Serving is the mirrors among a sandbox's listed ones that serve a session
// in the sandbox directory dir, as its session tags it: the project mirror of
// the innermost root holding dir and the transcript mirrors of dir, one per
// local end, each when listed. Paths are compared as absolute ones, a home-relative path being
// under home, so a root and a directory written in different forms still
// match.
func Serving(mirrors []Mirror, host, home, dir string) []Config {
	transcripts := transcriptsPrefix(host, dir)
	abs := pathmap.Absolute(home, dir)
	var (
		project *Mirror
		found   []Config
	)
	for i, m := range mirrors {
		switch {
		case m.Kind == Transcripts && hasTag(m.Name, transcripts):
			found = append(found, Config{Kind: Transcripts, Host: host, RemotePath: m.Remote, LocalPath: m.Local, SessionDir: dir})
		case m.Kind == Project:
			root := pathmap.Absolute(home, m.Remote)
			if _, ok := pathmap.Under(abs, root); ok && (project == nil || len(root) > len(pathmap.Absolute(home, project.Remote))) {
				project = &mirrors[i]
			}
		}
	}
	if project != nil {
		found = append([]Config{{Kind: Project, Host: host, RemotePath: project.Remote, LocalPath: project.Local}}, found...)
	}
	return found
}

// hasTag reports whether name is prefix followed by a local end's tag.
func hasTag(name, prefix string) bool {
	tag, ok := strings.CutPrefix(name, prefix)
	return ok && isLocalTag(tag)
}
