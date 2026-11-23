package claude

import (
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/secret"
)

const (
	airToken    = "Bearer air-run-" + credential
	workerToken = "worker-token-0123"
)

func TestHooks(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		port int
		want string
	}{
		{
			"Air's hooks aimed at the worker's port and token",
			`{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"http://localhost:63342/air/claude/hook","headers":{"Authorization":"` + airToken + `"},"timeout":1}]}],` +
				`"PostToolUse":[{"matcher":"Write|Edit","hooks":[{"type":"http","url":"http://localhost:63342/air/claude/hook?x=1","headers":{"authorization":"` + airToken + `","X-Other":"kept"},"timeout":1.5}]}]}}`,
			45001,
			`{"hooks":{"PostToolUse":[{"hooks":[{"headers":{"Authorization":"Bearer ` + workerToken + `","X-Other":"kept"},"timeout":1.5,"type":"http","url":"http://localhost:45001/air/claude/hook?x=1"}],"matcher":"Write|Edit"}],` +
				`"Stop":[{"hooks":[{"headers":{"Authorization":"Bearer ` + workerToken + `"},"timeout":1,"type":"http","url":"http://localhost:45001/air/claude/hook"}]}]}}`,
		},
		{
			"a hook without a header gets the worker's",
			`{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"http://[::1]:7000/air/h"}]}]}}`,
			45001,
			`{"hooks":{"Stop":[{"hooks":[{"headers":{"Authorization":"Bearer ` + workerToken + `"},"type":"http","url":"http://localhost:45001/air/h"}]}]}}`,
		},
		{
			"remote and command hooks left as they are",
			`{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"https://hooks.example/x"},{"type":"command","command":"/home/u/dev/api/hook.sh"}]}]}}`,
			45001,
			`{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"https://hooks.example/x"},{"type":"command","command":"/home/u/dev/api/hook.sh"}]}]}}`,
		},
		{
			"no hooks",
			`{"model":"opus"}`,
			45001,
			`{"model":"opus"}`,
		},
		{
			"Air's endpoint carried, another local one left out",
			`{"hooks":{"PostToolUse":[{"hooks":[{"type":"http","url":"http://localhost:9000/mine"}]}],"Stop":[{"hooks":[{"type":"http","url":"http://localhost:63342/air/claude/hook"},{"type":"http","url":"http://u:p@127.0.0.1:9000/mine"},{"type":"command","command":"x"}]}]}}`,
			45001,
			`{"hooks":{"Stop":[{"hooks":[{"headers":{"Authorization":"Bearer ` + workerToken + `"},"type":"http","url":"http://localhost:45001/air/claude/hook"},{"command":"x","type":"command"}]}]}}`,
		},
		{
			"without Air's, every loopback hook left out",
			`{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"http://localhost:2/h"}]}],"PostToolUse":[{"hooks":[{"type":"http","url":"http://localhost:1/h"}]}]}}`,
			45001,
			`{"hooks":{}}`,
		},
		{
			"Air's endpoint on its scheme's default port",
			`{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"http://localhost/air/claude/hook"}]}]}}`,
			45001,
			`{"hooks":{"Stop":[{"hooks":[{"headers":{"Authorization":"Bearer ` + workerToken + `"},"type":"http","url":"http://localhost:45001/air/claude/hook"}]}]}}`,
		},
		{
			"an https loopback hook left out, since the relay speaks plain http",
			`{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"https://localhost:63342/air/claude/hook"},{"type":"command","command":"x"}]}]}}`,
			45001,
			`{"hooks":{"Stop":[{"hooks":[{"command":"x","type":"command"}]}]}}`,
		},
		{
			"one endpoint with a second token: that hook left out",
			`{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"http://localhost:1/air/h","headers":{"Authorization":"a"}},{"type":"http","url":"http://localhost:1/air/h","headers":{"Authorization":"b"}}]}]}}`,
			45001,
			`{"hooks":{"Stop":[{"hooks":[{"headers":{"Authorization":"Bearer ` + workerToken + `"},"type":"http","url":"http://localhost:45001/air/h"}]}]}}`,
		},
		{
			"a left-out hook's credentials in its user info and query gone with it",
			`{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"http://localhost:63342/air/claude/hook"},{"type":"http","url":"http://user:` + credential + `@127.0.0.1:7777/x?tok=` + credential + `"}]}]}}`,
			45001,
			`{"hooks":{"Stop":[{"hooks":[{"headers":{"Authorization":"Bearer ` + workerToken + `"},"type":"http","url":"http://localhost:45001/air/claude/hook"}]}]}}`,
		},
		{"not JSON", `{"hooks":`, 45001, ""},
		{"not an object", `[1,2]`, 45001, ""},
		{"null", `null`, 45001, ""},
		{
			"a header that is not a string",
			`{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"http://localhost:1/h","headers":{"Authorization":1}}]}]}}`,
			45001, "",
		},
		{"a port out of range", `{"model":"opus"}`, 70000, ""},
		{"no port", `{"model":"opus"}`, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Agent{}.Hooks(secret.FromString(tc.doc), tc.port, secret.FromString(workerToken))
			if (err != nil) != (tc.want == "") {
				t.Fatalf("Hooks() error = %v, want error %v", err, tc.want == "")
			}
			if got.Reveal() != tc.want {
				t.Errorf("Hooks() =\n %s\nwant\n %s", got.Reveal(), tc.want)
			}
			if strings.Contains(got.Reveal(), credential) {
				t.Error("a local credential reached the remote's settings")
			}
			if err != nil && strings.Contains(err.Error(), credential) {
				t.Errorf("Hooks() error shows a credential: %v", err)
			}
		})
	}
}
