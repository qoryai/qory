package cmd_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/pushcheck"
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
	p.onSetCheck = func(c func(context.Context, session.RunAPIRequest) (*session.RunAPIRefusal, error)) error {
		check = c
		return nil
	}
	var refusal, verdict *session.RunAPIRefusal
	var checkErr, verdictErr error
	var parentMode fs.FileMode
	p.run(t, func(string) {
		if check == nil {
			t.Error("the walled run's open has no RunAPICheck")
			return
		}
		refusal, checkErr = check(context.Background(), session.RunAPIRequest{Path: "/artifacts", Body: specPush(t)})
		verdict, verdictErr = check(context.Background(), session.RunAPIRequest{Path: "/verdict", Body: []byte(`{"verdict":"passed"}`)})
		if info, err := os.Stat(filepath.Join(p.h.runDir, "push-checks")); err == nil {
			parentMode = info.Mode().Perm()
		}
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
	// The check's folders are in the run directory's push-checks, which the run's user,
	// another user, passes through and cannot list, and which is gone after the run.
	parent := filepath.Join(p.h.runDir, "push-checks")
	for _, dst := range []string{"/qory-push-check/artifacts", "/qory-push-check/files"} {
		folder := regexp.MustCompile(`src=(\S+),dst=` + dst + `,readonly`).FindStringSubmatch(created)
		if folder == nil || filepath.Dir(folder[1]) != parent {
			t.Errorf("the folder at %s is not in %s:\n%s", dst, parent, created)
		}
	}
	if parentMode != 0o711 {
		t.Errorf("%s had mode %o during the check, want 711", parent, parentMode)
	}
	if _, err := os.Stat(parent); !os.IsNotExist(err) {
		t.Errorf("%s is left after the run: %v", parent, err)
	}
	for _, want := range []string{
		"--label dev.qory.run=" + p.runID, "--pids-limit 512",
		"--network none", "--user 1000:1000",
		"--entrypoint " + program + " example.com/agent:1 push spec /qory-push-check/files/spec.md",
	} {
		if !strings.Contains(created, want) {
			t.Errorf("the check's container lacks %q:\n%s", want, created)
		}
	}
}

// TestAWalledRunWithoutPushChecksChecksNothing is a walled run whose harness declares
// no push check: it sets no run API check, and no container of a check starts.
func TestAWalledRunWithoutPushChecksChecksNothing(t *testing.T) {
	docker, log := checkDocker(t, 1, "a finding\n")
	p := newPushRepo(t, "write")
	walledBy(t, docker)
	p.flags = []string{"--home", filepath.Join(tempDir(t), "home")}
	r := p.run(t, func(dir string) { writeFile(t, filepath.Join(dir, "left.txt"), "left\n") }, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if slices.Contains(r.calls, "set run API check") {
		t.Errorf("the run set a run API check: %v", r.calls)
	}
	if got := checkLog(t, log); got != "" {
		t.Errorf("a check's container ran:\n%s", got)
	}
}

// TestAnUnwalledRunSetsNoCheck is an unwalled run whose harness declares push checks:
// it sets no run API check, and its push step stores the version unchecked.
func TestAnUnwalledRunSetsNoCheck(t *testing.T) {
	p, _ := newCheckedRepo(t, "")
	r := p.run(t, func(dir string) { writeFile(t, filepath.Join(dir, "left.txt"), "left\n") }, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if slices.Contains(r.calls, "set run API check") {
		t.Errorf("the unwalled run set a run API check: %v", r.calls)
	}
	if posts, _, stored := p.api.counts(); posts != 1 || stored != 1 {
		t.Errorf("%d POST and %d versions, want 1 and 1\n%s", posts, stored, r.out)
	}
}

// TestTheCheckIsSetBeforeTheSetupAndTheJoin is a walled run whose harness declares a
// check for spec: the run sets its run API check once, before the image, the setup and
// the join, and a push of spec from the setup container, which the check gets as the
// run's forwarder gives it, gets 422 with the check's finding.
func TestTheCheckIsSetBeforeTheSetupAndTheJoin(t *testing.T) {
	docker, _ := checkDocker(t, 1, "spec.md: a finding\n")
	p, _ := newCheckedRepo(t, docker)
	var check func(context.Context, session.RunAPIRequest) (*session.RunAPIRefusal, error)
	p.onSetCheck = func(c func(context.Context, session.RunAPIRequest) (*session.RunAPIRefusal, error)) error {
		check = c
		return nil
	}
	var fromSetup *session.RunAPIRefusal
	var setupErr error
	p.setup = func(string) {
		if check == nil {
			t.Error("the setup ran with no run API check set")
			return
		}
		fromSetup, setupErr = check(context.Background(), session.RunAPIRequest{Path: "/artifacts", Body: specPush(t)})
	}
	joined := false
	r := p.run(t, func(string) {
		joined = true
		if check == nil {
			t.Error("the session joined with no run API check set")
		}
	}, func() (*session.Result, error) { return &session.Result{State: event.StateFailed, ExitCode: 3}, nil })
	if !joined {
		t.Error("the session did not join")
	}
	if setupErr != nil || fromSetup == nil || fromSetup.Status != 422 || !slices.Equal(fromSetup.Findings, []string{"spec.md: a finding"}) {
		t.Errorf("the setup container's push got %+v, %v, want 422 with the finding", fromSetup, setupErr)
	}
	set := slices.Index(r.calls, "set run API check")
	if set < 0 || slices.Index(r.calls[set+1:], "set run API check") >= 0 {
		t.Fatalf("the run API check was set %v, want once", r.calls)
	}
	// The join of this test is a stand-in, which the calls do not list; the agent's
	// check covers it.
	for _, later := range []string{"fetch image", "setup"} {
		if i := slices.Index(r.calls, later); i < 0 || i < set {
			t.Errorf("%s at %d, the check's set at %d, want the set first: %v", later, i, set, r.calls)
		}
	}
}

// TestACheckThatCannotBeSetStopsTheRun is a walled run with push checks whose
// SetRunAPICheck fails: the run stops before the setup and the join, with exit 1 and
// the fix line.
func TestACheckThatCannotBeSetStopsTheRun(t *testing.T) {
	docker, _ := checkDocker(t, 0, "")
	p, _ := newCheckedRepo(t, docker)
	p.onSetCheck = func(func(context.Context, session.RunAPIRequest) (*session.RunAPIRefusal, error)) error {
		return errors.New("Call SetRunAPICheck before Setup: the setup started.")
	}
	joined := false
	r := p.run(t, func(string) { joined = true }, nil)
	r.wantsClose(t, event.StateFailed, 1)
	if slices.Contains(r.calls, "setup") || joined {
		t.Errorf("the run went on after the failed set: %v, joined %v", r.calls, joined)
	}
	wants(t, r.out, "qory run: Run again. Turning on the push checks for the POSTs from behind the wall failed, so the run stopped before the setup: Call SetRunAPICheck before Setup: the setup started.\n")
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
		"qory run: "+versionWays+", then take the work item in again: qory pushed qory/"+workItem+", and the version of the artifact branch was not stored: the version did not pass the check for branch; the code is used\n"+
			"qory run: The check for branch failed:\nqory run: branch.json: the ref is not the work item's\n")
	if strings.Contains(r.log(), "push again") || strings.Count(r.out, "The check for branch failed:") != 1 {
		t.Errorf("the step's lines have \"push again\", or the check's line not once:\n%s", r.out)
	}
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

// TestAHomeWhosePushChecksDoNotReadStopsTheRun is a walled run whose composed home has a
// push-checks.json that does not read: the run stops before the setup and the agent,
// with exit 2 and the fix line.
func TestAHomeWhosePushChecksDoNotReadStopsTheRun(t *testing.T) {
	docker, log := checkDocker(t, 0, "")
	p, _ := newCheckedRepo(t, docker)
	var file string
	t.Cleanup(cmd.SetLoadPushChecks(func(home string) ([]compose.PushCheck, error) {
		file = filepath.Join(home, "push-checks.json")
		if err := os.WriteFile(file, []byte("{"), 0o644); err != nil {
			return nil, err
		}
		return pushcheck.Load(home)
	}))
	setUp, joined := false, false
	p.setup = func(string) { setUp = true }
	r := p.run(t, func(string) { joined = true }, nil)
	r.wantsClose(t, event.StateFailed, cmd.ExitInput)
	if setUp || joined {
		t.Errorf("the setup ran %v, the agent ran %v, want neither", setUp, joined)
	}
	wants(t, r.out, "qory run: Compose the harness again, and run again. The push checks in "+file+" could not be read: it is not JSON in the form the compose writes: unexpected end of JSON input.\n")
	if got := checkLog(t, log); got != "" {
		t.Errorf("a check's container ran:\n%s", got)
	}
}

// TestACheckThatDidNotFinishIsAStatusLine is a push whose check's container the engine
// did not create: the check returns an error, for which Forager returns 503, and qory
// run writes it as one status line.
func TestACheckThatDidNotFinishIsAStatusLine(t *testing.T) {
	docker, _ := fakeDocker(t)
	script := filepath.Join(tempDir(t), "docker")
	writeFile(t, script, "#!/bin/sh\ncase \"$*\" in\n*qory-push-check-*)\n\techo 'Cannot connect to the Docker daemon at unix:///var/run/docker.sock' >&2\n\texit 1 ;;\nesac\nexec "+docker+" \"$@\"\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	p, _ := newCheckedRepo(t, script)
	var check func(context.Context, session.RunAPIRequest) (*session.RunAPIRefusal, error)
	p.onSetCheck = func(c func(context.Context, session.RunAPIRequest) (*session.RunAPIRefusal, error)) error {
		check = c
		return nil
	}
	var checkErr error
	r := p.run(t, func(string) {
		_, checkErr = check(context.Background(), session.RunAPIRequest{Path: "/artifacts", Body: specPush(t)})
	}, func() (*session.Result, error) { return &session.Result{State: event.StateFailed, ExitCode: 3}, nil })
	if checkErr == nil {
		t.Fatal("the check returned no error")
	}
	line := "qory run: the check before a push did not finish, and the push was not stored (503 push_check_unavailable): the engine did not create the check's container: Cannot connect to the Docker daemon at unix:///var/run/docker.sock\n"
	if strings.Count(r.out, line) != 1 {
		t.Errorf("the output does not have the line %q once:\n%s", line, r.out)
	}
}

// TestTheFetchOfTwoFilesThatAreOneFileHereFails is a version whose files A.md and a.md,
// or café.md with its é as one character and as e and an accent, are one file on this
// machine's file system, which is insensitive to case or to the form of an accent: the
// fetch of a push check fails with both names. On a file system that keeps them apart,
// both are there.
func TestTheFetchOfTwoFilesThatAreOneFileHereFails(t *testing.T) {
	inCheckout(t)
	for _, pair := range [][2]string{{"A.md", "a.md"}, {"caf\u00e9.md", "cafe\u0301.md"}} {
		server := startRunAPI(t)
		server.index = indexOf(artifactEntry("spec", "main", "", 1, versionEntry("av_1", 1, fileEntry(pair[0], "one\n"), fileEntry(pair[1], "two\n"))))
		server.files = map[string]string{"/artifacts/av_1/files/" + pair[0]: "one\n", "/artifacts/av_1/files/" + pair[1]: "two\n"}
		fetch, err := cmd.ArtifactsForCheck(server.URL + "/v1/runs/run-1")
		if err != nil {
			t.Fatal(err)
		}
		probe := t.TempDir()
		writeFile(t, filepath.Join(probe, pair[0]), "")
		_, statErr := os.Stat(filepath.Join(probe, pair[1]))
		dir := t.TempDir()
		err = fetch(context.Background(), dir, []string{"spec"})
		if statErr != nil {
			if err != nil {
				t.Errorf("%q on a file system that keeps them apart: %v", pair, err)
			}
			continue
		}
		want := fmt.Sprintf("the files %q and %q of the version av_1 are one file on this machine, whose file system is insensitive to case or to the form of an accent", pair[0], pair[1])
		if err == nil || err.Error() != want {
			t.Errorf("%q: the fetch = %v, want %q", pair, err, want)
		}
	}
}

// TestARunAPIThatFailsStopsARunWithPushChecks is a walled run from a run code, which has
// a control plane, whose push checks cannot reach the run API: the run stops before the
// setup and the agent, with exit 2 and the fix line, and sets no check.
func TestARunAPIThatFailsStopsARunWithPushChecks(t *testing.T) {
	docker, _ := checkDocker(t, 0, "")
	p, home := newCheckedRepo(t, docker)
	// The run API works for the run's reads before the compose, and fails once the
	// compose wrote the home's push checks.
	p.runAPIErr = func() error {
		if composed, _ := filepath.Glob(filepath.Join(home, "*", "push-checks.json")); len(composed) > 0 {
			return errors.New("Open another run to reach the run API: this run is closed.")
		}
		return nil
	}
	joined := false
	r := p.run(t, func(string) { joined = true }, nil)
	r.wantsClose(t, event.StateFailed, cmd.ExitInput)
	if joined || slices.Contains(r.calls, "setup") || slices.Contains(r.calls, "set run API check") {
		t.Errorf("the run went on: %v, joined %v", r.calls, joined)
	}
	wants(t, r.out, "qory run: Open the run through a gateway that sends the run API address, and run again. The push checks read the artifacts through the run API, which failed: Open another run to reach the run API: this run is closed.\n")
}
