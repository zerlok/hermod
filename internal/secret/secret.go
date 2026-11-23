// Package secret holds a credential as an opaque Token. It prints as a mask in
// every form fmt knows, so a log line, an error or a dry-run plan that formats
// one shows that it is there and never what it holds. Reveal is the one way to
// the value, so every use of a credential can be found by name.
package secret

import (
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
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
