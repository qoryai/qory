package cmd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
)

// TestAPushCheckReadsTheNewestVersionsInThePullLayout is the working folder of a push
// check of spec that reads questions and design: index.json byte for byte as qory
// artifact pull writes it, the newest version of each artifact of the three types under
// <type>/<name>/v<N>/, no folder for design, which has no version, and nothing of notes,
// which the check does not read.
func TestAPushCheckReadsTheNewestVersionsInThePullLayout(t *testing.T) {
	root := inCheckout(t)
	server := startRunAPI(t)
	server.index = indexOf(
		artifactEntry("spec", "main", "av_1", 2,
			versionEntry("av_1", 1, fileEntry("spec.md", "old\n")),
			versionEntry("av_2", 2, fileEntry("spec.md", "new\n"), fileEntry("notes.md", "notes\n"))),
		artifactEntry("spec", "other", "", 1, versionEntry("av_3", 1, fileEntry("spec.md", "other\n"))),
		artifactEntry("questions", "main", "", 1, versionEntry("av_4", 1, fileEntry("questions.json", "{}\n"))),
		artifactEntry("design", "main", "", 0),
		artifactEntry("notes", "main", "", 1, versionEntry("av_5", 1, fileEntry("notes.md", "n\n"))),
	)
	server.files = map[string]string{
		"/artifacts/av_1/files/spec.md":        "old\n",
		"/artifacts/av_2/files/spec.md":        "new\n",
		"/artifacts/av_2/files/notes.md":       "notes\n",
		"/artifacts/av_3/files/spec.md":        "other\n",
		"/artifacts/av_4/files/questions.json": "{}\n",
		"/artifacts/av_5/files/notes.md":       "n\n",
	}
	fetch, err := cmd.ArtifactsForCheck(server.URL + "/v1/runs/run-1")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := fetch(context.Background(), dir, []string{"spec", "questions", "design"}); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"index.json":                       server.index,
		"spec/main/v2/spec.md":             "new\n",
		"spec/main/v2/notes.md":            "notes\n",
		"spec/other/v1/spec.md":            "other\n",
		"questions/main/v1/questions.json": "{}\n",
	}
	got := map[string]string{}
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		data, _ := os.ReadFile(path)
		got[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("the working folder has\n%v\nwant\n%v", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "design")); !os.IsNotExist(err) {
		t.Errorf("design has a folder: %v", err)
	}
	if _, err := run(t, "artifact", "pull"); err != nil {
		t.Fatal(err)
	}
	pulled, err := os.ReadFile(filepath.Join(root, ".qory", "artifacts", "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(pulled) != got["index.json"] {
		t.Errorf("qory artifact pull writes index.json\n%s\nthe check's folder has\n%s", pulled, got["index.json"])
	}
}

// checksModule is a module that declares a push check for branch and one for spec,
// which reads questions; its program's #! line is for node.
var checksModule = map[string]string{
	"modules/checks/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: checks\npush_checks:\n" +
		"  - type: branch\n    command: [bin/check, push, branch]\n" +
		"  - type: spec\n    command: [bin/check, push, spec]\n    reads: [questions]\n",
	"modules/checks/bin/check":         "#!/usr/bin/env node\n",
	"modules/checks/skills/c/SKILL.md": "---\nname: c\ndescription: c\n---\n",
}

// newCheckedRepo is [newPushRepoWith] whose harness has [checksModule] beside hello,
// for a walled run with the engine docker and a home outside the checkout, which it
// returns; with docker "", for an unwalled run.
func newCheckedRepo(t *testing.T, docker string) (*pushRepo, string) {
	t.Helper()
	files := map[string]string{}
	for name, content := range checksModule {
		files[name] = content
	}
	stack := "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: hello\n    source: {path: modules/hello}\n" +
		"  - name: checks\n    source: {path: modules/checks}\nworkflows:\n  hello:\n    access: write\n"
	files["qory.yaml"] = ownStack(stack)
	p := newPushRepoWith(t, "write", files)
	if docker == "" {
		return p, ""
	}
	walledBy(t, docker)
	home := filepath.Join(tempDir(t), "home")
	p.flags = []string{"--home", home}
	return p, home
}

// checkDocker is the fake docker of [fakeDocker], with the commands of a push check's
// container: the create, the start, which prints out and exits code, the inspect,
// which reports that exit, and the removal. Each of those goes to the log it returns.
func checkDocker(t *testing.T, code int, out string) (string, string) {
	t.Helper()
	docker, _ := fakeDocker(t)
	dir := tempDir(t)
	log := filepath.Join(dir, "checks.log")
	output := filepath.Join(dir, "output")
	writeFile(t, output, out)
	script := filepath.Join(dir, "docker")
	writeFile(t, script, `#!/bin/sh
case "$*" in
*qory-push-check-*)
	echo "$*" >> `+log+`
	case "$1" in
	create) echo c0ffee ;;
	start) cat `+output+`; exit `+strconv.Itoa(code)+` ;;
	inspect) echo '{"Status":"exited","ExitCode":`+strconv.Itoa(code)+`,"Error":""}' ;;
	esac
	exit 0 ;;
esac
exec `+docker+` "$@"
`)
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	return script, log
}

// checkLog is the commands of the push checks' containers, "" for none.
func checkLog(t *testing.T, log string) string {
	t.Helper()
	data, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// specPush is the body of a push of the artifact spec with one file.
func specPush(t *testing.T) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"type": "spec", "name": "main", "files": []map[string]any{
		{"filename": "spec.md", "content_type": "text/markdown", "content_base64": []byte("# Spec\n")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// TestTheAgentsPushIsCheckedBeforeItLeaves is a walled run whose harness declares a
// check for spec: the run's open gets the check, a push of spec from behind the wall
// gets 422 push_check_failed with the check's findings, a verdict passes unchecked, and
// the check's container starts from the run's image, as the wall's user, with the
// composed home read-only at its path.
func TestTheAgentsPushIsCheckedBeforeItLeaves(t *testing.T) {
	docker, log := checkDocker(t, 1, "spec.md: the section Goal is missing\n")
	p, home := newCheckedRepo(t, docker)
	var check func(context.Context, session.RunAPIRequest) (*session.RunAPIRefusal, error)
	p.onOpen = func(spec session.OpenSpec) { check = spec.RunAPICheck }
	var refusal, verdict *session.RunAPIRefusal
	var checkErr, verdictErr error
	p.run(t, func(string) {
		if check == nil {
			t.Error("the walled run's open has no RunAPICheck")
			return
		}
		refusal, checkErr = check(context.Background(), session.RunAPIRequest{Path: "/artifacts", Body: specPush(t)})
		verdict, verdictErr = check(context.Background(), session.RunAPIRequest{Path: "/verdict", Body: []byte(`{"verdict":"passed"}`)})
	}, func() (*session.Result, error) { return &session.Result{State: event.StateFailed, ExitCode: 3}, nil })
	if checkErr != nil || refusal == nil {
		t.Fatalf("the push of spec got %+v, %v, want a refusal\n%s", refusal, checkErr, checkLog(t, log))
	}
	if refusal.Status != 422 || refusal.Code != "push_check_failed" || refusal.Message != "Fix each finding below and push again. The check for spec failed:" ||
		len(refusal.Findings) != 1 || refusal.Findings[0] != "spec.md: the section Goal is missing" {
		t.Errorf("the refusal is %+v", refusal)
	}
	if verdict != nil || verdictErr != nil {
		t.Errorf("the verdict got %+v, %v, want nil, nil", verdict, verdictErr)
	}
	created := ""
	for _, line := range strings.Split(checkLog(t, log), "\n") {
		if strings.HasPrefix(line, "create ") {
			created = line
		}
	}
	// The run's home is the folder of its checkout under --home, mounted at its path.
	composed := regexp.MustCompile(`--mount type=bind,src=(\S+),dst=(\S+),readonly --mount`).FindStringSubmatch(created)
	if composed == nil || composed[1] != composed[2] || filepath.Dir(composed[1]) != home {
		t.Fatalf("the check's container does not mount the run's home under %s read-only at its path:\n%s", home, created)
	}
	program := filepath.Join(composed[1], "modules", "checks", "bin", "check")
	for _, want := range []string{
		"--network none", "--user 1000:1000",
		"--entrypoint " + program + " example.com/agent:1 push spec /qory-push-check/files/spec.md",
	} {
		if !strings.Contains(created, want) {
			t.Errorf("the check's container lacks %q:\n%s", want, created)
		}
	}
}

// TestAWalledRunWithoutPushChecksChecksNothing is a walled run whose harness declares
// no push check: its POSTs all pass, and no container of a check starts.
func TestAWalledRunWithoutPushChecksChecksNothing(t *testing.T) {
	docker, log := checkDocker(t, 1, "a finding\n")
	p := newPushRepo(t, "write")
	walledBy(t, docker)
	p.flags = []string{"--home", filepath.Join(tempDir(t), "home")}
	var check func(context.Context, session.RunAPIRequest) (*session.RunAPIRefusal, error)
	p.onOpen = func(spec session.OpenSpec) { check = spec.RunAPICheck }
	var refusal *session.RunAPIRefusal
	var err error
	r := p.run(t, func(string) {
		refusal, err = check(context.Background(), session.RunAPIRequest{Path: "/artifacts", Body: specPush(t)})
	}, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if refusal != nil || err != nil {
		t.Errorf("the push got %+v, %v, want nil, nil", refusal, err)
	}
	if got := checkLog(t, log); got != "" {
		t.Errorf("a check's container ran:\n%s", got)
	}
}

// TestAnUnwalledRunSetsNoCheck is an unwalled run whose harness declares push checks:
// its open gets no check, and its push step stores the version unchecked.
func TestAnUnwalledRunSetsNoCheck(t *testing.T) {
	p, _ := newCheckedRepo(t, "")
	opened := false
	p.onOpen = func(spec session.OpenSpec) {
		opened = true
		if spec.RunAPICheck != nil {
			t.Error("an unwalled run's open has a RunAPICheck")
		}
	}
	r := p.run(t, func(dir string) { writeFile(t, filepath.Join(dir, "left.txt"), "left\n") }, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if !opened {
		t.Error("the unwalled run did not open")
	}
	if posts, _, stored := p.api.counts(); posts != 1 || stored != 1 {
		t.Errorf("%d POST and %d versions, want 1 and 1\n%s", posts, stored, r.out)
	}
}

// TestThePushStepChecksItsVersion is a walled run whose harness declares a check for
// branch: a check that exits 1 stops the step before its POST, the step's lines have
// the fix line and the findings, and the run fails as on another push failure.
func TestThePushStepChecksItsVersion(t *testing.T) {
	docker, log := checkDocker(t, 1, "branch.json: the ref is not the work item's\n")
	p, _ := newCheckedRepo(t, docker)
	r := p.run(t, func(dir string) { writeFile(t, filepath.Join(dir, "left.txt"), "left\n") }, nil)
	r.wantsClose(t, event.StateFailed, 1)
	if posts, _, _ := p.api.counts(); posts != 0 {
		t.Errorf("%d POST, want none", posts)
	}
	if _, state := r.step(); state != event.StateFailed {
		t.Errorf("the push step ended %s, want failed\n%s", state, r.out)
	}
	wants(t, r.log(),
		"qory run: Fix each finding below and push again. The check for branch failed:\nqory run: branch.json: the ref is not the work item's\n",
		"qory run: "+versionWays+", then take the work item in again: qory pushed qory/"+workItem+", and the version of the artifact branch was not stored: the version did not pass the check for branch; the code is used")
	if !strings.Contains(checkLog(t, log), "/qory-push-check/files/branch.json") {
		t.Errorf("the check did not get branch.json:\n%s", checkLog(t, log))
	}
}

// TestThePushStepStoresAVersionThatPasses is a walled run whose check of branch exits 0:
// the step stores the version.
func TestThePushStepStoresAVersionThatPasses(t *testing.T) {
	docker, log := checkDocker(t, 0, "")
	p, _ := newCheckedRepo(t, docker)
	r := p.run(t, func(dir string) { writeFile(t, filepath.Join(dir, "left.txt"), "left\n") }, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if posts, _, stored := p.api.counts(); posts != 1 || stored != 1 {
		t.Errorf("%d POST and %d versions, want 1 and 1\n%s", posts, stored, r.out)
	}
	if !strings.Contains(checkLog(t, log), "/qory-push-check/files/branch.json") {
		t.Errorf("the check did not run:\n%s", checkLog(t, log))
	}
}
