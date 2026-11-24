package air

import (
	"errors"
	"os"
	"path/filepath"
)

// standInName is the name Air's terminal is given as its Claude executable:
// hermod started under it runs as the stand-in.
const standInName = "claude"

// StandInPath is where `make install` puts the stand-in link by default, under
// the user's data directory. It is off PATH, so a local claude stays the one a
// shell finds.
func StandInPath(dataHome string) string {
	return filepath.Join(dataHome, "hermod", "bin", standInName)
}

// Request is what `hermod air configure claude` registers, every value
// resolved by the caller.
type Request struct {
	Home       string            // the user's home, which holds acp.json
	DataHome   string            // the user's data directory, which holds the default stand-in link
	Remote     string            // the remote the entry serves
	Name       string            // the entry's name; "" is DefaultName
	Hermod     string            // the hermod binary, an absolute path
	Env        map[string]string // the entry's environment; empty writes none
	SearchPath []string          // the directories of PATH, searched for the stand-in link
	DryRun     bool              // build the entry and change nothing
	// RemoteVariable is the variable Air's own environment sets to the remote
	// for terminal mode, named by the caller.
	RemoteVariable string
}

// Result is what Configure registered, and what terminal mode needs besides.
type Result struct {
	Entry Entry
	// StandIn is the stand-in link to enter as Air's Claude executable, ""
	// when none links to hermod.
	StandIn string
	// RemoteLine is the `<RemoteVariable>=<remote>` line for Air's own
	// environment, which terminal mode reads the remote from.
	RemoteLine string
}

// Configure merges the remote's entry into acp.json, keeping every other entry
// and member, and finds what terminal mode needs. It never creates or changes
// the stand-in link, which is the installer's.
func Configure(r Request) (Result, error) {
	// Checked before anything is written, so a missing name cannot leave a
	// registered entry with a "=<remote>" line nothing reads.
	if r.RemoteVariable == "" {
		return Result{}, errors.New("terminal mode needs the name of the remote variable")
	}
	entry, err := NewEntry(r.Name, r.Remote, r.Hermod, r.Env)
	if err != nil {
		return Result{}, err
	}
	// Loaded under a dry run too, so a file no entry can be merged into is
	// reported either way.
	cfg, err := Load(ACPPath(r.Home))
	if err != nil {
		return Result{}, err
	}
	if !r.DryRun {
		if err := cfg.Merge(entry).Save(); err != nil {
			return Result{}, err
		}
	}
	return Result{
		Entry:      entry,
		StandIn:    findStandIn(r.Hermod, standInCandidates(r)),
		RemoteLine: r.RemoteVariable + "=" + r.Remote,
	}, nil
}

// standInCandidates are the places the link may be, in the order a shell would
// find it. A relative place, such as an empty or "." PATH element or an unset
// data directory, is resolved against wherever configure happens to run, so it
// names no executable Air could start and is left out.
func standInCandidates(r Request) []string {
	links := make([]string, 0, len(r.SearchPath)+1)
	for _, dir := range r.SearchPath {
		links = append(links, filepath.Join(dir, standInName))
	}
	links = append(links, StandInPath(r.DataHome))
	absolute := links[:0]
	for _, l := range links {
		if filepath.IsAbs(l) {
			absolute = append(absolute, l)
		}
	}
	return absolute
}

// findStandIn is the first candidate that is a symlink to the hermod binary.
// A claude that is not one, such as Claude Code itself, is passed over.
func findStandIn(hermod string, candidates []string) string {
	want, err := filepath.EvalSymlinks(hermod)
	if err != nil {
		return ""
	}
	for _, c := range candidates {
		info, err := os.Lstat(c)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		if got, err := filepath.EvalSymlinks(c); err == nil && got == want {
			return c
		}
	}
	return ""
}
