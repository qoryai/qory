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

// fastProposal is a proposal whose ## Track puts the change on the fast track as "a document".
var fastProposal = "# Fix the guide\n\n## Summary\n\nFix a typo in the guide.\n\n## Track\n\n" +
	"Fast track: a document. Fix a typo in the guide.\n\nReason: The change keeps what the product does.\n"

// harnessRig runs the harness scope check in a container: the program, a temporary folder
// and the checker.
type harnessRig struct {
	t       *testing.T
	program string
	top     string
	c       *pushcheck.Checker
}

// newHarnessRig skips the test unless QORY_TEST_DOCKER=1, QORY_TEST_GIT_IMAGE and
// QORY_TEST_HARNESS are set and the engine has the image.
func newHarnessRig(t *testing.T) *harnessRig {
	t.Helper()
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
	top, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &harnessRig{t: t, top: top,
		program: filepath.Join(home, "modules/software-factory/skills/running-a-software-workflow/workflow-checks.cjs"),
		c:       &pushcheck.Checker{Home: home, Image: image, Label: "dev.qory.run=run-scope-check-test", Limits: wall.Limits{CPUs: "1", Memory: "512m"}}}
}

func (r *harnessRig) write(dir, name, text string) {
	r.t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *harnessRig) git(dir string, args ...string) string {
	r.t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...).CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes text to the file name in work and commits it with message.
func (r *harnessRig) commit(work, name, text, message string) {
	r.t.Helper()
	r.write(work, name, text)
	r.git(work, "add", "-A")
	r.git(work, "commit", "--quiet", "-m", message)
}

// scope clones the commits of work from start to HEAD into the folder clone, runs the scope
// check on it with snapshot, and checks that the clone can be removed after it.
func (r *harnessRig) scope(step, work, clone, snapshot, start string) pushcheck.ScopeEnd {
	r.t.Helper()
	clone = filepath.Join(r.top, clone)
	if err := preparation.ScopeClone(context.Background(), os.Environ(), work, clone, preparation.WorkRepository{Commit: start}, r.git(work, "rev-parse", "HEAD")); err != nil {
		r.t.Fatal(err)
	}
	end, err := r.c.Scope(context.Background(), []string{r.program, "scope", "--plan"}, clone, snapshot, start, pushcheck.ScopeUser())
	if err != nil {
		r.t.Fatal(err)
	}
	if err := os.RemoveAll(clone); err != nil {
		r.t.Errorf("%s: the clone cannot be removed: %v", step, err)
	}
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		r.t.Errorf("%s: the clone is left: %v", step, err)
	}
	return end
}

// TestTheHarnessScopeCheckRunsOnAFreshClone runs the scope check of build-plan of the
// harness checkout that QORY_TEST_HARNESS names, workflow-checks.cjs scope --plan, in a
// container from the image QORY_TEST_GIT_IMAGE names, which has git and node: commits in
// the plan pass; a file outside it and a planted key each exit 1. After each check the
// clone can be removed. It runs only with QORY_TEST_DOCKER=1, both set and docker present.
func TestTheHarnessScopeCheckRunsOnAFreshClone(t *testing.T) {
	r := newHarnessRig(t)
	snapshot := filepath.Join(r.top, "snapshot")
	r.write(snapshot, "build-plan/main/v1/build-plan.md", harnessPlan)
	r.write(snapshot, "index.json", `{"artifacts":[{"type":"build-plan","name":"main","approved_version":null,"newest_version":1,`+
		`"versions":[{"id":"ver_1","number":1,"writer":{"kind":"run","run_id":"run_1"},"files":[]}]}]}`+"\n")
	work := filepath.Join(r.top, "work")
	r.write(work, "src/a.js", "exports.add = (a, b) => a + b;\n")
	r.write(work, "qory.yaml", "harness: {}\n")
	r.git(work, "init", "--quiet")
	r.git(work, "add", "-A")
	r.git(work, "commit", "--quiet", "-m", "start")
	start := r.git(work, "rev-parse", "HEAD")
	for i, step := range []struct {
		name, file, text string
		exit             int
		line             string
	}{
		{"in the plan", "src/a.js", "exports.add = (a, b) => b + a;\n", 0, ""},
		{"outside the plan", "src/b.js", "exports.b = 1;\n", 1, `Take "src/b.js" out of the commits since the base: it is outside the scope.`},
		{"a planted key", "src/a.js", "const k = 'ghp_" + strings.Repeat("aB3dE5fG7h", 3) + "aB3dE5';\n", 1, "without a GitHub token"},
	} {
		r.git(work, "reset", "--quiet", "--hard", start)
		r.commit(work, step.file, step.text, step.name)
		end := r.scope(step.name, work, "clone-"+string(rune('a'+i)), snapshot, start)
		if end.ExitCode != step.exit || !strings.Contains(strings.Join(end.Lines, "\n"), step.line) {
			t.Errorf("%s: %+v, want exit %d with %q", step.name, end, step.exit, step.line)
		}
	}
}

// TestTheHarnessScopeCheckProvesAFastTrackKindOnAFreshClone runs the same scope check for a
// session on the fast track: the snapshot has an approved proposal whose ## Track declares
// the kind "a document", and no build plan. The harness then proves the commits against that
// kind. A commit that changes only README.md exits 0; with no build plan, the scope check of
// a plan exits 1, so exit 0 shows that the kind's proof ran. A second commit that changes
// src/a.js, code beside the declared kind, exits 1 with the line that takes the file out.
func TestTheHarnessScopeCheckProvesAFastTrackKindOnAFreshClone(t *testing.T) {
	r := newHarnessRig(t)
	snapshot := filepath.Join(r.top, "snapshot")
	r.write(snapshot, "proposal/main/v1/proposal.md", fastProposal)
	r.write(snapshot, "index.json", `{"artifacts":[{"type":"proposal","name":"main","approved_version":"ver_1","newest_version":1,`+
		`"versions":[{"id":"ver_1","number":1,"writer":{"kind":"run","run_id":"run_1"},"files":[]}]}]}`+"\n")
	work := filepath.Join(r.top, "work")
	r.write(work, "README.md", "# Guide\n\nOpen from 9 to 2O.\n")
	r.write(work, "src/a.js", "exports.add = (a, b) => a + b;\n")
	r.git(work, "init", "--quiet")
	r.git(work, "add", "-A")
	r.git(work, "commit", "--quiet", "-m", "start")
	start := r.git(work, "rev-parse", "HEAD")
	fixed := [2]string{"README.md", "# Guide\n\nOpen from 9 to 20.\n"}
	for i, step := range []struct {
		name    string
		commits [][2]string
		exit    int
		line    string
	}{
		{"a document", [][2]string{fixed}, 0, ""},
		{"code beside the kind", [][2]string{fixed, {"src/a.js", "exports.add = (a, b) => b + a;\n"}}, 1,
			`Take "src/a.js" out of the commits since the base: it is not a document file.`},
	} {
		r.git(work, "reset", "--quiet", "--hard", start)
		for _, file := range step.commits {
			r.commit(work, file[0], file[1], step.name)
		}
		end := r.scope(step.name, work, "fast-clone-"+string(rune('a'+i)), snapshot, start)
		if end.ExitCode != step.exit || !strings.Contains(strings.Join(end.Lines, "\n"), step.line) {
			t.Errorf("%s: %+v, want exit %d with %q", step.name, end, step.exit, step.line)
		}
	}
}
