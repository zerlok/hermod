package air

import (
	"errors"
	"os"
	"path/filepath"
)

// StandInPath is where `make install` puts the stand-in link named name by
// default, under the user's data directory. It is off PATH, so a local claude
// stays the one a shell finds.
func StandInPath(dataHome, name string) string {
	return filepath.Join(dataHome, "hermod", "bin", name)
}

// Request is what `hermod air configure claude` registers, every value
// resolved by the caller.
type Request struct {
	Home     string // the user's home, which holds acp.json
	DataHome string // the user's data directory, which holds the default stand-in link
	Remote   string // the remote the entry serves
	Name     string // the entry's name; "" is DefaultName
	Hermod   string // the hermod binary, an absolute path
	// ChatArgs are hermod's arguments before the remote that serve Air's
	// chat, named by the caller, whose command line they are.
	ChatArgs []string
	// StandInName is the name of the stand-in link, under which hermod
	// stands in for the agent program, named by the caller.
	StandInName string
	Env         map[string]string // the entry's environment; empty writes none
	SearchPath  []string          // the directories of PATH, searched for the stand-in link
	DryRun      bool              // build the entry and change nothing
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
	// registered entry with a "=<remote>" line nothing reads, or a link
	// nothing finds.
	if r.RemoteVariable == "" {
		return Result{}, errors.New("terminal mode needs the name of the remote variable")
	}
	if r.StandInName == "" {
		return Result{}, errors.New("terminal mode needs the name of the stand-in link")
	}
	entry, err := NewEntry(r.Name, r.Remote, r.Hermod, r.ChatArgs, r.Env)
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
		links = append(links, filepath.Join(dir, r.StandInName))
	}
	links = append(links, StandInPath(r.DataHome, r.StandInName))
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
