package provisioner

import (
	"time"

	"github.com/qoryai/integrations/contracts"
)

// FallbackTimeLimit is the time limit of a dispatched run whose run settings set no
// time_limit_minutes and whose workflow sets no time_limit, see [TimeLimit].
const FallbackTimeLimit = 3 * time.Hour

// TimeLimit is the time limit of a dispatched run: minutes, the run settings'
// time_limit_minutes, when it is above 0; else workflow, the time_limit of the workflow
// the run starts, when it is above 0; else [FallbackTimeLimit]. qory run sets it as the
// session's time limit, which counts from the run's open.
func TimeLimit(minutes int, workflow time.Duration) time.Duration {
	switch {
	case minutes > 0:
		return time.Duration(minutes) * time.Minute
	case workflow > 0:
		return workflow
	}
	return FallbackTimeLimit
}

// DefaultRuntime is the runtime of a dispatched run whose answer sets no run settings.
const DefaultRuntime = "claude"

// Setting is one compose flag of qory run that run settings set: the flag's name,
// without its dashes, and its value.
type Setting struct {
	Flag, Value string
}

// Settings are the runtime and the compose flags of qory run that rs sets, nil for no
// run settings, each value as rs has it:
//
//	runtime            the runtime; [DefaultRuntime] when rs is nil
//	model              model
//	effort             effort
//	tiers.<tier>       tier <tier>=<model> and, with its effort, tier-effort <tier>=<effort>
//
// A value rs does not set adds no flag. provider_models adds none either: Qory Apiary
// checked them against the model, the effort and the tiers before it answered.
func Settings(rs *contracts.RunSettings) (runtime string, flags []Setting) {
	if rs == nil {
		return DefaultRuntime, nil
	}
	if rs.Model != "" {
		flags = append(flags, Setting{"model", rs.Model})
	}
	if rs.Effort != "" {
		flags = append(flags, Setting{"effort", rs.Effort})
	}
	if t := rs.Tiers; t != nil {
		for _, tier := range []struct {
			name string
			t    *contracts.Tier
		}{{"light", t.Light}, {"fast", t.Fast}, {"thinking", t.Thinking}} {
			if tier.t == nil || tier.t.Model == "" {
				continue
			}
			flags = append(flags, Setting{"tier", tier.name + "=" + tier.t.Model})
			if tier.t.Effort != "" {
				flags = append(flags, Setting{"tier-effort", tier.name + "=" + tier.t.Effort})
			}
		}
	}
	return rs.Runtime, flags
}
