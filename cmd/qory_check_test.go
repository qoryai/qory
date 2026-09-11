package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// appStack is a checkout's own stack on the module [writeOwnModule] writes.
const appStack = "apiVersion: qory.ai/v1alpha1\ntarget: {runtime: claude}\nmodules:\n  - name: app\n    source: {path: modules/app}\n"

// writeOwnModule writes the app module a checkout's own stack names.
func writeOwnModule(t *testing.T, root string) {
	t.Helper()
	writeManifest(t, filepath.Join(root, "modules", "app"), "app")
	writeFile(t, filepath.Join(root, "modules", "app", "skills", "deploy", "SKILL.md"), "# deploy\n")
}

// writeAppCheckout writes the app module and the checkout's qory.yaml holding [appStack]
// under harness and the qory key at the top.
func writeAppCheckout(t *testing.T, root, qory string) {
	t.Helper()
	writeOwnModule(t, root)
	writeOwnStack(t, root, appStack)
	configure(t, root, nil, []string{"qory: \"" + qory + "\""})
}

// TestQoryKeyRefusesAQoryOutsideTheRange is the check on a release build: the checkout's
// qory.yaml wants a newer qory, the compose stops before writing with exit 5 and names the
// file, the version and the range; a qory in the range composes.
func TestQoryKeyRefusesAQoryOutsideTheRange(t *testing.T) {
	root := newCheckout(t)
	writeAppCheckout(t, root, ">=0.3.0 <0.4.0")

	release(t, "0.2.1")
	out, err := run(t, "harness", "compose")
	if err == nil {
		t.Fatalf("composed:\n%s", out)
	}
	if got := cmd.ExitCode(err); got != cmd.ExitVersion {
		t.Errorf("exit %d, want %d", got, cmd.ExitVersion)
	}
	wants(t, err.Error(), "qory.yaml: this qory is 0.2.1, and the file wants >=0.3.0 <0.4.0; install a qory in that range, or ask its owner")
	gone(t, root, ".qory", ".claude")

	release(t, "0.3.4")
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, err := run(t, "config"); err != nil || !strings.Contains(out, ">=0.3.0 <0.4.0") {
		t.Errorf("config does not show the range: %v\n%s", err, out)
	}
}

// TestQoryKeyOfTheBaseStackIsChecked is the case the key exists for: the base stack a
// checkout extends states the qory it needs, and every checkout extending it is refused
// on an older qory with the base named.
func TestQoryKeyOfTheBaseStackIsChecked(t *testing.T) {
	url := operatorRepoWith(t, "qory: \">=0.3.0\"\n"+baseStack)
	root := customerCheckout(t, url)
	release(t, "0.2.1")
	out, err := run(t, "harness", "compose")
	if err == nil {
		t.Fatalf("composed:\n%s", out)
	}
	if got := cmd.ExitCode(err); got != cmd.ExitVersion {
		t.Errorf("exit %d, want %d", got, cmd.ExitVersion)
	}
	wants(t, err.Error(), "qory.yaml: this qory is 0.2.1, and the base stack nextjs-15@", " wants >=0.3.0")
	gone(t, root, ".qory", ".claude")
}

// TestQoryKeyIsNotCheckedOnASourceBuild is a build between tags, which has no version
// to compare: the compose goes through and one row says the range was not checked, once
// for a qory.yaml read as the configuration and as the stack.
func TestQoryKeyIsNotCheckedOnASourceBuild(t *testing.T) {
	root := newCheckout(t)
	writeAppCheckout(t, root, ">=0.3.0")
	out, err := run(t, "harness", "compose")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "not checked against >=0.3.0 in ")
	if n := strings.Count(out, "not checked"); n != 1 {
		t.Errorf("%d rows say not checked, want 1:\n%s", n, out)
	}
}

// TestQoryKeyRefusesWorktreeAdd is worktree add on a release build outside the range: the
// worktree is not made, since its compose would refuse the same file.
func TestQoryKeyRefusesWorktreeAdd(t *testing.T) {
	root := newCheckout(t)
	writeAppCheckout(t, root, ">=0.3.0")
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "stack")
	release(t, "0.2.1")
	_, err := run(t, "worktree", "add", "feature")
	if got := cmd.ExitCode(err); got != cmd.ExitVersion {
		t.Fatalf("exit %d for %v, want %d", got, err, cmd.ExitVersion)
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(root), "wt-feature")); statErr == nil {
		t.Error("the worktree was made")
	}
}
