// Package pathmap translates paths between the local machine and the sandbox.
// A set of (local root, sandbox root) pairs maps a path by the pair whose root is
// its longest prefix at a path-segment boundary, and the same mapping applies to
// every string inside a JSON value. It knows nothing about the programs whose
// arguments, settings or streams carry the paths.
package pathmap

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

// Pair is one local root and the sandbox root it is mirrored to. Roots are
// absolute, slash-separated, and taken as written apart from any trailing slash.
// A relative root would match any string that merely starts with its first
// segment, such as a branch name or a word of prose in a stream, whereas the
// paths that programs write into arguments, hook bodies and streams are
// absolute.
type Pair struct {
	Local   string
	Sandbox string
}

// Roots is a validated set of pairs. Its zero value holds no pair and maps
// nothing.
type Roots struct {
	pairs []Pair
}

// New validates the pairs into Roots. A root must be absolute, and no two pairs
// may share a root on either side, because a path under a shared root would map
// to two places in one direction.
func New(pairs ...Pair) (Roots, error) {
	seenLocal := map[string]bool{}
	seenSandbox := map[string]bool{}
	out := make([]Pair, 0, len(pairs))
	for _, p := range pairs {
		local, sandbox := trimRoot(p.Local), trimRoot(p.Sandbox)
		switch {
		case !strings.HasPrefix(local, "/") || !strings.HasPrefix(sandbox, "/"):
			return Roots{}, fmt.Errorf("pathmap: pair %q → %q has a root that is not absolute", p.Local, p.Sandbox)
		case seenLocal[local]:
			return Roots{}, fmt.Errorf("pathmap: local root %q is given twice", p.Local)
		case seenSandbox[sandbox]:
			return Roots{}, fmt.Errorf("pathmap: sandbox root %q is given twice", p.Sandbox)
		}
		seenLocal[local], seenSandbox[sandbox] = true, true
		out = append(out, Pair{Local: local, Sandbox: sandbox})
	}
	return Roots{pairs: out}, nil
}

// ToSandbox maps local paths to sandbox paths.
func (r Roots) ToSandbox() Map {
	rules := make([]rule, len(r.pairs))
	for i, p := range r.pairs {
		rules[i] = rule{from: p.Local, to: p.Sandbox}
	}
	return newMap(rules)
}

// ToLocal maps sandbox paths to local paths.
func (r Roots) ToLocal() Map {
	rules := make([]rule, len(r.pairs))
	for i, p := range r.pairs {
		rules[i] = rule{from: p.Sandbox, to: p.Local}
	}
	return newMap(rules)
}

// Map translates paths in one direction. Its zero value maps nothing.
type Map struct {
	rules []rule // longest root first, so the first match is the longest prefix
}

type rule struct{ from, to string }

func newMap(rules []rule) Map {
	sort.SliceStable(rules, func(i, j int) bool { return len(rules[i].from) > len(rules[j].from) })
	return Map{rules: rules}
}

// Path maps one path. A path under no root is returned unchanged; the part below
// the root is kept as written.
func (m Map) Path(p string) string {
	for _, r := range m.rules {
		if rest, ok := under(p, r.from); ok {
			return join(r.to, rest)
		}
	}
	return p
}

// JSON maps every string value inside one JSON document, at any depth. Object
// keys, every other value, the order of members and the document's whitespace
// are left as they are; only a string that maps to a different path is
// re-encoded.
func (m Map) JSON(doc []byte) ([]byte, error) {
	if !json.Valid(doc) {
		return nil, errors.New("pathmap: not a JSON document")
	}
	var out bytes.Buffer
	out.Grow(len(doc))
	for i := 0; i < len(doc); {
		if doc[i] != '"' {
			out.WriteByte(doc[i])
			i++
			continue
		}
		end := stringEnd(doc, i)
		literal := doc[i:end]
		i = end
		if isKey(doc, end) {
			out.Write(literal)
			continue
		}
		mapped, err := m.literal(literal)
		if err != nil {
			return nil, err
		}
		out.Write(mapped)
	}
	return out.Bytes(), nil
}

// Line maps one newline-delimited JSON line, as a stream-json channel carries
// it. The line's ending, "\n" or "\r\n", or its absence on a final partial line,
// is kept; a blank line is returned unchanged.
func (m Map) Line(line []byte) ([]byte, error) {
	body := bytes.TrimRight(line, "\r\n")
	if len(bytes.TrimSpace(body)) == 0 {
		return line, nil
	}
	mapped, err := m.JSON(body)
	if err != nil {
		return nil, err
	}
	return append(mapped, line[len(body):]...), nil
}

// literal maps one encoded JSON string, returning it byte for byte when its
// path does not change, so escapes the producer chose are kept.
func (m Map) literal(lit []byte) ([]byte, error) {
	var s string
	if err := json.Unmarshal(lit, &s); err != nil {
		return nil, fmt.Errorf("pathmap: decode string: %w", err)
	}
	mapped := m.Path(s)
	if mapped == s {
		return lit, nil
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	// Paths may hold '&', '<' or '>', which the default encoder escapes for
	// HTML; the stream is not HTML, so they are written as they are.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(mapped); err != nil {
		return nil, fmt.Errorf("pathmap: encode string: %w", err)
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// stringEnd returns the index just past the string literal opening at start.
// The document is valid JSON, so the closing quote exists.
func stringEnd(doc []byte, start int) int {
	for i := start + 1; i < len(doc); i++ {
		switch doc[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return len(doc)
}

// isKey reports whether the string literal ending at end is an object key: in
// valid JSON, and only there, a string is followed by a colon.
func isKey(doc []byte, end int) bool {
	for i := end; i < len(doc); i++ {
		switch doc[i] {
		case ' ', '\t', '\n', '\r':
			continue
		case ':':
			return true
		default:
			return false
		}
	}
	return false
}

// trimRoot drops trailing slashes, keeping the filesystem root "/" itself.
func trimRoot(root string) string {
	trimmed := strings.TrimRight(root, "/")
	if trimmed == "" && root != "" {
		return "/"
	}
	return trimmed
}

// Under reports whether p is root or lies below it at a path-segment
// boundary, and returns p relative to root, "." for root itself. Both are
// absolute and clean.
func Under(p, root string) (rel string, ok bool) {
	rest, ok := under(p, root)
	switch {
	case !ok:
		return "", false
	case rest == "" || rest == "/":
		return ".", true
	default:
		return rest[1:], true
	}
}

// Absolute is a sandbox path as an absolute, clean one: a relative path is
// relative to home, as ssh, tmux and Mutagen all take it on the sandbox.
func Absolute(home, p string) string {
	if path.IsAbs(p) {
		return path.Clean(p)
	}
	return path.Join(home, p)
}

// under reports whether p is root or lies below it, and returns the rest of p
// after root ("" or starting with "/"). Matching stops at a segment boundary,
// so /a/api does not hold /a/api2.
func under(p, root string) (rest string, ok bool) {
	if root == "/" {
		if p == "/" {
			return "", true
		}
		return p, strings.HasPrefix(p, "/")
	}
	if p == root {
		return "", true
	}
	if strings.HasPrefix(p, root+"/") {
		return p[len(root):], true
	}
	return "", false
}

func join(root, rest string) string {
	if root == "/" && rest != "" {
		return rest
	}
	return root + rest
}
