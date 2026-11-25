// Package relaytest reads back the listings package relay asks Mutagen for and
// gives Mutagen's answer, for a fake Mutagen in tests. A fake keeps its own
// sessions and answers from them; the fakes in relay, workspace and session
// tests share this one copy of the listing's format. The template relay asks
// for and what Mutagen says of a name it has no session under repeat what
// relay's fs.go sends and reads on purpose: this package stands in for Mutagen,
// so a change to relay's side shows as a failing test until it is made here.
package relaytest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// Session is one Mutagen session as a fake keeps it.
type Session struct {
	Name   string // "" for a session made with no name
	Local  string // the alpha end, a local path
	Beta   string // the beta end as its URL: `host:path`, or a path for a local end
	Paused bool
	Labels map[string]string
}

// Listing is what one `mutagen sync list` asks for.
type Listing struct {
	Name     string // the one session named; "" for none
	Selector string // the label selector, `key=value`; "" for none
}

// template is the output relay asks Mutagen for: its sessions as one JSON
// array.
const template = "{{json .}}"

// ReadList reads the arguments after `mutagen sync list`; ok is false for a
// listing that does not ask for the structured output relay reads.
func ReadList(rest []string) (l Listing, ok bool) {
	i := slices.Index(rest, "--template")
	if i < 0 || i != len(rest)-2 || rest[i+1] != template {
		return Listing{}, false
	}
	selection := rest[:i]
	switch {
	case len(selection) == 0:
	case len(selection) == 2 && selection[0] == "--label-selector":
		l.Selector = selection[1]
	case len(selection) == 1:
		l.Name = selection[0]
	default:
		return Listing{}, false
	}
	return l, true
}

// errNotFound is the failure of a listing that names a session a fake does
// not keep; a fake fails the command its own way on any error of Answer's.
var errNotFound = errors.New("no session under the name")

// NotFound writes what Mutagen says of a name it has no session under.
func NotFound(errOut io.Writer) {
	_, _ = fmt.Fprintln(errOut, "Error: unable to locate requested sessions")
}

// Answer answers listing l as Mutagen does from the sessions a fake keeps:
// the sessions l selects on out, or, for a name no session is under,
// Mutagen's message on errOut and an error.
func Answer(l Listing, sessions []Session, out, errOut io.Writer) error {
	var listed []Session
	for _, s := range sessions {
		if l.selects(s) {
			listed = append(listed, s)
		}
	}
	if l.Name != "" && len(listed) == 0 {
		NotFound(errOut)
		return errNotFound
	}
	return writeList(out, listed)
}

// selects reports whether the listing holds s.
func (l Listing) selects(s Session) bool {
	switch {
	case l.Name != "":
		return s.Name == l.Name
	case l.Selector != "":
		key, value, _ := strings.Cut(l.Selector, "=")
		got, ok := s.Labels[key]
		return ok && got == value
	}
	return true
}

// session and endpoint are the part of Mutagen's model that the listing
// carries for the fields a fake keeps.
type session struct {
	Name   string            `json:"name,omitempty"`
	Alpha  endpoint          `json:"alpha"`
	Beta   endpoint          `json:"beta"`
	Labels map[string]string `json:"labels,omitempty"`
	Paused bool              `json:"paused"`
	Status string            `json:"status"`
}

type endpoint struct {
	Protocol  string `json:"protocol"`
	Host      string `json:"host,omitempty"`
	Path      string `json:"path"`
	Connected bool   `json:"connected"`
}

// writeList writes sessions as Mutagen's structured listing does.
func writeList(w io.Writer, sessions []Session) error {
	out := make([]session, 0, len(sessions))
	for _, s := range sessions {
		status := "watching"
		if s.Paused {
			status = "disconnected"
		}
		out = append(out, session{
			Name:   s.Name,
			Alpha:  endpoint{Protocol: "local", Path: s.Local, Connected: !s.Paused},
			Beta:   beta(s.Beta, !s.Paused),
			Labels: s.Labels,
			Paused: s.Paused,
			Status: status,
		})
	}
	return json.NewEncoder(w).Encode(out)
}

func beta(url string, connected bool) endpoint {
	host, path, remote := strings.Cut(url, ":")
	if !remote {
		return endpoint{Protocol: "local", Path: url, Connected: connected}
	}
	return endpoint{Protocol: "ssh", Host: host, Path: path, Connected: connected}
}
