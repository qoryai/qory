package compose

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/stack"
	"golang.org/x/text/unicode/norm"
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
// cause. opts.Checkout "" is the directory of the document; git reads the programs with
// opts.Timeout and opts.GitBase.
func checkHooks(res *Result, p *stack.Stack, opts Options) error {
	base := opts.Base
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
	programs := &programs{doc: p.Dir(), checkout: opts.Checkout, file: file, timeout: opts.Timeout, base: opts.GitBase}
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

// programs finds the program of a hook in the commit at HEAD of the checkout: doc is the
// directory of the document, which a program's path is from, checkout the root of the
// checkout that is composed, and file the document's name. Each git command runs at most
// timeout, 0 for no limit, from the environment base, nil for this process's.
type programs struct {
	doc, checkout, file string
	timeout             time.Duration
	base                []string
	// real is the checkout's path with every link resolved, "" until find sets it.
	real string
	// prefix is the checkout's path from the root of its git repository, "" or ending in
	// a slash, set once read is true.
	prefix string
	read   bool
}

// find returns the path of program in the commit at HEAD of the checkout, from the root
// of the checkout, as git has it, or the refusal of hook i: a program outside the
// checkout, the checkout itself, a checkout with no commit, a path with no entry in that
// commit or one that git has in another letter case or Unicode form, a link or a path
// through a linked folder, and an entry that is no file. The hook runner runs the file
// as committed in git at the run's start commit, so each of these would fail every run.
func (f *programs) find(i int, program string) (string, error) {
	if f.real == "" {
		checkout := f.checkout
		if checkout == "" {
			checkout = f.doc
		}
		real, err := filepath.EvalSymlinks(checkout)
		if err != nil {
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
	// fromDoc is a path from the checkout's root as the hook's run writes it, from doc.
	fromDoc := func(path string) string {
		if r, err := filepath.Rel(doc, filepath.Join(f.real, filepath.FromSlash(path))); err == nil {
			return exports.Value(filepath.ToSlash(r), false)
		}
		return exports.Value(path, false)
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return "", fmt.Errorf("Set hooks[%d].run to a program file in the checkout.\nhooks[%d].run, %s, is the checkout itself.", i, i, fromDoc(rel))
	}
	if !f.read {
		if err := f.head(); err != nil {
			return "", err
		}
		f.read = true
	}
	entries, err := f.tree(rel)
	if err != nil {
		return "", err
	}
	depth := strings.Count(rel, "/") + 1
	for _, e := range entries {
		if e.mode == "120000" && strings.Count(e.path, "/")+1 < depth {
			return "", fmt.Errorf("Point run at the file itself, not a link.\nhooks[%d].run passes through the link %s.", i, fromDoc(e.path))
		}
	}
	for _, e := range entries {
		if strings.Count(e.path, "/")+1 != depth {
			continue
		}
		switch e.mode {
		case "100644", "100755":
			return e.path, nil
		case "120000":
			return "", fmt.Errorf("Point run at the file itself, not a link.\nhooks[%d].run, %s, is a link.", i, fromDoc(e.path))
		case "040000":
			return "", fmt.Errorf("Point run at a file.\nhooks[%d].run, %s, is a folder in git.", i, fromDoc(e.path))
		case "160000":
			return "", fmt.Errorf("Point run at a file.\nhooks[%d].run, %s, is a submodule in git.", i, fromDoc(e.path))
		default:
			return "", fmt.Errorf("Point run at a file.\nhooks[%d].run, %s, has the mode %s in git.", i, fromDoc(e.path), e.mode)
		}
	}
	committed, err := f.near(rel)
	if err != nil {
		return "", err
	}
	if committed != "" {
		return "", fmt.Errorf("Write hooks[%d].run as committed in git.\nhooks[%d].run is %s, and git has %s.", i, i, fromDoc(rel), fromDoc(committed))
	}
	return "", fmt.Errorf("Commit the program of hooks[%d].run: a hook runs the file as committed in git.\nhooks[%d].run, %s, has no file in the commit at HEAD of the checkout.", i, i, fromDoc(rel))
}

// treeEntry is one entry of the commit at HEAD, as git ls-tree prints it: its mode and
// its path, from the checkout's root or, in a listing of one folder, its name.
type treeEntry struct{ mode, path string }

// tree is the entries of the commit at HEAD of the checkout at the path rel, with
// forward slashes, and at each folder on its way.
func (f *programs) tree(rel string) ([]treeEntry, error) {
	// Each folder on the way is a pathspec too: git ls-tree lists a link and stops there,
	// so a linked folder is an entry of its own path and the paths below it have none.
	// The paths are from the root of the repository, which the checkout may be a folder
	// of.
	parts := strings.Split(f.prefix+rel, "/")
	args := []string{"ls-tree", "-t", "-z", "--full-tree", "HEAD", "--"}
	asked := map[string]bool{}
	for n := range parts {
		path := strings.Join(parts[:n+1], "/")
		args = append(args, path)
		asked[path] = true
	}
	listed, err := f.list(args...)
	if err != nil {
		return nil, err
	}
	var entries []treeEntry
	for _, e := range listed {
		// A folder that is a pathspec also lists its files, so only the paths asked for
		// count.
		if !asked[e.path] {
			continue
		}
		if path, inside := strings.CutPrefix(e.path, f.prefix); inside && path != "" {
			entries = append(entries, treeEntry{mode: e.mode, path: path})
		}
	}
	return entries, nil
}

// near is the path from the checkout's root of the entry in the commit at HEAD that rel,
// with no entry of its own, differs from in letter case or Unicode form alone, or "" when
// there is none. A file system that ignores case or Unicode form finds the file by rel,
// and git does not.
func (f *programs) near(rel string) (string, error) {
	var found []string
	mode := "040000"
	for _, part := range strings.Split(rel, "/") {
		if mode != "040000" {
			return "", nil
		}
		listed, err := f.list("ls-tree", "-z", "HEAD:"+strings.TrimSuffix(f.prefix+strings.Join(found, "/"), "/"))
		if err != nil {
			return "", err
		}
		name := ""
		for _, e := range listed {
			if e.path == part {
				name, mode = part, e.mode
				break
			}
			if name == "" && strings.EqualFold(norm.NFC.String(e.path), norm.NFC.String(part)) {
				name, mode = e.path, e.mode
			}
		}
		if name == "" {
			return "", nil
		}
		found = append(found, name)
	}
	if committed := strings.Join(found, "/"); committed != rel {
		return committed, nil
	}
	return "", nil
}

// list is the entries git ls-tree prints with args, which include -z.
func (f *programs) list(args ...string) ([]treeEntry, error) {
	out, err := f.git(args...)
	if err != nil {
		return nil, f.failed(err)
	}
	var entries []treeEntry
	for _, line := range strings.Split(strings.TrimSuffix(out, "\x00"), "\x00") {
		about, path, ok := strings.Cut(line, "\t")
		if fields := strings.Fields(about); ok && len(fields) == 3 {
			entries = append(entries, treeEntry{mode: fields[0], path: path})
		}
	}
	return entries, nil
}

// head sets prefix to the checkout's path from the root of its git repository, or
// returns the refusal of a folder that is in no git checkout, a checkout with no commit,
// or a checkout git fails to read.
func (f *programs) head() error {
	fix := "Run qory in a git checkout: a hook runs the file as committed in git."
	out, err := f.git("rev-parse", "--show-prefix")
	if exit, ok := errors.AsType[*gitExit](err); ok && strings.Contains(exit.line, "not a git repository") {
		return fmt.Errorf("%s\nThe folder %s is in no git checkout.", fix, exports.HidePath(f.real))
	}
	if err != nil {
		return f.failed(err)
	}
	f.prefix = strings.TrimSuffix(out, "\n")
	_, err = f.git("rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if exit, ok := errors.AsType[*gitExit](err); ok && exit.code == 1 {
		return fmt.Errorf("%s\nThe checkout %s has no commit yet.", fix, exports.HidePath(f.real))
	}
	if err != nil {
		return f.failed(err)
	}
	return nil
}

// failed is the refusal of err, an error of [programs.git]: git's first line for a git
// that exited with an error.
func (f *programs) failed(err error) error {
	if exit, ok := errors.AsType[*gitExit](err); ok {
		return fmt.Errorf("Fix what git reports for the checkout: a hook runs the file as committed in git.\ngit %s in %s failed: %s", exit.command, exports.HidePath(f.real), exports.Value(exit.line, false))
	}
	return err
}

// gitExit is a git command that exited with an error: its subcommand, its exit code and
// the first line it wrote to its error output.
type gitExit struct {
	command, line string
	code          int
}

func (e *gitExit) Error() string { return "git " + e.command + ": " + e.line }

// git runs git with args in the checkout, with literal pathspecs, and returns its
// output. An error of a git that ran and exited with one is a [*gitExit].
func (f *programs) git(args ...string) (string, error) {
	ctx, cancel := context.Background(), func() {}
	if f.timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, f.timeout)
	}
	defer cancel()
	if f.base != nil {
		ctx = hostgit.WithBase(ctx, f.base)
	}
	cmd, err := hostgit.Command(ctx, f.real, args...)
	if err != nil {
		return "", err
	}
	cmd.Env = append(cmd.Env, "GIT_LITERAL_PATHSPECS=1")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", fmt.Errorf("Run the compose again, or raise git.timeout in the qory configuration.\ngit %s in %s ran past %s and was stopped.", args[0], exports.HidePath(f.real), f.timeout)
	}
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		line, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
		if line == "" {
			line = "exit " + strconv.Itoa(exit.ExitCode())
		}
		return "", &gitExit{command: args[0], line: line, code: exit.ExitCode()}
	}
	if err != nil {
		return "", exports.HideError(err)
	}
	return string(out), nil
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
