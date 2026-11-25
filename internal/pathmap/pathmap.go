// Package pathmap maps paths between the local and the remote workstation. A
// Map is built from places, each a local root and the remote root it is joined
// to, and maps a path by the place whose root is its longest prefix at a
// path-segment boundary, in either direction; the same mapping applies to every
// string inside a decoded JSON value. It knows nothing about the programs whose
// arguments, settings or messages carry the paths.
package pathmap

import (
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

func apply(rules []rule, p string) string {
	for _, r := range rules {
		if rest, ok := under(p, r.from); ok {
			return join(r.to, rest)
		}
	}
	return p
}

// Value is v, a value encoding/json decodes into an interface, with every
// string in it taken through mapString. Object keys and every other value are
// kept; objects and arrays are new, so v is left as it was.
func Value(v any, mapString func(string) string) any {
	switch v := v.(type) {
	case string:
		return mapString(v)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = Value(item, mapString)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, member := range v {
			out[key] = Value(member, mapString)
		}
		return out
	}
	return v
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
