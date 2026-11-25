package agent

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
)

// Handshake is the answer to a client's connection start: the protocol
// version the program speaks and the capabilities it offers, a JSON object
// written in the protocol's own terms.
type Handshake struct {
	version      int
	capabilities json.RawMessage
}

// NewHandshake checks that the capabilities are a JSON object.
func NewHandshake(version int, capabilities []byte) (Handshake, error) {
	var object map[string]any
	if err := json.Unmarshal(capabilities, &object); err != nil || object == nil {
		return Handshake{}, fmt.Errorf("agent: capabilities are not a JSON object: %q", capabilities)
	}
	return Handshake{version: version, capabilities: slices.Clone(capabilities)}, nil
}

// Version is the protocol version.
func (h Handshake) Version() int { return h.version }

// Capabilities is the capabilities object as it was given; the zero Handshake
// offers none, which is the empty object, so a client is never sent null.
func (h Handshake) Capabilities() json.RawMessage {
	if h.capabilities == nil {
		return json.RawMessage(noCapabilities)
	}
	return slices.Clone(h.capabilities)
}

const noCapabilities = `{}`

// Equal reports whether two handshakes give the same version and the same
// capabilities, however the objects are spaced or their keys ordered, so that
// an adapter's own answer can be told apart from the declared one only by
// what it says.
func (h Handshake) Equal(other Handshake) bool {
	if h.version != other.version {
		return false
	}
	var a, b any
	if json.Unmarshal(h.Capabilities(), &a) != nil || json.Unmarshal(other.Capabilities(), &b) != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}
