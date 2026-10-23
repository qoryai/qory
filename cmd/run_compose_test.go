package cmd_test

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/report"
)

// afterCompose is the line of out that follows qory run's compose line, which has to be
// the first line of out.
func afterCompose(t *testing.T, out string) string {
	t.Helper()
	first, rest, _ := strings.Cut(out, "\n")
	if !strings.HasPrefix(first, "qory run: composed ") {
		t.Errorf("the first line is %q, not the compose's\n%s", first, out)
	}
	line, _, _ := strings.Cut(rest, "\n")
	return line
}

// runApart is [run] with what the command prints on stdout and on stderr kept apart.
func runApart(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut output
	root := cmd.Root()
	root.SetArgs(args)
	root.SetOut(&out)
	root.SetErr(&errOut)
	err = root.Execute()
	return out.String(), errOut.String(), err
}

// fakeLaunch makes script claude's program, through harness.launch in the user's file,
// keeping --settings, as [composedForFake] does, and composes nothing.
func fakeLaunch(t *testing.T, script string) {
	t.Helper()
	machineConfig(t, "harness:\n  launch:\n    claude:\n      command: "+script+"\n      args:\n        - [--settings, \"${dir}/settings.json\"]\n")
}

// TestRunComposesBeforeTheSession is a run in a checkout whose harness was removed: the
// run composes it into the checkout's home before the agent starts, prints one line for
// it on stderr and nothing of the compose on stdout, which is the agent's, and the home
// and its report are there after the run.
func TestRunComposesBeforeTheSession(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	if out, err := run(t, "harness", "remove"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Setenv("QORY_RUN_KEY", "")
	stdout, stderr, err := runApart(t, "run", "claude")
	if cmd.ExitCode(err) != 3 {
		t.Fatalf("%v (exit %d)\nstdout:\n%s\nstderr:\n%s", err, cmd.ExitCode(err), stdout, stderr)
	}
	line := "qory run: composed 8 entries from 2 modules for claude\n"
	if !strings.HasPrefix(stderr, line) || strings.Count(stderr, line) != 1 {
		t.Errorf("stderr does not start with %q, once:\n%s", line, stderr)
	}
	wants(t, stdout, "hello from ")
	lacks(t, stdout, "composed", "🐝", "🍯")
	lacks(t, stderr, "🐝", "🍯", "✓ composed")
	rep, err := report.Read(filepath.Join(root, ".qory", "harness-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, ".qory", "harness"); !samePath(rep.Home, want) {
		t.Errorf("the report contains the home %s, want %s", rep.Home, want)
	}
}

// samePath reports whether a and b are one path once every link is resolved.
func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// TestRunTakesTheComposeFlags runs with --model and --tier, which the run's compose
// writes as qory harness compose does, and with -f naming a stack outside a checkout
// that has none, which the run composes for the runtime given as its first argument.
func TestRunTakesTheComposeFlags(t *testing.T) {
	root := tierCheckout(t)
	tierModule(t, root, "claude", tierAgents)
	fakeLaunch(t, fakeRuntime(t))
	t.Setenv("QORY_RUN_KEY", "")
	out, err := run(t, "run", "claude", "--model", "claude-opus-5-5", "--tier", "light=claude-haiku-5-5")
	if cmd.ExitCode(err) != 3 {
		t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	rep, err := report.Read(filepath.Join(root, ".qory", "harness-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Target.Model != "claude-opus-5-5" {
		t.Errorf("the report's model is %q, want claude-opus-5-5", rep.Target.Model)
	}
	if got := rep.Tiers["light"]; got != (report.Tier{Model: "claude-haiku-5-5", From: "--tier"}) {
		t.Errorf("the report's light tier is %+v", got)
	}
	explorer := readText(t, filepath.Join(root, ".qory", "harness", "claude", "agents", "explorer.md"))
	wants(t, explorer, "model: claude-haiku-5-5\n")

	bare := newCheckout(t)
	fakeLaunch(t, fakeRuntime(t))
	stacks := tempDir(t)
	writeFile(t, filepath.Join(stacks, "mods", "extra", "qory-module.yaml"), "apiVersion: qory.dev/v1alpha1\nname: extra\n")
	writeFile(t, filepath.Join(stacks, "mods", "extra", "AGENTS.md"), "# extra\n")
	file := filepath.Join(stacks, "qory-stack.yaml")
	writeFile(t, file, "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: extra\n    source: {path: ./mods/extra}\n")
	out, err = run(t, "run", "claude", "-f", file)
	if cmd.ExitCode(err) != 3 {
		t.Fatalf("-f: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out, "qory run: composed 0 entries from 1 module for claude\n")
	rep, err = report.Read(filepath.Join(bare, ".qory", "harness-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Modules) != 1 || rep.Modules[0].Name != "extra" {
		t.Errorf("-f: the report's modules are %+v", rep.Modules)
	}
}

// TestRunPrintsACollisionOnStderr is a run whose stack has two modules that ship one
// entry: the compose prints the collision on stderr, nothing on stdout, and the run stops
// with the collision's exit status before the agent starts. The run's record says it
// failed with no session started.
func TestRunPrintsACollisionOnStderr(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "collision-fails", root)
	fakeLaunch(t, fakeRuntime(t))
	t.Setenv("QORY_RUN_KEY", "")
	stdout, stderr, err := runApart(t, "run", "claude")
	if cmd.ExitCode(err) != cmd.ExitCollision || !errors.Is(err, cmd.ErrReported) {
		t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), stderr)
	}
	if stdout != "" {
		t.Errorf("stdout is not empty:\n%s", stdout)
	}
	wants(t, stderr, "skills/test is provided by 3 modules")
	if ends := endsOf(t, root); !slices.Equal(ends, []string{"failed/no_session/-1"}) {
		t.Errorf("the run whose compose failed ended %v", ends)
	}
}

// TestRunPrintsTheComposesWarnings is a module at main whose remote is gone: the run's
// compose keeps the cached commit and prints the warning on stderr, as a line of qory run,
// before its result line.
func TestRunPrintsTheComposesWarnings(t *testing.T) {
	root := newCheckout(t)
	remote, url := branchRemote(t)
	writeOwnStack(t, root, "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n    source: {git: "+url+", ref: main}\n")
	fakeLaunch(t, fakeRuntime(t))
	if out, err := run(t, "hc"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	pin := readReport(t, root).Modules[0].Pin
	if err := os.RemoveAll(remote); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QORY_RUN_KEY", "")
	_, stderr, err := runApart(t, "run", "claude")
	if cmd.ExitCode(err) != 3 {
		t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), stderr)
	}
	want := "qory run: " + url + "#main: could not reach the remote; kept " + pin + "\nqory run: composed "
	if !strings.HasPrefix(stderr, want) {
		t.Errorf("stderr does not start with %q:\n%s", want, stderr)
	}
	lacks(t, stderr, "⚠")
}

// TestComposeAndRunShareTheComposeFlags pins that -f, --model, --effort, --tier and
// --tier-effort are one flag on both commands: the same name, shorthand, type, default and
// help line.
func TestComposeAndRunShareTheComposeFlags(t *testing.T) {
	compose, _, err := cmd.Root().Find([]string{"harness", "compose"})
	if err != nil {
		t.Fatal(err)
	}
	runCmd, _, err := cmd.Root().Find([]string{"run"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"file", "model", "effort", "tier", "tier-effort"} {
		a, b := compose.Flags().Lookup(name), runCmd.Flags().Lookup(name)
		if a == nil || b == nil {
			t.Fatalf("--%s: compose %v, run %v", name, a, b)
		}
		if a.Shorthand != b.Shorthand || a.Usage != b.Usage || a.DefValue != b.DefValue || a.Value.Type() != b.Value.Type() {
			t.Errorf("--%s differs: compose %+v, run %+v", name, a, b)
		}
	}
}

// TestRunOutsideGit is qory run in a folder that is not a git working tree. With the home
// outside the folder, by --home or by harness.home in the folder's qory.yaml, the run
// composes there and starts the fake runtime, which exits 3; with the home inside, the
// run stops with the fix first and exit 2. No run writes into the folder.
func TestRunOutsideGit(t *testing.T) {
	dir := emptyDir(t)
	copyFixture(t, "two-modules", dir)
	fakeLaunch(t, fakeRuntime(t))
	t.Setenv("QORY_RUN_KEY", "")
	before := snapshot(t, dir)

	stdout, stderr, err := runApart(t, "run", "claude")
	want := "set --home or harness.home to a folder outside " + dir + ", or run git init in " + dir + ": a home inside a folder needs a git working tree there, and " + dir + " is not one"
	if err == nil || err.Error() != want {
		t.Errorf("got %v, want %q\nstdout:\n%s\nstderr:\n%s", err, want, stdout, stderr)
	}
	if got := cmd.ExitCode(err); got != cmd.ExitInput {
		t.Errorf("exit %d, want %d", got, cmd.ExitInput)
	}
	lacks(t, stdout+stderr, "hello from ")

	homes := tempDir(t)
	stdout, stderr, err = runApart(t, "run", "--home", homes, "claude")
	ranIn(t, homes, dir, stdout, stderr, err)
	if !maps.Equal(snapshot(t, dir), before) {
		t.Error("a run changed the folder")
	}

	homes = tempDir(t)
	configure(t, dir, []string{"home: " + homes}, nil)
	before = snapshot(t, dir)
	stdout, stderr, err = runApart(t, "run", "claude")
	ranIn(t, homes, dir, stdout, stderr, err)
	if !maps.Equal(snapshot(t, dir), before) {
		t.Error("a run with harness.home changed the folder")
	}
}

// ranIn checks a run of the fake runtime that composed the folder at dir into its home
// under homes: exit 3, the compose line on stderr, the home in the runtime's environment,
// and the report beside the home.
func ranIn(t *testing.T, homes, dir, stdout, stderr string, err error) {
	t.Helper()
	if cmd.ExitCode(err) != 3 {
		t.Fatalf("%v (exit %d)\nstdout:\n%s\nstderr:\n%s", err, cmd.ExitCode(err), stdout, stderr)
	}
	home := filepath.Join(homes, checkout.Key(dir))
	wants(t, stderr, "qory run: composed 8 entries from 2 modules for claude\n")
	wants(t, stdout, "hello from ", "harness "+home)
	if _, err := report.Read(home + "-report.json"); err != nil {
		t.Errorf("no report beside the home: %v", err)
	}
}

// TestRunOutsideGitWithALeftoverReport is qory run in a folder outside git that contains
// the report of an earlier compose: --home naming the home beside that report stops the
// run as a home inside the folder does, exit 2, before the runtime starts, and nothing is
// written into the folder.
func TestRunOutsideGitWithALeftoverReport(t *testing.T) {
	dir := leftoverReport(t)
	fakeLaunch(t, fakeRuntime(t))
	t.Setenv("QORY_RUN_KEY", "")
	before := snapshot(t, dir)
	stdout, stderr, err := runApart(t, "run", "--home", ".qory/harness", "claude")
	if err == nil || !strings.HasPrefix(err.Error(), "set --home or harness.home to a folder outside "+dir+", or run git init in "+dir+":") {
		t.Errorf("got %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if got := cmd.ExitCode(err); got != cmd.ExitInput {
		t.Errorf("exit %d, want %d", got, cmd.ExitInput)
	}
	lacks(t, stdout+stderr, "hello from ")
	if !maps.Equal(snapshot(t, dir), before) {
		t.Error("the run changed the folder")
	}
}
