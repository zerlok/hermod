// Package notify is the local notification back-channel: a sandbox process sends
// one short message over a unix socket (reverse-forwarded by the attach ssh) and
// Hermod, running locally, raises a native desktop notification.
//
// It is a leaf-tier package and deliberately knows nothing about how the socket
// gets there: it imports only shell and the standard library, never runs a remote
// shell, and never addresses or provisions an endpoint (sandbox owns the channel
// between local and remote). It binds a socket it is handed, and it sends to a
// socket the environment names.
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
	"time"
)

// ChannelName is the name Hermod asks the sandbox for when opening the endpoint
// this package listens on. One channel per sandbox user, so the address is the
// same for every project on the box.
const ChannelName = "notify"

// EnvSock names the environment variable carrying the sandbox-side socket path
// into the session (via sandbox.Config.Env → tmux -e). `hermod notify` reads it
// on the box.
const EnvSock = "HERMOD_NOTIFY_SOCK"

// maxMessage bounds a single message read so a rogue sender cannot exhaust memory.
const maxMessage = 8 << 10

// readTimeout bounds a single connection's read so a peer that connects but never
// sends a complete message cannot wedge the channel or leak its handler goroutine.
const readTimeout = 5 * time.Second

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

// Env is the session environment entry telling on-box senders where to write;
// sock is the channel's sandbox-side path.
func Env(sock string) []string { return []string{EnvSock + "=" + sock} }

// Listen binds sock (0600, in a 0700 dir) and serves an accept loop in a
// background goroutine until ctx is done, raising a notification for each message
// that arrives. It returns stop, which unlinks and closes the socket and is safe
// to call more than once. Every post-bind error is logged and swallowed, so a
// bound channel can never fail the session; only the bind itself can error.
func Listen(ctx context.Context, sock string, n Notifier, logger *log.Logger) (stop func() error, err error) {
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		return nil, err
	}
	_ = os.Remove(sock) // clear a stale socket from a prior run
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(sock, 0o600)

	l := &listener{n: n, log: logger}
	var once sync.Once
	stop = func() error {
		once.Do(func() {
			_ = ln.Close()
			_ = os.Remove(sock)
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
			// Handle in its own goroutine so a slow or stalled peer cannot starve
			// Accept and wedge the channel for the rest of the session.
			go l.handle(ctx, conn)
		}
	}()
	return stop, nil
}

// listener is the serving half of a bound channel: what to do with a message and
// where to log a dropped one.
type listener struct {
	n   Notifier
	log *log.Logger
}

// handle reads one bounded message from conn and dispatches it. A malformed,
// oversized, or empty-body message is dropped without a notification, and no error
// escapes to affect the session. A read deadline bounds a stalled sender so this
// goroutine always returns.
func (l *listener) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
	data, _ := io.ReadAll(io.LimitReader(conn, maxMessage))
	var m Message
	if err := json.Unmarshal(data, &m); err != nil || m.Body == "" {
		if l.log != nil {
			l.log.Printf("notify: dropped a malformed or empty message")
		}
		return
	}
	_ = l.n.Notify(ctx, m) // best-effort; never escapes
}

// Send is the remote half: dial the socket named by EnvSock and write one Message.
// Errors are the sender's concern and never affect any other process on the box.
func Send(ctx context.Context, m Message) error {
	sock := os.Getenv(EnvSock)
	if sock == "" {
		return errors.New("notify: " + EnvSock + " unset (run inside a hermod session with notifications enabled)")
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
