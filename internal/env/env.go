// Package env holds variables as key and value pairs, and is the one place
// that splits and joins KEY=VALUE. A value may be a secret.Token, which prints
// as a mask wherever the variables are formatted, so a dry-run plan, a log
// line or an error that shows them never shows a credential.
package env

import (
	"fmt"
	"io"
	"strings"

	"github.com/zerlok/hermod/internal/secret"
)

// Value is one variable's value: plain text, or a token that prints masked.
type Value struct {
	text   string
	token  secret.Token
	secret bool
}

// Plain is a value that may be printed as it is.
func Plain(text string) Value { return Value{text: text} }

// Secret is a value that prints as the token's mask.
func Secret(token secret.Token) Value { return Value{token: token, secret: true} }

// IsSecret reports whether the value prints masked.
func (v Value) IsSecret() bool { return v.secret }

// Reveal returns the value as the program that runs with it reads it. Call it
// only where the value is handed to that program.
func (v Value) Reveal() string {
	if v.secret {
		return v.token.Reveal()
	}
	return v.text
}

// String is the plain text, or the token's mask.
func (v Value) String() string {
	if v.secret {
		return v.token.String()
	}
	return v.text
}

// GoString keeps a secret masked under %#v.
func (v Value) GoString() string { return fmt.Sprintf("%q", v.String()) }

// Format keeps a secret masked under every verb, %x and %q included, which
// would otherwise print the fields of the struct.
func (v Value) Format(f fmt.State, verb rune) {
	if verb == 'v' && f.Flag('#') {
		_, _ = io.WriteString(f, v.GoString())
		return
	}
	_, _ = fmt.Fprintf(f, fmt.FormatString(f, verb), v.String())
}

// MarshalText keeps a secret masked in a display encoding.
func (v Value) MarshalText() ([]byte, error) { return []byte(v.String()), nil }

// Pair is one variable.
type Pair struct {
	Key   string
	Value Value
}

// Vars is an ordered set of variables, each key at most once. Its zero value
// holds none. It is never changed in place: every operation returns new Vars.
type Vars struct {
	pairs []Pair
}

// New builds variables from pairs. A key given again takes the later value in
// the place of the first, as a process environment resolves a repeated key.
func New(pairs ...Pair) Vars { return Vars{}.With(pairs...) }

// Environ builds variables from a process environment, as KEY=VALUE entries.
// Every value is a secret: the environment holds credentials such as
// ANTHROPIC_API_KEY beside everything else, and nothing tells them apart. An
// entry without a key is not a variable and is left out.
func Environ(entries []string) Vars {
	pairs := make([]Pair, 0, len(entries))
	for _, e := range entries {
		key, value, ok := strings.Cut(e, "=")
		if !ok || key == "" {
			continue
		}
		pairs = append(pairs, Pair{Key: key, Value: Secret(secret.FromString(value))})
	}
	return New(pairs...)
}

// With is the variables with more after them; a key already present takes the
// new value in its place.
func (v Vars) With(more ...Pair) Vars {
	out := make([]Pair, len(v.pairs), len(v.pairs)+len(more))
	copy(out, v.pairs)
	for _, p := range more {
		if i := index(out, p.Key); i >= 0 {
			out[i].Value = p.Value
			continue
		}
		out = append(out, p)
	}
	return Vars{pairs: out}
}

// Get reads one variable.
func (v Vars) Get(key string) (Value, bool) {
	if i := index(v.pairs, key); i >= 0 {
		return v.pairs[i].Value, true
	}
	return Value{}, false
}

// Select is the variables whose key keep accepts, in order.
func (v Vars) Select(keep func(key string) bool) Vars {
	var out []Pair
	for _, p := range v.pairs {
		if keep(p.Key) {
			out = append(out, p)
		}
	}
	return Vars{pairs: out}
}

// Len is the number of variables.
func (v Vars) Len() int { return len(v.pairs) }

// Keys are the variables' keys, in order.
func (v Vars) Keys() []string {
	keys := make([]string, len(v.pairs))
	for i, p := range v.pairs {
		keys[i] = p.Key
	}
	return keys
}

// Pairs are the variables, in order, each value still masked when printed.
func (v Vars) Pairs() []Pair { return append([]Pair(nil), v.pairs...) }

// Entries renders the variables as KEY=VALUE entries, in order, each value
// written by render: Value.String keeps a secret masked, Value.Reveal does not,
// and a caller may quote either for the line it builds.
func (v Vars) Entries(render func(Value) string) []string {
	out := make([]string, len(v.pairs))
	for i, p := range v.pairs {
		out[i] = p.Key + "=" + render(p.Value)
	}
	return out
}

// Reveal renders the variables as KEY=VALUE entries for the command that runs
// with them. Call it only where they are handed to that command.
func (v Vars) Reveal() []string { return v.Entries(Value.Reveal) }

// String renders the variables as KEY=VALUE entries with every secret masked.
func (v Vars) String() string {
	return "env.Vars[" + strings.Join(v.Entries(Value.String), " ") + "]"
}

// GoString keeps every secret masked under %#v.
func (v Vars) GoString() string { return v.String() }

// Format keeps every secret masked under every verb.
func (v Vars) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, v.String()) }

// MarshalText keeps every secret masked in a display encoding.
func (v Vars) MarshalText() ([]byte, error) { return []byte(v.String()), nil }

func index(pairs []Pair, key string) int {
	for i, p := range pairs {
		if p.Key == key {
			return i
		}
	}
	return -1
}
