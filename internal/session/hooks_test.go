package session

import (
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/relay"
	"github.com/zerlok/hermod/internal/secret"
)

func TestHookFilter(t *testing.T) {
	paths, err := pathmap.New(pathmap.Place{Local: api, Remote: apiR})
	if err != nil {
		t.Fatal(err)
	}
	header := func(pairs ...string) http.Header {
		h := http.Header{}
		for i := 0; i+1 < len(pairs); i += 2 {
			h.Set(pairs[i], pairs[i+1])
		}
		return h
	}
	cases := []struct {
		name       string
		client     secret.Token
		call       relay.Call
		wantStatus int // the refusal's; 0 passes the call
		want       relay.Call
		answer     string
		wantAnswer string
	}{
		{
			"the worker's token on a route, its body mapped",
			secret.FromString("Bearer client"),
			relay.Call{Method: http.MethodPost, URI: "/hook?x=1", Header: header("Authorization", "Bearer worker", "Content-Type", "application/json", "X-Remote", "r"), Body: []byte(`{"file":"` + apiR + `/a.go"}`)},
			0,
			relay.Call{Method: http.MethodPost, URI: "/hook?x=1", Header: header("Authorization", "Bearer client", "Content-Type", "application/json"), Body: []byte(`{"file":"` + api + `/a.go"}`)},
			`{"file":"` + api + `/b.go"}`, `{"file":"` + apiR + `/b.go"}`,
		},
		{
			"a client that expects no token, a body that is no JSON",
			secret.Token{},
			relay.Call{Method: http.MethodPost, URI: "/hook", Header: header("Authorization", "Bearer worker"), Body: []byte("plain " + apiR)},
			0,
			relay.Call{Method: http.MethodPost, URI: "/hook", Header: header(), Body: []byte("plain " + apiR)},
			"ok", "ok",
		},
		{"a wrong token", secret.FromString("Bearer client"),
			relay.Call{Method: http.MethodPost, URI: "/hook", Header: header("Authorization", "Bearer other")},
			http.StatusUnauthorized, relay.Call{}, "", ""},
		{"no token", secret.FromString("Bearer client"),
			relay.Call{Method: http.MethodPost, URI: "/hook", Header: header()},
			http.StatusUnauthorized, relay.Call{}, "", ""},
		{"the client's own token", secret.FromString("Bearer client"),
			relay.Call{Method: http.MethodPost, URI: "/hook", Header: header("Authorization", "Bearer client")},
			http.StatusUnauthorized, relay.Call{}, "", ""},
		{"a route the agent did not name", secret.FromString("Bearer client"),
			relay.Call{Method: http.MethodPost, URI: "/admin", Header: header("Authorization", "Bearer worker")},
			http.StatusNotFound, relay.Call{}, "", ""},
		{"another query on a named path", secret.FromString("Bearer client"),
			relay.Call{Method: http.MethodPost, URI: "/hook?x=2", Header: header("Authorization", "Bearer worker")},
			http.StatusNotFound, relay.Call{}, "", ""},
		{"another method", secret.FromString("Bearer client"),
			relay.Call{Method: http.MethodGet, URI: "/hook", Header: header("Authorization", "Bearer worker")},
			http.StatusMethodNotAllowed, relay.Call{}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newHookFilter(secret.FromString("worker"), []string{"/hook", "/hook?x=1"}, tc.client, paths)
			got, err := f.Pass(tc.call)
			var refusal *relay.Refusal
			switch {
			case tc.wantStatus != 0 && (!errors.As(err, &refusal) || refusal.Status != tc.wantStatus):
				t.Fatalf("Pass: %v, want a refusal with %d", err, tc.wantStatus)
			case tc.wantStatus != 0:
				return
			case err != nil:
				t.Fatalf("Pass: %v", err)
			}
			if got.Method != tc.want.Method || got.URI != tc.want.URI || !reflect.DeepEqual(got.Header, tc.want.Header) || string(got.Body) != string(tc.want.Body) {
				t.Errorf("Pass = %s %s %v %s, want %s %s %v %s", got.Method, got.URI, got.Header, got.Body, tc.want.Method, tc.want.URI, tc.want.Header, tc.want.Body)
			}
			if answer := string(got.Answer([]byte(tc.answer))); answer != tc.wantAnswer {
				t.Errorf("answer %s, want %s", answer, tc.wantAnswer)
			}
		})
	}
}

// TestAimerPicksOnce checks that the worker's port and token are asked for
// once however many hooks go to the client, and that after a failed pick no
// hook is aimed and the record fails with that pick's error.
func TestAimerPicksOnce(t *testing.T) {
	errPick := errors.New("no free port")
	client := endpoint{address: "127.0.0.1:1", authorization: airHook.Headers["Authorization"]}
	cases := []struct {
		name      string
		pickErr   error
		hooks     []agent.Hook
		wantPicks int
		wantAimed []bool
		wantErr   error
	}{
		{"picked once for every hook to the client", nil, []agent.Hook{airHook, airOther, airHook}, 1, []bool{true, true, true}, nil},
		{"a failed pick aims no later hook", errPick, []agent.Hook{airHook, airOther, airHook}, 1, []bool{false, false, false}, errPick},
		{"no hook to the client picks nothing", nil, []agent.Hook{webHook}, 0, []bool{true}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			picks := 0
			a := newAimer(Request{}, &client, func() (workerHook, error) {
				picks++
				if tc.pickErr != nil {
					return workerHook{}, tc.pickErr
				}
				return workerHook{port: 10000, token: secret.FromString(zeros)}, nil
			})
			var aimed []bool
			for _, h := range tc.hooks {
				_, ok := a.aim(h)
				aimed = append(aimed, ok)
			}
			if picks != tc.wantPicks {
				t.Errorf("picked %d times, want %d", picks, tc.wantPicks)
			}
			if !reflect.DeepEqual(aimed, tc.wantAimed) {
				t.Errorf("aimed %v, want %v", aimed, tc.wantAimed)
			}
			if _, err := a.record(); !errors.Is(err, tc.wantErr) {
				t.Errorf("record() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
