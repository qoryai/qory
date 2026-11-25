package cmd_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
)

// TestARunRunsTheSessionsHooksBeforeItsVerdictCounts is a walled run from a run code whose
// documents contain no harness document, in a checkout whose qory.yaml extends a stack whose
// workflow hello takes a check hook at its end, and lists one, scripts/check.sh, committed.
// After the session, qory runs the hook in a container of the fake docker, which exits 4, as a
// build step named for the hook with the container's output, and the hook is red.
func TestARunRunsTheSessionsHooksBeforeItsVerdictCounts(t *testing.T) {
	t.Cleanup(cmd.SetJoinResult(func(session.SessionSpec) (*session.Result, error) {
		return &session.Result{State: event.StateSucceeded, Session: 1}, nil
	}))
	r := codeHarness{
		checkout: true,
		stack: func(s string) string {
			s = strings.Replace(s, "\nextending: {}\n", "\nextending: {}\n  hooks: {per_point: 3}\n", 1)
			return s + "workflows:\n  hello:\n    access: write\n    hook_points:\n      after-hello: {at: end, kinds: [check]}\n"
		},
		document: func(s string) string {
			return strings.Replace(s, `{"harness":{`, `{"harness":{"hooks":[{"at":"after-hello","kind":"check","run":"scripts/check.sh"}],`, 1)
		},
		before: func() []string {
			writeFile(t, "scripts/check.sh", "#!/bin/sh\nexit 0\n")
			if err := os.Chmod("scripts/check.sh", 0o755); err != nil {
				t.Fatal(err)
			}
			runGit(t, ".", "add", "-A")
			runGit(t, ".", "-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "--quiet", "-m", "hooks")
			return nil
		},
	}.run(t)
	if !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	if !slices.ContainsFunc(r.events, func(e cmd.StepEvent) bool {
		return e.Kind == event.StepBuild && e.Name == "hook check.sh at after-hello"
	}) {
		t.Errorf("no build step of the hook in the events %+v\n%s", r.events, r.out)
	}
	wants(t, r.logged(), "inside the container\n", "qory run: the hook check.sh is red: it exited with status 4\n")
}
