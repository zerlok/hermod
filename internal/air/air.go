// Package air owns the files that register Hermod with JetBrains Air: the
// stand-in link through which Air starts Hermod as its claude, and the entry in
// Air's acp.json that makes a sandbox an agent in Air's chat.
package air

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// StandInPath is where the stand-in link lives, under the user's data directory
// (XDG_DATA_HOME, ~/.local/share by default). The directory is not on PATH, so a
// claude installed locally is still the one a shell finds.
func StandInPath(dataHome string) string {
	return filepath.Join(dataHome, "hermod", "bin", "claude")
}

// LinkOutcome is what LinkStandIn did.
type LinkOutcome int

const (
	// LinkUnchanged means the link already pointed at the binary.
	LinkUnchanged LinkOutcome = iota + 1
	// LinkCreated means there was nothing at the path.
	LinkCreated
	// LinkRepaired means something else was at the path and was replaced.
	LinkRepaired
)

func (o LinkOutcome) String() string {
	switch o {
	case LinkUnchanged:
		return "unchanged"
	case LinkCreated:
		return "created"
	case LinkRepaired:
		return "repaired"
	}
	return "unknown outcome"
}

// LinkStandIn makes link a symlink to the hermod binary at target, creating its
// directory when missing. It is idempotent: a link already to target is left as
// it is, and a link elsewhere or a file in its place is replaced in one rename,
// so a claude started meanwhile finds either the old entry or the new link. A
// directory in its place is refused, since it may hold the user's files.
func LinkStandIn(link, target string) (LinkOutcome, error) {
	if !filepath.IsAbs(target) {
		return 0, fmt.Errorf("stand-in target %q is not an absolute path", target)
	}
	outcome := LinkCreated
	info, err := os.Lstat(link)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return 0, fmt.Errorf("stand-in link: %w", err)
	case info.IsDir():
		return 0, fmt.Errorf("stand-in link %s is a directory", link)
	case info.Mode()&os.ModeSymlink != 0:
		current, err := os.Readlink(link)
		if err != nil {
			return 0, fmt.Errorf("stand-in link: %w", err)
		}
		if current == target {
			return LinkUnchanged, nil
		}
		outcome = LinkRepaired
	default:
		outcome = LinkRepaired
	}
	dir := filepath.Dir(link)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, fmt.Errorf("stand-in link: %w", err)
	}
	tmp := filepath.Join(dir, fmt.Sprintf(".%s.%d.tmp", filepath.Base(link), os.Getpid()))
	_ = os.Remove(tmp) // a leftover from a run that died between the two steps
	if err := os.Symlink(target, tmp); err != nil {
		return 0, fmt.Errorf("stand-in link: %w", err)
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return 0, fmt.Errorf("stand-in link: %w", err)
	}
	return outcome, nil
}
