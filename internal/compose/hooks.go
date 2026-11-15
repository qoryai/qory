package compose

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/stack"
)

// Hook is one workflow hook of the composed harness, as hooks.json lists it: an entry of
// harness.hooks in the checkout's qory.yaml, checked against the hook points and the
// limits of the base stack.
type Hook struct {
	// Workflow is the workflow of the base stack that declares the point.
	Workflow string `json:"workflow"`
	// At is the hook point's name.
	At string `json:"at"`
	// PointAt is where the point sits in the workflow's session: end, start or inside.
	PointAt string `json:"point_at"`
	// Kind is check, review or enrich.
	Kind string `json:"kind"`
	// Name is the hook's name, unique at its point: name as written, else the agent's
	// name, else the file name of the program.
	Name string `json:"name"`
	// Run is the program and its arguments, the program by its path from the directory
	// of qory.yaml; nil for a hook that runs an agent.
	Run []string `json:"run,omitempty"`
	// Agent is the composed agent the hook runs; "" for a hook that runs a program.
	Agent string `json:"agent,omitempty"`
	// Required reports whether a red result blocks.
	Required bool `json:"required"`
	// TimeoutSeconds is the longest the hook may run: its timeout, else the base stack's
	// extending.hooks.timeout, else 10 minutes.
	TimeoutSeconds int `json:"timeout_seconds"`
	// Index is the hook's place in harness.hooks, from 0.
	Index int `json:"index"`
}

// hookPoint is a point of the base stack with the workflow that declares it.
type hookPoint struct {
	workflow string
	point    stack.HookPoint
}

// checkHooks checks the checkout's workflow hooks against the base stack and the
// composed agents, and sets [Result.Hooks]. It refuses a hook on a base that allows none
// or declares no point, at a point the base does not declare, of a kind the point does
// not take, with a timeout over the base's, that runs an agent no module ships or a
// program that is no file, with the name of another hook at its point, and more hooks at
// a point or in a workflow than the base allows. Each message starts with its fix.
func checkHooks(res *Result, p *stack.Stack, base *Base) error {
	if len(p.Hooks) == 0 {
		return nil
	}
	file := "qory.yaml"
	if p.File != "" {
		file = filepath.Base(p.File)
	}
	if base == nil {
		return fmt.Errorf("Remove hooks from %s, or extend a stack that allows them: a hook extends a workflow of a base stack", file)
	}
	if base.Extending == nil || base.Extending.Hooks == nil {
		return fmt.Errorf("Remove hooks from %s, or extend a stack that allows them: %s has no extending.hooks.", file, base)
	}
	limits := base.Extending.Hooks
	points := map[string]hookPoint{}
	for name, w := range p.Workflows {
		for point, hp := range w.HookPoints {
			points[point] = hookPoint{workflow: name, point: hp}
		}
	}
	if len(points) == 0 {
		return fmt.Errorf("Remove hooks from %s, or extend a stack that declares hook points: %s declares none.", file, base)
	}
	timeout := limits.Timeout
	if timeout == 0 {
		timeout = stack.DefaultHookTimeout
	}
	agents := map[string]bool{}
	for _, e := range res.Entries {
		if e.Kind == "agents" {
			agents[e.Name] = true
		}
	}
	var dir *os.Root
	defer func() {
		if dir != nil {
			_ = dir.Close()
		}
	}()
	named := map[string]int{}
	perPoint := map[string]int{}
	perWorkflow := map[string]int{}
	hooks := make([]Hook, 0, len(p.Hooks))
	for i, h := range p.Hooks {
		hp, ok := points[h.At]
		if !ok {
			return fmt.Errorf("Set hooks[%d].at to a hook point of the base stack %s: %s; %s is not one.", i, base, strings.Join(slices.Sorted(maps.Keys(points)), ", "), shown(h.At))
		}
		if !slices.Contains(hp.point.Kinds, h.Kind) {
			if len(hp.point.Kinds) == 1 {
				return fmt.Errorf("Set hooks[%d].kind to %s: %s allows only that kind.", i, hp.point.Kinds[0], h.At)
			}
			return fmt.Errorf("Set hooks[%d].kind to %s: %s allows only those.", i, orList(hp.point.Kinds), h.At)
		}
		limit := timeout
		if h.Timeout > 0 {
			if h.Timeout > timeout {
				return fmt.Errorf("Set hooks[%d].timeout to %s or less, or leave it out: the base stack %s allows a hook no longer.", i, limits.TimeoutShown(), base)
			}
			limit = h.Timeout
		}
		if h.Agent != "" && !agents[h.Agent] {
			return fmt.Errorf("Ship agent %s in a module of %s, or fix hooks[%d].agent: no module ships it.", shown(h.Agent), file, i)
		}
		if len(h.Command) > 0 {
			if dir == nil {
				var err error
				if dir, err = os.OpenRoot(p.Dir()); err != nil {
					return exports.HideError(err)
				}
			}
			program := h.Command[0]
			info, err := dir.Stat(filepath.FromSlash(program))
			if err != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("Add the program %s to the repository, or fix hooks[%d].run: no file is at that path from the directory of %s.", exports.Value(program, false), i, file)
			}
		}
		key := h.At + "/" + h.Name()
		if j, ok := named[key]; ok {
			return fmt.Errorf("Set hooks[%d].name to a name that no other hook at %s has: hooks[%d] has the name %s there.", i, h.At, j, shown(h.Name()))
		}
		named[key] = i
		perPoint[h.At]++
		perWorkflow[hp.workflow]++
		hooks = append(hooks, Hook{Workflow: hp.workflow, At: h.At, PointAt: hp.point.At, Kind: h.Kind, Name: h.Name(), Run: h.Command, Agent: h.Agent, Required: h.Required(), TimeoutSeconds: int(limit.Seconds()), Index: i})
	}
	for _, point := range slices.Sorted(maps.Keys(perPoint)) {
		if n := perPoint[point]; n > limits.PerPoint() {
			return fmt.Errorf("Remove %s at %s: the base stack allows %d, and %s has %d.", over(n-limits.PerPoint()), point, limits.PerPoint(), file, n)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(perWorkflow)) {
		if n := perWorkflow[name]; n > limits.PerWorkflow() {
			return fmt.Errorf("Remove %s at the points of workflow %s: the base stack allows %d, and %s has %d.", over(n-limits.PerWorkflow()), name, limits.PerWorkflow(), file, n)
		}
	}
	res.Hooks = hooks
	return nil
}

// over is how many hooks to remove, for a message: "a hook" or "<n> hooks".
func over(n int) string {
	if n == 1 {
		return "a hook"
	}
	return fmt.Sprintf("%d hooks", n)
}

// orList joins names for a message: "a", "a or b", "a, b or c".
func orList(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}
