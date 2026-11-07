package cmd_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/checkout"
)

// pushChecksCheckout is a checkout whose stack composes module core for codex, with the
// fake runtime as codex's program through the user's file, launch. core declares a push
// check for spec when checks is set. It returns the checkout, the user's
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

// TestAWalledRunWithPushChecksAndTheHomeInTheCheckoutStops is a walled run whose
// harness.home is the checkout's .qory/harness and whose module declares a push check:
// the run stops before the compose, with exit 2 and the line of a home inside the
// folder the run runs in, as homeInFolders prints it, and the home has no
// push-checks.json. A run without a wall composes the push checks into that home. A
// walled run with harness.home outside the checkout starts, mounts its home read-only
// and works in a copy of the checkout.
func TestAWalledRunWithPushChecksAndTheHomeInTheCheckoutStops(t *testing.T) {
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

// TestWalledRunWithoutPushChecksStartsAsBefore is a walled run whose modules declare no
// push check, with no --home and no harness.home: it composes into
// qory/homes/runs/<name>-<hash>/<run id> in the cache directory, that home has no
// push-checks.json while the agent's container starts, and the agent starts. A stand-in
// docker in front of the wall's reads the home from the agent's environment file.
func TestWalledRunWithoutPushChecksStartsAsBefore(t *testing.T) {
	root, _, _ := pushChecksCheckout(t, false)
	walledForager(t)
	file := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	docker := regexp.MustCompile(`(?m)^  command: (.+)$`).FindStringSubmatch(string(data))
	if docker == nil {
		t.Fatalf("forager.yaml has no wall command:\n%s", data)
	}
	seen := filepath.Join(tempDir(t), "seen")
	front := filepath.Join(tempDir(t), "docker")
	writeFile(t, front, `#!/bin/sh
if [ "$1" = run ]; then
	env=
	for a; do [ "$prev" = --env-file ] && env=$a; prev=$a; done
	home=$(sed -n 's/^QORY_HARNESS_HOME=//p' "$env" 2>/dev/null)
	if [ -n "$home" ]; then
		echo "home=$home" >> `+seen+`
		sed -n 's/^QORY_RUN_ID=/id=/p' "$env" >> `+seen+`
		test -d "$home" && echo composed >> `+seen+`
		test -e "$home/push-checks.json" && echo checks >> `+seen+`
	fi
fi
exec `+docker[1]+` "$@"
`)
	if err := os.Chmod(front, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, file, strings.Replace(string(data), "command: "+docker[1], "command: "+front, 1))
	if out, err := run(t, "run", "codex"); cmd.ExitCode(err) != 4 {
		t.Fatalf("a walled run without push checks: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	got, err := os.ReadFile(seen)
	if err != nil {
		t.Fatalf("the agent's container did not start: %v", err)
	}
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(got)), "\n") {
		key, value, _ := strings.Cut(line, "=")
		fields[key] = value
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	// The home's folder is as the cache directory gives it, or with its links resolved
	// when qory/homes existed before the run.
	homes := filepath.Join(cache, "qory", "homes")
	want := filepath.Join(homes, "runs", checkout.Key(root), fields["id"])
	resolvedWant := want
	if resolved, err := filepath.EvalSymlinks(homes); err == nil {
		resolvedWant = filepath.Join(resolved, "runs", checkout.Key(root), fields["id"])
	}
	if fields["id"] == "" || fields["home"] != want && fields["home"] != resolvedWant {
		t.Errorf("the agent's home is %q, want %q\n%s", fields["home"], want, got)
	}
	if _, ok := fields["composed"]; !ok {
		t.Errorf("the home was not there when the agent's container started:\n%s", got)
	}
	if _, ok := fields["checks"]; ok {
		t.Errorf("a harness without push checks has push-checks.json in %s", fields["home"])
	}
}
