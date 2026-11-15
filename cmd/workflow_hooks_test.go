package cmd_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// hooksStack is a base stack whose workflow review takes check hooks after each review
// round, at most 3 at the point.
const hooksStack = `apiVersion: qory.dev/v1alpha1
name: nextjs-15
modules:
  - name: core
  - name: tools
workflows:
  review:
    hook_points:
      after-review-round: {at: end, kinds: [check]}
extending:
  kinds: [skills, agents]
  instructions: true
  settings: [permissions.allow]
  hooks: {per_point: 3}
`

// TestComposeWritesTheWorkflowHooks is a checkout that adds a check after each review
// round of the base's workflow: the compose writes hooks.json into the home, and a hook
// at a point the base does not declare fails the compose with exit 2 and the fix.
func TestComposeWritesTheWorkflowHooks(t *testing.T) {
	url := baseRepoWith(t, hooksStack)
	root := consumerCheckout(t, url)
	writeFile(t, filepath.Join(root, "scripts", "check-licences.sh"), "#!/bin/sh\nexit 0\n")
	hook := func(at string) string {
		return "  hooks:\n    - {at: " + at + ", kind: check, run: scripts/check-licences.sh --all}\n"
	}
	writeFile(t, filepath.Join(root, "qory.yaml"), consumerCompose(url, hook("after-review-round")))
	if _, err := run(t, "harness", "compose"); err != nil {
		t.Fatal(err)
	}
	if hooks := readHooks(t, filepath.Join(root, ".qory", "harness")); len(hooks) != 1 || hooks[0]["workflow"] != "review" || hooks[0]["at"] != "after-review-round" || hooks[0]["timeout_seconds"] != float64(600) {
		t.Errorf("hooks: %v", hooks)
	}

	writeFile(t, filepath.Join(root, "qory.yaml"), consumerCompose(url, hook("after-merge")))
	_, err := run(t, "harness", "compose")
	want := "Set hooks[0].at to a hook point of the base stack.\nThe base stack nextjs-15@"
	if err == nil || !strings.HasPrefix(err.Error(), want) || !strings.HasSuffix(err.Error(), " declares after-review-round; after-merge is not one.") || cmd.ExitCode(err) != 2 {
		t.Errorf("err = %v, exit %d; want %q with exit 2", err, cmd.ExitCode(err), want)
	}
}

// readHooks is the hooks of hooks.json in the home at home.
func readHooks(t *testing.T, home string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Hooks []map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	return got.Hooks
}

// TestComposeTakesTheHooksUnderTheFlag is a checkout whose qory.yaml contains its target
// and its workflow hooks and no extends: compose -f <stack> composes the hooks on that
// stack and writes hooks.json.
func TestComposeTakesTheHooksUnderTheFlag(t *testing.T) {
	url := baseRepoWith(t, hooksStack)
	stackFile := filepath.Join(strings.TrimPrefix(url, "file://"), "nextjs-15", "qory-stack.yaml")
	root := newCheckout(t)
	writeFile(t, filepath.Join(root, "scripts", "check-licences.sh"), "#!/bin/sh\nexit 0\n")
	writeFile(t, filepath.Join(root, "qory.yaml"), "harness:\n  target: {runtime: claude}\n  hooks:\n    - {at: after-review-round, kind: check, run: scripts/check-licences.sh, timeout: 2m}\n")
	if _, err := run(t, "harness", "compose", "-f", stackFile); err != nil {
		t.Fatal(err)
	}
	hooks := readHooks(t, filepath.Join(root, ".qory", "harness"))
	if len(hooks) != 1 || hooks[0]["at"] != "after-review-round" || hooks[0]["timeout_seconds"] != float64(120) {
		t.Errorf("hooks: %v", hooks)
	}
}

// TestComposeKeepsAnAncestorsHooksInTheCheckout is a qory.yaml in the folder above the
// checkout, which covers several repositories: a program in the checkout is in
// hooks.json by its path from the checkout's root, and a program in another repository
// under the folder fails the compose with exit 2.
func TestComposeKeepsAnAncestorsHooksInTheCheckout(t *testing.T) {
	url := baseRepoWith(t, hooksStack)
	root := newCheckout(t)
	above := filepath.Dir(root)
	writeFile(t, filepath.Join(root, "scripts", "check-licences.sh"), "#!/bin/sh\nexit 0\n")
	writeFile(t, filepath.Join(above, "other", "check.sh"), "#!/bin/sh\nexit 0\n")
	doc := func(run string) string {
		return "harness:\n  extends: {git: " + url + ", ref: main, path: nextjs-15}\n  target: {runtime: claude}\n  hooks:\n    - {at: after-review-round, kind: check, run: " + run + "}\n"
	}
	writeFile(t, filepath.Join(above, "qory.yaml"), doc(filepath.Base(root)+"/scripts/check-licences.sh --all"))
	if _, err := run(t, "harness", "compose"); err != nil {
		t.Fatal(err)
	}
	hooks := readHooks(t, filepath.Join(root, ".qory", "harness"))
	if len(hooks) != 1 || len(hooks[0]["run"].([]any)) != 2 || hooks[0]["run"].([]any)[0] != "scripts/check-licences.sh" {
		t.Errorf("hooks: %v", hooks)
	}
	writeFile(t, filepath.Join(above, "qory.yaml"), doc("other/check.sh"))
	_, err := run(t, "harness", "compose")
	want := "Move the program into the checkout, or fix hooks[0].run.\nother/check.sh, from the directory of "
	if err == nil || !strings.HasPrefix(err.Error(), want) || cmd.ExitCode(err) != 2 {
		t.Errorf("err = %v, exit %d; want %q with exit 2", err, cmd.ExitCode(err), want)
	}
}
