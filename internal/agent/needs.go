package agent

import (
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/zerlok/hermod/internal/env"
	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/secret"
)

// Needs is what a command of the agent's requires on the remote. It is a
// value: each With method returns a new Needs and leaves its receiver as it
// was.
type Needs struct {
	command  []string
	files    map[string]secret.Token
	declared []func(key string) bool
	places   []pathmap.Place
	settings settings
	dropped  []string
}

// settings is the settings document Agent.Hooks aims at the worker's port
// before it is written into the worker's directory. The callback lives inside
// it because hooks are declared only by that document: a callback with no
// document would open a relay nothing calls.
type settings struct {
	declared bool
	file     string
	document secret.Token
	hooked   bool
	callback Callback
}

// Callback is where the program's hooks call back on the local workstation:
// an address, the routes they call there, and the token the local endpoint
// expects, which carries no further than this workstation.
type Callback struct {
	address string
	routes  []string
	token   secret.Token
}

// NewCallback is a callback to address on the given routes, guarded by token.
func NewCallback(address string, routes []string, token secret.Token) Callback {
	return Callback{address: address, routes: clone(routes), token: token}
}

// DirVar is the variable a worker's process is given with its directory, an
// absolute path on the remote. A command is declared before anyone knows
// where its worker's directory will be, so it names the files its Needs
// declare under this variable.
const DirVar = "HERMOD_WORKER_DIR"

// NewNeeds is the needs of a command, given with its paths already mapped to
// the remote.
func NewNeeds(command []string) Needs { return Needs{command: clone(command)} }

// WithFile adds a file the command must find in its worker's directory. The
// content may hold a credential, so it stays masked; a second file of the
// same name replaces the first.
func (n Needs) WithFile(name string, content secret.Token) Needs {
	n.files = maps.Clone(n.files)
	if n.files == nil {
		n.files = map[string]secret.Token{}
	}
	n.files[name] = content
	return n
}

// WithVars declares the variables the command must be given, by name: only
// these leave the local workstation, so a local credential never travels by
// default. Each call declares more.
func (n Needs) WithVars(declared func(key string) bool) Needs {
	n.declared = append(clone(n.declared), declared)
	return n
}

// WithPlace adds a directory that must be the same on both workstations.
func (n Needs) WithPlace(place pathmap.Place) Needs {
	n.places = append(clone(n.places), place)
	return n
}

// WithSettings gives the settings document Agent.Hooks rewrites for the
// worker's port and token, and the file in the worker's directory it is then
// written to.
func (n Needs) WithSettings(file string, document secret.Token) Needs {
	n.settings = settings{declared: true, file: file, document: document}
	return n
}

// WithHookedSettings gives the settings document as WithSettings does, with
// the callback its hooks call on the local workstation.
func (n Needs) WithHookedSettings(file string, document secret.Token, callback Callback) Needs {
	n.settings = settings{declared: true, file: file, document: document, hooked: true, callback: callback}
	return n
}

// WithDroppedHooks records hooks the agent left out of its settings because
// no relay can carry them, each by scheme, host and path only, so the session
// can log what the agent runs without and no credential reaches the log.
func (n Needs) WithDroppedHooks(endpoints ...string) Needs {
	n.dropped = append(clone(n.dropped), endpoints...)
	return n
}

// Command is the command, its paths mapped to the remote.
func (n Needs) Command() []string { return clone(n.command) }

// Files are the files the command must find, by name.
func (n Needs) Files() map[string]secret.Token { return maps.Clone(n.files) }

// Vars selects, from the caller's environment, the variables the command was
// declared to need.
func (n Needs) Vars(environ env.Vars) env.Vars {
	return environ.Select(func(key string) bool {
		return slices.ContainsFunc(n.declared, func(declared func(string) bool) bool { return declared(key) })
	})
}

// Places are the directories that must be the same on both workstations.
func (n Needs) Places() []pathmap.Place { return clone(n.places) }

// Settings is the settings document for Agent.Hooks and the file it goes to;
// ok is false when the command takes none.
func (n Needs) Settings() (file string, document secret.Token, ok bool) {
	return n.settings.file, n.settings.document, n.settings.declared
}

// Hooks is where the hooks of the settings document call back locally; ok is
// false when they call nowhere on this workstation and no Net relay is needed
// for them.
func (n Needs) Hooks() (address string, routes []string, token secret.Token, ok bool) {
	c := n.settings.callback
	return c.address, clone(c.routes), c.token, n.settings.hooked
}

// DroppedHooks are the endpoints of the hooks left out of the settings.
func (n Needs) DroppedHooks() []string { return clone(n.dropped) }

// String names what the command needs and masks every content, so a plan, a
// log line or an error that shows a Needs shows no credential.
func (n Needs) String() string {
	return fmt.Sprintf("agent.Needs{command: %q, files: %q, places: %v, settings: %q %v, hooks: %q %q %v}",
		n.command, slices.Sorted(maps.Keys(n.files)), n.places,
		n.settings.file, n.settings.document,
		n.settings.callback.address, n.settings.callback.routes, n.settings.callback.token)
}

// GoString masks under %#v as String does.
func (n Needs) GoString() string { return n.String() }

// Format masks under every verb, which would otherwise print the fields of the
// struct, the secrets' among them.
func (n Needs) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, n.String()) }
