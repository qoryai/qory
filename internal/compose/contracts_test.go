package compose_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
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
	for _, line := range groupMemberFixLines(t) {
		if len(line) > 100 {
			t.Errorf("%d characters: %s", len(line), line)
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
// fix offers to remove it from the provides list that added it.
func TestAGroupMemberThatLacksAPartFails(t *testing.T) {
	_, err := composeTree(t, groupFiles("---\nname: python-reviewer\ndescription: Reviews Python.\ntools: Grep\n---\n\n## Findings\n"))
	want := "Complete agent python-reviewer, or remove it from provides.agents.reviewers of module python.\n" +
		"  The contract roles/reviewer.yaml of module factory requires these parts, which agents/python-reviewer.md of module python lacks:\n" +
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

// groupMemberFixLines are the first lines of the refusals of a group member that lacks a
// part, added to the group by each of the three lists: a module's provides, the bind of
// the stack composed, and the bind of the base stack a checkout extends.
func groupMemberFixLines(t *testing.T) []string {
	t.Helper()
	const bad = "---\nname: python-reviewer\n---\n"
	var out []string
	_, err := composeTree(t, groupFiles(bad))
	if err == nil {
		t.Fatal("provides: composed")
	}
	out = append(out, strings.SplitN(err.Error(), "\n", 2)[0])
	files := groupFiles(bad)
	files[stack.FileName] += "bind:\n  agents:\n    reviewers: [code-reviewer, python-reviewer]\n"
	files["modules/python/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: python\n"
	_, err = composeTree(t, files)
	if err == nil {
		t.Fatal("bind: composed")
	}
	out = append(out, strings.SplitN(err.Error(), "\n", 2)[0])
	dir := writeTree(t, map[string]string{
		"base/" + stack.FileName:                         "apiVersion: qory.dev/v1alpha1\nname: web\nextending: {}\nmodules:\n  - name: factory\n    source: {path: modules/factory}\nbind:\n  agents:\n    reviewers: [python-reviewer]\n",
		"base/modules/factory/qory-module.yaml":          "apiVersion: qory.dev/v1alpha1\nname: factory\nroles:\n  agents:\n    reviewers: {fill: many, contract: roles/reviewer.yaml}\n",
		"base/modules/factory/roles/reviewer.yaml":       "tools: [Read]\n",
		"base/modules/factory/skills/review/SKILL.md":    "---\nname: review\ndescription: review\n---\n\nStart ${qory:agents/reviewers}.\n",
		"base/modules/factory/agents/python-reviewer.md": bad,
		"app/" + config.FileName:                         "apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {path: ../base}\n  target: {runtime: claude}\n",
	})
	_, err = composeExtending(t, dir)
	if err == nil {
		t.Fatal("base: composed")
	}
	return append(out, strings.SplitN(err.Error(), "\n", 2)[0])
}

// TestTheGroupFixLineNamesTheMembersList is a member that lacks a part in each of the
// three lists: the fix line names the list that added it, in rule 4's words, and for the
// base stack's bind, which the checkout does not edit, offers an exclude in qory.yaml.
func TestTheGroupFixLineNamesTheMembersList(t *testing.T) {
	want := []string{
		"Complete agent python-reviewer, or remove it from provides.agents.reviewers of module python.",
		"Complete agent python-reviewer, or remove it from bind.agents.reviewers in qory-stack.yaml.",
		"Exclude agents/python-reviewer in qory.yaml, or ask the base stack's maintainer to complete it.",
	}
	if got := groupMemberFixLines(t); !slices.Equal(got, want) {
		t.Fatalf("fix lines:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestAGroupCauseLineIsIndented is each refusal of a group reference or member that has
// a cause line: the cause line comes after the fix, indented by two spaces as the
// contract blocks indent theirs.
func TestAGroupCauseLineIsIndented(t *testing.T) {
	noMember := groupFiles("")
	noMember["modules/factory/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: factory\nroles:\n  agents:\n    reviewers: many\n"
	noMember["modules/python/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: python\n"
	frontMatter := groupFiles("")
	frontMatter["modules/factory/skills/review/SKILL.md"] = "---\nname: review\ndescription: Starts ${qory:agents/reviewers}.\n---\n\nReview.\n"
	unshipped := groupFiles("")
	delete(unshipped, "modules/python/agents/python-reviewer.md")
	unshipped["modules/python/agents/other.md"] = "---\nname: other\n---\n"
	for _, c := range []struct {
		files map[string]string
		want  string
	}{
		{noMember, "Fill agents/reviewers, or remove it from skills/review/SKILL.md of module factory.\n  The group has no member."},
		{frontMatter, "Remove ${qory:agents/reviewers} from the front matter of skills/review/SKILL.md of module factory.\n  A group resolves to a list."},
		{unshipped, "Ship agent python-reviewer, or remove it from provides.agents.reviewers of module python.\n  No module ships agent python-reviewer."},
	} {
		if _, err := composeTree(t, c.files); err == nil || err.Error() != c.want {
			t.Errorf("error:\n%v\nwant:\n%s", err, c.want)
		}
	}
}

// TestAMemberThatTwoListsAddNamesBothLists is a member that lacks a part, which the
// provides of factory and of python both add: the fix offers to remove it from the lists
// that add it, and a line per list names them.
func TestAMemberThatTwoListsAddNamesBothLists(t *testing.T) {
	files := groupFiles("---\nname: python-reviewer\ndescription: Reviews Python.\ntools: Read\n---\n\n## Findings\n")
	files["modules/factory/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: factory\nroles:\n  agents:\n    reviewers: {fill: many, contract: roles/reviewer.yaml}\nprovides:\n  agents:\n    reviewers: [code-reviewer, python-reviewer]\n"
	want := "Complete agent python-reviewer, or remove it from the lists that add it.\n" +
		"  These lists add it:\n" +
		"  - provides.agents.reviewers of module factory\n" +
		"  - provides.agents.reviewers of module python\n" +
		"  The contract roles/reviewer.yaml of module factory requires these parts, which agents/python-reviewer.md of module python lacks:\n" +
		"  - the heading \"## Stop conditions\""
	if _, err := composeTree(t, files); err == nil || err.Error() != want {
		t.Fatalf("error:\n%v\nwant:\n%s", err, want)
	}
}

// baseGroupFiles is a checkout whose qory.yaml extends the base stack software, which
// declares the group role agents/reviewers in module factory and binds python-reviewer to
// it. With python set, the checkout composes the module python too, which provides
// python-reviewer to the group. The agent python-reviewer has the text member, in python
// when python is set and in factory otherwise; with member "" no module ships it.
func baseGroupFiles(member string, python bool) map[string]string {
	files := map[string]string{
		"base/" + stack.FileName:                      "apiVersion: qory.dev/v1alpha1\nname: software\nextending: {kinds: [agents]}\nmodules:\n  - name: factory\n    source: {path: modules/factory}\nbind:\n  agents:\n    reviewers: [python-reviewer]\n",
		"base/modules/factory/qory-module.yaml":       "apiVersion: qory.dev/v1alpha1\nname: factory\nroles:\n  agents:\n    reviewers: {fill: many, contract: roles/reviewer.yaml}\n",
		"base/modules/factory/roles/reviewer.yaml":    "tools: [Read]\n",
		"base/modules/factory/skills/review/SKILL.md": "---\nname: review\ndescription: review\n---\n\nStart ${qory:agents/reviewers}.\n",
		"base/modules/factory/agents/other.md":        "---\nname: other\ntools: Read\n---\n",
		"app/" + config.FileName:                      "apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {path: ../base}\n  target: {runtime: claude}\n",
	}
	where := "base/modules/factory/agents/"
	if python {
		files["app/"+config.FileName] += "  modules:\n    - name: python\n      source: {path: ../python}\n"
		files["python/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: python\nprovides:\n  agents:\n    reviewers: [python-reviewer]\n"
		files["python/agents/python-other.md"] = "---\nname: python-other\ntools: Read\n---\n"
		where = "python/agents/"
	}
	if member != "" {
		files[where+"python-reviewer.md"] = member
	}
	return files
}

// TestAnUnshippedMemberHasItsFixFirst is a member of a group that no module ships: added
// by the base stack's bind and python's provides, the fix offers to remove it from the
// lists that add it, and a line per list names them; added by one bind, the fix names
// that bind.
func TestAnUnshippedMemberHasItsFixFirst(t *testing.T) {
	want := "Ship agent python-reviewer, or remove it from the lists that add it.\n" +
		"  No module ships agent python-reviewer, and these lists add it:\n" +
		"  - bind.agents.reviewers in the base stack\n" +
		"  - provides.agents.reviewers of module python"
	if _, err := composeExtending(t, writeTree(t, baseGroupFiles("", true))); err == nil || err.Error() != want {
		t.Errorf("error:\n%v\nwant:\n%s", err, want)
	}
	want = "Ship agent python-reviewer, or remove it from bind.agents.reviewers in the base stack.\n" +
		"  No module ships agent python-reviewer."
	if _, err := composeExtending(t, writeTree(t, baseGroupFiles("", false))); err == nil || err.Error() != want {
		t.Errorf("error:\n%v\nwant:\n%s", err, want)
	}
	files := groupFiles("")
	files[stack.FileName] += "bind:\n  agents:\n    reviewers: [code-reviewer, docs-reviewer]\n"
	want = "Ship agent docs-reviewer, or remove it from bind.agents.reviewers in qory-stack.yaml.\n" +
		"  No module ships agent docs-reviewer."
	if _, err := composeTree(t, files); err == nil || err.Error() != want {
		t.Errorf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestABaseStackMemberOffersAnExclude is a checkout whose qory.yaml extends the base
// stack software, whose bind alone adds python-reviewer of module factory to the group,
// and the agent lacks a part of the contract: the fix offers to exclude the agent in
// qory.yaml or to ask the base stack's maintainer, and the cause line names the base
// stack. The module is on the cause line when the fix line would pass 100 characters.
func TestABaseStackMemberOffersAnExclude(t *testing.T) {
	want := "Exclude agents/python-reviewer in qory.yaml, or ask the base stack's maintainer to complete it.\n" +
		"  The base stack software@working-tree binds python-reviewer of module factory to agents/reviewers, and the agent lacks:\n" +
		"  - the tool Read"
	if _, err := composeExtending(t, writeTree(t, baseGroupFiles("---\nname: python-reviewer\n---\n", false))); err == nil || err.Error() != want {
		t.Errorf("error:\n%v\nwant:\n%s", err, want)
	}
	short := map[string]string{}
	for path, text := range baseGroupFiles("---\nname: pr\n---\n", false) {
		short[strings.ReplaceAll(path, "python-reviewer", "pr")] = strings.ReplaceAll(text, "python-reviewer", "pr")
	}
	want = "Exclude agents/pr of module factory in qory.yaml, or ask the base stack's maintainer to complete it.\n" +
		"  The base stack software@working-tree binds pr to agents/reviewers, and the agent lacks:\n" +
		"  - the tool Read"
	if _, err := composeExtending(t, writeTree(t, short)); err == nil || err.Error() != want {
		t.Errorf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestTheBindOfTheStackComposedKeepsItsRemoveLine is the base stack of
// [baseGroupFiles] composed by itself: its bind is in the document composed, and the
// fix offers to remove the member from that bind.
func TestTheBindOfTheStackComposedKeepsItsRemoveLine(t *testing.T) {
	files := map[string]string{}
	for path, text := range baseGroupFiles("---\nname: python-reviewer\n---\n", false) {
		if inner, ok := strings.CutPrefix(path, "base/"); ok {
			files[inner] = text
		}
	}
	want := "Complete agent python-reviewer, or remove it from bind.agents.reviewers in qory-stack.yaml.\n" +
		"  The contract roles/reviewer.yaml of module factory requires these parts, which agents/python-reviewer.md of module factory lacks:\n" +
		"  - the tool Read"
	if _, err := composeTree(t, files); err == nil || err.Error() != want {
		t.Errorf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestTheNoMemberFixLineFitsIn100Characters is a reference to a group with no member in
// a module whose name is 16 characters long: the fix line has at most 100.
func TestTheNoMemberFixLineFitsIn100Characters(t *testing.T) {
	files := groupFiles("")
	delete(files, "modules/factory/qory-module.yaml")
	delete(files, "modules/factory/roles/reviewer.yaml")
	delete(files, "modules/factory/skills/review/SKILL.md")
	delete(files, "modules/factory/agents/code-reviewer.md")
	files[stack.FileName] = strings.Replace(files[stack.FileName], "name: factory\n    source: {path: modules/factory}", "name: software-factory\n    source: {path: modules/software-factory}", 1)
	files["modules/software-factory/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: software-factory\nroles:\n  agents:\n    reviewers: many\n"
	files["modules/software-factory/skills/review/SKILL.md"] = "---\nname: review\ndescription: review\n---\n\nStart ${qory:agents/reviewers}.\n"
	files["modules/python/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: python\n"
	want := "Fill agents/reviewers, or remove it from skills/review/SKILL.md of module software-factory.\n  The group has no member."
	_, err := composeTree(t, files)
	if err == nil || err.Error() != want {
		t.Fatalf("error:\n%v\nwant:\n%s", err, want)
	}
	if first, _, _ := strings.Cut(err.Error(), "\n"); len(first) > 100 {
		t.Fatalf("fix line has %d characters: %s", len(first), first)
	}
}

// TestAnExcludedGroupMemberIsNotChecked is a member of a group with a contract that the
// providing module's exclude leaves out: it is no member, and the compose does not check
// it.
func TestAnExcludedGroupMemberIsNotChecked(t *testing.T) {
	files := groupFiles("---\nname: python-reviewer\n---\n")
	files[stack.FileName] += "    exclude: {agents: [python-reviewer]}\n"
	files["modules/python/agents/other.md"] = "---\nname: other\n---\n"
	res, err := composeTree(t, files)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Groups["agents/reviewers"]; !slices.Equal(got, []string{"code-reviewer"}) {
		t.Fatalf("members %v, want [code-reviewer]", got)
	}
}

// TestAnEmptyGroupWithAContractComposes is a group role with a contract, no member and no
// reference: the compose succeeds, and a contract file that does not read still fails it.
func TestAnEmptyGroupWithAContractComposes(t *testing.T) {
	files := groupFiles("")
	files["modules/factory/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: factory\nroles:\n  agents:\n    reviewers: {fill: many, contract: roles/reviewer.yaml}\n"
	files["modules/python/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: python\n"
	files["modules/factory/skills/review/SKILL.md"] = "---\nname: review\ndescription: review\n---\n\nReview the change.\n"
	if _, err := composeTree(t, files); err != nil {
		t.Fatal(err)
	}
	files["modules/factory/roles/reviewer.yaml"] = "tier: \"yes\"\n"
	want := "Set tier in roles/reviewer.yaml of module factory to true or false.\n  tier is \"yes\"."
	if _, err := composeTree(t, files); err == nil || err.Error() != want {
		t.Fatalf("error:\n%v\nwant:\n%s", err, want)
	}
}
