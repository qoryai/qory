package compose

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

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
	// Kind is check, review or enrich. An enrich hook writes notes for the agents that
	// start after it.
	Kind string `json:"kind"`
	// Name is the hook's name, unique at its point: name as written, else the agent's
	// name, else the file name of the program.
	Name string `json:"name"`
	// Run is the program and its arguments, the program by its path from the root of
	// the checkout that was composed, with forward slashes; nil for a hook that runs an
	// agent.
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

// checkHooks checks the checkout's workflow hooks against the base stack, the checkout
// at checkout and the composed agents, and sets [Result.Hooks]. It refuses a hook on a
// base that allows none or declares no point, at a point the base does not declare, of a
// kind the point does not take, with a timeout over the base's, that runs an agent no
// module ships or a program that is no file in the checkout, with the name of another
// hook at its point, and more hooks at a point or in a workflow than the base allows.
// Each message is a line with the fix, of at most 100 characters, and a line with the
// cause. checkout "" is the directory of the document.
func checkHooks(res *Result, p *stack.Stack, base *Base, checkout string) error {
	if len(p.Hooks) == 0 {
		return nil
	}
	file := "qory.yaml"
	if p.File != "" {
		file = filepath.Base(p.File)
	}
	if base == nil {
		return fmt.Errorf("Remove hooks from %s, or extend a stack that allows them.\nA hook extends a workflow of a base stack.", file)
	}
	if base.Extending == nil || base.Extending.Hooks == nil {
		return fmt.Errorf("Remove hooks from %s, or extend a stack that allows them.\nThe base stack %s has no extending.hooks.", file, base)
	}
	limits := base.Extending.Hooks
	points := map[string]hookPoint{}
	for name, w := range p.Workflows {
		for point, hp := range w.HookPoints {
			points[point] = hookPoint{workflow: name, point: hp}
		}
	}
	if len(points) == 0 {
		return fmt.Errorf("Remove hooks from %s, or extend a stack that declares hook points.\nThe base stack %s declares none.", file, base)
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
	programs := &programs{doc: p.Dir(), checkout: checkout, file: file}
	defer programs.close()
	named := map[string]int{}
	perPoint := map[string]int{}
	perWorkflow := map[string]int{}
	hooks := make([]Hook, 0, len(p.Hooks))
	for i, h := range p.Hooks {
		hp, ok := points[h.At]
		if !ok {
			return fmt.Errorf("Set hooks[%d].at to a hook point of the base stack.\nThe base stack %s declares %s; %s is not one.", i, base, strings.Join(slices.Sorted(maps.Keys(points)), ", "), shown(h.At))
		}
		if !slices.Contains(hp.point.Kinds, h.Kind) {
			if len(hp.point.Kinds) == 1 {
				return fmt.Errorf("Set hooks[%d].kind to %s.\n%s allows only that kind.", i, hp.point.Kinds[0], h.At)
			}
			return fmt.Errorf("Set hooks[%d].kind to %s.\n%s allows only those.", i, orList(hp.point.Kinds), h.At)
		}
		limit := timeout
		if h.Timeout > 0 {
			if h.Timeout > timeout {
				return fmt.Errorf("Set hooks[%d].timeout to %s or less, or leave it out.\nThe base stack %s allows a hook no longer.", i, limits.TimeoutShown(), base)
			}
			limit = h.Timeout
		}
		if h.Agent != "" && !agents[h.Agent] {
			return fmt.Errorf("Ship the agent in a module of %s, or fix hooks[%d].agent.\nNo module ships the agent %s.", file, i, shown(h.Agent))
		}
		var run []string
		if len(h.Command) > 0 {
			program, err := programs.find(i, h.Command[0])
			if err != nil {
				return err
			}
			run = append([]string{program}, h.Command[1:]...)
		}
		key := h.At + "/" + h.Name()
		if j, ok := named[key]; ok {
			return fmt.Errorf("Set hooks[%d].name to a name that no other hook at its point has.\nhooks[%d] at %s has the name %s too.", i, j, h.At, shown(h.Name()))
		}
		named[key] = i
		perPoint[h.At]++
		perWorkflow[hp.workflow]++
		hooks = append(hooks, Hook{Workflow: hp.workflow, At: h.At, PointAt: hp.point.At, Kind: h.Kind, Name: h.Name(), Run: run, Agent: h.Agent, Required: h.Required(), TimeoutSeconds: int(limit / time.Second), Index: i})
	}
	for _, point := range slices.Sorted(maps.Keys(perPoint)) {
		if n := perPoint[point]; n > limits.PerPoint() {
			return fmt.Errorf("Remove %s at %s.\nThe base stack allows %d, and %s has %d.", over(n-limits.PerPoint()), point, limits.PerPoint(), file, n)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(perWorkflow)) {
		if n := perWorkflow[name]; n > limits.PerWorkflow() {
			return fmt.Errorf("Remove %s at the points of workflow %s.\nThe base stack allows %d, and %s has %d.", over(n-limits.PerWorkflow()), name, limits.PerWorkflow(), file, n)
		}
	}
	res.Hooks = hooks
	return nil
}

// programs finds the program of a hook in the checkout: doc is the directory of the
// document, which a program's path is from, checkout the root of the checkout that is
// composed, and file the document's name.
type programs struct {
	doc, checkout, file string
	// root is the checkout opened, so a link on the way leads nowhere outside it.
	root *os.Root
	// real is the checkout's path with every link resolved.
	real string
}

// find returns the path of program from the root of the checkout, with forward slashes,
// or the refusal of hook i: a program outside the checkout, one that a link leads out of
// it, one that is a link or sits in a linked folder inside the checkout, and one that is
// no regular file.
func (f *programs) find(i int, program string) (string, error) {
	if f.root == nil {
		checkout := f.checkout
		if checkout == "" {
			checkout = f.doc
		}
		real, err := filepath.EvalSymlinks(checkout)
		if err != nil {
			return "", exports.HideError(err)
		}
		if f.root, err = os.OpenRoot(real); err != nil {
			return "", exports.HideError(err)
		}
		f.real = real
	}
	doc, err := filepath.EvalSymlinks(f.doc)
	if err != nil {
		return "", exports.HideError(err)
	}
	abs := filepath.Join(doc, filepath.FromSlash(program))
	rel, err := filepath.Rel(f.real, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("Move the program into the checkout, or fix hooks[%d].run.\n%s, from the directory of %s, is outside the checkout %s.", i, exports.Value(program, false), exports.HidePath(filepath.Join(f.doc, f.file)), exports.HidePath(f.real))
	}
	shownRel := exports.Value(filepath.ToSlash(rel), false)
	// A path that the root cannot reach and the file system can goes through a link
	// that leads out of the checkout.
	if _, err := f.root.Stat(rel); err != nil {
		if _, statErr := os.Stat(abs); statErr == nil {
			return "", fmt.Errorf("Set hooks[%d].run to a program inside the checkout.\nA link on the way to %s leads out of the checkout.", i, shownRel)
		}
	}
	// The hook runner runs the program's file as git holds it and refuses a link, so a
	// link anywhere on the path, to a file or a folder inside the checkout too, is refused
	// here.
	parts := strings.Split(rel, string(filepath.Separator))
	var info os.FileInfo
	for n := range parts {
		var err error
		if info, err = f.root.Lstat(filepath.Join(parts[:n+1]...)); err != nil {
			return "", fmt.Errorf("Add the program to the checkout, or fix hooks[%d].run.\nNo file is at %s in the checkout.", i, shownRel)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("Point run at the file itself, not a link.\n%s in %s is a link.", exports.Value(program, false), exports.HidePath(filepath.Join(f.doc, f.file)))
		}
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("Add the program to the checkout, or fix hooks[%d].run.\n%s in the checkout is not a file.", i, shownRel)
	}
	return filepath.ToSlash(rel), nil
}

// close closes the checkout, when find opened it.
func (f *programs) close() {
	if f.root != nil {
		_ = f.root.Close()
	}
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
