package cmd_test

import (
	"encoding/json"
	"errors"
	"os"
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

// reviewersList binds the agents role reviewers to the list test-reviewer,
// code-reviewer: an order that neither the names nor the module's files have.
var reviewersList = harnessBind(map[string]any{"agents": map[string]any{"reviewers": []any{"test-reviewer", "code-reviewer"}}})

// TestTheReportListsAGroupBindsAgentsInTheListsOrder is a run from a run code whose
// harness document defines a stack of the module hello, which declares the agents role
// reviewers many, and binds reviewers to the list test-reviewer, code-reviewer: the
// compose report beside the session's home has one row per agent of
// agents/reviewers, each a member of the group from the bind and not replaced, in the
// list's order.
func TestTheReportListsAGroupBindsAgentsInTheListsOrder(t *testing.T) {
	r := codeHarness{
		defines: map[string]any{"hello": map[string]any{"access": "write"}},
		files:   reviewModule("many"),
		change:  reviewersList,
	}.run(t)
	if !errors.Is(r.err, cmd.ErrJoinedNowhere) || !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	data, err := os.ReadFile(r.spec.HarnessHome + "-report.json")
	if err != nil {
		t.Fatal(err)
	}
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
	if want := []string{"test-reviewer", "code-reviewer"}; !slices.Equal(members, want) {
		t.Errorf("the group agents/reviewers has the members %q, want %q:\n%s", members, want, data)
	}
}

// TestARunCodesBindOfAListToARoleThatIsNoGroupStops is a run from a run code whose
// harness document binds the list test-reviewer, code-reviewer to the agents role
// reviewers, which the module hello declares one: the compose fails, exit 2, before the
// join. The error's first line is the fix, to correct the workspace's harness in Qory
// Apiary by writing one name for the role, and the next line is the cause.
func TestARunCodesBindOfAListToARoleThatIsNoGroupStops(t *testing.T) {
	r := codeHarness{
		defines: map[string]any{"hello": map[string]any{"access": "write"}},
		files:   reviewModule("one"),
		change:  reviewersList,
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
	p.change = func(d map[string]any) { d["access"] = "write-no-push" }
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
