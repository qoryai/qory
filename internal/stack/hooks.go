package stack

import (
	"errors"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/exports"
)

// A workflow hook is a step that a repository adds at a hook point of a workflow of the
// stack it extends: a check, a review or an enrichment, run by a program in the checkout
// or by a composed agent. It is not a runtime hook, a module's hooks/<file>, which the
// runtime runs. The stack declares its hook points under workflows.<name>.hook_points and
// the limits under extending.hooks; the repository lists its hooks under harness.hooks in
// qory.yaml.
//
//	# qory-stack.yaml
//	workflows:
//	  review-changes:
//	    hook_points: {after-review-round: {at: end, kinds: [check, review]}}
//	extending:
//	  hooks: {per_point: 3, per_workflow: 10, timeout: 10m}
//	# qory.yaml
//	harness:
//	  hooks:
//	    - {at: after-review-round, kind: check, run: scripts/check-licences.sh}

// Where a hook point sits in the session of its workflow: [HookPoint.At].
const (
	// HookAtEnd is a point when the session's agent has exited, before its verdict counts.
	HookAtEnd = "end"
	// HookAtStart is a point before the session's agent starts.
	HookAtStart = "start"
	// HookAtInside is a point inside the session, which the session's agent reaches.
	HookAtInside = "inside"
)

// The kinds of a workflow hook, [Hook.Kind].
const (
	HookCheck  = "check"
	HookReview = "review"
	HookEnrich = "enrich"
)

// HookKinds are the kinds a workflow hook may have, in the order the messages list them.
var HookKinds = []string{HookCheck, HookReview, HookEnrich}

// The limits a stack's extending.hooks sets when it leaves a key out: 3 hooks per point,
// 10 per workflow, and 10 minutes per hook.
const (
	DefaultHooksPerPoint    = 3
	DefaultHooksPerWorkflow = 10
	DefaultHookTimeout      = 10 * time.Minute
	defaultHookTimeoutText  = "10m"
)

// HookPoint is one entry of workflows.<name>.hook_points: where the point sits in the
// workflow's session and the kinds of hook it takes.
type HookPoint struct {
	// At is [HookAtEnd], [HookAtStart] or [HookAtInside].
	At string `yaml:"at"`
	// Kinds are the kinds of hook the point takes, from [HookKinds], at least one.
	Kinds []string `yaml:"kinds"`
}

// HookLimits is extending.hooks: a stack that sets it lets a checkout extending it add
// workflow hooks. Each key left out takes its default.
type HookLimits struct {
	// PerPointValue is per_point as written, nil when left out.
	PerPointValue *int `yaml:"per_point,omitempty"`
	// PerWorkflowValue is per_workflow as written, nil when left out.
	PerWorkflowValue *int `yaml:"per_workflow,omitempty"`
	// TimeoutText is timeout as written, a duration such as 10m, "" when left out.
	TimeoutText string `yaml:"timeout,omitempty"`
	// Timeout is TimeoutText read as a duration, or [DefaultHookTimeout]. It is set as the
	// stack is validated.
	Timeout time.Duration `yaml:"-"`
}

// PerPoint is the most hooks one point takes: per_point, else [DefaultHooksPerPoint].
func (l *HookLimits) PerPoint() int {
	if l.PerPointValue != nil {
		return *l.PerPointValue
	}
	return DefaultHooksPerPoint
}

// PerWorkflow is the most hooks the points of one workflow take together: per_workflow,
// else [DefaultHooksPerWorkflow].
func (l *HookLimits) PerWorkflow() int {
	if l.PerWorkflowValue != nil {
		return *l.PerWorkflowValue
	}
	return DefaultHooksPerWorkflow
}

// TimeoutShown is the longest a hook may run, as the stack writes it, for a message.
func (l *HookLimits) TimeoutShown() string {
	if l.TimeoutText != "" {
		return l.TimeoutText
	}
	return defaultHookTimeoutText
}

// validate checks the limits and sets Timeout.
func (l *HookLimits) validate() error {
	if l.PerPointValue != nil && *l.PerPointValue < 1 {
		return fmt.Errorf("set extending.hooks.per_point to 1 or more: %d lets no hook in", *l.PerPointValue)
	}
	if l.PerWorkflowValue != nil && *l.PerWorkflowValue < 1 {
		return fmt.Errorf("set extending.hooks.per_workflow to 1 or more: %d lets no hook in", *l.PerWorkflowValue)
	}
	l.Timeout = DefaultHookTimeout
	if l.TimeoutText != "" {
		d, err := time.ParseDuration(l.TimeoutText)
		if err != nil || d <= 0 {
			return fmt.Errorf("set extending.hooks.timeout to a duration above zero, such as 10m: %s is not one", exports.Value(l.TimeoutText, true))
		}
		l.Timeout = d
	}
	return nil
}

// Hook is one entry of harness.hooks in a checkout's qory.yaml.
type Hook struct {
	// At is the name of the hook point, one the base stack declares.
	At string `yaml:"at"`
	// Kind is one of [HookKinds], and one the point takes.
	Kind string `yaml:"kind"`
	// Run is the program the hook runs, by its path from the directory of qory.yaml,
	// alone or followed by its arguments, separated by spaces. Not with Agent.
	Run string `yaml:"run,omitempty"`
	// Agent is the name of a composed agent the hook runs. Not with Run; a review
	// requires it.
	Agent string `yaml:"agent,omitempty"`
	// NameText is name as written, "" when left out; [Hook.Name] is the name the hook
	// goes by.
	NameText string `yaml:"name,omitempty"`
	// RequiredValue is required as written, nil when left out.
	RequiredValue *bool `yaml:"required,omitempty"`
	// TimeoutText is timeout as written, "" when left out.
	TimeoutText string `yaml:"timeout,omitempty"`

	// Command is Run split at spaces: the program's path, then its arguments. It is set
	// as the document is validated.
	Command []string `yaml:"-"`
	// Timeout is TimeoutText read as a duration, 0 when left out. It is set as the
	// document is validated.
	Timeout time.Duration `yaml:"-"`
}

// Name is the name the hook goes by: name as written, else the agent's name, else the
// file name of the program.
func (h Hook) Name() string {
	switch {
	case h.NameText != "":
		return h.NameText
	case h.Agent != "":
		return h.Agent
	case len(h.Command) > 0:
		return path.Base(h.Command[0])
	}
	return ""
}

// Required reports whether a red result of the hook blocks: required as written, else
// true, so that a failure from any layer blocks.
func (h Hook) Required() bool { return h.RequiredValue == nil || *h.RequiredValue }

// hookName is the name a hook goes by: a letter or digit, then letters, digits, dots,
// hyphens and underscores, 64 characters at most.
var hookName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// validateHookPoints checks the hook points of every workflow: a point's name has the
// form of a workflow's name, its at is end, start or inside, it takes one kind or more,
// each known and listed once, and no two workflows declare one name, since a hook
// selects a point by its name alone.
func (p *Stack) validateHookPoints() error {
	declared := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(p.Workflows)) {
		w := p.Workflows[name]
		for _, point := range slices.Sorted(maps.Keys(w.HookPoints)) {
			hp := w.HookPoints[point]
			where := fmt.Sprintf("workflows.%s.hook_points.%s", name, accesskey.ShownName(point, true))
			if !workflowName.MatchString(point) {
				return fmt.Errorf("name the hook point %s with a lower-case letter, then up to 54 lower-case letters, digits and hyphens: workflows.%s.hook_points lists it", accesskey.ShownName(point, true), name)
			}
			if other, ok := declared[point]; ok {
				return fmt.Errorf("rename the hook point %s of workflow %s or of workflow %s: both declare it, and a hook selects a point by its name alone", point, other, name)
			}
			declared[point] = name
			switch hp.At {
			case HookAtEnd, HookAtStart, HookAtInside:
			case "":
				return fmt.Errorf("set %s.at to end, start or inside: the point sets none", where)
			default:
				return fmt.Errorf("set %s.at to end, start or inside: %s is none of them", where, accesskey.ShownName(hp.At, true))
			}
			if len(hp.Kinds) == 0 {
				return fmt.Errorf("set %s.kinds to one or more of check, review and enrich: the point takes no kind", where)
			}
			seen := map[string]bool{}
			for _, k := range hp.Kinds {
				if !slices.Contains(HookKinds, k) {
					return fmt.Errorf("set %s.kinds to kinds from check, review and enrich: %s is none of them", where, accesskey.ShownName(k, true))
				}
				if seen[k] {
					return fmt.Errorf("list %s once in %s.kinds", k, where)
				}
				seen[k] = true
			}
		}
	}
	return nil
}

// hasHookPoints reports whether a workflow of the stack declares a hook point.
func (p *Stack) hasHookPoints() bool {
	for _, w := range p.Workflows {
		if len(w.HookPoints) > 0 {
			return true
		}
	}
	return false
}

// validateHooks checks each hook of a checkout's document for the rules that need no
// base, and sets each hook's Command and Timeout. Whether the base stack has the point,
// takes the kind, allows the timeout and the count, and whether the agent and the
// program exist, is the compose's check, which knows the base and the composed entries.
func (p *Stack) validateHooks() error {
	if len(p.Hooks) == 0 {
		return nil
	}
	if !p.extends() {
		return errors.New("remove harness.hooks, or extend a stack that allows them: a hook extends a workflow of a base stack, and this harness section defines a stack")
	}
	for i := range p.Hooks {
		if err := p.Hooks[i].validate(i); err != nil {
			return err
		}
	}
	return nil
}

// validate checks hook i without the base and sets its Command and Timeout.
func (h *Hook) validate(i int) error {
	if h.At == "" {
		return fmt.Errorf("set hooks[%d].at to a hook point of the base stack: the hook sets none", i)
	}
	if h.Kind == "" {
		return fmt.Errorf("set hooks[%d].kind to check, review or enrich: the hook sets none", i)
	}
	if !slices.Contains(HookKinds, h.Kind) {
		return fmt.Errorf("set hooks[%d].kind to check, review or enrich: %s is none of them", i, accesskey.ShownName(h.Kind, true))
	}
	h.Command = strings.Fields(h.Run)
	switch {
	case len(h.Command) > 0 && h.Agent != "":
		return fmt.Errorf("set hooks[%d].run or hooks[%d].agent, not both: a hook runs a program or an agent", i, i)
	case len(h.Command) == 0 && h.Agent == "":
		return fmt.Errorf("set hooks[%d].run or hooks[%d].agent: a hook runs a program or an agent, and this one sets neither", i, i)
	case h.Kind == HookReview && h.Agent == "":
		return fmt.Errorf("set hooks[%d].agent in place of run: a review hook runs an agent", i)
	}
	if h.Agent != "" && !segment(h.Agent) {
		return fmt.Errorf("set hooks[%d].agent to the name of an agent: %s is not one path segment", i, accesskey.ShownName(h.Agent, true))
	}
	if len(h.Command) > 0 {
		program := h.Command[0]
		if path.IsAbs(program) || strings.Contains(program, `\`) || strings.HasPrefix(program, "~") {
			return fmt.Errorf("set hooks[%d].run to a program by its path from the directory of qory.yaml, with forward slashes: %s is not one", i, exports.Value(program, true))
		}
		if clean := path.Clean(program); clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("set hooks[%d].run to a program inside the repository: %s leads out of it", i, exports.Value(program, true))
		}
	}
	if name := h.Name(); !hookName.MatchString(name) {
		if h.NameText != "" {
			return fmt.Errorf("set hooks[%d].name to a letter or digit, then up to 63 letters, digits, dots, hyphens and underscores: %s is not one", i, accesskey.ShownName(name, true))
		}
		return fmt.Errorf("set hooks[%d].name to a letter or digit, then up to 63 letters, digits, dots, hyphens and underscores: the hook has no name, and the program's file name %s is not one", i, accesskey.ShownName(name, true))
	}
	if h.TimeoutText != "" {
		d, err := time.ParseDuration(h.TimeoutText)
		if err != nil || d <= 0 {
			return fmt.Errorf("set hooks[%d].timeout to a duration above zero, such as 5m: %s is not one", i, exports.Value(h.TimeoutText, true))
		}
		h.Timeout = d
	}
	return nil
}
