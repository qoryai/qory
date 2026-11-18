package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
)

// markedModule is a module that declares a submit program, and no more. The program writes a
// file at marker when it starts, so a test sees whether it ran.
func markedModule(marker string) map[string]string {
	return map[string]string{
		"modules/forge/qory-module.yaml":   "apiVersion: qory.dev/v1alpha1\nname: forge\nsubmit_program:\n  command: [bin/open-draft]\n",
		"modules/forge/bin/open-draft":     "#!/bin/sh\necho started > " + marker + "\nexit 0\n",
		"modules/forge/skills/f/SKILL.md":  "---\nname: f\ndescription: f\n---\n",
		"modules/forge/skills/f/helper.md": "A skill.\n",
	}
}

// newProgramRepo is a [pushRepo] whose harness has a module with a submit program beside
// hello, at access, with a wall whose docker is the fake one. It returns the docker's log and
// the file that the program writes when it starts.
func newProgramRepo(t *testing.T, access string) (p *pushRepo, log, marker string) {
	t.Helper()
	marker = filepath.Join(tempDir(t), "program-started")
	files := markedModule(marker)
	stack := "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: hello\n    source: {path: modules/hello}\n" +
		"  - name: forge\n    source: {path: modules/forge}\nworkflows:\n  hello:\n    access: " + access + "\n"
	files["qory.yaml"] = ownStack(stack)
	p = newPushRepoWith(t, access, files)
	var docker string
	docker, log = fakeDocker(t)
	walledBy(t, docker)
	p.flags = []string{"--home", filepath.Join(tempDir(t), "home")}
	return p, log, marker
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

// wantsNoProgram checks that nothing of a submit program started: the program's file is not
// there, and the engine got no command to create or start a container for it.
func wantsNoProgram(t *testing.T, p *pushRepo, log, marker string) {
	t.Helper()
	if _, err := os.Stat(marker); err == nil {
		t.Error("the submit program started")
	}
	if got := checkLog(t, log); strings.Contains(got, "create ") || strings.Contains(got, "start ") || strings.Contains(got, "qory-push-check") {
		t.Errorf("a container ran:\n%s", got)
	}
}

// TestARunWithARunCodeRunsNoSubmitProgram is a passed round of a run from a run code, of a
// harness with a submit program: qory pushes and stores the submission, and starts no program,
// no relay and no container. The draft is no-program, for the forge's adapter to fill from the
// stored submission, and no commit has an address.
func TestARunWithARunCodeRunsNoSubmitProgram(t *testing.T) {
	p, log, marker := newProgramRepo(t, "write")
	r := p.run(t, passedWithText(t, p), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	repo := p.api.document(t, 1).Repositories[0]
	if got := string(repo.Draft); got != `{"none":"no-program"}` {
		t.Errorf("the draft is %s, want no-program", got)
	}
	if repo.CommitURL != "" || repo.Base == nil || repo.Base.CommitURL != "" || len(repo.Commits) != 1 || repo.Commits[0].CommitURL != "" {
		t.Errorf("the repository has addresses: %+v", repo)
	}
	if posts, _, stored := p.api.counts(); posts != 1 || stored != 1 {
		t.Errorf("%d POST and %d versions, want 1 and 1", posts, stored)
	}
	wantsNoProgram(t, p, log, marker)
}

// TestARunWithNoPushRunsNoProgram is a run that pushed nothing, a write-no-push run and a
// read run, each of a harness with a submit program: nothing of the program starts.
func TestARunWithNoPushRunsNoProgram(t *testing.T) {
	for _, access := range []string{"write", "write-no-push", "read"} {
		t.Run(access, func(t *testing.T) {
			p, log, marker := newProgramRepo(t, access)
			agent := func(dir string) {}
			if access != "write" {
				agent = writesAndCommits(t)
			}
			r := p.run(t, agent, nil)
			r.wantsClose(t, event.StateSucceeded, 0)
			wantsNoProgram(t, p, log, marker)
			if posts, _, stored := p.api.counts(); posts != 0 || stored != 0 {
				t.Errorf("%d POST and %d versions, want none", posts, stored)
			}
		})
	}
}

// TestAClosedRunRunsNoSubmitStep is a run of a harness with a submit program that Qory Apiary
// closed during the session, so the session's result has no After: the run does not panic, it
// has no push step and no program, and no version is stored.
func TestAClosedRunRunsNoSubmitStep(t *testing.T) {
	p, log, marker := newProgramRepo(t, "write")
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
	wantsNoProgram(t, p, log, marker)
	if posts, _, stored := p.api.counts(); posts != 0 || stored != 0 {
		t.Errorf("%d POST and %d versions, want none", posts, stored)
	}
}
