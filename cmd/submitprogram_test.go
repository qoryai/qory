package cmd_test

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
	"github.com/qoryai/qory/internal/submission"
)

// forgeModule is a module that declares a submit program, and no more.
var forgeModule = map[string]string{
	"modules/forge/qory-module.yaml":   "apiVersion: qory.dev/v1alpha1\nname: forge\nsubmit_program:\n  command: [bin/open-draft]\n",
	"modules/forge/bin/open-draft":     "#!/usr/bin/env node\n",
	"modules/forge/skills/f/SKILL.md":  "---\nname: f\ndescription: f\n---\n",
	"modules/forge/skills/f/helper.md": "A skill.\n",
}

// testProgramMain is the submit program of these tests: the test binary, which TestMain
// starts as this function when QORY_TEST_PROGRAM names a mode. The fake docker starts it in
// the place of the container's program, with the host's paths in the three variables of the
// program. It reads and rewrites the submission file as R-01's program does, and reaches the
// proxy through the socket. Its modes are:
//
//	fill    links every commit, fills each placeholder draft, and asks the proxy for a tunnel
//	exit1   links every commit, prints the fix of a failed call, exits 1
//	exit2   leaves the file as it is, prints the fix of a wrong input, exits 2
//	tamper  as fill, and changes the text of the submission
//	hang    sleeps past any limit
//	crash   exits 137, as a program that the engine killed
//	secret  prints the proxy password and the line that carries it, exits 0
func testProgramMain(mode string) int {
	file := os.Getenv("QORY_SUBMISSION_FILE")
	var doc map[string]any
	raw, err := os.ReadFile(file)
	if err == nil {
		err = json.Unmarshal(raw, &doc)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Set QORY_SUBMISSION_FILE to a readable file:", err)
		return 2
	}
	write := func() {
		data, _ := json.MarshalIndent(doc, "", "  ")
		if err := os.WriteFile(file, append(data, '\n'), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	link := func(path, commit string) string { return "https://forge.example/" + path + "/commit/" + commit }
	repositories, _ := doc["repositories"].([]any)
	links := func() {
		for _, r := range repositories {
			r := r.(map[string]any)
			path := r["path"].(string)
			r["commit_url"] = link(path, r["commit"].(string))
			if base, ok := r["base"].(map[string]any); ok {
				base["commit_url"] = link(path, base["commit"].(string))
			}
			commits, _ := r["commits"].([]any)
			for _, c := range commits {
				c := c.(map[string]any)
				c["commit_url"] = link(path, c["commit"].(string))
			}
		}
	}
	switch mode {
	case "fill", "tamper":
		links()
		text, _ := doc["text"].(map[string]any)
		for _, r := range repositories {
			r := r.(map[string]any)
			if d, _ := r["draft"].(map[string]any); d["none"] == "no-program" && text != nil {
				path := r["path"].(string)
				r["draft"] = map[string]any{"number": 7, "url": "https://forge.example/" + path + "/pull/7", "title": text["title"], "state": "draft", "action": "opened"}
				fmt.Printf("https://forge.example/%s/pull/7 opened\n", path)
			}
		}
		if mode == "tamper" {
			doc["text"] = map[string]any{"title": "Another title", "description": "x"}
		}
		write()
		fmt.Println("proxy answered:", askProxy())
		return 0
	case "exit1":
		links()
		write()
		fmt.Fprintln(os.Stderr, "Give the GitHub App pull_requests: write on acme/site: POST pulls returned 403")
		return 1
	case "exit2":
		fmt.Fprintln(os.Stderr, "Add acme/site to the run documents' targets: no base branch for it")
		return 2
	case "hang":
		time.Sleep(time.Minute)
		return 0
	case "crash":
		return 137
	case "secret":
		password := os.Getenv("QORY_PROXY_PASSWORD")
		auth := base64.StdEncoding.EncodeToString([]byte("qory:" + password))
		fmt.Println("password", password, "Proxy-Authorization: Basic", auth)
		fmt.Fprintln(os.Stderr, "password", password, "Proxy-Authorization: Basic", auth)
		return 0
	}
	fmt.Fprintln(os.Stderr, "no such mode", mode)
	return 2
}

// askProxy sends a CONNECT request for the stand-in forge's host through the relay's socket
// and returns the first line of the answer.
func askProxy() string {
	c, err := net.DialTimeout("unix", os.Getenv("QORY_PROXY_SOCKET"), 5*time.Second)
	if err != nil {
		return "no socket: " + err.Error()
	}
	defer c.Close()
	auth := base64.StdEncoding.EncodeToString([]byte("qory:" + os.Getenv("QORY_PROXY_PASSWORD")))
	fmt.Fprintf(c, "CONNECT %s:443 HTTP/1.1\r\nHost: %s:443\r\nProxy-Authorization: Basic %s\r\n\r\n", proxygittest.Host, proxygittest.Host, auth)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return "no answer: " + err.Error()
	}
	return strings.TrimSpace(line)
}

// programDocker is the fake docker of [fakeDocker] with the container of the submit program:
// the create keeps its arguments, the start copies the submission file to snapshot and
// replaces itself with this test binary in mode, with the program's variables at the host's
// paths, and the inspect reports code. Each command goes to the log it returns.
func programDocker(t *testing.T, mode string, code int) (docker, log, snapshot string) {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fake, _ := fakeDocker(t)
	dir := tempDir(t)
	log, snapshot = filepath.Join(dir, "program.log"), filepath.Join(dir, "snapshot.json")
	args := filepath.Join(dir, "args")
	script := filepath.Join(dir, "docker")
	writeFile(t, script, `#!/bin/sh
case "$*" in
*qory-push-check-*)
	echo "$*" >> `+log+`
	case "$1" in
	create)
		for a do printf '%s\n' "$a"; done > `+args+`
		echo c0ffee ;;
	start)
		work=; net=; envfile=; prev=
		while IFS= read -r a; do
			case "$prev" in
			--mount)
				src=${a#type=bind,src=}; src=${src%%,*}
				case "$a" in
				*dst=/qory-submit/work*) work=$src ;;
				*dst=/qory-submit/net*) net=$src ;;
				esac ;;
			--env-file) envfile=$a ;;
			esac
			prev=$a
		done < `+args+`
		password=$(sed -n 's/^QORY_PROXY_PASSWORD=//p' "$envfile")
		cp "$work/submission.json" `+snapshot+`
		echo "start" >> `+log+`
		exec env QORY_TEST_PROGRAM=`+mode+` QORY_SUBMISSION_FILE="$work/submission.json" QORY_PROXY_SOCKET="$net/proxy.sock" QORY_PROXY_PASSWORD="$password" `+bin+` -test.run='^$' ;;
	inspect) echo '{"Status":"exited","ExitCode":`+strconv.Itoa(code)+`,"Error":""}' ;;
	esac
	exit 0 ;;
esac
exec `+fake+` "$@"
`)
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	return script, log, snapshot
}

// newProgramRepo is a [pushRepo] whose harness has [forgeModule] beside hello, at access,
// with a wall whose docker runs the submit program in mode and reports code. onCall notes the
// run's proxies in the program's log, so it shows when each opens and closes.
func newProgramRepo(t *testing.T, access, mode string, code int) (p *pushRepo, log, snapshot string) {
	t.Helper()
	files := map[string]string{}
	for name, content := range forgeModule {
		files[name] = content
	}
	stack := "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: hello\n    source: {path: modules/hello}\n" +
		"  - name: forge\n    source: {path: modules/forge}\nworkflows:\n  hello:\n    access: " + access + "\n"
	files["qory.yaml"] = ownStack(stack)
	p = newPushRepoWith(t, access, files)
	var docker string
	docker, log, snapshot = programDocker(t, mode, code)
	walledBy(t, docker)
	p.flags = []string{"--home", filepath.Join(tempDir(t), "home")}
	p.onCall = func(call string) {
		if strings.HasPrefix(call, "push proxy") || strings.HasPrefix(call, "host proxy") {
			f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err == nil {
				fmt.Fprintf(f, "call: %s\n", call)
				f.Close()
			}
		}
	}
	return p, log, snapshot
}

// passedWithText is an agent that commits a file and writes a hand-off with a text, and the
// verdict of the work item that this run wrote: passed.
func passedWithText(t *testing.T, p *pushRepo) func(string) {
	p.api.verdict = "passed"
	return func(dir string) {
		writeFile(t, filepath.Join(dir, "committed.txt"), "committed\n")
		commitAll(t, dir, "Add a file")
		writeFile(t, filepath.Join(dir, ".qory", "handoff", "submission.json"),
			`{"text":{"title":"Add a file","description":"A file."},"steps":{"`+gitHead(t, dir)+`":"implement"}}`)
	}
}

// programLines is the program's lines in the step's log.
func programLines(log string) []string {
	var out []string
	for line := range strings.Lines(log) {
		if rest, ok := strings.CutPrefix(line, "qory run: submit program: "); ok {
			out = append(out, strings.TrimSpace(rest))
		}
	}
	return out
}

// TestTheSubmitProgramFillsTheLinksAndTheDraft is a passed round of a harness with a submit
// program: the program runs after the push, and the stored version has its commit links and
// its draft. The program reached the proxy through the relay, which the run opened before
// the program and closed after it.
func TestTheSubmitProgramFillsTheLinksAndTheDraft(t *testing.T) {
	p, log, snapshot := newProgramRepo(t, "write", "fill", 0)
	r := p.run(t, passedWithText(t, p), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	doc := p.api.document(t, 1)
	repo := doc.Repositories[0]
	path := "acme/site"
	if repo.CommitURL != "https://forge.example/"+path+"/commit/"+repo.Commit {
		t.Errorf("the commit link is %q", repo.CommitURL)
	}
	if repo.Base == nil || repo.Base.CommitURL != "https://forge.example/"+path+"/commit/"+p.first {
		t.Errorf("the base is %+v", repo.Base)
	}
	if len(repo.Commits) != 1 {
		t.Fatalf("the commits are %+v, want 1", repo.Commits)
	}
	for _, c := range repo.Commits {
		if c.CommitURL != "https://forge.example/"+path+"/commit/"+c.Commit {
			t.Errorf("the link of commit %s is %q", c.Commit, c.CommitURL)
		}
	}
	var draft map[string]any
	if err := json.Unmarshal(repo.Draft, &draft); err != nil || draft["number"] != float64(7) || draft["url"] != "https://forge.example/acme/site/pull/7" ||
		draft["title"] != "Add a file" || draft["state"] != "draft" || draft["action"] != "opened" || len(draft) != 5 {
		t.Errorf("the draft is %s, want number 7 and the pull request's url, title, state draft and action opened", repo.Draft)
	}
	if want := []string{"https://forge.example/acme/site/pull/7 opened", "proxy answered: HTTP/1.1 200 Connection established"}; !slices.Equal(programLines(r.log()), want) {
		t.Errorf("the program's lines are %q, want %q", programLines(r.log()), want)
	}
	if got := p.h.server.Tunnels(); got < 1 {
		t.Errorf("the proxy led %d tunnels, want the program's", got)
	}
	// The push proxy opens at the push and closes after the program, which starts in between.
	lines := strings.Split(checkLog(t, log), "\n")
	pushOpen, start, pushClosed := slices.Index(lines, "call: push proxy"), slices.Index(lines, "start"), slices.Index(lines, "call: push proxy closed")
	if pushOpen < 0 || start < pushOpen || pushClosed < start {
		t.Errorf("the order of the proxy and the program is %v: push proxy at %d, start at %d, closed at %d", lines, pushOpen, start, pushClosed)
	}
	if got := lines[slices.Index(lines, "start")+1]; got != "call: push proxy closed" && !strings.HasPrefix(got, "rm ") && !strings.HasPrefix(got, "inspect ") {
		t.Errorf("the line after the start is %q", got)
	}
	// What the program got has no secret and no commit link yet.
	given, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	lacks(t, string(given), proxygittest.Password, "commit_url", "password", "http://", "token")
	if err := submission.Check(given); err != nil {
		t.Errorf("the file the program got: %v", err)
	}
}

// TestTheProgramsContainerHasNoNetworkAndNoPasswordOnItsCommandLine is the create of the
// program's container in a run: no network, the wall's default image and user, the home
// read-only, and the password in the environment file alone.
func TestTheProgramsContainerHasNoNetworkAndNoPasswordOnItsCommandLine(t *testing.T) {
	p, log, _ := newProgramRepo(t, "write", "fill", 0)
	r := p.run(t, passedWithText(t, p), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	created := ""
	for _, line := range strings.Split(checkLog(t, log), "\n") {
		if strings.HasPrefix(line, "create ") {
			created = line
		}
	}
	for _, want := range []string{
		"--network none", "--user 1000:1000", "--cap-drop ALL", "--security-opt no-new-privileges", "--init", "--pull never",
		"--label dev.qory.run=" + r.runID, "--workdir /qory-submit/work", "--env-file ", " example.com/agent:1",
		",dst=/qory-submit/net,readonly", "/modules/forge/bin/open-draft",
	} {
		if !strings.Contains(created, want) {
			t.Errorf("the create lacks %q:\n%s", want, created)
		}
	}
	lacks(t, created, proxygittest.Password, "--env ", " -e ", "--privileged")
	lacks(t, checkLog(t, log), proxygittest.Password)
}

// TestAProgramThatFailedAfterItsLinksKeepsTheLinks is an exit 1: the file the program wrote
// is used, with its commit links, and the draft of the repository is program-failed with exit
// 1. The run succeeds, and the step prints the program's line and the fix.
func TestAProgramThatFailedAfterItsLinksKeepsTheLinks(t *testing.T) {
	p, _, _ := newProgramRepo(t, "write", "exit1", 1)
	r := p.run(t, passedWithText(t, p), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	repo := p.api.document(t, 1).Repositories[0]
	if repo.CommitURL == "" || string(repo.Draft) != `{"none":"program-failed","exit":1}` {
		t.Errorf("the repository is %+v, want the link and program-failed with exit 1", repo)
	}
	wants(t, r.log(), "qory run: submit program: Give the GitHub App pull_requests: write on acme/site: POST pulls returned 403\n",
		"qory run: The submit program failed, so a repository it did not finish has no draft.\n  Exit code: 1.\n  Draft of the repository: program-failed.\n")
}

// TestAProgramThatRefusedItsInputLeavesTheFile is an exit 2 with the file as the step gave it:
// no commit link, and program-failed with exit 2.
func TestAProgramThatRefusedItsInputLeavesTheFile(t *testing.T) {
	p, _, _ := newProgramRepo(t, "write", "exit2", 2)
	r := p.run(t, passedWithText(t, p), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	repo := p.api.document(t, 1).Repositories[0]
	if repo.CommitURL != "" || string(repo.Draft) != `{"none":"program-failed","exit":2}` {
		t.Errorf("the repository is %+v, want no link and program-failed with exit 2", repo)
	}
	wants(t, r.log(), "qory run: submit program: Add acme/site to the run documents' targets: no base branch for it\n", "  Exit code: 2.\n")
}

// TestAProgramThatChangedAnotherFieldIsDiscarded is a program that exits 0 and changed the
// text of the submission: qory stores the submission it gave the program, with no link, and
// the draft program-failed with exit 1, and says what the program changed.
func TestAProgramThatChangedAnotherFieldIsDiscarded(t *testing.T) {
	p, _, _ := newProgramRepo(t, "write", "tamper", 0)
	r := p.run(t, passedWithText(t, p), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	doc := p.api.document(t, 1)
	if doc.Text == nil || doc.Text.Title != "Add a file" {
		t.Errorf("the text is %+v, want the hand-off's", doc.Text)
	}
	repo := doc.Repositories[0]
	if repo.CommitURL != "" || string(repo.Draft) != `{"none":"program-failed","exit":1}` {
		t.Errorf("the repository is %+v, want no link and program-failed with exit 1", repo)
	}
	wants(t, r.log(), "qory run: The submit program left a result qory cannot use, so a repository has no draft.\n  Cause: it changed the ref, the verdict, the text or the list of repositories\n  qory kept the submission it gave the program.\n")
}

// TestAProgramPastItsLimitHasNoDraft is a program that runs past the limit: the container is
// stopped and removed, the draft is program-timeout, and the run still stores its version.
func TestAProgramPastItsLimitHasNoDraft(t *testing.T) {
	t.Cleanup(cmd.SetSubmitLimit(300 * time.Millisecond))
	p, log, _ := newProgramRepo(t, "write", "hang", 0)
	r := p.run(t, passedWithText(t, p), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	repo := p.api.document(t, 1).Repositories[0]
	if string(repo.Draft) != `{"none":"program-timeout"}` {
		t.Errorf("the draft is %s, want program-timeout", repo.Draft)
	}
	wants(t, r.log(), "qory run: The submit program ran past its limit, so a repository it did not finish has no draft.\n  Limit: 300ms.\n  Draft of the repository: program-timeout.\n")
	if !strings.Contains(checkLog(t, log), "rm --force --volumes qory-push-check-") {
		t.Errorf("the container was not removed:\n%s", checkLog(t, log))
	}
}

// TestAProgramThatCrashedHasNoDraft is an exit of 137, a program that was killed: the draft
// is program-failed with exit 1, the run succeeds.
func TestAProgramThatCrashedHasNoDraft(t *testing.T) {
	p, _, _ := newProgramRepo(t, "write", "crash", 137)
	r := p.run(t, passedWithText(t, p), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	repo := p.api.document(t, 1).Repositories[0]
	if string(repo.Draft) != `{"none":"program-failed","exit":1}` {
		t.Errorf("the draft is %s, want program-failed with exit 1", repo.Draft)
	}
}

// TestTheProgramsOutputHidesTheProxyPassword is a program that prints the proxy password and
// the header that carries it: neither is in what the step prints.
func TestTheProgramsOutputHidesTheProxyPassword(t *testing.T) {
	p, _, _ := newProgramRepo(t, "write", "secret", 0)
	r := p.run(t, passedWithText(t, p), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	lacks(t, r.out, proxygittest.Password, base64.StdEncoding.EncodeToString([]byte("qory:"+proxygittest.Password)))
	if len(programLines(r.log())) != 2 {
		t.Errorf("the program's lines are %q, want 2", programLines(r.log()))
	}
}

// TestARoundThatDidNotPassGetsTheLinksAndNoDraft is a round with no verdict: the program
// runs, links each commit, and the draft stays not-passed.
func TestARoundThatDidNotPassGetsTheLinksAndNoDraft(t *testing.T) {
	p, _, _ := newProgramRepo(t, "write", "fill", 0)
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	repo := p.api.document(t, 1).Repositories[0]
	if repo.CommitURL == "" || string(repo.Draft) != `{"none":"not-passed"}` {
		t.Errorf("the repository is %+v, want the link and not-passed", repo)
	}
}

// TestAHarnessWithNoProgramLeavesNoProgramDrafts is a passed round whose harness has no submit
// program: no container runs, and the draft is no-program.
func TestAHarnessWithNoProgramLeavesNoProgramDrafts(t *testing.T) {
	p := newPushRepo(t, "write")
	docker, log, _ := programDocker(t, "fill", 0)
	walledBy(t, docker)
	r := p.run(t, passedWithText(t, p), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if got := string(p.api.document(t, 1).Repositories[0].Draft); got != `{"none":"no-program"}` {
		t.Errorf("the draft is %s, want no-program", got)
	}
	if got := checkLog(t, log); got != "" {
		t.Errorf("a container ran:\n%s", got)
	}
}

// TestARunWithNoPushRunsNoProgram is a run that pushed nothing, a write-no-push run and a
// read run, each of a harness with a submit program: no container runs for the program.
func TestARunWithNoPushRunsNoProgram(t *testing.T) {
	for _, access := range []string{"write", "write-no-push", "read"} {
		t.Run(access, func(t *testing.T) {
			p, log, _ := newProgramRepo(t, access, "fill", 0)
			agent := func(dir string) {}
			if access != "write" {
				agent = writesAndCommits(t)
			}
			r := p.run(t, agent, nil)
			r.wantsClose(t, event.StateSucceeded, 0)
			if got := checkLog(t, log); strings.Contains(got, "create ") {
				t.Errorf("a container ran:\n%s", got)
			}
			if posts, _, stored := p.api.counts(); posts != 0 || stored != 0 {
				t.Errorf("%d POST and %d versions, want none", posts, stored)
			}
		})
	}
}

// TestAClosedRunRunsNoSubmitStep is a run of a harness with a submit program that Qory Apiary
// closed during the session, so the session's result has no After: the run does not panic, it
// has no push step and no container for the program, and no version is stored.
func TestAClosedRunRunsNoSubmitStep(t *testing.T) {
	p, log, _ := newProgramRepo(t, "write", "fill", 0)
	r := p.run(t, writesAndCommits(t), func() (*session.Result, error) {
		return &session.Result{State: event.StateCancelled, Reason: "stopped", RunClosed: true}, nil
	})
	if name, _ := r.step(); name != "" {
		t.Errorf("a push step %q", name)
	}
	for _, e := range r.steps {
		if strings.Contains(strings.ToLower(e.Kind+" "+e.Name), "submit") {
			t.Errorf("a submit step: %+v", e)
		}
	}
	if got := checkLog(t, log); strings.Contains(got, "create ") {
		t.Errorf("a container ran:\n%s", got)
	}
	if posts, _, stored := p.api.counts(); posts != 0 || stored != 0 {
		t.Errorf("%d POST and %d versions, want none", posts, stored)
	}
}
