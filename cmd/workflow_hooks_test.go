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
	data, err := os.ReadFile(filepath.Join(root, ".qory", "harness", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Hooks []map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Hooks) != 1 || got.Hooks[0]["workflow"] != "review" || got.Hooks[0]["at"] != "after-review-round" || got.Hooks[0]["timeout_seconds"] != float64(600) {
		t.Errorf("hooks.json:\n%s", data)
	}

	writeFile(t, filepath.Join(root, "qory.yaml"), consumerCompose(url, hook("after-merge")))
	_, err = run(t, "harness", "compose")
	want := "Set hooks[0].at to a hook point of the base stack nextjs-15@"
	if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), ": after-review-round; after-merge is not one.") || cmd.ExitCode(err) != 2 {
		t.Errorf("err = %v, exit %d; want %q with exit 2", err, cmd.ExitCode(err), want)
	}
}
