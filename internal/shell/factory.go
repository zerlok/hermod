package shell

import "github.com/zerlok/hermod/internal/execx"

// Factory hands a collaborator the two leaf shells it builds transports on, and
// lets the collaborator pick which to use per operation:
//
//   - Real always executes — for plan-shaping probes (git identity, sync status)
//     that must reflect true state even under dry-run.
//   - Effective is dry-run-aware — for side effects, the interactive attach, and
//     the liveness probe (which only matters after a real attach, so it is
//     simulated when the attach was merely printed).
type Factory interface {
	Real() Shell
	Effective() Shell
}

type factory struct {
	real Shell
	eff  Shell
}

// NewFactory returns a Factory whose Effective leaf is configured by the given
// execx options (e.g. execx.WithDryRun()); the Real leaf always executes.
func NewFactory(effective ...execx.Option) Factory {
	return factory{
		real: NewLocal(execx.New()),
		eff:  NewLocal(execx.New(effective...)),
	}
}

func (f factory) Real() Shell      { return f.real }
func (f factory) Effective() Shell { return f.eff }
