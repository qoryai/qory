package pushcheck_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/pushcheck"
)

// harnessPlan is a build plan whose deliverables list src/a.js, lib/, qory.yaml and hooks/.
var harnessPlan = strings.Join([]string{"## Summary", "", "Two deliverables.", "", "## Deliverables", "",
	"| Id | Change | Criteria | Files | Check |", "|---|---|---|---|---|",
	"| d-1 | Add the sum. | ac-1 | `src/a.js`, `lib/` | `tests/a.test.js` |",
	"| d-2 | Change the settings. | ac-2 | `qory.yaml`, `hooks/` | `tests/a.test.js` |", "",
	"## Commands", "", "| Kind | Command | Result file | Source |", "|---|---|---|---|",
	"| targeted | `node tool/runner.js` | `out/junit.xml` | repository: `package.json` |",
	"| lint | `node tool/lint.js` | none | repository: `package.json` |",
	"| types | none | none | The repository has no types. |",
	"| build | none | none | The repository has no build. |",
	"| full | `node tool/runner.js` | `out/junit.xml` | repository: `package.json` |",
	"| locked | `node tool/runner.js` | `out/junit.xml` | repository: `package.json` |", ""}, "\n")

// TestTheHarnessScopeCheckRunsOnAFreshClone runs the scope check of build-plan of the
// harness checkout that QORY_TEST_HARNESS names, workflow-checks.cjs scope --plan, in a
// container from the image QORY_TEST_GIT_IMAGE names, which has git and node: commits in
// the plan pass; a file outside it and a planted key each exit 1. After each check the
// clone can be removed. It runs only with QORY_TEST_DOCKER=1, both set and docker present.
func TestTheHarnessScopeCheckRunsOnAFreshClone(t *testing.T) {
	image, home := os.Getenv("QORY_TEST_GIT_IMAGE"), os.Getenv("QORY_TEST_HARNESS")
	switch {
	case os.Getenv("QORY_TEST_DOCKER") != "1":
		t.Skip("QORY_TEST_DOCKER=1 runs it")
	case image == "" || home == "":
		t.Skip("QORY_TEST_GIT_IMAGE and QORY_TEST_HARNESS name no image or harness")
	}
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		t.Skipf("the engine does not hold %s", image)
	}
	program := filepath.Join(home, "modules/software-factory/skills/running-a-software-workflow/workflow-checks.cjs")
	top, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write := func(dir, name, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	snapshot := filepath.Join(top, "snapshot")
	write(snapshot, "build-plan/main/v1/build-plan.md", harnessPlan)
	write(snapshot, "index.json", `{"artifacts":[{"type":"build-plan","name":"main","approved_version":null,"newest_version":1,`+
		`"versions":[{"id":"ver_1","number":1,"writer":{"kind":"run","run_id":"run_1"},"files":[]}]}]}`+"\n")
	work := filepath.Join(top, "work")
	write(work, "src/a.js", "exports.add = (a, b) => a + b;\n")
	write(work, "qory.yaml", "harness: {}\n")
	git(work, "init", "--quiet")
	git(work, "add", "-A")
	git(work, "commit", "--quiet", "-m", "start")
	start := git(work, "rev-parse", "HEAD")
	c := &pushcheck.Checker{Home: home, Image: image, Label: "dev.qory.run=run-scope-check-test", Limits: wall.Limits{CPUs: "1", Memory: "512m"}}
	for i, step := range []struct {
		name, file, text string
		exit             int
		line             string
	}{
		{"in the plan", "src/a.js", "exports.add = (a, b) => b + a;\n", 0, ""},
		{"outside the plan", "src/b.js", "exports.b = 1;\n", 1, `Take "src/b.js" out of the commits since the base: it is outside the scope.`},
		{"a planted key", "src/a.js", "const k = 'ghp_" + strings.Repeat("aB3dE5fG7h", 3) + "aB3dE5';\n", 1, "without a GitHub token"},
	} {
		git(work, "reset", "--quiet", "--hard", start)
		write(work, step.file, step.text)
		git(work, "add", "-A")
		git(work, "commit", "--quiet", "-m", step.name)
		clone := filepath.Join(top, "clone-"+string(rune('a'+i)))
		if err := preparation.ScopeClone(context.Background(), os.Environ(), work, clone, preparation.WorkRepository{Commit: start}, git(work, "rev-parse", "HEAD")); err != nil {
			t.Fatal(err)
		}
		end, err := c.Scope(context.Background(), []string{program, "scope", "--plan"}, clone, snapshot, start, c.ScopeUser())
		if err != nil {
			t.Fatal(err)
		}
		if end.ExitCode != step.exit || !strings.Contains(strings.Join(end.Lines, "\n"), step.line) {
			t.Errorf("%s: %+v, want exit %d with %q", step.name, end, step.exit, step.line)
		}
		if err := os.RemoveAll(clone); err != nil {
			t.Errorf("%s: the clone cannot be removed: %v", step.name, err)
		}
		if _, err := os.Stat(clone); !os.IsNotExist(err) {
			t.Errorf("%s: the clone is left: %v", step.name, err)
		}
	}
}
