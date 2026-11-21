// Package secret holds credentials as opaque types: a Token, and an Env that
// may hold some. Each prints as a mask in every form fmt knows, so a log line,
// an error or a dry-run plan that formats one shows that it is there and never
// what it holds. Reveal is the one way to the value, so every use of a
// credential can be found by name.
package secret

import (
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

// Token is a credential: a hook token, Air's or a session's. Its zero value is
// no token.
type Token struct {
	value string
}

// mask is every printed form of a token that holds a value.
const mask = "secret.Token(redacted)"

// tokenBytes is the entropy of a generated token: 256 bits.
const tokenBytes = 32

// NewToken generates a token from random, which is crypto/rand.Reader outside
// tests. It is hexadecimal, so it is safe in a file, a header and a JSON string.
func NewToken(random io.Reader) (Token, error) {
	b := make([]byte, tokenBytes)
	if _, err := io.ReadFull(random, b); err != nil {
		return Token{}, fmt.Errorf("generate token: %w", err)
	}
	return Token{value: hex.EncodeToString(b)}, nil
}

// FromString wraps a credential read from a file, a header or a settings
// document, at the point it enters.
func FromString(s string) Token { return Token{value: s} }

// Reveal returns the credential. Call it only where the value is sent to
// whatever checks it: a header, a file written over ssh stdin, a comparison.
func (t Token) Reveal() string { return t.value }

// IsZero reports whether the token holds no value.
func (t Token) IsZero() bool { return t.value == "" }

// Equal compares two tokens in constant time, so a wrong guess learns nothing
// from how long the check took.
func (t Token) Equal(other Token) bool {
	return subtle.ConstantTimeCompare([]byte(t.value), []byte(other.value)) == 1
}

// String masks the token.
func (t Token) String() string {
	if t.IsZero() {
		return "secret.Token(none)"
	}
	return mask
}

// GoString masks the token under %#v.
func (t Token) GoString() string { return t.String() }

// Format masks the token under every verb, %x and %q included, which would
// otherwise print the fields of the struct.
func (t Token) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, t.String()) }

// MarshalText masks the token, so encoding a value that holds one for display
// (JSON, a log record) reveals nothing; a wire format calls Reveal instead.
func (t Token) MarshalText() ([]byte, error) { return []byte(t.String()), nil }

// Env is an environment, as KEY=VALUE entries, that may hold credentials such
// as ANTHROPIC_API_KEY. It prints as a mask that tells only how many entries it
// holds; Reveal is the one way to them.
type Env struct {
	entries []string
}

// NewEnv wraps an environment at the point it enters, copying the entries.
func NewEnv(entries []string) Env { return Env{entries: append([]string(nil), entries...)} }

// Reveal returns a copy of the entries. Call it only where they are handed to
// what runs with them: a file written over ssh stdin.
func (e Env) Reveal() []string { return append([]string(nil), e.entries...) }

// With is the environment with more entries after its own.
func (e Env) With(more ...string) Env {
	return Env{entries: append(e.Reveal(), more...)}
}

// Filter is the environment with only the entries keep accepts, in order. keep
// sees each entry's key and value; the result stays masked.
func (e Env) Filter(keep func(key, value string) bool) Env {
	var out []string
	for _, entry := range e.entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok && keep(key, value) {
			out = append(out, entry)
		}
	}
	return Env{entries: out}
}

// String masks the environment.
func (e Env) String() string { return fmt.Sprintf("secret.Env(%d entries)", len(e.entries)) }

// GoString masks the environment under %#v.
func (e Env) GoString() string { return e.String() }

// Format masks the environment under every verb.
func (e Env) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, e.String()) }

// MarshalText masks the environment for display encodings.
func (e Env) MarshalText() ([]byte, error) { return []byte(e.String()), nil }
