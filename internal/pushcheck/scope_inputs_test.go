package pushcheck_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/pushcheck"
)

// TestScopeInputsSetsOnlyThePathsOfTheChecksContainer is an inputs.json with two entries of
// start_commits and a member that qory does not write: the copy has the work path for the entry
// of the checked folder and the files path for start_artifacts, and every other value is
// unchanged. Data that is no inputs.json comes back as it is.
func TestScopeInputsSetsOnlyThePathsOfTheChecksContainer(t *testing.T) {
	const data = `{"qory":{"start_commits":[{"path":"/run/app","commit":"a","more":1},{"path":"/run/lib","commit":"b"}],` +
		`"start_artifacts":"/run/start-artifacts","branch_base":{"commit":"c","tree":"d"}},"track":"fast-track"}`
	var got struct {
		Qory struct {
			StartCommits   []map[string]any `json:"start_commits"`
			StartArtifacts string           `json:"start_artifacts"`
			BranchBase     map[string]any   `json:"branch_base"`
		} `json:"qory"`
		Track string `json:"track"`
	}
	if err := json.Unmarshal(pushcheck.ScopeInputs([]byte(data), "/run/lib", "/in/artifacts", "/in/files"), &got); err != nil {
		t.Fatal(err)
	}
	q := got.Qory
	if q.StartArtifacts != "/in/files" || len(q.StartCommits) != 2 || q.StartCommits[0]["path"] != "/run/app" || q.StartCommits[0]["more"] != float64(1) ||
		q.StartCommits[1]["path"] != "/in/artifacts" || q.StartCommits[1]["commit"] != "b" || q.BranchBase["tree"] != "d" || got.Track != "fast-track" {
		t.Errorf("the copy is %+v, want the paths of the container for lib and the artifacts and the rest as it was", got)
	}
	for _, bad := range []string{``, `[]`, `{}`, `{"qory":{}}`, `{"qory":{"start_commits":{}}}`} {
		if out := pushcheck.ScopeInputs([]byte(bad), "/run/lib", "/in/artifacts", "/in/files"); string(out) != bad {
			t.Errorf("pushcheck.ScopeInputs(%q) is %q, want it unchanged", bad, out)
		}
	}
}

// TestScopeInputsFindsTheCheckedFolderThroughALink is a folder that the inputs list by its real
// path and that qory reaches through a link: the entry of that folder gets the work path, and
// the entry of another folder keeps its path.
func TestScopeInputsFindsTheCheckedFolderThroughALink(t *testing.T) {
	top, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real, other, link := filepath.Join(top, "real"), filepath.Join(top, "other"), filepath.Join(top, "link")
	for _, dir := range []string{real, other} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("no links here: %v", err)
	}
	data := `{"qory":{"start_commits":[{"path":"` + other + `","commit":"a"},{"path":"` + real + `","commit":"b"}],"start_artifacts":"` + top + `"}}`
	var got struct {
		Qory struct {
			StartCommits []struct{ Path, Commit string } `json:"start_commits"`
		} `json:"qory"`
	}
	if err := json.Unmarshal(pushcheck.ScopeInputs([]byte(data), link, "/in/artifacts", "/in/files"), &got); err != nil {
		t.Fatal(err)
	}
	if c := got.Qory.StartCommits; len(c) != 2 || c[0].Path != other || c[1].Path != "/in/artifacts" || c[1].Commit != "b" {
		t.Errorf("the entries are %+v, want %s unchanged and the entry of %s, reached as %s, at /in/artifacts", c, other, real, link)
	}
}

// TestScopeInputsKeepsAnEmptyStartArtifacts is a run with no launch snapshot, whose
// start_artifacts is "": the copy keeps it, since the check has no snapshot to read.
func TestScopeInputsKeepsAnEmptyStartArtifacts(t *testing.T) {
	const data = `{"qory":{"start_commits":[{"path":"/run/app","commit":"a"}],"start_artifacts":""}}`
	var got struct {
		Qory struct {
			StartCommits   []struct{ Path string } `json:"start_commits"`
			StartArtifacts *string                 `json:"start_artifacts"`
		} `json:"qory"`
	}
	if err := json.Unmarshal(pushcheck.ScopeInputs([]byte(data), "/run/app", "/in/artifacts", "/in/files"), &got); err != nil {
		t.Fatal(err)
	}
	if got.Qory.StartArtifacts == nil || *got.Qory.StartArtifacts != "" || len(got.Qory.StartCommits) != 1 || got.Qory.StartCommits[0].Path != "/in/artifacts" {
		t.Errorf("the copy is %+v, want start_artifacts \"\" and the entry at /in/artifacts", got)
	}
}

// TestTheHarnessScopeVerbAcceptsTheCopyOfTheInputs runs the scope verb of the harness
// checkout that QORY_TEST_HARNESS_DIR names, with node, on a fresh clone of a push of a plan type:
// with the inputs as [pushcheck.ScopeInputs] rewrites them for the folders that the verb runs in and
// reads, it exits 0 for a commit in the plan, and exits 3 for the inputs as qory wrote them,
// whose paths are the run's. The verb only reads the harness folder. It runs only with
// QORY_TEST_HARNESS_DIR set and node and git on the PATH.
func TestTheHarnessScopeVerbAcceptsTheCopyOfTheInputs(t *testing.T) {
	harness := os.Getenv("QORY_TEST_HARNESS_DIR")
	if harness == "" {
		t.Skip("QORY_TEST_HARNESS_DIR names no harness folder")
	}
	for _, tool := range []string{"node", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("no %s command", tool)
		}
	}
	program := filepath.Join(harness, "modules/software-factory/skills/running-a-software-workflow/workflow-checks.cjs")
	if _, err := os.Stat(program); err != nil {
		t.Fatalf("QORY_TEST_HARNESS_DIR has no scope verb: %v", err)
	}
	top, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
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
	plan := strings.Join([]string{"## Summary", "", "One deliverable.", "", "## Deliverables", "",
		"| Id | Change | Criteria | Files | Check |", "|---|---|---|---|---|",
		"| d-1 | Add the sum. | ac-1 | `src/a.js` | `tests/a.test.js` |", "",
		"## Commands", "", "| Kind | Command | Result file | Source |", "|---|---|---|---|",
		"| targeted | `node tool/runner.js` | `out/junit.xml` | repository: `package.json` |",
		"| lint | `node tool/lint.js` | none | repository: `package.json` |",
		"| types | none | none | The repository has no types. |",
		"| build | none | none | The repository has no build. |",
		"| full | `node tool/runner.js` | `out/junit.xml` | repository: `package.json` |",
		"| locked | `node tool/runner.js` | `out/junit.xml` | repository: `package.json` |", ""}, "\n")
	index := `{"artifacts":[{"type":"build-plan","name":"main","approved_version":null,"newest_version":1,` +
		`"versions":[{"id":"ver_1","number":1,"writer":{"kind":"run","run_id":"run_1"},"files":[]}]}]}` + "\n"
	runSnapshot, files := filepath.Join(top, "run", "start-artifacts"), filepath.Join(top, "check", "files")
	for _, dir := range []string{runSnapshot, files} {
		write(filepath.Join(dir, "build-plan/main/v1/build-plan.md"), plan)
		write(filepath.Join(dir, "index.json"), index)
	}
	work := filepath.Join(top, "run", "app")
	write(filepath.Join(work, "src/a.js"), "exports.add = (a, b) => a + b;\n")
	git(work, "init", "--quiet")
	git(work, "add", "-A")
	git(work, "commit", "--quiet", "-m", "start")
	start := git(work, "rev-parse", "HEAD")
	write(filepath.Join(work, "src/a.js"), "exports.add = (a, b) => b + a;\n")
	git(work, "commit", "--quiet", "-am", "change")
	clone := filepath.Join(top, "check", "artifacts")
	if err := preparation.ScopeClone(context.Background(), os.Environ(), work, clone, preparation.WorkRepository{Commit: start}, git(work, "rev-parse", "HEAD")); err != nil {
		t.Fatal(err)
	}
	original, err := json.Marshal(map[string]any{"qory": map[string]any{"harness_extensions": map[string]any{}, "outputs": []any{},
		"start_commits": []any{map[string]any{"path": work, "commit": start}}, "start_artifacts": runSnapshot}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		data []byte
		exit int
	}{
		{"the copy", pushcheck.ScopeInputs(original, work, clone, files), 0},
		{"the inputs as qory wrote them", original, 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			inputs := filepath.Join(t.TempDir(), "inputs.json")
			write(inputs, string(c.data))
			cmd := exec.Command("node", program, "scope", "--plan", "--inputs", inputs, "--base", start, "--artifacts", files)
			cmd.Dir = clone
			out, err := cmd.CombinedOutput()
			exit := 0
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				exit = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if exit != c.exit {
				t.Errorf("exit %d, want %d\n%s", exit, c.exit, out)
			}
		})
	}
}
