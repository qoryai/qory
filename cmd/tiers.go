package cmd

import (
	"fmt"
	"slices"
	"strings"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/module"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/stack"
	"github.com/qoryai/qory/internal/ui"
)

// tierFlag is the From of a tier entry the command line set: its model from --tier, and
// its effort, when it has one, from --tier-effort.
const tierFlag = "--tier"

// tierEffortFlag is the flag that sets the effort of a tier entry of the command line.
const tierEffortFlag = "--tier-effort"

// tierFlags reads the values of --tier, each <tier>=<model id>, and of --tier-effort,
// each <tier>=<effort>, into one map by tier: the command line's entries, each with the
// model of --tier and the effort of --tier-effort. A value of another form, a name that
// is not a tier, a tier given twice to one flag, a model that is a tier placeholder and
// an effort for a tier --tier does not map each return an error with the value as given.
// An entry comes whole from one source, so an effort on the command line is for the model
// that --tier maps the tier to.
func tierFlags(values, efforts []string) (map[string]stack.Tier, error) {
	tiers := map[string]stack.Tier{}
	for _, value := range values {
		name, model, ok := strings.Cut(value, "=")
		if !ok || name == "" || model == "" || module.HasControl(value) {
			return nil, fmt.Errorf("--tier %s: write --tier <tier>=<model id>, such as --tier light=claude-haiku-5-5", module.Escape(exports.Value(value, false)))
		}
		if !module.IsTier(name) {
			return nil, module.NotATier("--tier "+exports.Value(value, false), accesskey.ShownName(name, false))
		}
		if _, given := tiers[name]; given {
			return nil, fmt.Errorf("--tier %s is given twice; give each tier once", name)
		}
		if _, placeholder := module.TierOf(model); placeholder {
			return nil, fmt.Errorf("--tier %s: write a model id; %s is a tier placeholder, which goes only in an agent's model", exports.Value(value, false), exports.Value(model, false))
		}
		tiers[name] = stack.Tier{Model: model, From: tierFlag}
	}
	given := map[string]bool{}
	for _, value := range efforts {
		name, effort, ok := strings.Cut(value, "=")
		if !ok || name == "" || effort == "" || module.HasControl(value) {
			return nil, fmt.Errorf("%s %s: write %s <tier>=<effort>, such as %s thinking=high", tierEffortFlag, module.Escape(exports.Value(value, false)), tierEffortFlag, tierEffortFlag)
		}
		if !module.IsTier(name) {
			return nil, module.NotATier(tierEffortFlag+" "+exports.Value(value, false), accesskey.ShownName(name, false))
		}
		if given[name] {
			return nil, fmt.Errorf("%s %s is given twice; give each tier once", tierEffortFlag, name)
		}
		given[name] = true
		t, mapped := tiers[name]
		if !mapped {
			return nil, fmt.Errorf("%s %s: give %s %s=<model id> as well; the effort is for the model that %s maps the tier to", tierEffortFlag, exports.Value(value, false), tierFlag, name, tierFlag)
		}
		t.Effort = effort
		tiers[name] = t
	}
	return tiers, nil
}

// resolveTiers is the tier map a compose writes: per tier, the flag's entry, else the
// machine's, else the repository's, each entry whole from the one source.
func resolveTiers(flags map[string]stack.Tier, conf config.Config) map[string]stack.Tier {
	out := map[string]stack.Tier{}
	for _, name := range module.Tiers {
		for _, from := range []map[string]stack.Tier{flags, conf.Tiers, conf.RepoTiers} {
			if t, ok := from[name]; ok {
				out[name] = t
				break
			}
		}
	}
	return out
}

// mainModelTier refuses a main model that is a tier placeholder, from the document's
// target.model, the configuration's harness.model or --model, each with where it is set.
func mainModelTier(p *stack.Stack, conf config.Config, model string) error {
	for _, c := range []struct{ where, value string }{
		{p.File + ": target.model", p.Target.Model},
		{conf.Origin("harness.model") + ": harness.model", conf.Model},
		{"--model", model},
	} {
		if _, ok := module.TierOf(c.value); ok {
			return fmt.Errorf("%s %s is a tier placeholder; set the main model by its id, and a tier only in an agent's model", c.where, exports.Value(c.value, false))
		}
	}
	return nil
}

// tierAgents lists, per tier, the composed agents that set it, in name order.
func tierAgents(res *compose.Result) map[string][]string {
	out := map[string][]string{}
	for _, e := range res.Entries {
		if e.Kind == "agents" && e.Tier != "" {
			out[e.Tier] = append(out[e.Tier], e.Name)
		}
	}
	return out
}

// writesAgents reports whether the runtime places agents at all; a runtime that skips the
// kind already has a row for it.
func writesAgents(rt render.Runtime) bool { return !slices.Contains(rt.Skips(), "agents") }

// checkTierEfforts refuses a tier's effort that a runtime writing efforts into its agents
// does not take, for a tier an agent sets. from is shortened against root.
func checkTierEfforts(res *compose.Result, runtimes []render.Runtime, root string) error {
	used := tierAgents(res)
	for _, rt := range runtimes {
		am, ok := rt.(render.AgentModel)
		if !ok || am.AgentEfforts() == nil || !writesAgents(rt) {
			continue
		}
		for _, name := range module.Tiers {
			t, mapped := res.Tiers[name]
			if !mapped || t.Effort == "" || len(used[name]) == 0 || slices.Contains(am.AgentEfforts(), t.Effort) {
				continue
			}
			return fmt.Errorf("tier %s: set effort to one of %s for %s agents; %s is not one (from %s)", name, strings.Join(am.AgentEfforts(), ", "), rt.Name(), exports.Value(t.Effort, false), tierEffortFrom(t, root))
		}
	}
	return nil
}

// shownTierValue is a tier entry as [config.TierValue] writes it, with the model and the
// effort as [exports.Value] prints them.
func shownTierValue(t stack.Tier) string {
	return config.TierValue(stack.Tier{Model: exports.Value(t.Model, false), Effort: exports.Value(t.Effort, false)})
}

// tierFrom is where a tier entry came from, for a row or a message: for an entry of the
// command line, --tier, and --tier-effort beside it when the entry has an effort.
func tierFrom(t stack.Tier, root string) string {
	if t.From == tierFlag {
		if t.Effort != "" {
			return tierFlag + " and " + tierEffortFlag
		}
		return tierFlag
	}
	return ui.Short(t.From, root)
}

// tierEffortFrom is where a tier entry's effort came from, for a message: --tier-effort
// for an entry of the command line, since --tier sets no effort, else the entry's file.
func tierEffortFrom(t stack.Tier, root string) string {
	if t.From == tierFlag {
		return tierEffortFlag
	}
	return tierFrom(t, root)
}

// tierRows are the rows of a compose for the tiers its agents set: each tier with the
// model it maps to or that it has none, then, per runtime, a tier's model or effort the
// runtime does not write into its agents.
func tierRows(res *compose.Result, runtimes []render.Runtime, root string) [][2]string {
	used := tierAgents(res)
	who := func(agents []string) (string, bool) {
		if len(agents) == 1 {
			return "agent " + accesskey.ShownName(agents[0], false), false
		}
		return "agents " + strings.Join(accesskey.ShownNames(agents), ", "), true
	}
	var rows [][2]string
	for _, name := range module.Tiers {
		agents := used[name]
		if len(agents) == 0 {
			continue
		}
		w, many := who(agents)
		if t, ok := res.Tiers[name]; ok {
			rows = append(rows, [2]string{"tier", name + "  " + shownTierValue(t) + "  (" + w + "; from " + tierFrom(t, root) + ")"})
			continue
		}
		verbs := "gets no model and runs"
		if many {
			verbs = "get no model and run"
		}
		rows = append(rows, [2]string{"tier", name + "  not mapped  (" + w + " " + verbs + " on the main model; map it with --tier " + name + "=<model id> or harness.tiers)"})
	}
	for _, rt := range runtimes {
		if !writesAgents(rt) {
			continue
		}
		am, writesModel := rt.(render.AgentModel)
		for _, name := range module.Tiers {
			t, ok := res.Tiers[name]
			if !ok || len(used[name]) == 0 {
				continue
			}
			w, many := who(used[name])
			switch {
			case !writesModel:
				verb := "runs"
				if many {
					verb = "run"
				}
				rows = append(rows, [2]string{"skipped", "tier " + name + " for " + rt.Name() + "  (" + rt.Name() + " agents have no model setting; " + w + " " + verb + " on the main model)"})
			case t.Effort != "" && am.AgentEfforts() == nil:
				rows = append(rows, [2]string{"skipped", "effort " + exports.Value(t.Effort, false) + " of tier " + name + " for " + rt.Name() + "  (qory writes no effort into " + rt.Name() + " agents)"})
			}
		}
	}
	return rows
}
