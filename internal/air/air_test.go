package air

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStandInPath(t *testing.T) {
	cases := []struct {
		name     string
		dataHome string
		want     string
	}{
		{"default data home", "/home/me/.local/share", "/home/me/.local/share/hermod/bin/claude"},
		{"XDG data home", "/xdg/data", "/xdg/data/hermod/bin/claude"},
		{"trailing slash", "/xdg/data/", "/xdg/data/hermod/bin/claude"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StandInPath(tc.dataHome); got != tc.want {
				t.Errorf("StandInPath(%q) = %q, want %q", tc.dataHome, got, tc.want)
			}
		})
	}
}

// existing sets up what is at the link's path before LinkStandIn runs.
type existing int

const (
	nothing existing = iota
	linkToTarget
	linkElsewhere
	danglingLink
	regularFile
	directory
)

func TestLinkStandIn(t *testing.T) {
	cases := []struct {
		name        string
		before      existing
		target      string // relative to the temporary root, or absolute as given
		relative    bool   // pass the target as given, not under the root
		wantOutcome LinkOutcome
		wantErr     bool
		wantKept    bool // the entry at the path is left as it was
	}{
		{"created with its directory", nothing, "go/bin/hermod", false, LinkCreated, false, false},
		{"already right is unchanged", linkToTarget, "go/bin/hermod", false, LinkUnchanged, false, false},
		{"link elsewhere repaired", linkElsewhere, "go/bin/hermod", false, LinkRepaired, false, false},
		{"dangling link repaired", danglingLink, "go/bin/hermod", false, LinkRepaired, false, false},
		{"file in its place repaired", regularFile, "go/bin/hermod", false, LinkRepaired, false, false},
		{"directory refused", directory, "go/bin/hermod", false, 0, true, true},
		{"relative target refused", nothing, "hermod", true, 0, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			link := StandInPath(filepath.Join(root, "data"))
			target := tc.target
			if !tc.relative {
				target = filepath.Join(root, tc.target)
			}
			setUp(t, root, link, target, tc.before)
			before := describe(link)

			outcome, err := LinkStandIn(link, target)
			if (err != nil) != tc.wantErr {
				t.Fatalf("LinkStandIn() error = %v, wantErr %v", err, tc.wantErr)
			}
			if outcome != tc.wantOutcome {
				t.Errorf("outcome = %v, want %v", outcome, tc.wantOutcome)
			}
			if tc.wantKept {
				if after := describe(link); after != before {
					t.Errorf("entry changed from %q to %q", before, after)
				}
			} else if got, err := os.Readlink(link); err != nil || got != target {
				t.Errorf("link = %q (%v), want %q", got, err, target)
			}
			assertNoLeftovers(t, filepath.Dir(link), filepath.Base(link))
		})
	}
}

// TestLinkStandInIsIdempotent runs the link twice and expects the second run to
// change nothing.
func TestLinkStandInIsIdempotent(t *testing.T) {
	cases := []struct {
		name  string
		first LinkOutcome
		again LinkOutcome
	}{
		{"second run unchanged", LinkCreated, LinkUnchanged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			link := StandInPath(root)
			target := filepath.Join(root, "hermod")
			for i, want := range []LinkOutcome{tc.first, tc.again} {
				got, err := LinkStandIn(link, target)
				if err != nil || got != want {
					t.Errorf("run %d = %v, %v, want %v", i+1, got, err, want)
				}
			}
		})
	}
}

func setUp(t *testing.T, root, link, target string, before existing) {
	t.Helper()
	if before == nothing {
		return
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	var err error
	switch before {
	case linkToTarget:
		err = os.Symlink(target, link)
	case linkElsewhere:
		other := filepath.Join(root, "old-hermod")
		if err = os.WriteFile(other, nil, 0o755); err == nil {
			err = os.Symlink(other, link)
		}
	case danglingLink:
		err = os.Symlink(filepath.Join(root, "gone"), link)
	case regularFile:
		err = os.WriteFile(link, []byte("#!/bin/sh\n"), 0o755)
	case directory:
		err = os.Mkdir(link, 0o755)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// describe renders what is at path, for comparing before and after.
func describe(path string) string {
	info, err := os.Lstat(path)
	if err != nil {
		return "absent"
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(path)
		return "link to " + target
	}
	return info.Mode().String()
}

func assertNoLeftovers(t *testing.T, dir, keep string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // the directory was never made
	}
	for _, e := range entries {
		if e.Name() != keep {
			t.Errorf("leftover %s in %s", e.Name(), dir)
		}
	}
}

func TestLinkOutcomeString(t *testing.T) {
	cases := []struct {
		outcome LinkOutcome
		want    string
	}{
		{LinkUnchanged, "unchanged"},
		{LinkCreated, "created"},
		{LinkRepaired, "repaired"},
		{LinkOutcome(0), "unknown outcome"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.outcome.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}
