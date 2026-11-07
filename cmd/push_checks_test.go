package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// pushChecksCheckout is a checkout whose stack composes module core for codex, with the
// fake runtime as codex's program through the user's file, launch. codex passes no
// plugin directory, which stops a walled run with a home inside the checkout. core
// declares a push check for spec when checks is set. It returns the checkout, the user's
// file and the file's content.
func pushChecksCheckout(t *testing.T, checks bool) (root, user, launch string) {
	t.Helper()
	root = newCheckout(t)
	manifest := "apiVersion: qory.dev/v1alpha1\nname: core\n"
	if checks {
		manifest += "push_checks:\n  - type: spec\n    command: [checks/check.js, push, spec]\n"
	}
	writeFile(t, filepath.Join(root, "modules", "core", "qory-module.yaml"), manifest)
	writeFile(t, filepath.Join(root, "modules", "core", "checks", "check.js"), "#!/usr/bin/env node\n")
	writeFile(t, filepath.Join(root, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  target:\n    runtime: codex\n  modules:\n    - name: core\n      source: {path: modules/core}\n")
	user = filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "qory.yaml")
	launch = "apiVersion: qory.dev/v1alpha1\nharness:\n  launch:\n    codex:\n      command: " + fakeRuntime(t) + "\n"
	writeFile(t, user, launch)
	return root, user, launch
}

// TestWalledRunNeedsTheHomeOutsideForPushChecks is a walled run whose harness.home is the
// checkout's .qory/harness and whose module declares a push check: the run stops before
// the compose, with exit 2 and the line of a home inside the folder, and the home has no
// push-checks.json, which the agent could change. A run without a wall starts as
// before, and so does a walled run with harness.home outside the checkout, whose home is
// mounted read-only, and which works in a copy of the checkout.
func TestWalledRunNeedsTheHomeOutsideForPushChecks(t *testing.T) {
	root, user, launch := pushChecksCheckout(t, true)
	home := filepath.Join(root, ".qory", "harness")
	if out, err := run(t, "run", "codex"); cmd.ExitCode(err) != 3 {
		t.Fatalf("a run without a wall: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	if _, err := os.Stat(filepath.Join(home, "push-checks.json")); err != nil {
		t.Fatalf("a run without a wall wrote no push-checks.json: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(root, ".qory")); err != nil {
		t.Fatal(err)
	}
	log := walledForager(t)
	writeFile(t, user, strings.Replace(launch, "harness:\n", "harness:\n  home: "+home+"\n", 1))
	out, err := run(t, "run", "codex")
	want := "set --home to a folder outside " + root + ": " + home + " is inside " + root + ", and a walled run or a run with a run key writes no harness into the folder it runs in"
	if cmd.ExitCode(err) != cmd.ExitInput || err == nil || err.Error() != want {
		t.Fatalf("a walled run with push checks in the checkout's home: %v (exit %d), want %q\n%s", err, cmd.ExitCode(err), want, out)
	}
	// The relay may start before the compose; the agent's container, which mounts the
	// checkout, does not.
	if data, err := os.ReadFile(log); err == nil && strings.Contains(string(data), "src="+root+",") {
		t.Errorf("the wall started the agent's container:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(home, "push-checks.json")); !os.IsNotExist(err) {
		t.Errorf("the stopped run wrote push-checks.json: %v", err)
	}

	homes := tempDir(t)
	writeFile(t, user, strings.Replace(launch, "harness:\n", "harness:\n  home: "+homes+"\n", 1))
	if out, err := run(t, "run", "codex"); cmd.ExitCode(err) != 4 {
		t.Fatalf("a walled run with the home outside the checkout: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(data), "--mount type=bind,src="+homes+string(filepath.Separator))
	// A walled run that may write binds its copy of the checkout, not the checkout.
	if strings.Contains(string(data), "src="+root+",") {
		t.Errorf("the wall mounted the checkout:\n%s", data)
	}
	checks, err := filepath.Glob(filepath.Join(homes, "*", "push-checks.json"))
	if err != nil || len(checks) != 1 {
		t.Errorf("the home outside the checkout has push-checks.json %v (%v), want one", checks, err)
	}
}

// TestWalledRunWithoutPushChecksStartsAsBefore is a walled run whose home is the
// checkout's .qory/harness and whose modules declare no push check: it starts, and the
// home has no push-checks.json.
func TestWalledRunWithoutPushChecksStartsAsBefore(t *testing.T) {
	root, _, _ := pushChecksCheckout(t, false)
	walledForager(t)
	if out, err := run(t, "run", "codex"); cmd.ExitCode(err) != 4 {
		t.Fatalf("a walled run without push checks: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	if _, err := os.Stat(filepath.Join(root, ".qory", "harness", "push-checks.json")); !os.IsNotExist(err) {
		t.Errorf("a harness without push checks has push-checks.json: %v", err)
	}
}

// TestAWritableMountInTheHomeStopsARunWithPushChecks is a walled run whose home is
// outside the checkout and declares a push check, given a read-write --mount of the
// home: the run stops before the agent starts, with exit 2 and the fix line. A walled run
// with no run code works in a copy, so any read-write mount stops it with the copy's line
// before the compose. A read-only mount of the home starts as before.
func TestAWritableMountInTheHomeStopsARunWithPushChecks(t *testing.T) {
	_, user, launch := pushChecksCheckout(t, true)
	homes := tempDir(t)
	writeFile(t, user, strings.Replace(launch, "harness:\n", "harness:\n  home: "+homes+"\n", 1))
	log := walledForager(t)
	if out, err := run(t, "run", "codex"); cmd.ExitCode(err) != 4 {
		t.Fatalf("the first run: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	checks, err := filepath.Glob(filepath.Join(homes, "*", "push-checks.json"))
	if err != nil || len(checks) != 1 {
		t.Fatalf("the home has push-checks.json %v (%v), want one", checks, err)
	}
	home := filepath.Dir(checks[0])
	inside := filepath.Join(home, "modules")
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	for _, mount := range []string{home, inside} {
		out, err := run(t, "run", "--mount", mount, "codex")
		want := "make the mount " + mount + " read-only, with :ro on --mount or in wall.mounts: a walled run reads the checkout or works in a copy of it, and a read-write mount lets it write your files directly"
		if cmd.ExitCode(err) != cmd.ExitInput || err == nil || err.Error() != want {
			t.Errorf("--mount %s: %v (exit %d), want %q\n%s", mount, err, cmd.ExitCode(err), want, out)
		}
	}
	if data, err := os.ReadFile(log); err == nil && strings.Contains(string(data), "src="+inside) {
		t.Errorf("the wall started the agent's container:\n%s", data)
	}
	if out, err := run(t, "run", "--mount", inside+":ro", "codex"); cmd.ExitCode(err) != 4 {
		t.Errorf("a read-only mount in the home: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
}
