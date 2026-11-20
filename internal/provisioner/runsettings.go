package provisioner

import (
	"github.com/qoryai/integrations/contracts"
)

// Setting is one compose flag of qory run that run settings set: the flag's name,
// without its dashes, and its value.
type Setting struct {
	Flag, Value string
}

// Settings are the runtime and the compose flags of qory run that rs sets, each value as
// rs has it. rs is nil for documents with no run settings, which qory refuses for a step
// that starts an agent session:
//
//	runtime            the runtime; "" when rs is nil or sets none
//	model              model
//	effort             effort
//	tiers.<tier>       tier <tier>=<model> and, with its effort, tier-effort <tier>=<effort>
//
// A value rs does not set adds no flag. provider_models adds none either: Qory Apiary
// checked them against the model, the effort and the tiers before it answered.
func Settings(rs *contracts.RunSettings) (runtime string, flags []Setting) {
	if rs == nil {
		return "", nil
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
		}{{"light", t.Light}, {"balanced", t.Balanced}, {"thinking", t.Thinking}} {
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
