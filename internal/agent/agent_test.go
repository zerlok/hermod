package agent

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zerlok/hermod/internal/env"
	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/secret"
)

var errUnreadable = errors.New("unreadable answer")

// answers is a reading that looks its output up in a table, as an agent's
// declared reading turns a program's output into a value.
func answers[T any](table map[string]T) func(out []byte) (T, error) {
	return func(out []byte) (T, error) {
		v, ok := table[string(out)]
		if !ok {
			var zero T
			return zero, errUnreadable
		}
		return v, nil
	}
}

func TestProbeRead(t *testing.T) {
	read := answers(map[string]State{"running": Running, "stopped": Stopped, "bogus": State(7)})
	cases := []struct {
		name    string
		read    func([]byte) (State, error)
		out     string
		want    State
		wantErr bool
	}{
		{"running", read, "running", Running, false},
		{"stopped", read, "stopped", Stopped, false},
		{"unreadable answer", read, "garbage", Stopped, true},
		{"a state the contract does not have", read, "bogus", Stopped, true},
		{"no reading declared", nil, "running", Stopped, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewProbe("c1", []string{"probe"}, tc.read).Read([]byte(tc.out))
			if (err != nil) != tc.wantErr {
				t.Fatalf("Read() error = %v, want error %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("Read() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestListingRead(t *testing.T) {
	updated := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	inRoot := Conversation{ID: "c1", Dir: "/home/u/dev/api", State: Running, Title: "fix", Updated: updated}
	inSub := Conversation{ID: "c2", Dir: "/home/u/dev/api/web", State: Stopped}
	sibling := Conversation{ID: "c3", Dir: "/home/u/dev/api2", State: Stopped}
	noID := Conversation{Dir: "/home/u/dev/api", State: Stopped}
	relative := Conversation{ID: "c4", Dir: "dev/api", State: Stopped}
	unknownState := Conversation{ID: "c5", Dir: "/home/u/dev/api", State: State(9)}
	climbing := Conversation{ID: "c6", Dir: "/home/u/dev/api/../x", State: Stopped}
	trailing := Conversation{ID: "c7", Dir: "/home/u/dev/api/", State: Stopped}
	doubled := Conversation{ID: "c8", Dir: "/home/u/dev//api", State: Stopped}
	read := answers(map[string][]Conversation{
		"none":     nil,
		"root":     {inRoot},
		"nested":   {inRoot, inSub},
		"sibling":  {inRoot, sibling},
		"no id":    {noID},
		"relative": {relative},
		"state":    {unknownState},
		"climbing": {climbing},
		"trailing": {trailing},
		"doubled":  {doubled},
	})
	cases := []struct {
		name    string
		dir     string
		read    func([]byte) ([]Conversation, error)
		out     string
		want    []Conversation
		wantErr bool
	}{
		{"no conversation", "/home/u/dev/api", read, "none", nil, false},
		{"one in the directory", "/home/u/dev/api", read, "root", []Conversation{inRoot}, false},
		{"one under the directory", "/home/u/dev/api", read, "nested", []Conversation{inRoot, inSub}, false},
		{"one in a sibling directory", "/home/u/dev/api", read, "sibling", nil, true},
		{"one without an ID", "/home/u/dev/api", read, "no id", nil, true},
		{"one with a relative directory", "/home/u/dev/api", read, "relative", nil, true},
		{"one in an unknown state", "/home/u/dev/api", read, "state", nil, true},
		{"one climbing out of the directory", "/home/u/dev/api", read, "climbing", nil, true},
		{"one with a trailing slash", "/home/u/dev/api", read, "trailing", nil, true},
		{"one with a doubled slash", "/home/u/dev", read, "doubled", nil, true},
		{"the directory asked with a trailing slash", "/home/u/dev/api/", read, "nested", []Conversation{inRoot, inSub}, false},
		{"unreadable answer", "/home/u/dev/api", read, "garbage", nil, true},
		{"no reading declared", "/home/u/dev/api", nil, "root", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewListing(tc.dir, []string{"list"}, tc.read).Read([]byte(tc.out))
			if (err != nil) != tc.wantErr {
				t.Fatalf("Read() error = %v, want error %v", err, tc.wantErr)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Read() = %v, want %v", got, tc.want)
			}
		})
	}
}

// lookupAnswer is what a lookup's reading returns for one output.
type lookupAnswer struct {
	c     Conversation
	found bool
}

func TestLookupRead(t *testing.T) {
	running := Conversation{ID: "c1", Dir: "/home/u/dev/api", State: Running, Title: "fix"}
	stopped := Conversation{ID: "c1", Dir: "/home/u/dev/api/web", State: Stopped}
	other := Conversation{ID: "c2", Dir: "/home/u/dev/api", State: Running}
	relative := Conversation{ID: "c1", Dir: "api", State: Running}
	unclean := Conversation{ID: "c1", Dir: "/home/u/dev/api/../x/", State: Running}
	table := answers(map[string]lookupAnswer{
		"running":  {running, true},
		"stopped":  {stopped, true},
		"missing":  {Conversation{}, false},
		"other":    {other, true},
		"relative": {relative, true},
		"unclean":  {unclean, true},
	})
	read := func(out []byte) (Conversation, bool, error) {
		a, err := table(out)
		return a.c, a.found, err
	}
	cases := []struct {
		name      string
		read      func([]byte) (Conversation, bool, error)
		out       string
		want      Conversation
		wantFound bool
		wantErr   bool
	}{
		{"running", read, "running", running, true, false},
		{"stopped", read, "stopped", stopped, true, false},
		{"the program does not have it", read, "missing", Conversation{}, false, false},
		{"an answer for another conversation", read, "other", Conversation{}, false, true},
		{"a relative directory", read, "relative", Conversation{}, false, true},
		{"a directory that is not clean", read, "unclean", Conversation{}, false, true},
		{"unreadable answer", read, "garbage", Conversation{}, false, true},
		{"no reading declared", nil, "running", Conversation{}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found, err := NewLookup("c1", []string{"locate"}, tc.read).Read([]byte(tc.out))
			if (err != nil) != tc.wantErr {
				t.Fatalf("Read() error = %v, want error %v", err, tc.wantErr)
			}
			if found != tc.wantFound || got != tc.want {
				t.Fatalf("Read() = %v, %v, want %v, %v", got, found, tc.want, tc.wantFound)
			}
		})
	}
}

func TestQueryCommand(t *testing.T) {
	cases := []struct {
		name    string
		command []string
		got     func([]string) []string
	}{
		{"probe", []string{"claude", "agents", "--json"}, func(c []string) []string { return NewProbe("c1", c, nil).Command() }},
		{"listing", []string{"sh", "-c", "list"}, func(c []string) []string { return NewListing("/d", c, nil).Command() }},
		{"lookup", []string{"sh", "-c", "locate"}, func(c []string) []string { return NewLookup("c1", c, nil).Command() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			given := append([]string(nil), tc.command...)
			got := tc.got(given)
			given[0] = "changed"
			if !reflect.DeepEqual(got, tc.command) {
				t.Fatalf("Command() = %v, want %v", got, tc.command)
			}
		})
	}
}

// file is one file a Needs is built with.
type file struct {
	name    string
	content secret.Token
}

func TestNeeds(t *testing.T) {
	isClaude := func(key string) bool { return key == "CLAUDE_MODEL" }
	isAnthropic := func(key string) bool { return key == "ANTHROPIC_API_KEY" }
	environ := env.Environ([]string{"HOME=/home/me", "CLAUDE_MODEL=opus", "ANTHROPIC_API_KEY=sk", "TERM=xterm"})
	settings := secret.FromString(`{"hooks":{}}`)
	clientToken := secret.FromString("air-token")
	cases := []struct {
		name         string
		command      []string
		files        []file
		declared     []func(string) bool
		places       []pathmap.Place
		settings     settingsCase
		wantFiles    map[string]secret.Token
		wantVars     []string
		wantSettings bool
		wantHooks    bool
	}{
		{
			name:     "a command alone",
			command:  []string{"claude", "--session-id", "c1"},
			wantVars: []string{},
		},
		{
			name:    "everything",
			command: []string{"claude-agent-acp"},
			files: []file{
				{"mcp.json", secret.FromString(`{"servers":{}}`)},
				{"env", secret.FromString("A=1")},
			},
			declared: []func(string) bool{isClaude, isAnthropic},
			places:   []pathmap.Place{{Local: "/home/me/.claude/projects/a", Remote: "/home/u/.claude/projects/b"}},
			settings: settingsCase{
				form:     hookedSettings,
				file:     "settings.json",
				document: settings,
				address:  "127.0.0.1:6123",
				routes:   []string{"/air/hook?x=1", "/air/stop"},
				token:    clientToken,
			},
			wantFiles: map[string]secret.Token{
				"mcp.json": secret.FromString(`{"servers":{}}`),
				"env":      secret.FromString("A=1"),
			},
			wantVars:     []string{"CLAUDE_MODEL", "ANTHROPIC_API_KEY"},
			wantSettings: true,
			wantHooks:    true,
		},
		{
			name:         "settings whose hooks call nowhere locally",
			command:      []string{"claude"},
			settings:     settingsCase{form: plainSettings, file: "settings.json", document: settings},
			wantVars:     []string{},
			wantSettings: true,
		},
		{
			name:         "settings declared under an empty file name",
			command:      []string{"claude"},
			settings:     settingsCase{form: plainSettings, document: settings},
			wantVars:     []string{},
			wantSettings: true,
		},
		{
			name:         "a callback declared with an empty address",
			command:      []string{"claude"},
			settings:     settingsCase{form: hookedSettings, file: "settings.json", document: settings, token: clientToken},
			wantVars:     []string{},
			wantSettings: true,
			wantHooks:    true,
		},
		{
			name:    "a file given twice keeps the last",
			command: []string{"claude"},
			files: []file{
				{"mcp.json", secret.FromString("first")},
				{"mcp.json", secret.FromString("second")},
			},
			wantFiles: map[string]secret.Token{"mcp.json": secret.FromString("second")},
			wantVars:  []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := NewNeeds(tc.command)
			for _, f := range tc.files {
				n = n.WithFile(f.name, f.content)
			}
			for _, d := range tc.declared {
				n = n.WithVars(d)
			}
			for _, p := range tc.places {
				n = n.WithPlace(p)
			}
			n = tc.settings.declare(n)

			if got := n.Command(); !reflect.DeepEqual(got, tc.command) {
				t.Errorf("Command() = %v, want %v", got, tc.command)
			}
			if got := n.Files(); !sameFiles(got, tc.wantFiles) {
				t.Errorf("Files() = %v, want %v", got, tc.wantFiles)
			}
			if got := n.Vars(environ).Keys(); !reflect.DeepEqual(got, tc.wantVars) {
				t.Errorf("Vars() = %v, want %v", got, tc.wantVars)
			}
			if got := n.Places(); !reflect.DeepEqual(got, tc.places) {
				t.Errorf("Places() = %v, want %v", got, tc.places)
			}
			file, doc, ok := n.Settings()
			if file != tc.settings.file || !doc.Equal(tc.settings.document) || ok != tc.wantSettings {
				t.Errorf("Settings() = %q, %v, %v, want %q, %v, %v", file, doc, ok, tc.settings.file, tc.settings.document, tc.wantSettings)
			}
			address, routes, token, ok := n.Hooks()
			if address != tc.settings.address || !reflect.DeepEqual(routes, tc.settings.routes) || !token.Equal(tc.settings.token) || ok != tc.wantHooks {
				t.Errorf("Hooks() = %q, %v, %v, %v, want %q, %v, %v, %v", address, routes, token, ok,
					tc.settings.address, tc.settings.routes, tc.settings.token, tc.wantHooks)
			}
		})
	}
}

type settingsForm int

const (
	noSettings settingsForm = iota
	plainSettings
	hookedSettings
)

// settingsCase is the settings a TestNeeds case declares, in one of the forms
// the builder offers.
type settingsCase struct {
	form     settingsForm
	file     string
	document secret.Token
	address  string
	routes   []string
	token    secret.Token
}

func (c settingsCase) declare(n Needs) Needs {
	switch c.form {
	case noSettings:
		return n
	case plainSettings:
		return n.WithSettings(c.file, c.document)
	case hookedSettings:
		return n.WithHookedSettings(c.file, c.document, NewCallback(c.address, c.routes, c.token))
	}
	panic(fmt.Sprintf("settings form %d", c.form))
}

func sameFiles(got, want map[string]secret.Token) bool {
	if len(got) != len(want) {
		return false
	}
	for name, content := range want {
		if c, ok := got[name]; !ok || !c.Equal(content) {
			return false
		}
	}
	return true
}

// TestNeedsIsAValue checks that deriving a Needs, or changing what it handed
// out, leaves the Needs it came from as it was built.
func TestNeedsIsAValue(t *testing.T) {
	place := pathmap.Place{Local: "/l", Remote: "/r"}
	cases := []struct {
		name   string
		derive func(Needs) Needs
		read   func(Needs) any
	}{
		{"a file added", func(n Needs) Needs { return n.WithFile("new", secret.FromString("x")) }, func(n Needs) any { return len(n.Files()) }},
		{"a place added", func(n Needs) Needs { return n.WithPlace(pathmap.Place{Local: "/l2", Remote: "/r2"}) }, func(n Needs) any { return n.Places() }},
		{"a variable declared", func(n Needs) Needs { return n.WithVars(func(string) bool { return true }) }, func(n Needs) any {
			return n.Vars(env.Environ([]string{"A=1", "B=2"})).Keys()
		}},
		{"the command changed by its reader", func(n Needs) Needs { n.Command()[0] = "changed"; return n }, func(n Needs) any { return n.Command() }},
		{"the places changed by their reader", func(n Needs) Needs { n.Places()[0].Local = "/changed"; return n }, func(n Needs) any { return n.Places() }},
		{"the files changed by their reader", func(n Needs) Needs { n.Files()["f"] = secret.FromString("changed"); return n }, func(n Needs) any {
			return n.Files()["f"].Reveal()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := NewNeeds([]string{"cmd"}).
				WithFile("f", secret.FromString("content")).
				WithPlace(place).
				WithVars(func(key string) bool { return key == "A" })
			before := tc.read(base)
			tc.derive(base)
			if after := tc.read(base); !reflect.DeepEqual(after, before) {
				t.Fatalf("base changed from %v to %v", before, after)
			}
		})
	}
}

func TestCall(t *testing.T) {
	args := []string{"claude", "--resume", "c1"}
	conversation, err := NewConversation("c1", args)
	if err != nil {
		t.Fatalf("NewConversation() error: %v", err)
	}
	cases := []struct {
		name        string
		call        Call
		wantID      string
		wantIDOK    bool
		wantOneShot bool
	}{
		{"a conversation", conversation, "c1", true, false},
		{"a one-shot call", NewOneShot(args), "", false, true},
		{"a command the agent does not run", NewCommand(args), "", false, false},
		{"the zero call", Call{}, "", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, ok := tc.call.ID()
			if id != tc.wantID || ok != tc.wantIDOK {
				t.Errorf("ID() = %q, %v, want %q, %v", id, ok, tc.wantID, tc.wantIDOK)
			}
			if got := tc.call.OneShot(); got != tc.wantOneShot {
				t.Errorf("OneShot() = %v, want %v", got, tc.wantOneShot)
			}
		})
	}
}

func TestCallArgs(t *testing.T) {
	cases := []struct {
		name  string
		build func([]string) Call
	}{
		{"conversation", func(a []string) Call { c, _ := NewConversation("c1", a); return c }},
		{"one-shot", NewOneShot},
		{"command", NewCommand},
	}
	args := []string{"claude", "auth", "status"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			given := append([]string(nil), args...)
			call := tc.build(given)
			given[0] = "changed"
			call.Args()[1] = "changed"
			if got := call.Args(); !reflect.DeepEqual(got, args) {
				t.Fatalf("Args() = %v, want %v", got, args)
			}
		})
	}
}

func TestNewConversationNeedsAnID(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{"an ID", "c1", false},
		{"no ID", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewConversation(tc.id, nil); (err != nil) != tc.wantErr {
				t.Fatalf("NewConversation(%q) error = %v, want error %v", tc.id, err, tc.wantErr)
			}
		})
	}
}

func TestNewHandshake(t *testing.T) {
	cases := []struct {
		name         string
		version      int
		capabilities string
		wantErr      bool
	}{
		{"an object", 1, `{"loadSession":true}`, false},
		{"an empty object", 1, `{}`, false},
		{"null", 1, `null`, true},
		{"an array", 1, `[]`, true},
		{"not JSON", 1, `{`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, err := NewHandshake(tc.version, []byte(tc.capabilities))
			if (err != nil) != tc.wantErr {
				t.Fatalf("NewHandshake() error = %v, want error %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if h.Version() != tc.version || string(h.Capabilities()) != tc.capabilities {
				t.Fatalf("NewHandshake() = %d, %s, want %d, %s", h.Version(), h.Capabilities(), tc.version, tc.capabilities)
			}
		})
	}
}

func TestZeroHandshakeCapabilities(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"offers the empty object", `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(Handshake{}.Capabilities()); got != tc.want {
				t.Fatalf("Capabilities() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestHandshakeEqual(t *testing.T) {
	cases := []struct {
		name  string
		a, b  string
		va    int
		vb    int
		zeroA bool
		zeroB bool
		equal bool
	}{
		{"the zero handshake and itself", `{}`, `{}`, 0, 0, true, true, true},
		{"the zero handshake and no capabilities", `{}`, `{}`, 0, 0, true, false, true},
		{"the zero handshake and a capability", `{}`, `{"a":true}`, 0, 0, true, false, false},
		{"the same", `{"a":true,"b":{"c":1}}`, `{"a":true,"b":{"c":1}}`, 1, 1, false, false, true},
		{"spaced and ordered otherwise", `{"a":true,"b":{"c":1}}`, `{ "b": {"c": 1}, "a": true }`, 1, 1, false, false, true},
		{"another version", `{"a":true}`, `{"a":true}`, 1, 2, false, false, false},
		{"another capability", `{"a":true}`, `{"a":false}`, 1, 1, false, false, false},
		{"one capability more", `{"a":true}`, `{"a":true,"b":true}`, 1, 1, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, errA := NewHandshake(tc.va, []byte(tc.a))
			b, errB := NewHandshake(tc.vb, []byte(tc.b))
			if errA != nil || errB != nil {
				t.Fatalf("NewHandshake() errors: %v, %v", errA, errB)
			}
			if tc.zeroA {
				a = Handshake{}
			}
			if tc.zeroB {
				b = Handshake{}
			}
			if got := a.Equal(b); got != tc.equal {
				t.Fatalf("Equal() = %v, want %v", got, tc.equal)
			}
		})
	}
}

func TestNeedsPrintsNoSecret(t *testing.T) {
	cases := []struct {
		name   string
		needs  Needs
		secret string
	}{
		{"a file's content", NewNeeds([]string{"c"}).WithFile("mcp.json", secret.FromString("mcp-credential")), "mcp-credential"},
		{"the settings document", NewNeeds([]string{"c"}).WithSettings("settings.json", secret.FromString("settings-credential")), "settings-credential"},
		{"the hooks' token", NewNeeds([]string{"c"}).WithHookedSettings("settings.json", secret.FromString("{}"), NewCallback("127.0.0.1:1", nil, secret.FromString("hook-credential"))), "hook-credential"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
				if got := fmt.Sprintf(verb, tc.needs); strings.Contains(got, tc.secret) {
					t.Fatalf("%s printed %q", verb, got)
				}
			}
		})
	}
}
