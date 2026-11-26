package env

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/secret"
)

func TestEnvironSplits(t *testing.T) {
	cases := []struct {
		name       string
		entries    []string
		wantKeys   []string
		wantReveal []string
	}{
		{"pairs in order", []string{"A=1", "B=2"}, []string{"A", "B"}, []string{"A=1", "B=2"}},
		{"value holding '='", []string{"OPTS=a=b"}, []string{"OPTS"}, []string{"OPTS=a=b"}},
		{"empty value", []string{"EMPTY="}, []string{"EMPTY"}, []string{"EMPTY="}},
		{"entry without '=' left out", []string{"noequals", "A=1"}, []string{"A"}, []string{"A=1"}},
		{"entry without a key left out", []string{"=C:", "A=1"}, []string{"A"}, []string{"A=1"}},
		{"repeated key takes the later value in the first place", []string{"A=1", "B=2", "A=3"}, []string{"A", "B"}, []string{"A=3", "B=2"}},
		{"none", nil, []string{}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := Environ(tc.entries)
			if got := v.Keys(); !reflect.DeepEqual(got, tc.wantKeys) {
				t.Errorf("Keys() = %q, want %q", got, tc.wantKeys)
			}
			if got := v.Reveal(); !reflect.DeepEqual(got, tc.wantReveal) {
				t.Errorf("Reveal() = %q, want %q", got, tc.wantReveal)
			}
		})
	}
}

func TestOperations(t *testing.T) {
	base := New(Pair{"A", Plain("1")}, Pair{"KEY", Secret(secret.FromString("k"))}, Pair{"B", Plain("2")})
	cases := []struct {
		name       string
		vars       Vars
		wantReveal []string
	}{
		{"new", base, []string{"A=1", "KEY=k", "B=2"}},
		{"with appends", base.With(Pair{"C", Plain("3")}), []string{"A=1", "KEY=k", "B=2", "C=3"}},
		{"with replaces in place", base.With(Pair{"A", Plain("9")}), []string{"A=9", "KEY=k", "B=2"}},
		{"select by key", base.Select(func(k string) bool { return k != "A" }), []string{"KEY=k", "B=2"}},
		{"select none", base.Select(func(string) bool { return false }), []string{}},
		{"base unchanged by with", func() Vars { base.With(Pair{"A", Plain("9")}); return base }(), []string{"A=1", "KEY=k", "B=2"}},
		{"zero", Vars{}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.vars.Reveal(); !reflect.DeepEqual(got, tc.wantReveal) {
				t.Errorf("Reveal() = %q, want %q", got, tc.wantReveal)
			}
			if got := tc.vars.Len(); got != len(tc.wantReveal) {
				t.Errorf("Len() = %d, want %d", got, len(tc.wantReveal))
			}
		})
	}
}

func TestEntries(t *testing.T) {
	vars := New(Pair{"A", Plain("x y")}, Pair{"KEY", Secret(secret.FromString("k"))})
	cases := []struct {
		name   string
		vars   Vars
		render func(Value) string
		want   []string
	}{
		{"masked", vars, Value.String, []string{"A=x y", "KEY=secret.Token(redacted)"}},
		{"revealed", vars, Value.Reveal, []string{"A=x y", "KEY=k"}},
		{"custom renderer", vars, func(v Value) string { return "<" + v.String() + ">" }, []string{"A=<x y>", "KEY=<secret.Token(redacted)>"}},
		{"zero", Vars{}, Value.String, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.vars.Entries(tc.render); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Entries() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGet(t *testing.T) {
	vars := New(Pair{"A", Plain("1")}, Pair{"KEY", Secret(secret.FromString("k"))})
	cases := []struct {
		name       string
		key        string
		wantOK     bool
		wantReveal string
		wantSecret bool
	}{
		{"plain", "A", true, "1", false},
		{"secret", "KEY", true, "k", true},
		{"absent", "NONE", false, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := vars.Get(tc.key)
			if ok != tc.wantOK || got.Reveal() != tc.wantReveal || got.IsSecret() != tc.wantSecret {
				t.Errorf("Get(%q) = %q (secret %v), %v, want %q (secret %v), %v",
					tc.key, got.Reveal(), got.IsSecret(), ok, tc.wantReveal, tc.wantSecret, tc.wantOK)
			}
		})
	}
}

func TestSecretsPrintMasked(t *testing.T) {
	const credential = "sk-ant-secret"
	vars := New(Pair{"ANTHROPIC_API_KEY", Secret(secret.FromString(credential))}, Pair{"A", Plain("1")})
	value, _ := vars.Get("ANTHROPIC_API_KEY")
	plain, _ := vars.Get("A")
	cases := []struct {
		name   string
		render func() string
		hidden string // "" when nothing must be hidden
		want   string
	}{
		{"vars %v", func() string { return fmt.Sprintf("%v", vars) }, credential, "env.Vars[ANTHROPIC_API_KEY=secret.Token(redacted) A=1]"},
		{"vars %#v", func() string { return fmt.Sprintf("%#v", vars) }, credential, "env.Vars[ANTHROPIC_API_KEY=secret.Token(redacted) A=1]"},
		{"vars %q", func() string { return fmt.Sprintf("%q", vars) }, credential, "env.Vars[ANTHROPIC_API_KEY=secret.Token(redacted) A=1]"},
		{"vars in a struct %+v", func() string { return fmt.Sprintf("%+v", struct{ E Vars }{vars}) }, credential, "{E:env.Vars[ANTHROPIC_API_KEY=secret.Token(redacted) A=1]}"},
		{"vars JSON", func() string { b, _ := json.Marshal(struct{ E Vars }{vars}); return string(b) }, credential, `{"E":"env.Vars[ANTHROPIC_API_KEY=secret.Token(redacted) A=1]"}`},
		{"pairs %v", func() string { return fmt.Sprintf("%v", vars.Pairs()) }, credential, "[{ANTHROPIC_API_KEY secret.Token(redacted)} {A 1}]"},
		{"pairs %#v", func() string { return fmt.Sprintf("%#v", vars.Pairs()) }, credential, `[]env.Pair{env.Pair{Key:"ANTHROPIC_API_KEY", Value:"secret.Token(redacted)"}, env.Pair{Key:"A", Value:"1"}}`},
		{"secret %s", func() string { return fmt.Sprintf("%s", value) }, credential, "secret.Token(redacted)"},
		{"secret %q", func() string { return fmt.Sprintf("%q", value) }, credential, `"secret.Token(redacted)"`},
		{"secret %x", func() string { return fmt.Sprintf("%x", value) }, fmt.Sprintf("%x", credential), fmt.Sprintf("%x", "secret.Token(redacted)")},
		{"secret JSON", func() string { b, _ := json.Marshal(value); return string(b) }, credential, `"secret.Token(redacted)"`},
		{"plain %q", func() string { return fmt.Sprintf("%q", plain) }, "", `"1"`},
		{"plain %5s", func() string { return fmt.Sprintf("%5s", plain) }, "", "    1"},
		{"environ masks every value", func() string { return Environ([]string{"HOME=/home/u"}).String() }, "/home/u", "env.Vars[HOME=secret.Token(redacted)]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.render()
			if tc.hidden != "" && strings.Contains(got, tc.hidden) {
				t.Errorf("rendered %q reveals %q", got, tc.hidden)
			}
			if got != tc.want {
				t.Errorf("rendered %q, want %q", got, tc.want)
			}
		})
	}
}
