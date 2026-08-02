// Package notify is the local notification back-channel: a sandbox process sends
// one short message over a unix socket (reverse-forwarded by the attach ssh) and
// Hermod, running locally, raises a native desktop notification.
//
// The local end is an ordinary net/http server bound to that socket, so the
// accept loop, per-connection lifecycle, read timeouts and graceful shutdown are
// the standard library's rather than hand-rolled. A sender is therefore any HTTP
// client that can reach a unix socket — `hermod notify` is the convenient one, but
// a bare box can use curl.
//
// It is a leaf-tier package and deliberately knows nothing about how the socket
// gets there: it imports only shell and the standard library, never runs a remote
// shell, and never addresses or provisions an endpoint (sandbox owns the channel
// between local and remote). It serves a socket it is handed, and it sends to a
// socket the environment names.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ChannelName is the name Hermod asks the sandbox for when opening the endpoint
// this package serves. One channel per sandbox user, so the address is the same
// for every project on the box.
const ChannelName = "notify"

// EnvSock names the environment variable carrying the sandbox-side socket path
// into the session (via sandbox.Config.Env → tmux -e). `hermod notify` reads it
// on the box.
const EnvSock = "HERMOD_NOTIFY_SOCK"

// apiPath is the one endpoint on the channel. The host in a request URL is
// meaningless over a unix socket, so any host reaches it.
const apiPath = "/notify"

const (
	// maxMessage bounds a request body so a rogue sender cannot exhaust memory.
	maxMessage = 8 << 10
	// maxInFlight bounds how many senders are served at once, and with it the
	// goroutines the server runs. Beyond it, connections wait in the kernel's
	// backlog rather than becoming work in this process.
	maxInFlight = 8
	// requestTimeout bounds reading and answering one request, so a peer that
	// connects but never finishes cannot hold a slot.
	requestTimeout = 5 * time.Second
	// notifyTimeout bounds raising one notification, so a wedged desktop tool
	// cannot hold a slot either — the tool is killed and the message dropped.
	notifyTimeout = 10 * time.Second
)

// Message is the entire wire payload — deliberately tiny, and the single source of
// truth for both the on-box sender and the local server.
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

// Listen serves the channel on sock (bound 0600, in a 0700 dir) until ctx is done,
// raising a notification for each message that arrives. It returns stop, which
// shuts the server down and unlinks the socket, and is safe to call more than
// once. Only the bind can fail: once served, every error — malformed request,
// stalled peer, broken notify tool — is logged and swallowed, so the channel can
// never fail the session.
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

	mux := http.NewServeMux()
	mux.Handle("POST "+apiPath, &handler{base: ctx, n: n, log: logger})
	srv := &http.Server{
		Handler:           mux,
		ReadTimeout:       requestTimeout,
		ReadHeaderTimeout: requestTimeout,
		WriteTimeout:      requestTimeout + notifyTimeout,
		IdleTimeout:       requestTimeout,
		ErrorLog:          logger,
	}

	var once sync.Once
	stop = func() error {
		once.Do(func() {
			_ = srv.Close()
			_ = os.Remove(sock)
		})
		return nil
	}

	go func() {
		<-ctx.Done()
		_ = stop()
	}()
	go func() {
		// Serve owns the accept loop and the per-connection goroutines; it returns
		// only once the listener is closed, which is what stop does.
		_ = srv.Serve(limit(ln, maxInFlight))
	}()
	return stop, nil
}

// handler turns one request into one notification. It answers as soon as the
// message has been raised, so a sender learns whether it landed; base is the
// channel's context, so a sender that hangs up mid-toast does not cancel it.
type handler struct {
	base context.Context
	n    Notifier
	log  *log.Logger
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var m Message
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMessage)).Decode(&m); err != nil || m.Body == "" {
		if h.log != nil {
			h.log.Printf("notify: dropped a malformed or empty message")
		}
		http.Error(w, "malformed or empty message", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(h.base, notifyTimeout)
	defer cancel()
	_ = h.n.Notify(ctx, m) // best-effort; never escapes
	w.WriteHeader(http.StatusNoContent)
}

// Send is the sandbox half: POST one Message to the socket named by EnvSock.
// Errors are the sender's concern and never affect any other process on the box.
func Send(ctx context.Context, m Message) error {
	sock := os.Getenv(EnvSock)
	if sock == "" {
		return errors.New("notify: " + EnvSock + " unset (run inside a hermod session with notifications enabled)")
	}
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://hermod"+apiPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Transport: unixTransport(sock)}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("notify: channel refused the message (%s)", resp.Status)
	}
	return nil
}

// unixTransport dials the given unix socket for every request, whatever the URL's
// host says — over a unix socket the host is only there to make a valid URL.
// Keep-alives are off: one message is one connection, and an idle connection left
// open would hold one of the server's slots for nothing.
func unixTransport(sock string) *http.Transport {
	return &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}
}

// limitListener caps the connections served at once, and with them the goroutines
// the server runs: a slot is taken before each accept and released when the
// connection closes. Closing it releases anything waiting for a slot.
type limitListener struct {
	net.Listener
	sem  chan struct{}
	done chan struct{}
	once sync.Once
}

func limit(ln net.Listener, n int) net.Listener {
	return &limitListener{Listener: ln, sem: make(chan struct{}, n), done: make(chan struct{})}
}

func (l *limitListener) Accept() (net.Conn, error) {
	select {
	case l.sem <- struct{}{}:
	case <-l.done:
		return nil, net.ErrClosed
	}
	conn, err := l.Listener.Accept()
	if err != nil {
		<-l.sem
		return nil, err
	}
	return &limitConn{Conn: conn, release: sync.OnceFunc(func() { <-l.sem })}, nil
}

func (l *limitListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.Listener.Close()
}

// limitConn releases its slot when closed — which http.Server always does, once,
// per connection it accepted.
type limitConn struct {
	net.Conn
	release func()
}

func (c *limitConn) Close() error {
	defer c.release()
	return c.Conn.Close()
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
