package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zerlok/hermod/internal/shell"
)

// Channel is a communication endpoint pair between the sandbox and the local
// machine: a unix socket a sandbox process writes to, forwarded over the session's
// ssh connection to a socket Hermod binds locally. What travels over it is the
// caller's business — sandbox only establishes it. The zero Channel means none.
type Channel struct {
	Local  string // local unix socket path, bound by whoever serves the channel
	Remote string // path inside the sandbox that a process writes to
}

// IsZero reports whether the channel is absent (nothing to forward or serve).
func (c Channel) IsZero() bool { return c.Local == "" || c.Remote == "" }

// reverseSpec is the `ssh -R` argument mapping the sandbox end onto the local one.
func (c Channel) reverseSpec() string { return c.Remote + ":" + c.Local }

// OpenChannel provisions the named channel's endpoints for host and returns their
// addresses; the forward itself is established by the session that carries the
// channel (see Config.Channel).
//
// The sandbox end is one socket per sandbox *user*, not per project: it lives at
// `${XDG_RUNTIME_DIR:-$HOME/.hermod/run}/hermod/<name>.sock`, so its address is
// the same for every project on that box and a sender needs to know only its own
// user's socket. The base is read with a real probe (a probe that shapes the plan
// stays real under dry-run) and the directory is created through effective. Both
// ends sit in a 0700 directory, so only the session user can reach the channel —
// unlike a loopback TCP forward, which every user on a shared box could reach.
//
// The local end is per host, so concurrent sessions to different sandboxes do not
// collide. Two sessions to the *same* sandbox user share the one endpoint: the
// most recent attach owns it (see the remote-agent proposal, which multiplexes
// this properly).
func OpenChannel(ctx context.Context, real, effective shell.Shell, host, name string) (Channel, error) {
	base, err := probeRuntimeDir(ctx, real, host)
	if err != nil {
		return Channel{}, fmt.Errorf("probe sandbox runtime dir: %w", err)
	}
	dir := base + "/hermod"
	if _, err := shell.NewSSH(effective, host).Run(ctx, shell.Command{
		Argv: []string{"mkdir", "-p", "-m", "700", dir},
	}); err != nil {
		return Channel{}, fmt.Errorf("create sandbox channel dir: %w", err)
	}
	return Channel{
		Local:  filepath.Join(localRuntimeDir(), "hermod", pathSafe(host), name+".sock"),
		Remote: dir + "/" + name + ".sock",
	}, nil
}

// probeRuntimeDir reads the sandbox's runtime directory (XDG_RUNTIME_DIR, or a
// per-user fallback under $HOME) with a read-only ssh probe.
func probeRuntimeDir(ctx context.Context, real shell.Shell, host string) (string, error) {
	res, err := shell.NewSSH(real, host).Run(ctx, shell.Command{
		Argv:    []string{"sh", "-c", `printf %s "${XDG_RUNTIME_DIR:-$HOME/.hermod/run}"`},
		Capture: true,
	})
	if err != nil {
		return "", err
	}
	base := strings.TrimSpace(res.Stdout)
	if base == "" {
		return "", errors.New("empty sandbox runtime dir")
	}
	// The base flows into the `ssh -R <remote>:<local>` spec as a raw argv element
	// (not a shell word), where ssh's own forward parser splits on ':'. A colon or
	// whitespace in the base could change the forward's meaning, so require a clean
	// absolute path and otherwise refuse to open the channel.
	if !filepath.IsAbs(base) || strings.ContainsAny(base, ": \t\n") {
		return "", fmt.Errorf("unexpected sandbox runtime dir %q", base)
	}
	return base, nil
}

// localRuntimeDir is the base for the local endpoint: XDG_RUNTIME_DIR when set,
// else the OS temp dir.
func localRuntimeDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return d
	}
	return os.TempDir()
}

// pathSafe renders a host alias as one path segment. An alias is normally already
// safe, but it is user input on the way into a filesystem path, so separators are
// flattened and a traversal name is refused.
func pathSafe(host string) string {
	host = strings.NewReplacer("/", "-", `\`, "-").Replace(host)
	if host == "" || host == "." || host == ".." {
		return "sandbox"
	}
	return host
}
