// Package pathmap maps paths between the local and the remote workstation. A
// Map is built from places, each a local root and the remote root it is joined
// to, and maps a path by the place whose root is its longest prefix at a
// path-segment boundary, in either direction; the same mapping applies to every
// string inside a JSON value. It knows nothing about the programs whose
// arguments, settings or messages carry the paths.
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

// Place is one local root and the remote root it lies at. Roots are absolute,
// slash-separated, and taken as written apart from any trailing slash. A
// relative root would match any string that merely starts with its first
// segment, such as a branch name or a word of prose in a message, whereas the
// paths that programs write into arguments, hook bodies and messages are
// absolute.
type Place struct {
	Local  string
	Remote string
}

// Map maps paths between the workstations. Its zero value holds no place and
// maps nothing.
type Map struct {
	places   []Place
	toRemote []rule // longest root first, so the first match is the longest prefix
	toLocal  []rule
}

type rule struct{ from, to string }

// New builds a Map from places. A root must be absolute, and no two places may
// share a root on either side, because a path under a shared root would map to
// two places in one direction.
func New(places ...Place) (Map, error) {
	seenLocal := map[string]bool{}
	seenRemote := map[string]bool{}
	m := Map{places: make([]Place, 0, len(places))}
	for _, p := range places {
		local, remote := trimRoot(p.Local), trimRoot(p.Remote)
		switch {
		case !strings.HasPrefix(local, "/") || !strings.HasPrefix(remote, "/"):
			return Map{}, fmt.Errorf("pathmap: place %q → %q has a root that is not absolute", p.Local, p.Remote)
		case seenLocal[local]:
			return Map{}, fmt.Errorf("pathmap: local root %q is given twice", p.Local)
		case seenRemote[remote]:
			return Map{}, fmt.Errorf("pathmap: remote root %q is given twice", p.Remote)
		}
		seenLocal[local], seenRemote[remote] = true, true
		m.places = append(m.places, Place{Local: local, Remote: remote})
		m.toRemote = append(m.toRemote, rule{from: local, to: remote})
		m.toLocal = append(m.toLocal, rule{from: remote, to: local})
	}
	sort.SliceStable(m.places, func(i, j int) bool { return len(m.places[i].Local) > len(m.places[j].Local) })
	longestFirst(m.toRemote)
	longestFirst(m.toLocal)
	return m, nil
}

func longestFirst(rules []rule) {
	sort.SliceStable(rules, func(i, j int) bool { return len(rules[i].from) > len(rules[j].from) })
}

// Places are the map's places, longest local root first.
func (m Map) Places() []Place { return append([]Place(nil), m.places...) }

// ToRemote maps a local path to the remote. A path under no root is returned
// unchanged; the part below the root is kept as written.
func (m Map) ToRemote(p string) string { return apply(m.toRemote, p) }

// ToLocal maps a remote path to the local workstation, as ToRemote does the
// other way.
func (m Map) ToLocal(p string) string { return apply(m.toLocal, p) }

// ToRemoteJSON maps every string inside a JSON document to the remote.
func (m Map) ToRemoteJSON(doc []byte) ([]byte, error) { return JSON(doc, m.ToRemote) }

// ToLocalJSON maps every string inside a JSON document to the local
// workstation.
func (m Map) ToLocalJSON(doc []byte) ([]byte, error) { return JSON(doc, m.ToLocal) }

func apply(rules []rule, p string) string {
	for _, r := range rules {
		if rest, ok := under(p, r.from); ok {
			return join(r.to, rest)
		}
	}
	return p
}

// JSON applies mapPath to every string value inside one JSON document, at any
// depth. Object keys, every other value, the order of members and the
// document's whitespace are left as they are; only a string that maps to a
// different one is encoded again, so escapes the producer chose are kept.
func JSON(doc []byte, mapPath func(string) string) ([]byte, error) {
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
		mapped, err := mapLiteral(literal, mapPath)
		if err != nil {
			return nil, err
		}
		out.Write(mapped)
	}
	return out.Bytes(), nil
}

// mapLiteral maps one encoded JSON string, returning it byte for byte when it
// does not change.
func mapLiteral(lit []byte, mapPath func(string) string) ([]byte, error) {
	var s string
	if err := json.Unmarshal(lit, &s); err != nil {
		return nil, fmt.Errorf("pathmap: decode string: %w", err)
	}
	mapped := mapPath(s)
	if mapped == s {
		return lit, nil
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	// Paths may hold '&', '<' or '>', which the default encoder escapes for
	// HTML; the document is not HTML, so they are written as they are.
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

// Absolute is a remote path as an absolute, clean one: a relative path is
// relative to home, as ssh, tmux and Mutagen all take it on the remote.
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
