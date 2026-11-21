package sandbox

import (
	"bufio"
	"context"
	"fmt"
	"path"
	"strings"
	"sync"

	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/shell"
)

// livenessScript prints the sandbox's home and the `@hermod-dir` tag of every
// tmux session, one `dir=` line each, the tag empty on an untagged session,
// then `end`. A tmux with no server running has no sessions; any other tmux
// failure ends the script non-zero, without its `end` line, since sessions may
// exist that it could not list: the caller reads either as unknown liveness.
var livenessScript = layout(`printf 'home=%s\n' "$HOME"
if ! out=$(tmux list-sessions -F 'dir=#{@DIRTAG@}' 2>&1); then
  case "$out" in
    *"no server running"*|*"error connecting to"*) out= ;;
    *) printf '%s\n' "$out" >&2; exit 5 ;;
  esac
fi
if [ -n "$out" ]; then printf '%s\n' "$out"; fi
echo end`)

// Liveness reads which directories on host have a tmux session tagged with
// them, which is what keeps a mirror of a root under them alive. It looks once,
// on the first question, and answers every root from that one look, so the
// mirrors of one run settle on one ssh call.
type Liveness struct {
	host  string
	probe shell.Shell

	once sync.Once
	home string
	dirs []string // every tagged session's directory, absolute
	err  error
}

// NewLiveness prepares the look at host's sessions over inner; it runs nothing.
func NewLiveness(inner shell.Shell, host string) *Liveness {
	return &Liveness{host: host, probe: shell.NewSSH(inner, host)}
}

// Root is the liveness of one sandbox directory, home-relative or absolute.
func (l *Liveness) Root(dir string) RootLiveness { return RootLiveness{l: l, dir: dir} }

// RootLiveness is the liveness of one sandbox directory.
type RootLiveness struct {
	l   *Liveness
	dir string
}

// IsActive reports whether any tmux session tagged with a directory at or under
// the root exists. It is read-only; a look that failed is returned for the
// caller to choose a safe default.
func (r RootLiveness) IsActive(ctx context.Context) (bool, error) {
	r.l.once.Do(func() { r.l.home, r.l.dirs, r.l.err = r.l.look(ctx) })
	if r.l.err != nil {
		return false, r.l.err
	}
	root := pathmap.Absolute(r.l.home, r.dir)
	for _, d := range r.l.dirs {
		if _, under := pathmap.Under(d, root); under {
			return true, nil
		}
	}
	return false, nil
}

func (l *Liveness) look(ctx context.Context) (home string, dirs []string, err error) {
	res, err := l.probe.Run(ctx, shell.Command{
		Argv:    []string{"sh", "-c", livenessScript},
		Capture: true,
	})
	if err != nil {
		return "", nil, fmt.Errorf("list tmux sessions on %s: %w%s", l.host, err, detail(res.Stderr))
	}
	return parseLiveness(res.Stdout, l.host)
}

// parseLiveness reads the liveness script's output. Output without its home or
// its end line is not a complete look and reads as unknown.
func parseLiveness(out, host string) (home string, dirs []string, err error) {
	var (
		tagged []string
		ended  bool
	)
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		switch key, value, _ := strings.Cut(line, "="); {
		case line == "end":
			ended = true
		case key == "home":
			home = value
		case key == "dir" && value != "":
			tagged = append(tagged, value)
		}
	}
	if !ended || !path.IsAbs(home) {
		return "", nil, fmt.Errorf("list tmux sessions on %s: incomplete output", host)
	}
	for _, d := range tagged {
		dirs = append(dirs, pathmap.Absolute(home, d))
	}
	return home, dirs, nil
}
