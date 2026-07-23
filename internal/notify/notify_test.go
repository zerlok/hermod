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

func TestSocketName(t *testing.T) {
	cases := []struct {
		name  string
		token string
		want  string
	}{
		{"hex token", "ab12cd34", "hermod-notify-ab12cd34.sock"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SocketName(tc.token); got != tc.want {
				t.Errorf("SocketName() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestChannelAddressing(t *testing.T) {
	cases := []struct {
		name       string
		localSock  string
		remoteSock string
		wantSpec   string
		wantEnv    []string
	}{
		{
			"spec and env derived from paths",
			"/local/hermod/s.sock", "/run/user/1000/hermod/s.sock",
			"/run/user/1000/hermod/s.sock:/local/hermod/s.sock",
			[]string{"HERMOD_NOTIFY_SOCK=/run/user/1000/hermod/s.sock"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := New(noopNotifier{}, tc.localSock, tc.remoteSock, discardLogger())
			if got := c.ReverseSpec(); got != tc.wantSpec {
				t.Errorf("ReverseSpec() = %q, want %q", got, tc.wantSpec)
			}
			if got := c.Env(); !reflect.DeepEqual(got, tc.wantEnv) {
				t.Errorf("Env() = %q, want %q", got, tc.wantEnv)
			}
			if got := c.LocalSock(); got != tc.localSock {
				t.Errorf("LocalSock() = %q, want %q", got, tc.localSock)
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
			c := New(cn, sock, "unused", discardLogger())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stop, err := c.Listen(ctx)
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
			c := New(cn, sock, "unused", discardLogger())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stop, err := c.Listen(ctx)
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
			c := New(noopNotifier{}, sock, "unused", discardLogger())
			stop, err := c.Listen(context.Background())
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
