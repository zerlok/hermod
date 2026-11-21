package control

import (
	"fmt"
	"io"

	"github.com/zerlok/hermod/internal/air"
	"github.com/zerlok/hermod/internal/claude"
)

// The variables an invocation reads its sandbox and origin from when not
// given them, which an Air entry sets for the chats it starts.
const (
	EnvSandbox = air.EnvSandbox
	EnvOrigin  = air.EnvOrigin
)

// EnvClaudeConfigDir is the local Claude config directory an invocation reads.
const EnvClaudeConfigDir = claude.EnvConfigDir

// Registration is the resolved intent of one `hermod air configure claude`:
// every path absolute, the sandbox already chosen.
type Registration struct {
	Sandbox  string
	Name     string  // the entry's name; "" is air.DefaultName
	Node     string  // the Node binary that runs the adapter
	Adapter  string  // the claude-agent-acp script
	Hermod   string  // this binary, which the entry runs and the stand-in link names
	DataHome string  // where the stand-in link and the sandbox's Claude config directory live
	Home     string  // the user's home, where Air keeps acp.json
	Origin   *string // HERMOD_ORIGIN for the entry; nil writes none
	DryRun   bool    // print the entry, change nothing
	Out      io.Writer
}

// Configure registers a sandbox with Air: it creates or repairs the stand-in
// link and merges the sandbox's entry into acp.json, which is all chat mode
// needs, then prints what terminal mode needs, which Air keeps in its own
// settings and environment. A dry run prints the entry instead.
func Configure(r Registration) error {
	link, acpFile := air.StandInPath(r.DataHome), air.ACPPath(r.Home)
	configDir, err := air.ConfigDir(r.DataHome, r.Sandbox)
	if err != nil {
		return err
	}
	agent := air.ClaudeAgent{
		Name:      r.Name,
		Sandbox:   r.Sandbox,
		Hermod:    r.Hermod,
		Node:      r.Node,
		Adapter:   r.Adapter,
		StandIn:   link,
		ConfigDir: configDir,
	}
	if r.Origin != nil {
		agent.Origin = air.OriginOf(*r.Origin)
	}
	entry, err := air.NewEntry(agent)
	if err != nil {
		return err
	}
	// Read first, so a file no entry can be merged into fails the run before
	// the link is touched.
	acp, err := air.ReadACP(acpFile)
	if err != nil {
		return err
	}
	if r.DryRun {
		doc, err := entry.JSON()
		if err != nil {
			return fmt.Errorf("encode the Air entry: %w", err)
		}
		fmt.Fprintf(r.Out, "Dry run: %s and %s are left as they are. The entry:\n%s", acpFile, link, doc)
	} else {
		outcome, err := air.LinkStandIn(link, r.Hermod)
		if err != nil {
			return err
		}
		fmt.Fprintf(r.Out, "Stand-in link %s → %s: %s\n", link, r.Hermod, outcome)
		if err := acp.Merge(entry); err != nil {
			return err
		}
		fmt.Fprintf(r.Out, "Air agent %q written to %s\n", entry.Name(), acpFile)
	}
	fmt.Fprintf(r.Out, "\nFor Air's terminal mode, enter this as Air's Claude executable:\n  %s\n"+
		"and put this line in the environment Air starts with:\n  %s\n", link, air.SandboxVariable(r.Sandbox))
	return nil
}
