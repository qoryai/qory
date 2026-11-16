package cmd_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
)

// reviewModule is the files of the module hello, in the base's folder examples/hello,
// that declare the agents role reviewers as fill, one or many, and ship the agents
// code-reviewer and test-reviewer, beside the module's command hello.
func reviewModule(fill string) map[string]string {
	dir := "examples/hello/modules/hello/"
	return map[string]string{
		dir + "qory-module.yaml":        "apiVersion: qory.dev/v1alpha1\nname: hello\nroles:\n  agents:\n    reviewers: " + fill + "\n",
		dir + "agents/code-reviewer.md": "---\nname: code-reviewer\ndescription: Reviews the code of a change.\n---\n\nReview the code.\n",
		dir + "agents/test-reviewer.md": "---\nname: test-reviewer\ndescription: Reviews the tests of a change.\n---\n\nReview the tests.\n",
	}
}

// bindReviewers sets the bind of the documents' harness section to the agents role
// reviewers filled by the list code-reviewer, test-reviewer.
func bindReviewers(d map[string]any) {
	d["harness"].(map[string]any)["harness"].(map[string]any)["bind"] = map[string]any{
		"agents": map[string]any{"reviewers": []string{"code-reviewer", "test-reviewer"}},
	}
}

// harnessReport is the one compose report of the run r, read from its run directory,
// the working folder and the cache folder.
func harnessReport(t *testing.T, r harnessRun) []byte {
	t.Helper()
	var reports []string
	cache, _ := os.UserCacheDir()
	for _, dir := range []string{r.runDir, ".", cache} {
		filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err == nil && strings.HasSuffix(path, "-report.json") {
				reports = append(reports, path)
			}
			return nil
		})
	}
	if len(reports) != 1 {
		t.Fatalf("the run has the reports %q", reports)
	}
	data, err := os.ReadFile(reports[0])
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestARunCodesBindOfAGroupRoleToTwoAgentsComposesBoth is a run from a run code whose
// harness document defines a stack of the module hello, which declares the agents role
// reviewers many, and binds reviewers to the list code-reviewer, test-reviewer: the
// compose fills the group with both agents, in the list's order, each a member from the
// bind, and the run joins.
func TestARunCodesBindOfAGroupRoleToTwoAgentsComposesBoth(t *testing.T) {
	r := codeHarness{
		defines: map[string]any{"hello": map[string]any{"access": "write"}},
		files:   reviewModule("many"),
		change:  bindReviewers,
	}.run(t)
	if !errors.Is(r.err, cmd.ErrJoinedNowhere) || !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	data := harnessReport(t, r)
	var rep struct {
		Roles []struct {
			Role, Agent, From string
			Group, Replaced   bool
		} `json:"roles"`
	}
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatal(err)
	}
	var members []string
	for _, role := range rep.Roles {
		if role.Role != "agents/reviewers" {
			continue
		}
		if !role.Group || role.Replaced || role.From != "bind" {
			t.Errorf("the member %s is %+v, want a member of the group from the bind", role.Agent, role)
		}
		members = append(members, role.Agent)
	}
	if want := []string{"code-reviewer", "test-reviewer"}; !slices.Equal(members, want) {
		t.Errorf("the group agents/reviewers has the members %q, want %q:\n%s", members, want, data)
	}
	r.noAddress(t)
}

// TestARunCodesBindOfAListToARoleThatIsNoGroupStops is a run from a run code whose
// harness document binds the list code-reviewer, test-reviewer to the agents role
// reviewers, which the module hello declares one: the compose fails, exit 2, before the
// join. The error's first line is the fix, to correct the workspace's harness in Qory
// Apiary by writing one name for the role, and the next line is the cause.
func TestARunCodesBindOfAListToARoleThatIsNoGroupStops(t *testing.T) {
	r := codeHarness{
		defines: map[string]any{"hello": map[string]any{"access": "write"}},
		files:   reviewModule("one"),
		change:  bindReviewers,
	}.run(t)
	if r.joined || cmd.ExitCode(r.err) != cmd.ExitInput {
		t.Fatalf("%v (exit %d), want exit 2 before the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	first, rest, _ := strings.Cut(r.err.Error(), "\n")
	if !strings.HasPrefix(first, "correct "+workspaceHarness+", then take the work item in again in Qory Apiary: Write one name for bind.agents.reviewers in ") {
		t.Errorf("the error's first line is not the fix:\n%v", r.err)
	}
	if !strings.HasPrefix(strings.TrimSpace(rest), "No module declares agents/reviewers as many, so one agent fills it") {
		t.Errorf("the error's second line is not the cause:\n%v", r.err)
	}
	r.noAddress(t)
}

// TestARunWithAccessWriteNoPushRunsTheAgentAndPushesNothing is a walled run of the
// workflow hello, which the stack declares write, from documents with access
// write-no-push, whose agent commits a file and leaves another: the agent runs with the
// checkout writable, and the run has no push step, writes no branch on the forge and
// posts no version. qory pushes only for access write.
func TestARunWithAccessWriteNoPushRunsTheAgentAndPushesNothing(t *testing.T) {
	p := newPushRepo(t, "write")
	docker, _ := fakeDocker(t)
	walledBy(t, docker)
	p.change = func(d map[string]any) { d["access"] = "write-no-push" }
	p.flags = []string{"--home", filepath.Join(tempDir(t), "home")}
	var spec session.SessionSpec
	joined := false
	p.mountsOf = func(s session.SessionSpec) { spec, joined = s, true }
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if !joined {
		t.Fatalf("the agent did not run\n%s", r.out)
	}
	if spec.WorkspaceReadOnly {
		t.Error("the agent's checkout is read-only")
	}
	if name, _ := r.step(); name != "" {
		t.Errorf("a push step %q", name)
	}
	if head := p.branchHead(t); head != "" {
		t.Errorf("the forge has the branch at %s", head)
	}
	if posts, _, _ := p.api.counts(); posts != 0 {
		t.Errorf("%d POST, want 0", posts)
	}
}
