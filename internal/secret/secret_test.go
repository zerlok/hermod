package secret

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const value = "0123456789abcdef"

func TestTokenNeverPrints(t *testing.T) {
	tok := FromString(value)
	cases := []struct {
		name   string
		render func() string
	}{
		{"%v", func() string { return fmt.Sprintf("%v", tok) }},
		{"%+v", func() string { return fmt.Sprintf("%+v", tok) }},
		{"%#v", func() string { return fmt.Sprintf("%#v", tok) }},
		{"%s", func() string { return fmt.Sprintf("%s", tok) }},
		{"%q", func() string { return fmt.Sprintf("%q", tok) }},
		{"%x", func() string { return fmt.Sprintf("%x", tok) }},
		{"%X", func() string { return fmt.Sprintf("%X", tok) }},
		{"pointer %v", func() string { return fmt.Sprintf("%v", &tok) }},
		{"inside a struct %+v", func() string { return fmt.Sprintf("%+v", struct{ T Token }{tok}) }},
		{"inside a struct %#v", func() string { return fmt.Sprintf("%#v", struct{ T Token }{tok}) }},
		{"String", tok.String},
		{"error wrap", func() string { return fmt.Errorf("bad %v", tok).Error() }},
		{"JSON", func() string { b, _ := json.Marshal(struct{ T Token }{tok}); return string(b) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.render()
			if strings.Contains(got, value) || strings.Contains(got, fmt.Sprintf("%x", value)) {
				t.Errorf("rendered %q reveals the token", got)
			}
			if !strings.Contains(got, "secret.Token") {
				t.Errorf("rendered %q does not say a token is there", got)
			}
		})
	}
}

func TestNewToken(t *testing.T) {
	cases := []struct {
		name    string
		random  []byte
		want    string
		wantErr bool
	}{
		{"hex of 32 bytes", bytes.Repeat([]byte{0xab}, 32), strings.Repeat("ab", 32), false},
		{"extra randomness unused", bytes.Repeat([]byte{0x01}, 40), strings.Repeat("01", 32), false},
		{"short randomness fails", []byte{1, 2, 3}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok, err := NewToken(bytes.NewReader(tc.random))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tok.Reveal() != tc.want {
				t.Errorf("Reveal() = %q, want %q", tok.Reveal(), tc.want)
			}
			if tok.IsZero() != (tc.want == "") {
				t.Errorf("IsZero() = %v for %q", tok.IsZero(), tc.want)
			}
		})
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("no entropy") }

func TestNewTokenReaderFailure(t *testing.T) {
	cases := []struct {
		name string
	}{
		{"reader error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok, err := NewToken(failingReader{})
			if err == nil || !tok.IsZero() {
				t.Errorf("NewToken() = %v, %v, want no token and an error", tok, err)
			}
		})
	}
}

func TestTokenEqualAndZero(t *testing.T) {
	cases := []struct {
		name      string
		a, b      Token
		wantEqual bool
		wantZeroA bool
		wantPrint string
	}{
		{"same value", FromString("x1"), FromString("x1"), true, false, "secret.Token(redacted)"},
		{"different values", FromString("x1"), FromString("x2"), false, false, "secret.Token(redacted)"},
		{"zero against a value", Token{}, FromString("x1"), false, true, "secret.Token(none)"},
		{"zero against zero", Token{}, Token{}, true, true, "secret.Token(none)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.Equal(tc.b); got != tc.wantEqual {
				t.Errorf("Equal() = %v, want %v", got, tc.wantEqual)
			}
			if got := tc.a.IsZero(); got != tc.wantZeroA {
				t.Errorf("IsZero() = %v, want %v", got, tc.wantZeroA)
			}
			if got := tc.a.String(); got != tc.wantPrint {
				t.Errorf("String() = %q, want %q", got, tc.wantPrint)
			}
		})
	}
}

func TestEnvNeverPrints(t *testing.T) {
	const credential = "sk-ant-secret"
	env := NewEnv([]string{"ANTHROPIC_API_KEY=" + credential, "A=1"})
	cases := []struct {
		name   string
		render func() string
		want   string
	}{
		{"%v", func() string { return fmt.Sprintf("%v", env) }, "secret.Env(2 entries)"},
		{"%#v", func() string { return fmt.Sprintf("%#v", env) }, "secret.Env(2 entries)"},
		{"%q", func() string { return fmt.Sprintf("%q", env) }, "secret.Env(2 entries)"},
		{"inside a struct %+v", func() string { return fmt.Sprintf("%+v", struct{ E Env }{env}) }, "{E:secret.Env(2 entries)}"},
		{"JSON", func() string { b, _ := json.Marshal(struct{ E Env }{env}); return string(b) }, `{"E":"secret.Env(2 entries)"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.render(); got != tc.want {
				t.Errorf("rendered %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEnvOperations(t *testing.T) {
	cases := []struct {
		name string
		env  Env
		want []string
	}{
		{"reveal", NewEnv([]string{"A=1", "B=2"}), []string{"A=1", "B=2"}},
		{"with", NewEnv([]string{"A=1"}).With("B=2"), []string{"A=1", "B=2"}},
		{"filter by key", NewEnv([]string{"A=1", "B=2", "noequals"}).Filter(func(k, _ string) bool { return k != "A" }), []string{"B=2"}},
		{"empty", NewEnv(nil), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.env.Reveal(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Reveal() = %q, want %q", got, tc.want)
			}
		})
	}
}
