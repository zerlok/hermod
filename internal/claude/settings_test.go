package claude

import (
	"reflect"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/secret"
)

// The hook functions of TestRewriteHooks.
var (
	// keepAll keeps every hook as it is.
	keepAll = func(h agent.Hook) (agent.Hook, bool) { return h, true }
	// dropPort9000 leaves out every hook to port 9000.
	dropPort9000 = func(h agent.Hook) (agent.Hook, bool) { return h, !strings.Contains(h.URL, ":9000/") }
	// clearHeaders keeps every hook without its headers.
	clearHeaders = func(h agent.Hook) (agent.Hook, bool) { return agent.Hook{URL: h.URL}, true }
)

func TestRewriteHooks(t *testing.T) {
	cases := []struct {
		name     string
		doc      string
		rewrite  func(agent.Hook) (agent.Hook, bool)
		want     string
		wantSeen []string // the URL of each hook the function was given, in order
		wantErr  bool
	}{
		{
			name:     "every http hook rewritten, in the events' order, other fields kept as written",
			doc:      airHook[:len(airHook)-2] + `,"PostToolUse":[{"matcher":"Write|Edit","hooks":[{"type":"http","url":"http://localhost:63342/air/p","timeout":1.5}]}]}}`,
			rewrite:  aim,
			want:     `{"hooks":{"PostToolUse":[{"hooks":[{"headers":{"Authorization":"Bearer worker"},"timeout":1.5,"type":"http","url":"http://localhost:45001/air/p"}],"matcher":"Write|Edit"}],"Stop":[{"hooks":[{"headers":{"Authorization":"Bearer worker"},"type":"http","url":"http://localhost:45001/air/claude/hook?x=1"}]}]}}`,
			wantSeen: []string{"http://localhost:63342/air/p", "http://localhost:63342/air/claude/hook?x=1"},
		},
		{
			name:     "a hook left out, its group and event with it; command hooks kept",
			doc:      `{"hooks":{"PostToolUse":[{"hooks":[{"type":"http","url":"http://localhost:9000/mine"}]}],"Stop":[{"hooks":[{"type":"http","url":"https://hooks.example/x"},{"type":"http","url":"http://u:` + credential + `@127.0.0.1:9000/mine"},{"type":"command","command":"x"}]}]}}`,
			rewrite:  dropPort9000,
			want:     `{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"https://hooks.example/x"},{"command":"x","type":"command"}]}]}}`,
			wantSeen: []string{"http://localhost:9000/mine", "https://hooks.example/x", "http://u:" + credential + "@127.0.0.1:9000/mine"},
		},
		{
			name:     "every hook left out",
			doc:      `{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"http://localhost:9000/a"}]}]}}`,
			rewrite:  dropPort9000,
			want:     `{"hooks":{}}`,
			wantSeen: []string{"http://localhost:9000/a"},
		},
		{
			name:     "headers removed",
			doc:      airHook,
			rewrite:  clearHeaders,
			want:     `{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"http://localhost:63342/air/claude/hook?x=1"}]}]}}`,
			wantSeen: []string{"http://localhost:63342/air/claude/hook?x=1"},
		},
		{
			name:     "nothing changed: the document as given",
			doc:      `{ "hooks": {"Stop":[{"hooks":[{"type":"http","url":"http://localhost:1/h","headers":{"X":"y"}}]}]} }`,
			rewrite:  keepAll,
			want:     `{ "hooks": {"Stop":[{"hooks":[{"type":"http","url":"http://localhost:1/h","headers":{"X":"y"}}]}]} }`,
			wantSeen: []string{"http://localhost:1/h"},
		},
		{
			name:    "no function: the document as given",
			doc:     `{"hooks":`,
			rewrite: nil,
			want:    `{"hooks":`,
		},
		{name: "no hooks", doc: `{"model":"opus"}`, rewrite: aim, want: `{"model":"opus"}`},
		{name: "not JSON", doc: `{"hooks":`, rewrite: aim, wantErr: true},
		{name: "not an object", doc: `[1,2]`, rewrite: aim, wantErr: true},
		{name: "null", doc: `null`, rewrite: aim, wantErr: true},
		{
			name:    "a header that is not a string",
			doc:     `{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"http://localhost:1/h","headers":{"Authorization":1}}]}]}}`,
			rewrite: aim,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen []string
			rewrite := tc.rewrite
			if rewrite != nil {
				rewrite = func(h agent.Hook) (agent.Hook, bool) {
					seen = append(seen, h.URL)
					return tc.rewrite(h)
				}
			}
			got, err := rewriteHooks(secret.FromString(tc.doc), rewrite)
			if (err != nil) != tc.wantErr {
				t.Fatalf("rewriteHooks() error = %v, want error %v", err, tc.wantErr)
			}
			if got.Reveal() != tc.want {
				t.Errorf("rewriteHooks() =\n %s\nwant\n %s", got.Reveal(), tc.want)
			}
			if !tc.wantErr && !reflect.DeepEqual(seen, tc.wantSeen) {
				t.Errorf("the function was given %q, want %q", seen, tc.wantSeen)
			}
			if err != nil && strings.Contains(err.Error(), credential) {
				t.Errorf("rewriteHooks() error shows a credential: %v", err)
			}
		})
	}
}
