package notify

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// capNotifier records each dispatched message on a channel so a test can wait for
// (or confirm the absence of) a dispatch.
type capNotifier struct{ ch chan Message }

func (c capNotifier) Notify(_ context.Context, m Message) error {
	c.ch <- m
	return nil
}

func discardLogger() *log.Logger { return log.New(io.Discard, "", 0) }

// serve binds a channel on a short-pathed socket (a unix socket path must fit in
// ~108 bytes, which t.TempDir()'s subtest-derived name can overflow) and returns
// its address plus the messages it dispatches.
func serve(t *testing.T, n Notifier) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "h")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stop, err := Listen(ctx, sock, n, discardLogger())
	if err != nil {
		t.Fatalf("Listen() error: %v", err)
	}
	t.Cleanup(func() { _ = stop() })
	return sock
}

func TestEnv(t *testing.T) {
	cases := []struct {
		name string
		sock string
		want []string
	}{
		{"sandbox socket path", "/run/user/1000/hermod/notify.sock", []string{"HERMOD_NOTIFY_SOCK=/run/user/1000/hermod/notify.sock"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Env(tc.sock); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Env() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestServeDispatch drives the channel as an HTTP peer would: what the server
// accepts, what it refuses, and what reaches the notifier.
func TestServeDispatch(t *testing.T) {
	cases := []struct {
		name       string
		method     string
		path       string
		payload    string
		wantStatus int
		wantMsg    Message
		wantDrop   bool
	}{
		{"well formed", http.MethodPost, apiPath, `{"title":"t","body":"b","urgency":"low"}`, http.StatusNoContent, Message{Title: "t", Body: "b", Urgency: "low"}, false},
		{"body only", http.MethodPost, apiPath, `{"body":"done"}`, http.StatusNoContent, Message{Body: "done"}, false},
		{"raw urgency passes through unnormalised", http.MethodPost, apiPath, `{"body":"b","urgency":"bogus"}`, http.StatusNoContent, Message{Body: "b", Urgency: "bogus"}, false},
		{"missing body refused", http.MethodPost, apiPath, `{"title":"t"}`, http.StatusBadRequest, Message{}, true},
		{"malformed json refused", http.MethodPost, apiPath, `{not json`, http.StatusBadRequest, Message{}, true},
		{"just under the cap dispatches", http.MethodPost, apiPath, `{"body":"` + strings.Repeat("x", maxMessage-32) + `"}`, http.StatusNoContent, Message{Body: strings.Repeat("x", maxMessage-32)}, false},
		{"over the cap refused", http.MethodPost, apiPath, `{"body":"` + strings.Repeat("x", maxMessage) + `"}`, http.StatusBadRequest, Message{}, true},
		{"unknown path refused", http.MethodPost, "/elsewhere", `{"body":"b"}`, http.StatusNotFound, Message{}, true},
		{"wrong method refused", http.MethodGet, apiPath, "", http.StatusMethodNotAllowed, Message{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cn := capNotifier{ch: make(chan Message, 1)}
			sock := serve(t, cn)

			req, err := http.NewRequest(tc.method, "http://hermod"+tc.path, strings.NewReader(tc.payload))
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			resp, err := (&http.Client{Transport: unixTransport(sock)}).Do(req)
			if err != nil {
				t.Fatalf("do request: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %s, want %d", resp.Status, tc.wantStatus)
			}

			select {
			case got := <-cn.ch:
				if tc.wantDrop {
					t.Errorf("expected no dispatch, got %+v", got)
				} else if got != tc.wantMsg {
					t.Errorf("dispatched %+v, want %+v", got, tc.wantMsg)
				}
			case <-time.After(500 * time.Millisecond):
				if !tc.wantDrop {
					t.Errorf("expected dispatch %+v, got none", tc.wantMsg)
				}
			}
		})
	}
}

// TestPlainHTTPSenderIsEnough pins the zero-install fallback: because the channel
// speaks HTTP, anything that can write a request to a unix socket (the documented
// curl one-liner, or this hand-written request) is a valid sender.
func TestPlainHTTPSenderIsEnough(t *testing.T) {
	cases := []struct {
		name string
		body string
		want Message
	}{
		{"hand-written request", `{"body":"done"}`, Message{Body: "done"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cn := capNotifier{ch: make(chan Message, 1)}
			conn, err := net.Dial("unix", serve(t, cn))
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer conn.Close()

			raw := fmt.Sprintf("POST %s HTTP/1.1\r\nHost: hermod\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
				apiPath, len(tc.body), tc.body)
			if _, err := io.WriteString(conn, raw); err != nil {
				t.Fatalf("write: %v", err)
			}

			select {
			case got := <-cn.ch:
				if got != tc.want {
					t.Errorf("dispatched %+v, want %+v", got, tc.want)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatalf("no dispatch for a plain HTTP request")
			}
			answer, err := io.ReadAll(conn)
			if err != nil {
				t.Fatalf("read response: %v", err)
			}
			if !strings.Contains(string(answer), "204") {
				t.Errorf("response = %q, want a 204", answer)
			}
		})
	}
}

func TestSendRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		msg  Message
	}{
		{"full message", Message{Title: "t", Body: "b", Urgency: "critical"}},
		{"body only", Message{Body: "done"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cn := capNotifier{ch: make(chan Message, 1)}
			t.Setenv(EnvSock, serve(t, cn))
			if err := Send(context.Background(), tc.msg); err != nil {
				t.Fatalf("Send() error: %v", err)
			}
			select {
			case got := <-cn.ch:
				if got != tc.msg {
					t.Errorf("dispatched %+v, want %+v (round trip)", got, tc.msg)
				}
			case <-time.After(500 * time.Millisecond):
				t.Errorf("no dispatch for %+v", tc.msg)
			}
		})
	}
}

// TestSendReadsTheCarriedAddress asserts the two halves agree on one address: the
// entry Env puts into the session environment is the one Send dials.
func TestSendReadsTheCarriedAddress(t *testing.T) {
	cases := []struct {
		name string
	}{
		{"send dials the address Env carries"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cn := capNotifier{ch: make(chan Message, 1)}
			key, value, _ := strings.Cut(Env(serve(t, cn))[0], "=")
			t.Setenv(key, value)
			if err := Send(context.Background(), Message{Body: "x"}); err != nil {
				t.Fatalf("Send() error: %v", err)
			}
			select {
			case <-cn.ch:
			case <-time.After(500 * time.Millisecond):
				t.Error("no dispatch: Send did not reach the address Env carries")
			}
		})
	}
}

// TestSendReportsRefusal asserts a sender learns when its message did not land,
// rather than reporting success into the void.
func TestSendReportsRefusal(t *testing.T) {
	cases := []struct {
		name string
		msg  Message
	}{
		{"empty body is refused", Message{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cn := capNotifier{ch: make(chan Message, 1)}
			t.Setenv(EnvSock, serve(t, cn))
			if err := Send(context.Background(), tc.msg); err == nil {
				t.Fatal("expected an error for a refused message")
			}
		})
	}
}

func TestSendUnsetEnvErrors(t *testing.T) {
	cases := []struct {
		name string
	}{
		{"env unset"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvSock, "") // present but empty reads the same as unset
			err := Send(context.Background(), Message{Body: "x"})
			if err == nil {
				t.Fatal("expected an error when the address env is unset")
			}
			if !strings.Contains(err.Error(), EnvSock) {
				t.Errorf("error %v should name %s", err, EnvSock)
			}
		})
	}
}

// TestConcurrentSendersAllDelivered asserts a burst of senders costs no messages.
func TestConcurrentSendersAllDelivered(t *testing.T) {
	cases := []struct {
		name    string
		senders int
	}{
		{"a burst of senders", 24},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cn := capNotifier{ch: make(chan Message, tc.senders)}
			t.Setenv(EnvSock, serve(t, cn))

			var wg sync.WaitGroup
			errs := make(chan error, tc.senders)
			for i := range tc.senders {
				wg.Add(1)
				go func() {
					defer wg.Done()
					errs <- Send(context.Background(), Message{Body: fmt.Sprintf("m%d", i)})
				}()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Errorf("Send() error: %v", err)
				}
			}
			if got := len(cn.ch); got != tc.senders {
				t.Errorf("dispatched %d messages, want %d", got, tc.senders)
			}
		})
	}
}

func TestListenStopUnlinks(t *testing.T) {
	cases := []struct {
		name string
	}{
		{"stop unlinks the bound socket"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "h")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			sock := filepath.Join(dir, "s.sock")

			stop, err := Listen(context.Background(), sock, noopNotifier{}, discardLogger())
			if err != nil {
				t.Fatalf("Listen() error: %v", err)
			}
			if _, err := os.Stat(sock); err != nil {
				t.Fatalf("socket not bound: %v", err)
			}
			if err := stop(); err != nil {
				t.Fatalf("stop() error: %v", err)
			}
			if _, err := os.Stat(sock); !os.IsNotExist(err) {
				t.Errorf("socket still present after stop: %v", err)
			}
			if err := stop(); err != nil { // idempotent
				t.Errorf("second stop() error: %v", err)
			}
		})
	}
}
