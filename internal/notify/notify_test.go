package notify

import (
	"context"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func TestListenDispatch(t *testing.T) {
	cases := []struct {
		name     string
		payload  string
		wantMsg  Message
		wantDrop bool
	}{
		{"well formed", `{"title":"t","body":"b","urgency":"low"}`, Message{Title: "t", Body: "b", Urgency: "low"}, false},
		{"body only", `{"body":"done"}`, Message{Body: "done"}, false},
		{"raw urgency passes through unnormalised", `{"body":"b","urgency":"bogus"}`, Message{Body: "b", Urgency: "bogus"}, false},
		{"missing body dropped", `{"title":"t"}`, Message{}, true},
		{"malformed json dropped", `{not json`, Message{}, true},
		{"large body just under the cap dispatches", `{"body":"` + strings.Repeat("x", maxMessage-32) + `"}`, Message{Body: strings.Repeat("x", maxMessage-32)}, false},
		{"body over the cap is truncated and dropped", `{"body":"` + strings.Repeat("x", maxMessage) + `"}`, Message{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sock := filepath.Join(t.TempDir(), "s.sock")
			cn := capNotifier{ch: make(chan Message, 1)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stop, err := Listen(ctx, sock, cn, discardLogger())
			if err != nil {
				t.Fatalf("Listen() error: %v", err)
			}
			defer stop()

			conn, err := net.Dial("unix", sock)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			if _, err := conn.Write([]byte(tc.payload)); err != nil {
				t.Fatalf("write: %v", err)
			}
			conn.Close()

			select {
			case got := <-cn.ch:
				if tc.wantDrop {
					t.Errorf("expected drop, got dispatch %+v", got)
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
			sock := filepath.Join(t.TempDir(), "s.sock")
			cn := capNotifier{ch: make(chan Message, 1)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stop, err := Listen(ctx, sock, cn, discardLogger())
			if err != nil {
				t.Fatalf("Listen() error: %v", err)
			}
			defer stop()

			t.Setenv(EnvSock, sock)
			if err := Send(ctx, tc.msg); err != nil {
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
			sock := filepath.Join(t.TempDir(), "s.sock")
			cn := capNotifier{ch: make(chan Message, 1)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stop, err := Listen(ctx, sock, cn, discardLogger())
			if err != nil {
				t.Fatalf("Listen() error: %v", err)
			}
			defer stop()

			key, value, _ := strings.Cut(Env(sock)[0], "=")
			t.Setenv(key, value)
			if err := Send(ctx, Message{Body: "x"}); err != nil {
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

func TestListenStopUnlinks(t *testing.T) {
	cases := []struct {
		name string
	}{
		{"stop unlinks the bound socket"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sock := filepath.Join(t.TempDir(), "s.sock")
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
