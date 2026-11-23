package secret

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestTokenNeverPrints(t *testing.T) {
	const value = "0123456789abcdef"
	tok := FromString(value)
	cases := []struct {
		name   string
		render func() string
		hidden []string // what the rendering must not contain
		want   string   // what it must contain
	}{
		{"%v", func() string { return fmt.Sprintf("%v", tok) }, []string{value}, mask},
		{"%+v", func() string { return fmt.Sprintf("%+v", tok) }, []string{value}, mask},
		{"%#v", func() string { return fmt.Sprintf("%#v", tok) }, []string{value}, mask},
		{"%s", func() string { return fmt.Sprintf("%s", tok) }, []string{value}, mask},
		{"%q", func() string { return fmt.Sprintf("%q", tok) }, []string{value}, mask},
		{"%x", func() string { return fmt.Sprintf("%x", tok) }, []string{value, fmt.Sprintf("%x", value)}, mask},
		{"%X", func() string { return fmt.Sprintf("%X", tok) }, []string{value, fmt.Sprintf("%X", value)}, mask},
		{"pointer %v", func() string { return fmt.Sprintf("%v", &tok) }, []string{value}, mask},
		{"inside a struct %+v", func() string { return fmt.Sprintf("%+v", struct{ T Token }{tok}) }, []string{value}, mask},
		{"inside a struct %#v", func() string { return fmt.Sprintf("%#v", struct{ T Token }{tok}) }, []string{value}, mask},
		{"String", tok.String, []string{value}, mask},
		{"error wrap", func() string { return fmt.Errorf("bad %v", tok).Error() }, []string{value}, mask},
		{"JSON", func() string { b, _ := json.Marshal(struct{ T Token }{tok}); return string(b) }, []string{value}, mask},
		{"zero token", Token{}.String, nil, "secret.Token(none)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.render()
			for _, h := range tc.hidden {
				if strings.Contains(got, h) {
					t.Errorf("rendered %q reveals %q", got, h)
				}
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("rendered %q, want it to contain %q", got, tc.want)
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
