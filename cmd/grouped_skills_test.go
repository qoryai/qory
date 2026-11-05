package cmd_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/module"
	"github.com/qoryai/qory/internal/render"
)

// TestRuntimeFoldersAreTheRegisteredRuntimes keeps the module package's list of runtime
// names equal to the runtimes this qory renders. It is here because the cmd package
// imports every runtime that the program registers, so a runtime added there and not to
// the list fails.
func TestRuntimeFoldersAreTheRegisteredRuntimes(t *testing.T) {
	if !slices.Equal(module.RuntimeFolders(), render.Names()) {
		t.Errorf("module.RuntimeFolders() is %v, and the registered runtimes are %v", module.RuntimeFolders(), render.Names())
	}
}

// writeGroupedCheckout is a checkout whose module app has the files, for claude.
func writeGroupedCheckout(t *testing.T, files map[string]string) string {
	t.Helper()
	root := newCheckout(t)
	writeFile(t, filepath.Join(root, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  target:\n    runtime: claude\n  modules:\n    - name: app\n      source: {path: modules/app}\n    - name: other\n      source: {path: modules/other}\n")
	writeFile(t, filepath.Join(root, "modules", "app", "qory-module.yaml"), "apiVersion: qory.dev/v1alpha1\nname: app\n")
	writeFile(t, filepath.Join(root, "modules", "other", "qory-module.yaml"), "apiVersion: qory.dev/v1alpha1\nname: other\n")
	for path, text := range files {
		writeFile(t, filepath.Join(root, path), text)
	}
	return root
}

// TestInspectAndTheReportShowAGroupedSkillsFlatName composes a module with a group: qory
// harness inspect and the report show skills/deploy, and neither shows a name with the group.
func TestInspectAndTheReportShowAGroupedSkillsFlatName(t *testing.T) {
	root := writeGroupedCheckout(t, map[string]string{"modules/app/skills/delivery/deploy/SKILL.md": "---\nname: deploy\ndescription: d\n---\nDeploy.\n"})
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err := run(t, "harness", "inspect")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "skills/deploy")
	lacks(t, out, "skills/delivery")
	data, err := os.ReadFile(filepath.Join(root, ".qory", "harness-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Entries []struct{ Kind, Name, Module string } `json:"entries"`
	}
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Entries) != 1 || rep.Entries[0].Kind != "skills" || rep.Entries[0].Name != "deploy" {
		t.Errorf("the report has %+v", rep.Entries)
	}
}

// TestTwoSkillsOfOneNameStopTheComposeWithTheirExit is the same name in one module, exit 2
// as a folder with no SKILL.md, and in two modules, the collision's exit 3.
func TestTwoSkillsOfOneNameStopTheComposeWithTheirExit(t *testing.T) {
	const s = "---\nname: review\ndescription: d\n---\n"
	writeGroupedCheckout(t, map[string]string{"modules/app/skills/a/review/SKILL.md": s, "modules/app/skills/b/review/SKILL.md": s})
	_, err := run(t, "harness", "compose")
	if err == nil || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("one module: %v (exit %d), want exit %d", err, cmd.ExitCode(err), cmd.ExitInput)
	}
	wants(t, err.Error(), "module app: rename skills/a/review or skills/b/review: each skill goes to skills/<name>, and both are the skill review")

	writeGroupedCheckout(t, map[string]string{"modules/app/skills/a/ok/SKILL.md": s, "modules/app/skills/a/nothing/notes.md": "n\n"})
	_, err = run(t, "harness", "compose")
	if err == nil || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("no SKILL.md: %v (exit %d), want exit %d", err, cmd.ExitCode(err), cmd.ExitInput)
	}
	wants(t, err.Error(), "module app: add SKILL.md to skills/a/nothing: skills/a/nothing has no SKILL.md and no skill below it")

	writeGroupedCheckout(t, map[string]string{"modules/app/skills/a/review/SKILL.md": s, "modules/other/skills/review/SKILL.md": s})
	out, err := run(t, "harness", "compose")
	if err == nil || cmd.ExitCode(err) != 3 {
		t.Fatalf("two modules: %v (exit %d), want exit 3\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out+err.Error(), "skills/review", "exclude: {skills: [review]}")
}
