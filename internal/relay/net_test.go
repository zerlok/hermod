package relay

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// forwarded plays the connection of a Net relay: it reports the local port
// its forward points at, then runs as asked.
type forwarded struct {
	mu   sync.Mutex
	port string
}

func (f *forwarded) answer(play script) func([]string) script {
	return func(args []string) script {
		for i, a := range args {
			if a == "-R" {
				_, port, _ := strings.Cut(args[i+1], ":127.0.0.1:")
				f.mu.Lock()
				f.port = port
				f.mu.Unlock()
			}
		}
		return play
	}
}

func (f *forwarded) address() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return "127.0.0.1:" + f.port
}

// filterFunc is a Filter made of a function.
type filterFunc func(Call) (Call, error)

func (f filterFunc) Pass(c Call) (Call, error) { return f(c) }

// seen is one call as the local endpoint got it.
type seen struct {
	method, uri, auth, body string
}

func TestNetFilter(t *testing.T) {
	upper := func(b []byte) []byte { return []byte(strings.ToUpper(string(b))) }
	cases := []struct {
		name       string
		filter     filterFunc
		unreached  bool // the local address listens to nothing
		send       string
		wantStatus int
		wantBody   string
		wantSeen   []seen
	}{
		{
			name: "a call passed as the filter rewrote it, and its answer",
			filter: func(c Call) (Call, error) {
				c.Header.Set("Authorization", "Bearer local")
				c.Body = []byte(strings.ReplaceAll(string(c.Body), "/home/u", "/home/me"))
				c.Answer = upper
				return c, nil
			},
			send:       `{"cwd":"/home/u/api"}`,
			wantStatus: http.StatusOK,
			wantBody:   `ANSWER`,
			wantSeen:   []seen{{http.MethodPost, "/hook?q=1", "Bearer local", `{"cwd":"/home/me/api"}`}},
		},
		{
			name:       "a call refused with a status",
			filter:     func(Call) (Call, error) { return Call{}, &Refusal{Status: http.StatusUnauthorized, Reason: "no token"} },
			send:       `{}`,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "a call refused without a status is forbidden",
			filter:     func(Call) (Call, error) { return Call{}, &Refusal{Reason: "no token"} },
			send:       `{}`,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "a call refused with a status that is no refusal is forbidden",
			filter:     func(Call) (Call, error) { return Call{}, &Refusal{Status: http.StatusOK, Reason: "no token"} },
			send:       `{}`,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "a call the filter fails on is forbidden",
			filter:     func(Call) (Call, error) { return Call{}, errFake },
			send:       `{}`,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "a local address that does not answer",
			filter:     func(c Call) (Call, error) { return c, nil },
			unreached:  true,
			send:       `{}`,
			wantStatus: http.StatusBadGateway,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu  sync.Mutex
				got []seen
			)
			local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				got = append(got, seen{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), string(body)})
				mu.Unlock()
				_, _ = io.WriteString(w, "answer")
			}))
			defer local.Close()
			address := strings.TrimPrefix(local.URL, "http://")
			if tc.unreached {
				local.Close()
			}
			var fwd forwarded
			ex := &fakeExec{answer: fwd.answer(writes(readyLine + "\n"))}
			l, err := NewNet(ex, t.TempDir(), "hermod-net", "prod-box", 45001, address, tc.filter).Connect(context.Background())
			if err != nil {
				t.Fatalf("Connect() error: %v", err)
			}
			defer l.Release(context.Background())

			resp, err := http.Post("http://"+fwd.address()+"/hook?q=1", "application/json", strings.NewReader(tc.send))
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if tc.wantBody != "" && string(body) != tc.wantBody {
				t.Errorf("body = %q, want %q", body, tc.wantBody)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(got) != len(tc.wantSeen) {
				t.Fatalf("local endpoint got %+v, want %+v", got, tc.wantSeen)
			}
			for i := range got {
				if got[i] != tc.wantSeen[i] {
					t.Errorf("local endpoint got %+v, want %+v", got[i], tc.wantSeen[i])
				}
			}
		})
	}
}

func TestNetWithoutFilter(t *testing.T) {
	cases := []struct {
		name string
		send string
		want string
	}{
		{"bytes pass both ways as they are", "ping\n", "pong: ping\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			go func() {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				line, _ := bufio.NewReader(conn).ReadString('\n')
				_, _ = io.WriteString(conn, "pong: "+line)
			}()
			var fwd forwarded
			ex := &fakeExec{answer: fwd.answer(writes(readyLine + "\n"))}
			l, err := NewNet(ex, t.TempDir(), "hermod-net", "prod-box", 45001, ln.Addr().String(), nil).Connect(context.Background())
			if err != nil {
				t.Fatalf("Connect() error: %v", err)
			}
			defer l.Release(context.Background())

			conn, err := net.Dial("tcp", fwd.address())
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer conn.Close()
			if _, err := io.WriteString(conn, tc.send); err != nil {
				t.Fatal(err)
			}
			got, err := bufio.NewReader(conn).ReadString('\n')
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNetConnection(t *testing.T) {
	cases := []struct {
		name     string
		ssh      script
		timeout  bool // the Connect's ctx ends while it waits
		wantErr  bool
		wantMsg  string // a part of the Connect error
		wantDone error  // why Done closed on its own; nil when it stays open
	}{
		{
			name: "the remote port is taken",
			ssh: func(_ context.Context, _ io.Reader, _, errOut io.Writer) error {
				_, _ = io.WriteString(errOut, "Error: remote port forwarding failed for listen port 45001\n")
				return errFake
			},
			wantErr: true,
			wantMsg: "remote port forwarding failed",
		},
		{
			name:     "the connection drops",
			ssh:      ends(readyLine+"\n", errFake),
			wantDone: errFake,
		},
		{
			name: "a remote that prints before the command runs",
			ssh:  writes("Welcome to prod-box\n" + readyLine + "\n"),
		},
		{
			name: "a remote whose command ends without the ready line",
			ssh: func(_ context.Context, in io.Reader, out, _ io.Writer) error {
				_, _ = io.WriteString(out, "Welcome to prod-box\n")
				if c, ok := out.(io.Closer); ok {
					_ = c.Close()
				}
				_, err := io.Copy(io.Discard, in)
				return err
			},
			wantErr: true,
			wantMsg: ErrEnded.Error(),
		},
		{
			name:    "the Connect gives up waiting for the ready line",
			ssh:     untilDone,
			timeout: true,
			wantErr: true,
			wantMsg: context.DeadlineExceeded.Error(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ex := &fakeExec{answer: func([]string) script { return tc.ssh }}
			locks := t.TempDir()
			ctx := context.Background()
			if tc.timeout {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
			}
			l, err := NewNet(ex, locks, "hermod-net", "prod-box", 45001, "127.0.0.1:1", nil).Connect(ctx)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Connect() error = %v, want an error: %v", err, tc.wantErr)
			}
			if err != nil {
				if !strings.Contains(err.Error(), tc.wantMsg) {
					t.Errorf("Connect() error = %v, want it to say %q", err, tc.wantMsg)
				}
				if held, _ := leased(locks, "hermod-net"); held {
					t.Error("a relay that could not connect is still leased")
				}
				if got := ex.running(); got != 0 {
					t.Errorf("%d commands still running after a failed Connect", got)
				}
				return
			}
			if tc.wantDone == nil {
				if isDone(l) {
					t.Errorf("Done closed with %v, want it open", l.Err())
				}
			} else {
				<-l.Done()
				if err := l.Err(); !errors.Is(err, tc.wantDone) {
					t.Errorf("Err() = %v, want %v", err, tc.wantDone)
				}
			}
			if err := l.Release(context.Background()); err != nil {
				t.Errorf("Release() error: %v", err)
			}
		})
	}
}

// TestNetShutdown checks that a filtered Net relay's shutdown, at its last
// lease's release, waits for the calls it serves only within the context the
// release is handed.
func TestNetShutdown(t *testing.T) {
	cases := []struct {
		name    string
		calling bool // a call is still running at the release
		ended   bool // the release's context has ended
		wantErr error
	}{
		{"no call running", false, false, nil},
		{"no call running, past the deadline", false, true, nil},
		{"a running call cut at the context's end", true, true, context.Canceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entered, unblock := make(chan struct{}), make(chan struct{})
			local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				<-unblock
			}))
			defer local.Close()
			defer close(unblock)
			var fwd forwarded
			ex := &fakeExec{answer: fwd.answer(writes(readyLine + "\n"))}
			pass := filterFunc(func(c Call) (Call, error) { return c, nil })
			l, err := NewNet(ex, t.TempDir(), "hermod-net", "prod-box", 45001, strings.TrimPrefix(local.URL, "http://"), pass).Connect(context.Background())
			if err != nil {
				t.Fatalf("Connect() error: %v", err)
			}
			if tc.calling {
				go func() {
					if resp, err := http.Post("http://"+fwd.address()+"/hook", "application/json", strings.NewReader(`{}`)); err == nil {
						_ = resp.Body.Close()
					}
				}()
				<-entered
			}
			ctx, cancel := context.WithCancel(context.Background())
			if tc.ended {
				cancel()
			}
			defer cancel()
			if err := l.Release(ctx); !errors.Is(err, tc.wantErr) || (err == nil) != (tc.wantErr == nil) {
				t.Errorf("Release: %v, want %v", err, tc.wantErr)
			}
		})
	}
}
