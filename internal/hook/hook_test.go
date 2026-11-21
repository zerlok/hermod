package hook

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/secret"
)

const (
	sessionToken = "session-0123"
	airAuth      = "Bearer air-run-token"
	hookURI      = "/air/claude/hook?q=1" // the hook the session's settings route
)

// received is one request as the fake Air saw it.
type received struct {
	path, query, auth, contentType, body string
}

// fakeAir records the requests it gets and answers each with a canned reply.
func fakeAir(t *testing.T, status int, reply string) (*httptest.Server, *[]received) {
	t.Helper()
	var got []received
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = append(got, received{r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), string(body)})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func roots(t *testing.T) pathmap.Roots {
	t.Helper()
	r, err := pathmap.New(pathmap.Pair{Local: "/home/me/dev/api", Sandbox: "/home/u/dev/api"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestHandler(t *testing.T) {
	cases := []struct {
		name       string
		method     string
		uri        string // the request URI Claude posts to
		auth       string // the Authorization header Claude sends
		airAuth    string // Air's header value; "" sends none
		body       string
		airStatus  int
		airReply   string
		wantStatus int
		wantAir    []received // what reached Air
		wantReply  string     // what Claude got back; "" not checked
		wantLog    string     // a substring of the proxy's log; "" for none
	}{
		{
			"forwarded with local paths and Air's token",
			http.MethodPost, hookURI,
			"Bearer " + sessionToken, airAuth,
			`{"hook_event_name":"PostToolUse","cwd":"/home/u/dev/api","tool_input":{"file_path":"/home/u/dev/api/main.go"}}`,
			200, `{"decision":"block","reason":"see /home/me/dev/api/main.go"}`,
			200,
			[]received{{"/air/claude/hook", "q=1", airAuth, "application/json", `{"hook_event_name":"PostToolUse","cwd":"/home/me/dev/api","tool_input":{"file_path":"/home/me/dev/api/main.go"}}`}},
			`{"decision":"block","reason":"see /home/me/dev/api/main.go"}`, "",
		},
		{
			"reply paths mapped back",
			http.MethodPost, hookURI,
			"Bearer " + sessionToken, airAuth,
			`{}`,
			200, `{"path":"/home/me/dev/api/x"}`,
			200,
			[]received{{"/air/claude/hook", "q=1", airAuth, "application/json", `{}`}},
			`{"path":"/home/u/dev/api/x"}`, "",
		},
		{
			"Air's status passed back",
			http.MethodPost, hookURI,
			"Bearer " + sessionToken, airAuth,
			`not json /home/u/dev/api`,
			500, `oops`,
			500,
			[]received{{"/air/claude/hook", "q=1", airAuth, "application/json", `not json /home/u/dev/api`}},
			`oops`, "",
		},
		{
			"no Air token, no header",
			http.MethodPost, hookURI,
			"Bearer " + sessionToken, "",
			`{}`,
			200, `{}`,
			200,
			[]received{{"/air/claude/hook", "q=1", "", "application/json", `{}`}},
			`{}`, "",
		},
		{
			"wrong token refused",
			http.MethodPost, hookURI,
			"Bearer guess", airAuth, `{}`, 200, `{}`,
			http.StatusUnauthorized, nil, "", "refused",
		},
		{
			"Air's own token refused",
			http.MethodPost, hookURI,
			airAuth, airAuth, `{}`, 200, `{}`,
			http.StatusUnauthorized, nil, "", "refused",
		},
		{
			"no token refused",
			http.MethodPost, hookURI,
			"", airAuth, `{}`, 200, `{}`,
			http.StatusUnauthorized, nil, "", "refused",
		},
		{
			"token without the scheme refused",
			http.MethodPost, hookURI,
			sessionToken, airAuth, `{}`, 200, `{}`,
			http.StatusUnauthorized, nil, "", "refused",
		},
		{
			"another path on Air's endpoint refused",
			http.MethodPost, "/api/internal/exec?x=1",
			"Bearer " + sessionToken, airAuth, `{}`, 200, `{}`,
			http.StatusNotFound, nil, "", "not one of the session's hooks",
		},
		{
			"the hook's path with another query refused",
			http.MethodPost, "/air/claude/hook?q=2",
			"Bearer " + sessionToken, airAuth, `{}`, 200, `{}`,
			http.StatusNotFound, nil, "", "not one of the session's hooks",
		},
		{
			"a dot-dot path refused",
			http.MethodPost, "/%2e%2e/other",
			"Bearer " + sessionToken, airAuth, `{}`, 200, `{}`,
			http.StatusNotFound, nil, "", "not one of the session's hooks",
		},
		{
			"another method on the hook's path refused",
			http.MethodDelete, hookURI,
			"Bearer " + sessionToken, airAuth, `{}`, 200, `{}`,
			http.StatusMethodNotAllowed, nil, "", "not a hook's method",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			air, got := fakeAir(t, tc.airStatus, tc.airReply)
			target, _ := url.Parse(air.URL)
			var logged bytes.Buffer
			h := NewHandler(Config{
				Session:       secret.FromString(sessionToken),
				Target:        target,
				Routes:        []string{hookURI},
				Authorization: secret.FromString(tc.airAuth),
				ToLocal:       roots(t).ToLocal(),
				ToSandbox:     roots(t).ToSandbox(),
				Log:           log.New(&logged, "", 0),
			})
			req := httptest.NewRequest(tc.method, "http://localhost:45001"+tc.uri, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if fmt.Sprint(*got) != fmt.Sprint(tc.wantAir) {
				t.Errorf("Air got %+v, want %+v", *got, tc.wantAir)
			}
			if tc.wantReply != "" && rec.Body.String() != tc.wantReply {
				t.Errorf("reply = %q, want %q", rec.Body.String(), tc.wantReply)
			}
			if !strings.Contains(logged.String(), tc.wantLog) || (tc.wantLog == "" && logged.Len() > 0) {
				t.Errorf("log = %q, want %q", logged.String(), tc.wantLog)
			}
			if strings.Contains(logged.String(), sessionToken) || strings.Contains(logged.String(), "air-run-token") {
				t.Errorf("log reveals a token: %q", logged.String())
			}
		})
	}
}

func TestHandlerAirUnreachable(t *testing.T) {
	cases := []struct {
		name       string
		wantStatus int
		wantLog    string
	}{
		{"Air gone", http.StatusBadGateway, "Air unreachable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			air := httptest.NewServer(http.NotFoundHandler())
			target, _ := url.Parse(air.URL)
			air.Close()
			var logged bytes.Buffer
			h := NewHandler(Config{Session: secret.FromString(sessionToken), Target: target, Routes: []string{"/h"}, Log: log.New(&logged, "", 0)})
			req := httptest.NewRequest(http.MethodPost, "http://localhost:45001/h", strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Bearer "+sessionToken)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus || !strings.Contains(logged.String(), tc.wantLog) {
				t.Errorf("status %d, log %q, want %d, %q", rec.Code, logged.String(), tc.wantStatus, tc.wantLog)
			}
		})
	}
}

// TestProxyServes runs the proxy on a loopback port, as the reverse forward
// reaches it, and stops it.
func TestProxyServes(t *testing.T) {
	cases := []struct {
		name       string
		auth       string
		wantStatus int
		wantAir    int // requests that reached Air
	}{
		{"session token forwarded", "Bearer " + sessionToken, 204, 1},
		{"other token refused", "Bearer x", http.StatusUnauthorized, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			air, got := fakeAir(t, 204, "")
			target, _ := url.Parse(air.URL)
			p, err := Start(NewHandler(Config{Session: secret.FromString(sessionToken), Target: target, Routes: []string{"/h"}}))
			if err != nil {
				t.Fatalf("Start() error: %v", err)
			}
			req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/h", p.Port()), strings.NewReader("{}"))
			req.Header.Set("Authorization", tc.auth)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("post: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != tc.wantStatus || len(*got) != tc.wantAir {
				t.Errorf("status %d, Air got %d, want %d, %d", resp.StatusCode, len(*got), tc.wantStatus, tc.wantAir)
			}
			if err := p.Close(context.Background()); err != nil {
				t.Errorf("Close() error: %v", err)
			}
			if _, err := http.DefaultClient.Do(req); err == nil {
				t.Error("the proxy still serves after Close")
			}
		})
	}
}
