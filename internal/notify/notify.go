// Package notify is the local notification back-channel: a sandbox process sends
// one short message over a per-session unix socket (reverse-forwarded by the
// attach ssh) and Hermod, running locally, raises a native desktop notification.
// It is a leaf-tier package — it imports only shell and the standard library, and
// never runs a remote shell itself (control owns remote-endpoint provisioning).
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// EnvSock names the environment variable carrying the remote socket path into the
// session (via sandbox.Config.Env → tmux -e). `hermod notify` reads it on the box.
const EnvSock = "HERMOD_NOTIFY_SOCK"

// maxMessage bounds a single message read so a rogue sender cannot exhaust memory.
const maxMessage = 8 << 10

// Message is the entire wire payload — deliberately tiny, and the single source of
// truth for both the on-box sender and the local listener.
type Message struct {
	Title   string `json:"title,omitempty"`
	Body    string `json:"body"`
	Urgency string `json:"urgency,omitempty"` // "low" | "normal" | "critical"; "" == normal
}

// Notifier raises one local desktop notification (toast + sound). Implementations
// run their tools through a shell.Shell so they are argv-recordable in tests and
// uniform with every other side effect. Best-effort by contract: Notify returns
// nil and swallows tool errors, so a missing or broken notify tool can never
// surface to the caller.
type Notifier interface {
	Notify(ctx context.Context, m Message) error
}

// SocketName is the per-session socket basename, shared by both ends so the local
// and remote paths stay in lockstep. token is caller-supplied entropy.
func SocketName(token string) string { return "hermod-notify-" + token + ".sock" }

// Channel is the local half of the back-channel for one session. New only records
// paths and the notifier; it binds nothing. Listen does the binding. This split is
// why notify needs no dry-run flag: control calls Listen only on the real path and
// prints a note under --dry-run.
type Channel struct {
	n          Notifier
	localSock  string
	remoteSock string
	log        *log.Logger
}

// New builds a channel from already-resolved absolute socket paths: localSock in a
// local 0700 dir and remoteSock in the sandbox's 0700 dir, both resolved by control.
func New(n Notifier, localSock, remoteSock string, logger *log.Logger) *Channel {
	return &Channel{n: n, localSock: localSock, remoteSock: remoteSock, log: logger}
}

// ReverseSpec is the `ssh -R` argument mapping the remote socket to the local one.
func (c *Channel) ReverseSpec() string { return c.remoteSock + ":" + c.localSock }

// Env is the session environment entry telling on-box senders where to write.
func (c *Channel) Env() []string { return []string{EnvSock + "=" + c.remoteSock} }

// LocalSock is the local socket path, used for the dry-run listener note.
func (c *Channel) LocalSock() string { return c.localSock }

// Listen binds the local unix socket (0600, in a 0700 dir) and serves an accept
// loop in a background goroutine until ctx is done. It returns stop, which unlinks
// and closes the socket and is safe to call more than once. Only control's
// non-dry-run path calls Listen; every post-bind error is logged and swallowed.
func (c *Channel) Listen(ctx context.Context) (stop func() error, err error) {
	if err := os.MkdirAll(filepath.Dir(c.localSock), 0o700); err != nil {
		return nil, err
	}
	_ = os.Remove(c.localSock) // clear a stale socket from a prior run
	ln, err := net.Listen("unix", c.localSock)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(c.localSock, 0o600)

	var once sync.Once
	stop = func() error {
		once.Do(func() {
			_ = ln.Close()
			_ = os.Remove(c.localSock)
		})
		return nil
	}

	go func() {
		<-ctx.Done()
		_ = stop()
	}()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed
			}
			c.handle(ctx, conn)
		}
	}()
	return stop, nil
}

// handle reads one bounded message from conn and dispatches it. A malformed,
// oversized, or empty-body message is dropped without a notification, and no error
// escapes to affect the session.
func (c *Channel) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	data, _ := io.ReadAll(io.LimitReader(conn, maxMessage))
	var m Message
	if err := json.Unmarshal(data, &m); err != nil || m.Body == "" {
		if c.log != nil {
			c.log.Printf("notify: dropped a malformed or empty message")
		}
		return
	}
	_ = c.n.Notify(ctx, m) // best-effort; never escapes
}

// Send is the remote half: dial the socket named by EnvSock and write one Message.
// Errors are the sender's concern and never affect any other process on the box.
func Send(ctx context.Context, m Message) error {
	sock := os.Getenv(EnvSock)
	if sock == "" {
		return errors.New("notify: " + EnvSock + " unset (run inside a hermod --notify session)")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", sock)
	if err != nil {
		return err
	}
	defer conn.Close()
	return json.NewEncoder(conn).Encode(m)
}

// urgency normalises the wire urgency to a notify-send value, defaulting to normal.
func urgency(u string) string {
	switch u {
	case "low", "critical":
		return u
	default:
		return "normal"
	}
}

// orDefault returns s, or def when s is empty.
func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// asAppleStr renders s as a double-quoted AppleScript string literal, dropping
// control characters and escaping quotes and backslashes, so an untrusted body
// cannot break out of the osascript literal.
func asAppleStr(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r < 0x20 {
			continue
		}
		if r == '"' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}
