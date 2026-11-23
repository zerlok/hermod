package agent

import (
	"errors"
	"fmt"
	"path"

	"github.com/zerlok/hermod/internal/pathmap"
)

// errNoReading is a query built without a way to read its answer.
var errNoReading = errors.New("agent: the query declares no reading of its answer")

// Probe asks the agent program whether one conversation runs.
type Probe struct {
	id      string
	command []string
	read    func(out []byte) (State, error)
}

// NewProbe is the command that asks about conversation id, and how to read
// its output into the conversation's state.
func NewProbe(id string, command []string, read func(out []byte) (State, error)) Probe {
	return Probe{id: id, command: clone(command), read: read}
}

// Command is the command to run on the remote.
func (p Probe) Command() []string { return clone(p.command) }

// Read reads the command's output. An answer that cannot be read is an
// error, never a state.
func (p Probe) Read(out []byte) (State, error) {
	if p.read == nil {
		return Stopped, errNoReading
	}
	state, err := p.read(out)
	if err == nil {
		err = state.check()
	}
	if err != nil {
		return Stopped, fmt.Errorf("probe conversation %s: %w", p.id, err)
	}
	return state, nil
}

// Listing asks the agent program for the conversations of a directory on the
// remote and of the directories under it.
type Listing struct {
	dir     string
	command []string
	read    func(out []byte) ([]Conversation, error)
}

// NewListing is the command that lists the conversations of dir, and how to
// read its output. dir is cleaned, as pathmap.Under compares clean paths.
func NewListing(dir string, command []string, read func(out []byte) ([]Conversation, error)) Listing {
	return Listing{dir: path.Clean(dir), command: clone(command), read: read}
}

// Command is the command to run on the remote.
func (l Listing) Command() []string { return clone(l.command) }

// Read reads the command's output. A conversation outside the directory, or
// one with no ID or directory, fails the whole reading: the program answered
// another question than the one asked.
func (l Listing) Read(out []byte) ([]Conversation, error) {
	if l.read == nil {
		return nil, errNoReading
	}
	found, err := l.read(out)
	if err != nil {
		return nil, fmt.Errorf("list conversations of %s: %w", l.dir, err)
	}
	for _, c := range found {
		if err := c.check(); err != nil {
			return nil, fmt.Errorf("list conversations of %s: %w", l.dir, err)
		}
		if _, ok := pathmap.Under(c.Dir, l.dir); !ok {
			return nil, fmt.Errorf("list conversations of %s: conversation %s is in %s", l.dir, c.ID, c.Dir)
		}
	}
	return clone(found), nil
}

// Lookup asks the agent program where one conversation belongs and its state.
type Lookup struct {
	id      string
	command []string
	read    func(out []byte) (c Conversation, found bool, err error)
}

// NewLookup is the command that looks up conversation id, and how to read
// its output into the conversation, or into none when the program does not
// have it.
func NewLookup(id string, command []string, read func(out []byte) (Conversation, bool, error)) Lookup {
	return Lookup{id: id, command: clone(command), read: read}
}

// Command is the command to run on the remote.
func (l Lookup) Command() []string { return clone(l.command) }

// Read reads the command's output: the conversation, or found false when the
// program does not have it, which is an answer and not an error.
func (l Lookup) Read(out []byte) (c Conversation, found bool, err error) {
	if l.read == nil {
		return Conversation{}, false, errNoReading
	}
	c, found, err = l.read(out)
	switch {
	case err != nil:
	case !found:
		return Conversation{}, false, nil
	case c.ID != l.id:
		err = fmt.Errorf("the program answered for conversation %q", c.ID)
	default:
		err = c.check()
	}
	if err != nil {
		return Conversation{}, false, fmt.Errorf("locate conversation %s: %w", l.id, err)
	}
	return c, true, nil
}
