package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/zerlok/hermod/internal/session"
	"github.com/zerlok/hermod/internal/worker"
)

// resolveRemote picks the remote from the argument, else the environment. A
// blank variable counts as unset: it names no host. The alias becomes an ssh
// argument and part of a Mutagen endpoint, so one starting with '-' is
// rejected: ssh would read it as an option, and an option such as
// ProxyCommand runs a local command. An ssh host alias holds no whitespace,
// quote or control character, and one that did would also break the
// HERMOD_REMOTE line and the Air entry written for it, so such a value is
// rejected too.
func resolveRemote(arg string, lookupEnv func(string) (string, bool)) (string, error) {
	alias, source := arg, "the argument"
	if strings.TrimSpace(alias) == "" {
		v, _ := lookupEnv(envRemote)
		alias, source = v, envRemote
	}
	switch {
	case strings.TrimSpace(alias) == "":
		return "", errors.New("a remote host alias is required: give one or set " + envRemote)
	case strings.HasPrefix(alias, "-"):
		return "", fmt.Errorf("remote %q from %s is not a host alias: it starts with '-'", alias, source)
	case strings.ContainsFunc(alias, notInAlias):
		return "", fmt.Errorf("remote %q from %s is not a host alias: it holds whitespace, a quote or a control character", alias, source)
	}
	return alias, nil
}

func notInAlias(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsControl(r) || r == '"' || r == '\''
}

// userShellLine runs its one argument as a command line of the remote user's
// shell, so the line means there what it means in a terminal of that user.
const userShellLine = `exec "$SHELL" -c "$1"`

// resolveCommand is the worker's command: the words after `--`, else the
// environment's command line run by the remote user's shell, else none, which
// is that user's login shell. A blank line is none: run as a line, it would
// exit at once and end the worker as it starts.
func resolveCommand(passthrough []string, lookupEnv func(string) (string, bool)) []string {
	if len(passthrough) > 0 {
		return passthrough
	}
	if line, _ := lookupEnv(envRemoteCmd); strings.TrimSpace(line) != "" {
		return []string{"sh", "-c", userShellLine, "sh", line}
	}
	return nil
}

// parseForwards reads each `-R PORT:ADDRESS`: a port on the remote and a
// host:port on this workstation.
func parseForwards(specs []string) ([]session.Forward, error) {
	var fwds []session.Forward
	for _, spec := range specs {
		port, address, found := strings.Cut(spec, ":")
		n, err := strconv.Atoi(port)
		if !found || err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("-R %q: want PORT:ADDRESS, PORT a port number on the remote", spec)
		}
		if _, _, err := net.SplitHostPort(address); err != nil {
			return nil, fmt.Errorf("-R %q: want PORT:ADDRESS, ADDRESS a local host:port: %w", spec, err)
		}
		fwds = append(fwds, session.Forward{Port: n, Address: address})
	}
	return fwds, nil
}

// resolveEntryOrigin is the HERMOD_ORIGIN an Air entry carries: the flag when
// given, even empty, else the environment's when set, even empty, else none.
func resolveEntryOrigin(flagSet bool, flag string, lookupEnv func(string) (string, bool)) *string {
	if flagSet {
		return &flag
	}
	if v, set := lookupEnv(envOrigin); set {
		return &v
	}
	return nil
}

// resolveOrigin names this machine: the environment's value when set, even
// empty, else the hostname. A hostname that cannot be read names nothing.
func resolveOrigin(lookupEnv func(string) (string, bool), hostname func() (string, error)) string {
	if v, set := lookupEnv(envOrigin); set {
		return v
	}
	h, err := hostname()
	if err != nil {
		return ""
	}
	return h
}

// localDir is the directory a session works in: dir, "" being the working
// directory, with a leading ~ for the home and relative to the working
// directory.
func localDir(dir, cwd, home string) string {
	switch {
	case dir == "":
		dir = cwd
	case dir == "~":
		dir = home
	case strings.HasPrefix(dir, "~/"):
		dir = filepath.Join(home, dir[2:])
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(cwd, dir)
	}
	return filepath.Clean(dir)
}

// workerName is the default name of the worker of a command that is no
// agent's conversation: the directory's path under the home, or its whole
// path outside it, with each separator a dash, so projects that share a base
// name do not share a worker. A path with a character a worker key cannot
// hold, such as '.' or a space, gets a hash of the path appended, so two
// directories that differ only there, such as web.v2 and web-v2, do not share
// one either; a path with no character a key can hold, such as / or a name in
// another script, is the hash alone.
func workerName(dir, home string) string {
	p := dir
	if rel, err := filepath.Rel(home, dir); err == nil && home != "" && rel != ".." && !strings.HasPrefix(rel, "../") {
		p = rel
		if rel == "." {
			p = filepath.Base(dir)
		}
	}
	lossy := false
	slug := strings.Map(func(r rune) rune {
		switch {
		case r == '/':
			return '-'
		case worker.IsKeyRune(r):
			return r
		default:
			lossy = true
			return '-'
		}
	}, strings.Trim(filepath.ToSlash(p), "/"))
	// A key does not start with '-', so a name that would takes the hash.
	if !lossy && slug != "" && slug[0] != '-' {
		return slug
	}
	sum := sha256.Sum256([]byte(dir))
	tag := hex.EncodeToString(sum[:])[:workerHashLen]
	if slug = strings.Trim(slug, "-"); slug == "" {
		return tag
	}
	return slug + "-" + tag
}

// workerHashLen is how many hex digits of its directory's hash a default
// worker name carries when the directory's own name cannot tell it apart.
const workerHashLen = 8

// xdgHome is an XDG base directory: the variable when it holds an absolute
// path, else fallback under home. The XDG spec has a relative value ignored.
func xdgHome(key, fallback, home string, lookupEnv func(string) (string, bool)) string {
	if v, _ := lookupEnv(key); filepath.IsAbs(v) {
		return v
	}
	return filepath.Join(home, fallback)
}

// lockDir is where the relays' lock files live: under the runtime directory,
// which the system empties at logout and which suits files that only carry
// locks; without one (macOS has none) under the cache directory.
func lockDir(home string, lookupEnv func(string) (string, bool)) string {
	if v, _ := lookupEnv(envRuntimeDir); filepath.IsAbs(v) {
		return filepath.Join(v, "hermod", "lease")
	}
	return filepath.Join(xdgHome(envCacheHome, ".cache", home, lookupEnv), "hermod", "lease")
}

// logDir is where the stand-in's sessions and the adapters' stderr are
// logged, one file per worker.
func logDir(home string, lookupEnv func(string) (string, bool)) string {
	return filepath.Join(xdgHome(envCacheHome, ".cache", home, lookupEnv), "hermod", "air")
}
