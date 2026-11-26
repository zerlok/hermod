package relay

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/zerlok/hermod/internal/shell"
)

// Call is one HTTP call through a filtered Net relay.
type Call struct {
	Method string
	URI    string // the request URI: path and query
	Header http.Header
	Body   []byte
	// Answer rewrites the body of the local answer on its way back to the
	// remote; nil passes it as it is.
	Answer func([]byte) []byte
}

// Refusal is a call a Filter does not let through, answered with Status, or
// with 403 Forbidden when Status is no 4xx or 5xx.
type Refusal struct {
	Status int
	Reason string
}

func (r *Refusal) Error() string { return fmt.Sprintf("refused (%d): %s", r.Status, r.Reason) }

// Filter decides what a Net relay lets through: given a call that arrived on
// the remote port, it returns the call to make to the local address, or
// refuses it with a *Refusal; any other error refuses it as forbidden.
type Filter interface {
	Pass(Call) (Call, error)
}

// netTimeout bounds a call's round trip to the local address, and how long a
// call's header may take to arrive. The callers on the remote, such as an
// agent's hooks, give up within seconds; this only keeps a call from
// outliving them.
const netTimeout = 10 * time.Second

// maxBody bounds a call's body read into memory. An agent's hooks carry tool
// inputs and responses, so the bound is generous.
const maxBody = 64 << 20

// readyLine is what the remote command of the relay's connection prints once
// it runs, by which time the forward is bound: ssh requests its forwards before
// the session that runs the command and the server answers in order, so with
// ExitOnForwardFailure a refused forward ends ssh before the line can arrive.
const readyLine = "hermod-net-ready"

// Net is a Net relay: `ssh -R` from a port on the remote to a listener in this
// process, which passes each call to a local address, through a Filter when it
// has one. Nothing outside this process can buffer for it, so it is never
// Disconnected: the last lease given up removes it, suspended or released.
type Net struct {
	ex         shell.Executor
	host       string
	remotePort int
	local      string // host:port
	filter     Filter
	leases     leases
}

// NewNet is the Net relay named name from remotePort on host to the local
// address local (host:port), through filter unless it is nil. Its connection
// runs on ex, wrapped in ssh; the lock files of its leases are in locks.
func NewNet(ex shell.Executor, locks, name, host string, remotePort int, local string, filter Filter) *Net {
	r := &Net{ex: ex, host: host, remotePort: remotePort, local: local, filter: filter}
	r.leases = leases{name: name, locks: locks, mech: r}
	return r
}

// Connect leases the relay, starting its listener and its connection for the
// first lease this process takes. It fails when the remote port cannot be
// bound, such as when another process took it.
func (r *Net) Connect(ctx context.Context) (Lease, error) { return r.leases.connect(ctx) }

func (r *Net) make(context.Context) error { return nil }

// settle has nothing to do: stopping what serves the relay, the listener and
// the connection that carries the forward, removed it.
func (r *Net) settle(context.Context, giveUp) error { return nil }

func (r *Net) serve(ctx context.Context) (*serving, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("relay: listen: %w", err)
	}
	srv, sctx := newServing(ctx)
	if r.filter == nil {
		srv.run(func() error { return r.proxy(sctx, ln) })
	} else {
		r.serveHTTP(srv, ln)
	}
	// NOTE: a "tcp" listener's address is always a *net.TCPAddr.
	port := ln.Addr().(*net.TCPAddr).Port
	if err := r.forward(ctx, sctx, srv, port); err != nil {
		return nil, errors.Join(err, srv.stop(ctx))
	}
	return srv, nil
}

// forward starts the connection that carries the forward and waits until the
// forward is bound, or the connection ends without it.
func (r *Net) forward(ctx, sctx context.Context, srv *serving, port int) error {
	ssh := shell.NewSSH(r.ex, r.host,
		shell.WithoutSharedMaster(), shell.WithExitOnForwardFailure(), shell.WithReverseForward(r.remotePort, port))
	stderr := &lockedBuffer{}
	ex := ssh.Exec(sctx, shell.Command{
		// cat keeps the connection open until this process ends it, and never
		// reads anything: the input is a pipe nothing is written to.
		Args:   []string{"sh", "-c", "echo " + readyLine + " && exec cat >/dev/null"},
		Stderr: stderr,
	})
	ready := make(chan bool, 1)
	go func() {
		// A remote's rc files may print before the command runs, so the line is
		// looked for rather than expected first.
		sc := bufio.NewScanner(ex.Stdout)
		ok := false
		for !ok && sc.Scan() {
			ok = sc.Text() == readyLine
		}
		ready <- ok
		_, _ = io.Copy(io.Discard, ex.Stdout)
	}()
	select {
	case ok := <-ready:
		if !ok {
			// Closing the input ends a command still running, so the wait cannot
			// outlast a connection whose output ended early.
			_ = ex.Stdin.Close()
			err := ex.Wait()
			return fmt.Errorf("relay: forward remote port %d: %w: %s", r.remotePort, ended(err), strings.TrimSpace(stderr.String()))
		}
	case <-ctx.Done():
		// sctx ends with ctx, which stops the connection; it is reaped here,
		// since the serving does not wait for it yet.
		_ = ex.Stdin.Close()
		_ = ex.Wait()
		return fmt.Errorf("relay: forward remote port %d: %w", r.remotePort, ctx.Err())
	}
	srv.run(func() error {
		err := ex.Wait()
		_ = ex.Stdin.Close()
		return ended(err)
	})
	return nil
}

// proxy passes each connection to the listener on to the local address,
// byte for byte, until ctx ends.
func (r *Net) proxy(ctx context.Context, ln net.Listener) error {
	stop := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		wg.Go(func() { r.pipe(ctx, conn) })
	}
}

// pipe copies between one remote caller and the local address both ways.
func (r *Net) pipe(ctx context.Context, remote net.Conn) {
	defer remote.Close()
	var d net.Dialer
	local, err := d.DialContext(ctx, "tcp", r.local)
	if err != nil {
		return
	}
	defer local.Close()
	stop := context.AfterFunc(ctx, func() {
		_ = remote.Close()
		_ = local.Close()
	})
	defer stop()
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(local, remote)
		closeWrite(local)
		close(done)
	}()
	_, _ = io.Copy(remote, local)
	closeWrite(remote)
	<-done
}

func closeWrite(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
}

// serveHTTP serves each call to the listener through the filter until the
// serving stops.
func (r *Net) serveHTTP(srv *serving, ln net.Listener) {
	server := &http.Server{
		Handler:           filtered{filter: r.filter, local: r.local, client: &http.Client{Timeout: netTimeout}},
		ReadHeaderTimeout: netTimeout,
	}
	srv.shutdown = func(ctx context.Context) error {
		err := server.Shutdown(ctx)
		if err != nil {
			// The calls still running are cut, as the process's end would cut
			// them.
			err = errors.Join(err, server.Close())
		}
		return err
	}
	srv.run(func() error {
		err := server.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	})
}

// filtered is the handler of a filtered Net relay.
type filtered struct {
	filter Filter
	local  string
	client *http.Client
}

func (h filtered) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(io.LimitReader(req.Body, maxBody))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	call, err := h.filter.Pass(Call{Method: req.Method, URI: req.URL.RequestURI(), Header: req.Header.Clone(), Body: body})
	if err != nil {
		var refusal *Refusal
		if errors.As(err, &refusal) {
			// A status that is no refusal would either let the call look
			// answered or, outside HTTP's range, make the server drop the
			// connection instead of answering.
			status := refusal.Status
			if status < 400 || status > 599 {
				status = http.StatusForbidden
			}
			http.Error(w, http.StatusText(status), status)
			return
		}
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	out, err := http.NewRequestWithContext(req.Context(), call.Method, "http://"+h.local+call.URI, bytes.NewReader(call.Body))
	if err != nil {
		http.Error(w, "bad call", http.StatusBadGateway)
		return
	}
	out.Header = call.Header.Clone()
	resp, err := h.client.Do(out)
	if err != nil {
		http.Error(w, "local endpoint unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		http.Error(w, "local endpoint answer", http.StatusBadGateway)
		return
	}
	if call.Answer != nil {
		answer = call.Answer(answer)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(answer)
}

// lockedBuffer collects what a command writes while another goroutine may
// read it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
