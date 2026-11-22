package cmd_test

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// scopeDocker is the fake docker of [fakeDocker] whose scope check's container, the one
// whose create has --base, copies index.json of its snapshot beside the log, and runs as start: "exit N" prints out and exits N, "sleep" runs past
// any limit, and "engine" fails the create as an engine that is not reachable. The create
// of each scope check goes to the log it returns.
func scopeDocker(t *testing.T, start, out string) (string, string) {
	t.Helper()
	docker, _ := fakeDocker(t)
	dir := tempDir(t)
	log := filepath.Join(dir, "scope.log")
	writeFile(t, filepath.Join(dir, "output"), out)
	code := "0"
	if n, ok := strings.CutPrefix(start, "exit "); ok {
		code = n
	}
	script := filepath.Join(dir, "docker")
	writeFile(t, script, `#!/bin/sh
case "$1 $*" in
"create "*" --base "*)
	echo "$*" >> `+log+`
	files=$(echo "$*" | sed -n 's/.*src=\([^,]*\),dst=\/qory-push-check\/files,.*/\1/p')
	cp "$files/index.json" `+dir+`/index.json
	: > `+dir+`/scope-$3
	if [ "`+start+`" = engine ]; then echo "Cannot connect to the Docker daemon" >&2; exit 1; fi
	echo c0ffee; exit 0 ;;
esac
case "$1" in
start)
	test -e `+dir+`/scope-$3 || exec `+docker+` "$@"
	if [ "`+start+`" = sleep ]; then exec sleep 30; fi
	cat `+dir+`/output; exit `+code+` ;;
inspect)
	test -e `+dir+`/scope-$4 || exec `+docker+` "$@"
	echo '{"Status":"exited","ExitCode":`+code+`,"Error":""}'; exit 0 ;;
esac
exec `+docker+` "$@"
`)
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	return script, log
}

// scopedRepo is a [newPushRepo] at write access, whose workflow has the plan type
// change-plan, behind the wall of docker.
func scopedRepo(t *testing.T, docker string) *pushRepo {
	t.Helper()
	p := newPushRepo(t, "write")
	walledBy(t, docker)
	p.flags = []string{"--home", filepath.Join(tempDir(t), "home")}
	return p
}

// leavesApp is a session that leaves src/app.go.
func leavesApp(t *testing.T) func(string) {
	return func(dir string) { writeFile(t, filepath.Join(dir, "src", "app.go"), "package app\n") }
}

// wantsNoPush fails the test unless r closed failed with code and exit 1, its output has
// want, and the forge has no branch of the run.
func (p *pushRepo) wantsNoPush(t *testing.T, r pushed, code contracts.EndCode, want string) {
	t.Helper()
	if len(r.closes) != 1 || r.closes[0].State != event.StateFailed || r.closes[0].Reason != string(code) {
		t.Errorf("the run closed with %+v, want failed %s", r.closes, code)
	}
	wants(t, r.out, want)
	if head := p.branchHead(t); head != "" {
		t.Errorf("the forge has the branch at %s\n%s", head, r.out)
	}
}

// TestCommitsOutsideThePlanStopTheirPush is a session whose scope check exits 1 with its
// findings: nothing is pushed, the run ends push_scope_refused, and the line has the
// repository, the plan type, the commits and the findings. The check ran on the clone with
// --base at the session's start and --artifacts at the snapshot, read-only, and the clone
// writable.
func TestCommitsOutsideThePlanStopTheirPush(t *testing.T) {
	docker, log := scopeDocker(t, "exit 1", "src/app.go: outside the plan\n")
	p := scopedRepo(t, docker)
	r := p.run(t, leavesApp(t), nil)
	p.wantsNoPush(t, r, contracts.EndCodePushScopeRefused, "qory run: The commits do not pass the scope check of the session's plan, so qory pushed nothing.\n  Repository acme/site, plan type change-plan, commits "+p.first[:12]+" to ")
	wants(t, r.out, ".\n  src/app.go: outside the plan\n")
	created := checkLog(t, log)
	for _, want := range []string{"/modules/hello/checks/scope.sh example.com/agent:1 --base " + p.first + " --artifacts /qory-push-check/files\n", "/clone,dst=/qory-push-check/artifacts ", "/artifacts,dst=/qory-push-check/files,readonly", " --network none "} {
		if !strings.Contains(created, want) {
			t.Errorf("the scope check's create lacks %q:\n%s", want, created)
		}
	}
}

// TestCommitsInThePlanPush is a session that pulled artifacts and left files, whose scope
// check passes: the push lands.
func TestCommitsInThePlanPush(t *testing.T) {
	docker, log := scopeDocker(t, "exit 0", "")
	p := scopedRepo(t, docker)
	r := p.run(t, func(dir string) {
		writeFile(t, filepath.Join(dir, ".qory", "artifacts", "index.json"), "{\"artifacts\":[]}\n")
		writeFile(t, filepath.Join(dir, "src", "app.go"), "package app\n")
	}, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if p.branchHead(t) == "" || checkLog(t, log) == "" {
		t.Errorf("the push did not land after a scope check:\n%s", r.out)
	}
}

// TestAWorkflowWithNoPlanPushesNoChangedFile is a workflow with no plan: a commit that
// changes src/app.go stops the push with push_scope_refused and runs no container; one
// that changes only a file under .qory/ passes.
func TestAWorkflowWithNoPlanPushesNoChangedFile(t *testing.T) {
	docker, log := scopeDocker(t, "exit 1", "")
	p := scopedRepo(t, docker)
	t.Cleanup(cmd.SetPlan("hello", ""))
	r := p.run(t, func(dir string) {
		leavesApp(t)(dir)
		commitAll(t, dir, "Add the app")
	}, nil)
	p.wantsNoPush(t, r, contracts.EndCodePushScopeRefused, "qory run: this workflow has no plan, so its commits may change no file outside .qory/; qory pushed nothing.\n  Workflow hello. Repository acme/site. Commit ")
	wants(t, r.out, " changes src/app.go.\n")
	if got := checkLog(t, log); got != "" {
		t.Errorf("a scope check's container ran:\n%s", got)
	}
}

// TestOneRepositoryOutsideTheScopeStopsEveryPush is a run of two repositories with no plan:
// the target's commits change only .qory/ and pass, acme/lib's change lib.txt, so no
// repository is pushed.
func TestOneRepositoryOutsideTheScopeStopsEveryPush(t *testing.T) {
	docker, _ := scopeDocker(t, "exit 0", "")
	p := scopedRepo(t, docker)
	t.Cleanup(cmd.SetPlan("hello", ""))
	p.h.server.Repository(t, "acme/lib", "main", map[string]string{"lib.txt": "lib\n"})
	libBare := filepath.Join(p.h.server.Root, "acme", "lib.git")
	proxygittest.Git(t, libBare, "config", "http.receivepack", "true")
	p.repositories = "acme/site,acme/lib"
	r := p.run(t, func(dir string) {
		writeFile(t, filepath.Join(dir, ".qory", "build", "notes.md"), "notes\n")
		commitAll(t, dir, "Add notes")
		writeFile(t, filepath.Join(dir, ".qory", "repositories", "acme", "lib", "lib.txt"), "changed\n")
	}, nil)
	p.wantsNoPush(t, r, contracts.EndCodePushScopeRefused, "Repository acme/lib. Commit ")
	if got := proxygittest.Git(t, libBare, "for-each-ref", "refs/heads/qory/"); got != "" {
		t.Errorf("acme/lib has %s", got)
	}
}

// TestAScopeCheckThatDoesNotPassStopsEveryPush is each way a scope check fails closed: an
// exit 2 or 3 ends harness_invalid, a check past its limit push_scope_refused, a home with no
// scope-checks.json harness_invalid, a clone that fails repository_check_failed, and an
// engine that fails machine_setup_invalid. Nothing is pushed.
func TestAScopeCheckThatDoesNotPassStopsEveryPush(t *testing.T) {
	notRun := "qory run: qory could not run the scope check of the session's plan, so it pushed nothing.\n  Repository acme/site.\n  Cause: "
	for _, c := range []struct {
		name, start string
		code        contracts.EndCode
		want        string
		setUp       func(t *testing.T)
	}{
		{name: "exit 2", start: "exit 2", code: contracts.EndCodeHarnessInvalid, want: "qory run: The scope check of the session's plan ended with an error, so qory pushed nothing.\n  Repository acme/site.\n  Cause: usage: scope --plan\n"},
		{name: "exit 3", start: "exit 3", code: contracts.EndCodeHarnessInvalid, want: "  Cause: usage: scope --plan\n"},
		{name: "a timeout", start: "sleep", code: contracts.EndCodePushScopeRefused, want: "qory run: The scope check of the session's plan did not end within 30 seconds; qory pushed nothing.\n  Repository acme/site.\n",
			setUp: func(t *testing.T) { t.Cleanup(cmd.SetScopeCheckLimit(300 * time.Millisecond)) }},
		{name: "no scope-checks.json", start: "exit 0", code: contracts.EndCodeHarnessInvalid, want: notRun + "the home has no scope checks, so no module declares one for the plan type change-plan.\n",
			setUp: func(t *testing.T) { t.Cleanup(cmd.SetNoScopeChecks()) }},
		{name: "a failed clone", start: "exit 0", code: contracts.EndCodeRepositoryCheckFailed, want: notRun + "git init failed: no space left.\n",
			setUp: func(t *testing.T) { t.Cleanup(cmd.SetScopeClone(errors.New("git init failed: no space left"))) }},
		{name: "a failed engine", start: "engine", code: contracts.EndCodeMachineSetupInvalid, want: notRun + "the engine did not create the check's container: Cannot connect to the Docker daemon"},
	} {
		t.Run(c.name, func(t *testing.T) {
			docker, _ := scopeDocker(t, c.start, "usage: scope --plan\n")
			p := scopedRepo(t, docker)
			if c.setUp != nil {
				c.setUp(t)
			}
			r := p.run(t, leavesApp(t), nil)
			p.wantsNoPush(t, r, c.code, c.want)
		})
	}
}

// TestEachSessionsScopeCheckStartsAtItsStart is a shared run of two sessions with a plan:
// session 1's commits are checked from the run's start, and session 2's from the commit
// that session 1 pushed.
func TestEachSessionsScopeCheckStartsAtItsStart(t *testing.T) {
	p, _ := sharedPushRepo(t, laterAnswer(2, "claude", "example-provider-2", 30))
	p.noOutput = true
	docker, log := scopeDocker(t, "exit 0", "")
	walledBy(t, docker)
	var heads []string
	r := p.run(t, func(d string) {
		writeFile(t, filepath.Join(d, "n"+strconv.Itoa(len(heads))+".txt"), "n\n")
		commitAll(t, d, "Add a file")
		heads = append(heads, gitHead(t, d))
	}, numbered())
	r.wantsClose(t, event.StateSucceeded, 0)
	var bases []string
	for _, line := range strings.Split(strings.TrimSpace(checkLog(t, log)), "\n") {
		_, base, _ := strings.Cut(line, " --base ")
		base, _, _ = strings.Cut(base, " ")
		bases = append(bases, base)
	}
	if len(heads) != 2 || len(bases) != 2 || bases[0] != p.first || bases[1] != heads[0] {
		t.Errorf("the scope checks' bases are %v, want %s and the head of session 1, %v\n%s", bases, p.first, heads, r.out)
	}
}

// TestTheScopeCheckReadsTheLaunchSnapshot is a session whose agent writes a newer plan into
// .qory/artifacts of the checkout, while the run API's index changes after the launch: the
// scope check reads the index of the session's launch snapshot, as the agent read it in
// the inputs folder.
func TestTheScopeCheckReadsTheLaunchSnapshot(t *testing.T) {
	docker, log := scopeDocker(t, "exit 0", "")
	p := scopedRepo(t, docker)
	p.api.index = func(n int, w http.ResponseWriter) bool {
		fmt.Fprintf(w, `{"version":1,"read":%d,"artifacts":[]}`, n)
		return true
	}
	var launched string
	p.mountsOf = func(spec session.SessionSpec) {
		for _, m := range spec.Mounts {
			if data, err := os.ReadFile(filepath.Join(m.Path, "start-artifacts", "index.json")); err == nil {
				launched = string(data)
			}
		}
	}
	r := p.run(t, func(dir string) {
		writeFile(t, filepath.Join(dir, ".qory", "artifacts", "index.json"), `{"version":1,"read":99,"artifacts":[]}`)
		leavesApp(t)(dir)
	}, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	checked := checkLog(t, filepath.Join(filepath.Dir(log), "index.json"))
	if launched == "" || checked != launched {
		t.Errorf("the scope check read %q, want the launch snapshot %q", checked, launched)
	}
}

// TestALaunchSnapshotThatFailsStopsTheSession is a run whose read of the artifact index for
// the launch snapshot fails: the agent does not start, and the run closes run_api_failed.
func TestALaunchSnapshotThatFailsStopsTheSession(t *testing.T) {
	docker, _ := scopeDocker(t, "exit 0", "")
	p := scopedRepo(t, docker)
	p.api.index = func(n int, w http.ResponseWriter) bool {
		if n < 2 {
			return false
		}
		failing(w, http.StatusInternalServerError, "internal", "")
		return true
	}
	joined := false
	r := p.run(t, func(string) { joined = true }, nil)
	if joined || len(r.closes) != 1 || r.closes[0].Reason != string(contracts.EndCodeRunAPIFailed) {
		t.Errorf("joined %v, closed %+v, want no join and run_api_failed\n%s", joined, r.closes, r.out)
	}
	wants(t, r.out, "qory run: The fetch of the session's start artifacts failed, so the agent did not start.\n  Cause: ")
}
