package compose_test

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/stack"
)

// demosStack is a stack over software-factory that declares the workflow
// capture-demos.
const demosStack = `apiVersion: qory.dev/v1alpha1
modules:
  - name: software-factory
    source: {path: modules/software-factory}
workflows:
  capture-demos: {access: write}
`

// workflowFiles is a tree whose workflow capture-demos starts agents/demo-author, which
// nothing fills.
func workflowFiles() map[string]string {
	return map[string]string{
		stack.FileName: demosStack,
		"modules/software-factory/qory-module.yaml":                       "apiVersion: qory.dev/v1alpha1\nname: software-factory\n",
		"modules/software-factory/skills/workflow-capture-demos/SKILL.md": "---\nname: workflow-capture-demos\ndescription: d\n---\n\nStart ${qory:agents/demo-author}.\n",
	}
}

// workflowRefusal is the refusal of the workflow capture-demos for the role role, started
// by entry.
func workflowRefusal(role, entry string) string {
	fix := "Add a module that fills agents/" + role + ", or drop capture-demos from the workflows and process.\n"
	if utf8.RuneCountInString(fix) > 101 {
		// The first line passes 100 characters: the alternative moves to the next line.
		fix = "Add a module that fills agents/" + role + ".\n  You can also drop capture-demos from the workflows and process.\n"
	}
	return fix +
		"  The stack in qory-stack.yaml declares the workflow capture-demos. Its " + entry + " of module software-factory starts agents/" + role + ", and no module ships or provides that agent, and the stack does not bind it."
}

// TestAWorkflowThatStartsAnUnfilledRoleFails is the workflow's skill referencing an agent
// role that nothing fills: the compose fails with the role and the workflow.
func TestAWorkflowThatStartsAnUnfilledRoleFails(t *testing.T) {
	_, err := composeTree(t, workflowFiles())
	if want := workflowRefusal("demo-author", "skill workflow-capture-demos"); err == nil || err.Error() != want {
		t.Fatalf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestTheCommandOfAWorkflowIsChecked is a workflow whose entry is a command of the plain
// name: the command's reference is checked.
func TestTheCommandOfAWorkflowIsChecked(t *testing.T) {
	files := workflowFiles()
	delete(files, "modules/software-factory/skills/workflow-capture-demos/SKILL.md")
	files["modules/software-factory/commands/capture-demos.md"] = "---\ndescription: d\n---\n\nStart ${qory:agents/demo-author}.\n"
	_, err := composeTree(t, files)
	if want := workflowRefusal("demo-author", "command capture-demos"); err == nil || err.Error() != want {
		t.Fatalf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestOnlyTheEntryARunStartsIsChecked is a skill workflow-capture-demos, which a run
// starts, beside a skill capture-demos that references another unfilled role: only the
// first is the workflow's, and the second keeps rule 4's line.
func TestOnlyTheEntryARunStartsIsChecked(t *testing.T) {
	files := workflowFiles()
	files["modules/software-factory/skills/workflow-capture-demos/SKILL.md"] = "---\nname: workflow-capture-demos\ndescription: d\n---\n\nFilm it.\n"
	files["modules/software-factory/skills/capture-demos/SKILL.md"] = "---\nname: capture-demos\ndescription: d\n---\n\nStart ${qory:agents/demo-author}.\n"
	_, err := composeTree(t, files)
	want := "Ship agent demo-author, bind agents.demo-author in qory-stack.yaml, or remove the reference.\n" +
		"  The skill capture-demos of module software-factory references agent demo-author. No module ships or provides it, and the stack does not bind it."
	if err == nil || err.Error() != want {
		t.Fatalf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestASkillOfNoDeclaredWorkflowKeepsRule4sLine is the same skill with no workflow
// declared: rule 4's line.
func TestASkillOfNoDeclaredWorkflowKeepsRule4sLine(t *testing.T) {
	files := workflowFiles()
	files[stack.FileName] = strings.Replace(demosStack, "workflows:\n  capture-demos: {access: write}\n", "", 1)
	_, err := composeTree(t, files)
	want := "Ship agent demo-author, bind agents.demo-author in qory-stack.yaml, or remove the reference.\n" +
		"  The skill workflow-capture-demos of module software-factory references agent demo-author. No module ships or provides it, and the stack does not bind it."
	if err == nil || err.Error() != want {
		t.Fatalf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestAnExcludedOrDroppedRoleKeepsRule4sLine is the role's agent left out by an exclude,
// and the role provided by an agent that an exclude left out: rule 4's line in each case,
// which says which module left it out.
func TestAnExcludedOrDroppedRoleKeepsRule4sLine(t *testing.T) {
	files := workflowFiles()
	files[stack.FileName] = strings.Replace(demosStack, "    source: {path: modules/software-factory}\n", "    source: {path: modules/software-factory}\n    exclude: {agents: [demo-author]}\n", 1)
	files["modules/software-factory/agents/demo-author.md"] = "---\nname: demo-author\n---\n"
	_, err := composeTree(t, files)
	if want := "and module software-factory leaves it out."; err == nil || !strings.HasSuffix(err.Error(), want) {
		t.Fatalf("an excluded agent: %v", err)
	}
	files = workflowFiles()
	files[stack.FileName] = strings.Replace(demosStack, "workflows:", "  - name: screens\n    source: {path: modules/screens}\n    exclude: {agents: [browser-demo-author]}\nworkflows:", 1)
	files["modules/screens/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: screens\nprovides:\n  agents:\n    demo-author: browser-demo-author\n"
	files["modules/screens/agents/browser-demo-author.md"] = "---\nname: browser-demo-author\n---\n"
	files["modules/screens/agents/other.md"] = "---\nname: other\n---\n"
	_, err = composeTree(t, files)
	if want := "which module screens provides as agent browser-demo-author.\n  Module screens leaves out agent browser-demo-author."; err == nil || !strings.HasSuffix(err.Error(), want) {
		t.Fatalf("a dropped provider: %v", err)
	}
}

// TestABoundWorkflowRoleComposes is the role bound, and the role provided: the compose
// succeeds.
func TestABoundWorkflowRoleComposes(t *testing.T) {
	files := workflowFiles()
	files[stack.FileName] = demosStack + "bind:\n  agents:\n    demo-author: browser-demo-author\n"
	files["modules/software-factory/agents/browser-demo-author.md"] = "---\nname: browser-demo-author\n---\n"
	if _, err := composeTree(t, files); err != nil {
		t.Fatal(err)
	}
	files = workflowFiles()
	files[stack.FileName] = strings.Replace(demosStack, "workflows:", "  - name: screens\n    source: {path: modules/screens}\nworkflows:", 1)
	files["modules/screens/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: screens\nprovides:\n  agents:\n    demo-author: browser-demo-author\n"
	files["modules/screens/agents/browser-demo-author.md"] = "---\nname: browser-demo-author\n---\n"
	if _, err := composeTree(t, files); err != nil {
		t.Fatal(err)
	}
}

// TestTwoRolesGetTwoBlocks is the workflow's skill referencing two unfilled roles: one
// block each, in sorted order.
func TestTwoRolesGetTwoBlocks(t *testing.T) {
	files := workflowFiles()
	files["modules/software-factory/skills/workflow-capture-demos/SKILL.md"] = "---\nname: workflow-capture-demos\ndescription: d\n---\n\nStart ${qory:agents/screen-reviewer} and ${qory:agents/demo-author}.\n"
	_, err := composeTree(t, files)
	want := workflowRefusal("demo-author", "skill workflow-capture-demos") + "\n" + workflowRefusal("screen-reviewer", "skill workflow-capture-demos")
	if err == nil || err.Error() != want {
		t.Fatalf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestAWorkflowRoleAndOtherFailuresComeTogether is the workflow's unfilled role beside an
// unrelated reference that nothing meets: one error lists the workflow's block first, then
// rule 4's line, and no rule 4 line for the workflow's reference.
func TestAWorkflowRoleAndOtherFailuresComeTogether(t *testing.T) {
	files := workflowFiles()
	files["modules/software-factory/skills/release/SKILL.md"] = "---\nname: release\ndescription: d\n---\n\nRun ${qory:skills/missing}.\n"
	_, err := composeTree(t, files)
	want := workflowRefusal("demo-author", "skill workflow-capture-demos") + "\n" +
		"Ship skill missing, bind skills.missing in qory-stack.yaml, or remove the reference.\n" +
		"  The skill release of module software-factory references skill missing. No module ships or provides it, and the stack does not bind it."
	if err == nil || err.Error() != want {
		t.Fatalf("error:\n%v\nwant:\n%s", err, want)
	}
	var unresolved *compose.UnresolvedError
	if !errors.As(err, &unresolved) || len(unresolved.Lines) != 2 {
		t.Fatalf("an UnresolvedError of 2 lines: %#v", err)
	}
}

// TestAWorkflowThatStartsAGroupComposes is the workflow's skill referencing a group role
// that a module fills: the workflow check passes it, and with no member rule 4 refuses
// it, not the workflow check.
func TestAWorkflowThatStartsAGroupComposes(t *testing.T) {
	files := workflowFiles()
	files["modules/software-factory/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: software-factory\nroles:\n  agents:\n    demo-author: many\nprovides:\n  agents:\n    demo-author: [browser-demo-author]\n"
	files["modules/software-factory/agents/browser-demo-author.md"] = "---\nname: browser-demo-author\n---\n"
	if _, err := composeTree(t, files); err != nil {
		t.Fatal(err)
	}
	files["modules/software-factory/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: software-factory\nroles:\n  agents:\n    demo-author: many\n"
	_, err := composeTree(t, files)
	if err == nil || !strings.Contains(err.Error(), "\n  The group has no member.\n") || strings.Contains(err.Error(), "drop capture-demos") {
		t.Fatalf("a group with no member: %v", err)
	}
}
