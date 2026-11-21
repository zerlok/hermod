// Package hook is the local end of a session's hook forward. Claude on the
// sandbox posts its hooks to a sandbox port that ssh forwards to a proxy here;
// the proxy accepts only the session's own token on the hook URLs the session's
// settings name, maps the paths in the bodies between the sandbox and this
// machine, and forwards each hook to Air's current endpoint with Air's current
// token. Air's token therefore never leaves this machine, nor serves anything on
// Air's endpoint but those hooks, and a running Claude's settings stay valid when
// Air restarts with a new port and token.
package hook

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/secret"
)

// Config is what one proxy forwards, and where.
type Config struct {
	// Session is the token the session's settings carry, the only one accepted.
	Session secret.Token
	// Target is Air's hook endpoint (scheme and host); each hook keeps its path.
	Target *url.URL
	// Routes are the request URIs (path and query) of the hooks the session's
	// settings route through the proxy, the only ones forwarded: the session's
	// token is readable by every process of the sandbox user, and Air's token
	// must not reach whatever else Air serves on its endpoint.
	Routes []string
	// Authorization is the header value Air expects; zero sends none.
	Authorization secret.Token
	// ToLocal maps request bodies, which Claude writes with sandbox paths;
	// ToSandbox maps Air's replies back.
	ToLocal, ToSandbox pathmap.Map
	// Client sends the forwarded requests; nil is a client with forwardTimeout.
	Client *http.Client
	// Log receives a line per refused or failed hook; nil discards them.
	Log *log.Logger
}

// forwardTimeout bounds a hook's round trip to Air. Claude gives its http hooks
// 1–3 s and gives up on its own; this only stops a request outliving that.
const forwardTimeout = 10 * time.Second

// maxBody bounds a hook body read into memory. Tool inputs and responses ride in
// PostToolUse bodies, so the bound is generous.
const maxBody = 64 << 20

// NewHandler is the proxy's HTTP handler.
func NewHandler(cfg Config) http.Handler {
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: forwardTimeout}
	}
	if cfg.Log == nil {
		cfg.Log = log.New(io.Discard, "", 0)
	}
	return handler{cfg: cfg}
}

type handler struct{ cfg Config }

func (h handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		// Anyone on the sandbox can reach the forwarded port; without the
		// session's token nothing reaches Air.
		h.cfg.Log.Printf("hook proxy: refused a %s %s without the session's token", r.Method, r.URL.Path)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !h.routed(r) {
		h.cfg.Log.Printf("hook proxy: refused a %s %s, not one of the session's hooks", r.Method, r.URL.Path)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	// Claude's http hooks are POSTs; nothing else is a hook.
	if r.Method != http.MethodPost {
		h.cfg.Log.Printf("hook proxy: refused a %s %s, not a hook's method", r.Method, r.URL.Path)
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	out, err := h.forward(r, mapBody(body, h.cfg.ToLocal))
	if err != nil {
		h.cfg.Log.Printf("hook proxy: Air unreachable: %v", err)
		http.Error(w, "hook endpoint unreachable", http.StatusBadGateway)
		return
	}
	defer out.Body.Close()
	reply, err := io.ReadAll(io.LimitReader(out.Body, maxBody))
	if err != nil {
		h.cfg.Log.Printf("hook proxy: read Air's reply: %v", err)
		http.Error(w, "hook endpoint reply", http.StatusBadGateway)
		return
	}
	reply = mapBody(reply, h.cfg.ToSandbox)
	if ct := out.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(out.StatusCode)
	_, _ = w.Write(reply)
}

// authorized checks the Authorization header against the session's token, in
// constant time.
func (h handler) authorized(r *http.Request) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && !h.cfg.Session.IsZero() && secret.FromString(got).Equal(h.cfg.Session)
}

// routed reports whether the request goes to one of the session's hook URLs.
func (h handler) routed(r *http.Request) bool {
	uri := r.URL.RequestURI()
	for _, route := range h.cfg.Routes {
		if uri == route {
			return true
		}
	}
	return false
}

// forward sends the hook to Air at the same path, with Air's token in place of
// the session's.
func (h handler) forward(r *http.Request, body []byte) (*http.Response, error) {
	target := *h.cfg.Target
	target.Path, target.RawPath = r.URL.Path, r.URL.RawPath
	target.RawQuery = r.URL.RawQuery
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if !h.cfg.Authorization.IsZero() {
		req.Header.Set("Authorization", h.cfg.Authorization.Reveal())
	}
	return h.cfg.Client.Do(req)
}

// mapBody maps the paths in a JSON body; a body that is not JSON passes as it is.
func mapBody(body []byte, m pathmap.Map) []byte {
	if len(bytes.TrimSpace(body)) == 0 {
		return body
	}
	mapped, err := m.JSON(body)
	if err != nil {
		return body
	}
	return mapped
}

// Proxy is a running hook proxy on this machine's loopback.
type Proxy struct {
	server *http.Server
	port   int
	done   chan error
}

// Start serves handler on 127.0.0.1 at a port the kernel picks.
func Start(handler http.Handler) (*Proxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("hook proxy: %w", err)
	}
	// NOTE: a "tcp" listener's address is always a *net.TCPAddr.
	p := &Proxy{
		server: &http.Server{Handler: handler, ReadHeaderTimeout: forwardTimeout},
		port:   ln.Addr().(*net.TCPAddr).Port,
		done:   make(chan error, 1),
	}
	go func() { p.done <- p.server.Serve(ln) }()
	return p, nil
}

// Port is the local port the reverse forward points at.
func (p *Proxy) Port() int { return p.port }

// Close stops the proxy, letting hooks in flight finish until ctx ends.
func (p *Proxy) Close(ctx context.Context) error {
	err := p.server.Shutdown(ctx)
	if serveErr := <-p.done; !errors.Is(serveErr, http.ErrServerClosed) {
		err = errors.Join(err, serveErr)
	}
	return err
}
