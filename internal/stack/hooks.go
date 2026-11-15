package stack

import (
	"fmt"
	"maps"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/exports"
)

// A workflow hook is a step that a repository adds at a hook point of a workflow of the
// stack it extends: a check hook, a review hook or an enrich hook, which writes notes for
// the agents that start after it. A program in the checkout or a composed agent runs it.
// A workflow hook is not a runtime hook, a module's hooks/<file>, which the runtime runs. The stack declares its hook points under workflows.<name>.hook_points and
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
	// TimeoutText is timeout as written, a duration such as 10m, nil when left out.
	TimeoutText *string `yaml:"timeout,omitempty"`
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
	if l.TimeoutText != nil {
		return *l.TimeoutText
	}
	return defaultHookTimeoutText
}

// validate checks the limits and sets Timeout.
func (l *HookLimits) validate() error {
	for _, c := range []struct {
		key   string
		value *int
	}{{"per_point", l.PerPointValue}, {"per_workflow", l.PerWorkflowValue}} {
		if c.value != nil && *c.value < 1 {
			return hookErrorf("Set extending.hooks.%s to 1 or more.", "{path} sets %d, which lets no hook in.", c.key, *c.value)
		}
	}
	l.Timeout = DefaultHookTimeout
	if l.TimeoutText != nil {
		d, ok := wholeSeconds(*l.TimeoutText)
		if !ok {
			return hookErrorf("Set extending.hooks.timeout to whole seconds, 1s or more, such as 10m.", "{path} sets %s.", exports.Value(*l.TimeoutText, true))
		}
		l.Timeout = d
	}
	return nil
}

// wholeSeconds reads text as a duration of whole seconds, 1s or more: hooks.json records
// a timeout in seconds, so a fraction of a second would be lost.
func wholeSeconds(text string) (time.Duration, bool) {
	d, err := time.ParseDuration(text)
	if err != nil || d < time.Second || d%time.Second != 0 {
		return 0, false
	}
	return d, true
}

// HookError is a refusal of a hook point, of extending.hooks or of a hook, as a line
// with the fix, of at most 100 characters, and a line with the cause. The file it is
// about goes inside the text, see [exports.InFile]: its name at {file} and its path at
// {path}.
type HookError struct {
	fix, cause string
	file       string
}

// Error is the fix line, a line feed and the cause line.
func (e *HookError) Error() string {
	name, path := "the file", "the file"
	if e.file != "" {
		name, path = filepath.Base(e.file), e.file
	}
	r := strings.NewReplacer("\x00", name, "\x01", path)
	cause := r.Replace(e.cause)
	if e.file == "" {
		cause = upperFirst(cause)
	}
	return r.Replace(e.fix) + "\n" + cause
}

// Fix is the fix line, with the file's name in it.
func (e *HookError) Fix() string { return strings.SplitN(e.Error(), "\n", 2)[0] }

// In is the error with file as the path of the file it is about.
func (e *HookError) In(file string) error { return &HookError{fix: e.fix, cause: e.cause, file: file} }

// hookErrorf is a [HookError] of the fix and the cause formatted with args, {file} and
// {path} in either format marking the file's name and path. The text is formatted first,
// with a NUL byte and a SOH byte at the marks, so a % in the path stays as it is.
func hookErrorf(fix, cause string, args ...any) *HookError {
	marks := strings.NewReplacer("{file}", "\x00", "{path}", "\x01")
	fix, cause = marks.Replace(fix), marks.Replace(cause)
	n := strings.Count(fix, "%") - 2*strings.Count(fix, "%%")
	return &HookError{fix: fmt.Sprintf(fix, args[:n]...), cause: fmt.Sprintf(cause, args[n:]...)}
}

// upperFirst is s with its first letter in upper case, for a cause line that starts with
// "the file" in place of a path.
func upperFirst(s string) string {
	if s == "" || !unicode.IsLower(rune(s[0])) {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Hook is one entry of harness.hooks in a checkout's qory.yaml. A key that is present
// with an empty value is refused, not read as left out.
type Hook struct {
	// At is the name of the hook point, one the base stack declares.
	At string `yaml:"at"`
	// Kind is one of [HookKinds], and one the point takes.
	Kind string `yaml:"kind"`
	// RunText is run as written, nil when left out: the program, by its path from the
	// directory of qory.yaml, alone or followed by its arguments, separated by spaces.
	// Not with AgentText.
	RunText *string `yaml:"run,omitempty"`
	// AgentText is agent as written, nil when left out: the name of a composed agent.
	// Not with RunText; a review requires it.
	AgentText *string `yaml:"agent,omitempty"`
	// NameText is name as written, nil when left out; [Hook.Name] is the name the hook
	// goes by.
	NameText *string `yaml:"name,omitempty"`
	// RequiredValue is required as written, nil when left out.
	RequiredValue *bool `yaml:"required,omitempty"`
	// TimeoutText is timeout as written, nil when left out.
	TimeoutText *string `yaml:"timeout,omitempty"`

	// Command is RunText split at spaces: the program's path, then its arguments; nil for
	// a hook that runs an agent. It is set as the document is validated.
	Command []string `yaml:"-"`
	// Agent is AgentText, "" for a hook that runs a program. It is set as the document is
	// validated.
	Agent string `yaml:"-"`
	// Timeout is TimeoutText read as a duration, 0 when left out. It is set as the
	// document is validated.
	Timeout time.Duration `yaml:"-"`
}

// Name is the name the hook goes by: name as written, else the agent's name, else the
// file name of the program.
func (h Hook) Name() string {
	switch {
	case h.NameText != nil:
		return *h.NameText
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
			if !workflowName.MatchString(point) {
				return hookErrorf("Name the hook point with a lower-case letter, then up to 54 lower-case letters, digits and hyphens.", "{path} lists %s under workflows.%s.hook_points.", accesskey.ShownName(point, true), name)
			}
			if other, ok := declared[point]; ok {
				return hookErrorf("Rename one of the two hook points %s in {file}: a point name is unique in a stack.", "Workflows %s and %s both declare it.", point, other, name)
			}
			declared[point] = name
			where := fmt.Sprintf("workflows.%s.hook_points.%s", name, point)
			switch hp.At {
			case HookAtEnd, HookAtStart, HookAtInside:
			case "":
				return hookErrorf("Set at of the hook point to end, start or inside.", "{path} sets no %s.at.", where)
			default:
				return hookErrorf("Set at of the hook point to end, start or inside.", "{path} sets %s.at to %s.", where, accesskey.ShownName(hp.At, true))
			}
			if len(hp.Kinds) == 0 {
				return hookErrorf("Add one or more kinds to the hook point: check, review or enrich.", "{path} sets no kind in %s.kinds.", where)
			}
			seen := map[string]bool{}
			for _, k := range hp.Kinds {
				if !slices.Contains(HookKinds, k) {
					return hookErrorf("Set each kind of the hook point to check, review or enrich.", "{path} lists %s in %s.kinds.", accesskey.ShownName(k, true), where)
				}
				if seen[k] {
					return hookErrorf("List each kind of the hook point once.", "{path} lists %s twice in %s.kinds.", k, where)
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
// base, and sets each hook's Command, Agent and Timeout. Whether the base stack has the
// point, takes the kind, allows the timeout and the count, and whether the agent and the
// program exist, is the compose's check, which knows the base, the checkout and the
// composed entries.
func (p *Stack) validateHooks() error {
	if len(p.Hooks) == 0 {
		return nil
	}
	if !p.extends() {
		return hookErrorf("Remove harness.hooks from {file}, or extend a stack that allows them.", "A hook extends a workflow of a base stack, and {path} defines a stack.")
	}
	for i := range p.Hooks {
		if err := p.Hooks[i].validate(i); err != nil {
			return err
		}
	}
	return nil
}

// validate checks hook i for the rules that need no base and sets its Command, Agent
// and Timeout.
func (h *Hook) validate(i int) error {
	for _, c := range []struct {
		key   string
		value *string
	}{{"run", h.RunText}, {"agent", h.AgentText}, {"name", h.NameText}, {"timeout", h.TimeoutText}} {
		if c.value != nil && strings.TrimSpace(*c.value) == "" {
			return hookErrorf("Remove hooks[%d].%s, or give it a value.", "{path} sets hooks[%d].%s to an empty value.", i, c.key, i, c.key)
		}
	}
	if h.At == "" {
		return hookErrorf("Set hooks[%d].at to a hook point of the base stack.", "{path} sets no hooks[%d].at.", i, i)
	}
	if h.Kind == "" {
		return hookErrorf("Set hooks[%d].kind to check, review or enrich.", "{path} sets no hooks[%d].kind.", i, i)
	}
	if !slices.Contains(HookKinds, h.Kind) {
		return hookErrorf("Set hooks[%d].kind to check, review or enrich.", "{path} sets it to %s.", i, accesskey.ShownName(h.Kind, true))
	}
	switch {
	case h.RunText != nil && h.AgentText != nil:
		return hookErrorf("Set one of hooks[%d].run and hooks[%d].agent, not both.", "A hook runs a program or an agent, and hooks[%d] in {path} sets both.", i, i, i)
	case h.RunText == nil && h.AgentText == nil:
		return hookErrorf("Set hooks[%d].run or hooks[%d].agent.", "A hook runs a program or an agent, and hooks[%d] in {path} sets neither.", i, i, i)
	case h.Kind == HookReview && h.AgentText == nil:
		return hookErrorf("Set hooks[%d].agent in place of hooks[%d].run.", "A review hook runs an agent, and hooks[%d] in {path} runs a program.", i, i, i)
	}
	if h.AgentText != nil {
		h.Agent = *h.AgentText
		if !segment(h.Agent) {
			return hookErrorf("Set hooks[%d].agent to the name of an agent.", "{path} sets it to %s, which is not one path segment.", i, accesskey.ShownName(h.Agent, true))
		}
	}
	if h.RunText != nil {
		h.Command = strings.Fields(*h.RunText)
		program := h.Command[0]
		if path.IsAbs(program) || strings.Contains(program, `\`) || strings.HasPrefix(program, "~") {
			return hookErrorf("Set hooks[%d].run to a program by its relative path, with forward slashes.", "{path} sets the program %s; the path is from the directory of {file}.", i, exports.Value(program, true))
		}
		if clean := path.Clean(program); clean == ".." || strings.HasPrefix(clean, "../") {
			return hookErrorf("Set hooks[%d].run to a program inside the checkout.", "{path} sets the program %s, which leads out of the checkout.", i, exports.Value(program, true))
		}
	}
	if name := h.Name(); !hookName.MatchString(name) {
		if h.NameText != nil {
			return hookErrorf("Set hooks[%d].name to letters, digits, dots, hyphens and underscores, 64 at most.", "{path} sets %s; a name starts with a letter or digit.", i, accesskey.ShownName(name, true))
		}
		return hookErrorf("Set hooks[%d].name to letters, digits, dots, hyphens and underscores, 64 at most.", "The hook has no name, and the program's file name %s is not one; a name starts with a letter or digit.", i, accesskey.ShownName(name, true))
	}
	if h.TimeoutText != nil {
		d, ok := wholeSeconds(*h.TimeoutText)
		if !ok {
			return hookErrorf("Set hooks[%d].timeout to whole seconds, 1s or more, such as 90s or 5m.", "{path} sets %s.", i, exports.Value(*h.TimeoutText, true))
		}
		h.Timeout = d
	}
	return nil
}
