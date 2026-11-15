package compose_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/stack"
)

// contractStack is a stack over core, which declares the role agents/developer with the
// contract roles/developer.yaml and binds it, and python, which provides it.
const contractStack = `apiVersion: qory.dev/v1alpha1
modules:
  - name: core
    source: {path: modules/core}
  - name: python
    source: {path: modules/python}
`

// contractFiles is a tree in which python-developer fills agents/developer and meets the
// contract, with the agent's text replaced by agent when it is not "".
func contractFiles(agent string) map[string]string {
	if agent == "" {
		agent = fullAgent
	}
	return map[string]string{
		stack.FileName:                              contractStack,
		"modules/core/qory-module.yaml":             "apiVersion: qory.dev/v1alpha1\nname: core\nroles:\n  agents:\n    developer: {fill: one, contract: roles/developer.yaml}\n",
		"modules/core/roles/developer.yaml":         "sections: [Guardrails, The work, Stop conditions, Expected output]\nfront_matter: [name, description, tools, model]\ntools: [Read, Edit, Write, Bash]\ntier: true\nreport: {}\n",
		"modules/core/skills/deploy/SKILL.md":       "---\nname: deploy\ndescription: deploy\n---\n\nDispatch ${qory:agents/developer}.\n",
		"modules/python/qory-module.yaml":           "apiVersion: qory.dev/v1alpha1\nname: python\nprovides:\n  agents:\n    developer: python-developer\n",
		"modules/python/agents/python-developer.md": agent,
	}
}

// fullAgent is an agent with every part the contract of contractFiles lists.
const fullAgent = `---
name: python-developer
description: Writes Python code.
tools: Read, Edit, Write, Bash, Grep
model: ${qory:thinking}
---

## Guardrails

## The work

## Stop conditions

## Expected output
`

// TestAnAgentThatMeetsTheContractComposes is an agent with every part of the contract,
// with its tools as a list too: the compose succeeds.
func TestAnAgentThatMeetsTheContractComposes(t *testing.T) {
	for _, agent := range []string{fullAgent, strings.Replace(fullAgent, "tools: Read, Edit, Write, Bash, Grep", "tools: [Read, Edit, Write, Bash]", 1)} {
		if _, err := composeTree(t, contractFiles(agent)); err != nil {
			t.Fatal(err)
		}
	}
}

// contractRefusal is the refusal of python-developer for the parts it lacks.
func contractRefusal(parts ...string) string {
	return "Complete agent python-developer of module python, or bind agents/developer to another agent.\n" +
		"  The contract roles/developer.yaml of module core requires these parts, which agents/python-developer.md lacks:\n  - " +
		strings.Join(parts, "\n  - ")
}

// TestAnAgentThatLacksAPartFails is an agent without each part of the contract in turn:
// the compose fails with that part in the error.
func TestAnAgentThatLacksAPartFails(t *testing.T) {
	for _, c := range []struct {
		name, old, new, part string
	}{
		{"heading", "## Stop conditions\n", "### Stop conditions\n", `the heading "## Stop conditions"`},
		{"key", "description: Writes Python code.\n", "description: \"\"\n", "the front-matter key description"},
		{"tool", "Write, Bash, Grep", "Write, Bash(git:*), Grep", "the tool Bash"},
		{"tier", "model: ${qory:thinking}", "model: claude-sonnet-5", "a tier in model, such as ${qory:fast}"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := composeTree(t, contractFiles(strings.Replace(fullAgent, c.old, c.new, 1)))
			if want := contractRefusal(c.part); err == nil || err.Error() != want {
				t.Fatalf("error:\n%v\nwant:\n%s", err, want)
			}
		})
	}
}

// TestEveryMissingPartIsListed is an agent with none of the parts: the error lists each
// one, in the contract's order.
func TestEveryMissingPartIsListed(t *testing.T) {
	_, err := composeTree(t, contractFiles("Writes code.\n"))
	want := contractRefusal(`the heading "## Guardrails"`, `the heading "## The work"`, `the heading "## Stop conditions"`, `the heading "## Expected output"`,
		"the front-matter key name", "the front-matter key description", "the front-matter key tools", "the front-matter key model",
		"the tool Read", "the tool Edit", "the tool Write", "the tool Bash", "a tier in model, such as ${qory:fast}")
	if err == nil || err.Error() != want {
		t.Fatalf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestTheBaseAgentIsCheckedToo is a role that the stack binds to an agent of the module
// that declares the contract: that agent is checked as a replacing one is.
func TestTheBaseAgentIsCheckedToo(t *testing.T) {
	files := contractFiles("")
	files[stack.FileName] = contractStack + "bind:\n  agents:\n    developer: core-developer\n"
	files["modules/python/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: python\n"
	files["modules/core/agents/core-developer.md"] = strings.Replace(strings.Replace(fullAgent, "python-developer", "core-developer", 1), "## Guardrails\n", "", 1)
	_, err := composeTree(t, files)
	want := "Complete agent core-developer of module core, or bind agents/developer to another agent.\n" +
		"  The contract roles/developer.yaml of module core requires these parts, which agents/core-developer.md lacks:\n" +
		`  - the heading "## Guardrails"`
	if err == nil || err.Error() != want {
		t.Fatalf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestTheAgentOfTheRolesNameIsChecked is a role that no layer sets, filled by the agent
// of its name: that agent is checked.
func TestTheAgentOfTheRolesNameIsChecked(t *testing.T) {
	files := contractFiles("")
	files["modules/python/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: python\n"
	delete(files, "modules/python/agents/python-developer.md")
	files["modules/python/agents/developer.md"] = "---\nname: developer\n---\n"
	_, err := composeTree(t, files)
	if err == nil || !strings.HasPrefix(err.Error(), "Complete agent developer of module python, or bind agents/developer to another agent.\n") {
		t.Fatalf("error: %v", err)
	}
}

// TestEachFailingAgentGetsABlock is two roles with contracts, each filled by an agent
// that lacks a part: one block per agent, by role.
func TestEachFailingAgentGetsABlock(t *testing.T) {
	files := contractFiles(strings.Replace(fullAgent, "## Guardrails\n", "", 1))
	files["modules/core/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: core\nroles:\n  agents:\n    developer: {fill: one, contract: roles/developer.yaml}\n    reviewer: {fill: one, contract: roles/reviewer.yaml}\n"
	files["modules/core/roles/reviewer.yaml"] = "tools: [Read]\n"
	files["modules/core/agents/reviewer.md"] = "---\nname: reviewer\ntools: Grep\n---\n"
	_, err := composeTree(t, files)
	want := contractRefusal(`the heading "## Guardrails"`) + "\n" +
		"Complete agent reviewer of module core, or bind agents/reviewer to another agent.\n" +
		"  The contract roles/reviewer.yaml of module core requires these parts, which agents/reviewer.md lacks:\n" +
		"  - the tool Read"
	if err == nil || err.Error() != want {
		t.Fatalf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestTwoContractsOfOneRoleAreEachChecked is a role that two modules give a contract: the
// agent meets each, and lacking a part of the second fails with the second's block.
func TestTwoContractsOfOneRoleAreEachChecked(t *testing.T) {
	files := contractFiles("")
	files["modules/python/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: python\nroles:\n  agents:\n    developer: {fill: one, contract: developer.yaml}\nprovides:\n  agents:\n    developer: python-developer\n"
	files["modules/python/developer.yaml"] = "sections: [Tests]\n"
	_, err := composeTree(t, files)
	want := "Complete agent python-developer of module python, or bind agents/developer to another agent.\n" +
		"  The contract developer.yaml of module python requires these parts, which agents/python-developer.md lacks:\n" +
		`  - the heading "## Tests"`
	if err == nil || err.Error() != want {
		t.Fatalf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestARoleNoAgentFillsChecksNoAgent is a role with a contract that no agent fills and
// nothing references: the compose reads the contract and checks no agent.
func TestARoleNoAgentFillsChecksNoAgent(t *testing.T) {
	files := contractFiles("")
	delete(files, "modules/core/skills/deploy/SKILL.md")
	delete(files, "modules/python/agents/python-developer.md")
	files["modules/core/skills/a/SKILL.md"] = "---\nname: a\ndescription: a\n---\n"
	files["modules/python/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: python\n"
	if _, err := composeTree(t, files); err != nil {
		t.Fatal(err)
	}
	files["modules/core/roles/developer.yaml"] = "paths: [a]\n"
	if _, err := composeTree(t, files); err == nil || !strings.HasPrefix(err.Error(), "Remove key paths from roles/developer.yaml of module core.\n") {
		t.Fatalf("a bad contract of an unfilled role: %v", err)
	}
}

// TestAHeadingInACodeFenceOrAtLevel3DoesNotCount is an agent whose Guardrails heading is
// inside a fenced code block and whose Stop conditions heading is level 3.
func TestAHeadingInACodeFenceOrAtLevel3DoesNotCount(t *testing.T) {
	agent := strings.Replace(fullAgent, "## Guardrails\n", "```\n## Guardrails\n```\n", 1)
	agent = strings.Replace(agent, "## Stop conditions\n", "### Stop conditions\n", 1)
	_, err := composeTree(t, contractFiles(agent))
	if want := contractRefusal(`the heading "## Guardrails"`, `the heading "## Stop conditions"`); err == nil || err.Error() != want {
		t.Fatalf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestAMissingContractFileFails is a contract path with no file, and one that is a
// directory.
func TestAMissingContractFileFails(t *testing.T) {
	files := contractFiles("")
	delete(files, "modules/core/roles/developer.yaml")
	_, err := composeTree(t, files)
	want := "Add roles/developer.yaml to module core, or remove contract from roles.agents.developer.\n" +
		"  qory-module.yaml sets roles.agents.developer.contract to roles/developer.yaml, and the module has no file at that path."
	if err == nil || err.Error() != want {
		t.Fatalf("error:\n%v\nwant:\n%s", err, want)
	}
	files["modules/core/roles/developer.yaml/README.md"] = "a directory\n"
	_, err = composeTree(t, files)
	want = "Point the contract of agents/developer in module core at a file.\n  roles/developer.yaml is a directory."
	if err == nil || err.Error() != want {
		t.Fatalf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestAContractThatLinksOutOfTheModuleFails is a contract file that is a link to a file
// outside the module: the compose refuses it and reads nothing there, for a module read
// through a root and for any other module alike.
func TestAContractThatLinksOutOfTheModuleFails(t *testing.T) {
	files := contractFiles("")
	delete(files, "modules/core/roles/developer.yaml")
	files["outside.yaml"] = "{}\n"
	files["modules/core/roles/reviewer.yaml"] = "{}\n"
	dir := writeTree(t, files)
	if err := os.Symlink(filepath.Join(dir, "outside.yaml"), filepath.Join(dir, "modules/core/roles/developer.yaml")); err != nil {
		t.Fatal(err)
	}
	want := "Replace roles/developer.yaml of module core with a file inside the module.\n  It is an absolute link, or a link that leads out of the module, and qory reads only the module's files."
	for _, opts := range []compose.Options{{}, {Copies: copyAll}} {
		p, err := loadFor(filepath.Join(dir, stack.FileName), "claude")
		if err != nil {
			t.Fatal(err)
		}
		_, err = compose.ComposeWith(p, opts)
		if err == nil || err.Error() != want {
			t.Fatalf("copied %v, error:\n%v\nwant:\n%s", opts.Copies != nil, err, want)
		}
	}
}

// TestABadContractFails is a contract with a key it does not have: the compose fails with
// the contract's place in the fix.
func TestABadContractFails(t *testing.T) {
	files := contractFiles("")
	files["modules/core/roles/developer.yaml"] = "sections: Guardrails\n"
	_, err := composeTree(t, files)
	want := "Write sections in roles/developer.yaml of module core as a list of names.\n  sections is a string."
	if err == nil || err.Error() != want {
		t.Fatalf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestTheFirstLineOfEachRefusalFitsIn100Characters is each refusal of the contract check
// and the workflow check for the names of the contract's example: its first line, which a
// log viewer keeps, has at most 100 characters.
func TestTheFirstLineOfEachRefusalFitsIn100Characters(t *testing.T) {
	missing := contractFiles("")
	delete(missing, "modules/core/roles/developer.yaml")
	directory := contractFiles("")
	delete(directory, "modules/core/roles/developer.yaml")
	directory["modules/core/roles/developer.yaml/a"] = "a\n"
	bad := contractFiles("")
	bad["modules/core/roles/developer.yaml"] = "front_matter: [\"\"]\n"
	trees := []map[string]string{contractFiles("Writes code.\n"), missing, directory, bad, workflowFiles(), groupFiles("---\nname: python-reviewer\n---\n")}
	for _, roles := range []string{
		"agents:\n    developer: {fill: one, paths: [a]}",
		"agents:\n    developer: {contract: roles/developer.yaml}",
		"skills:\n    deploy-target: {fill: one}",
		"agents:\n    developer: {fill: one, contract: ../developer.yaml}",
	} {
		files := contractFiles("")
		files["modules/core/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: core\nroles:\n  " + roles + "\n"
		trees = append(trees, files)
	}
	for _, files := range trees {
		_, err := composeTree(t, files)
		if err == nil {
			t.Fatal("composed")
		}
		for line := range strings.SplitSeq(err.Error(), "\n") {
			if !strings.HasPrefix(line, "  ") && len(line) > 100 {
				t.Errorf("%d characters: %s", len(line), line)
			}
		}
	}
}

// copyAll marks every module as one whose home contains copies of its entries, so the
// compose reads it through a root.
func copyAll(string) bool { return true }

// TestACopiedModuleIsChecked is the contract check on modules read through a root: an
// agent with every part composes, and one that lacks a part fails as it does otherwise.
func TestACopiedModuleIsChecked(t *testing.T) {
	if _, err := composeTreeWith(t, contractFiles(""), compose.Options{Copies: copyAll}); err != nil {
		t.Fatal(err)
	}
	_, err := composeTreeWith(t, contractFiles(strings.Replace(fullAgent, "## Guardrails\n", "", 1)), compose.Options{Copies: copyAll})
	if want := contractRefusal(`the heading "## Guardrails"`); err == nil || err.Error() != want {
		t.Fatalf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestAnAbsoluteLinkInsideACopiedModuleIsRead is a contract that is an absolute link to a
// file inside the module: a module read through a root reads it, and any other module
// refuses it, since its root follows only relative links.
func TestAnAbsoluteLinkInsideACopiedModuleIsRead(t *testing.T) {
	files := contractFiles("")
	files["modules/core/contracts/developer.yaml"] = "sections: [Tests]\n"
	files["modules/core/roles/reviewer.yaml"] = "{}\n"
	delete(files, "modules/core/roles/developer.yaml")
	dir := writeTree(t, files)
	real, err := filepath.EvalSymlinks(filepath.Join(dir, "modules/core/contracts/developer.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(dir, "modules/core/roles/developer.yaml")); err != nil {
		t.Fatal(err)
	}
	p, err := loadFor(filepath.Join(dir, stack.FileName), "claude")
	if err != nil {
		t.Fatal(err)
	}
	_, err = compose.ComposeWith(p, compose.Options{Copies: copyAll})
	if want := "  - the heading \"## Tests\""; err == nil || !strings.HasSuffix(err.Error(), want) {
		t.Fatalf("a copied module: %v, want the contract read", err)
	}
	p, err = loadFor(filepath.Join(dir, stack.FileName), "claude")
	if err != nil {
		t.Fatal(err)
	}
	_, err = compose.Compose(p)
	if want := "Replace roles/developer.yaml of module core with a file inside the module.\n"; err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("a module not copied: %v", err)
	}
}

// TestAContractPathThroughAFileFailsAsARead is a contract path that goes through a file,
// roles/developer.yaml/x: the lookup fails with the file system's error, which exits 1,
// for a module read through a root and for any other module alike.
func TestAContractPathThroughAFileFailsAsARead(t *testing.T) {
	files := contractFiles("")
	files["modules/core/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: core\nroles:\n  agents:\n    developer: {fill: one, contract: roles/developer.yaml/x}\n"
	for _, opts := range []compose.Options{{}, {Copies: copyAll}} {
		_, err := composeTreeWith(t, files, opts)
		var pathErr *fs.PathError
		if !errors.As(err, &pathErr) {
			t.Errorf("copied %v: %v, want a *fs.PathError", opts.Copies != nil, err)
		}
	}
}

// TestAContractThroughMoreThan40LinksFails is a contract path that passes 41 links inside
// a module read through a root: the compose refuses it with a fix line and its cause.
func TestAContractThroughMoreThan40LinksFails(t *testing.T) {
	files := contractFiles("")
	files["modules/core/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: core\nroles:\n  agents:\n    developer: {fill: one, contract: roles/l0}\n"
	dir := writeTree(t, files)
	roles := filepath.Join(dir, "modules/core/roles")
	for i := range 41 {
		next := "developer.yaml"
		if i < 40 {
			next = fmt.Sprintf("l%d", i+1)
		}
		if err := os.Symlink(next, filepath.Join(roles, fmt.Sprintf("l%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	p, err := loadFor(filepath.Join(dir, stack.FileName), "claude")
	if err != nil {
		t.Fatal(err)
	}
	_, err = compose.ComposeWith(p, compose.Options{Copies: copyAll})
	want := "Replace roles/l0 of module core with a file inside the module.\n  The path passes through more than 40 links."
	if err == nil || err.Error() != want {
		t.Fatalf("error:\n%v\nwant:\n%s", err, want)
	}
}

// groupFiles is a tree in which the group role agents/reviewers of module factory has the
// contract roles/reviewer.yaml, and factory and python provide its members code-reviewer
// and python-reviewer, with python-reviewer's text replaced by member when it is not "".
func groupFiles(member string) map[string]string {
	full := func(name string) string {
		return "---\nname: " + name + "\ndescription: Reviews a change.\ntools: Read, Grep\n---\n\n## Findings\n\n## Stop conditions\n"
	}
	if member == "" {
		member = full("python-reviewer")
	}
	return map[string]string{
		stack.FileName: `apiVersion: qory.dev/v1alpha1
modules:
  - name: factory
    source: {path: modules/factory}
  - name: python
    source: {path: modules/python}
`,
		"modules/factory/qory-module.yaml":         "apiVersion: qory.dev/v1alpha1\nname: factory\nroles:\n  agents:\n    reviewers: {fill: many, contract: roles/reviewer.yaml}\nprovides:\n  agents:\n    reviewers: [code-reviewer]\n",
		"modules/factory/roles/reviewer.yaml":      "sections: [Findings, Stop conditions]\nfront_matter: [name, description]\ntools: [Read]\n",
		"modules/factory/skills/review/SKILL.md":   "---\nname: review\ndescription: review\n---\n\nStart ${qory:agents/reviewers}.\n",
		"modules/factory/agents/code-reviewer.md":  full("code-reviewer"),
		"modules/python/qory-module.yaml":          "apiVersion: qory.dev/v1alpha1\nname: python\nprovides:\n  agents:\n    reviewers: [python-reviewer]\n",
		"modules/python/agents/python-reviewer.md": member,
	}
}

// TestAGroupWhoseMembersMeetTheContractComposes is a group role with a contract, each of
// whose two members has every part: the compose succeeds.
func TestAGroupWhoseMembersMeetTheContractComposes(t *testing.T) {
	res, err := composeTree(t, groupFiles(""))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Groups["agents/reviewers"]; len(got) != 2 {
		t.Fatalf("members %v, want 2", got)
	}
}

// TestAGroupMemberThatLacksAPartFails is a group role with a contract, one of whose
// members lacks a heading and a tool: the compose fails with that member named, and the
// fix offers to remove it from the group.
func TestAGroupMemberThatLacksAPartFails(t *testing.T) {
	_, err := composeTree(t, groupFiles("---\nname: python-reviewer\ndescription: Reviews Python.\ntools: Grep\n---\n\n## Findings\n"))
	want := "Complete agent python-reviewer of module python, or remove it from the group agents/reviewers.\n" +
		"  The contract roles/reviewer.yaml of module factory requires these parts, which agents/python-reviewer.md lacks:\n" +
		"  - the heading \"## Stop conditions\"\n" +
		"  - the tool Read"
	if err == nil || err.Error() != want {
		t.Fatalf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestAMemberTheBindReplacedIsNotChecked is a group whose members the stack's bind
// replaces with code-reviewer alone: python-reviewer, which lacks parts, fills nothing and
// is not checked.
func TestAMemberTheBindReplacedIsNotChecked(t *testing.T) {
	files := groupFiles("---\nname: python-reviewer\n---\n")
	files[stack.FileName] += "bind:\n  agents:\n    reviewers: [code-reviewer]\n"
	if _, err := composeTree(t, files); err != nil {
		t.Fatal(err)
	}
}
