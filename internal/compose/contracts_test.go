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
	"unicode/utf8"

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
// three lists: the fix line names the list that added it, in rule 4's words. The bind of
// a base stack that lets an extending module ship no agents is one the checkout neither
// edits nor excludes from, and the fix asks the base stack's maintainer.
func TestTheGroupFixLineNamesTheMembersList(t *testing.T) {
	want := []string{
		"Complete agent python-reviewer, or remove it from provides.agents.reviewers of module python.",
		"Complete agent python-reviewer, or remove it from bind.agents.reviewers in qory-stack.yaml.",
		"Ask the base stack's maintainer to complete agent python-reviewer.",
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

// TestAnUnshippedMemberHasItsFixFirst is a member of a group that no module ships. Added
// by the base stack's bind, alone or with python's provides, the checkout does not edit
// the base's list, and the fix offers to ship the agent in a module of qory.yaml or to
// ask the base stack's maintainer, with a line per list. Added by the bind of the stack
// composed, the fix names that bind.
func TestAnUnshippedMemberHasItsFixFirst(t *testing.T) {
	want := "Ship agent python-reviewer in a module of qory.yaml, or ask the base stack's maintainer to ship it.\n" +
		"  No module ships agent python-reviewer, and these lists add it:\n" +
		"  - bind.agents.reviewers in the base stack\n" +
		"  - provides.agents.reviewers of module python"
	if _, err := composeExtending(t, writeTree(t, baseGroupFiles("", true))); err == nil || err.Error() != want {
		t.Errorf("error:\n%v\nwant:\n%s", err, want)
	}
	want = "Ship agent python-reviewer in a module of qory.yaml, or ask the base stack's maintainer to ship it.\n" +
		"  No module ships agent python-reviewer, and this list adds it:\n" +
		"  - bind.agents.reviewers in the base stack"
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

// renamed is files with each "python-reviewer" in a path or a text replaced by name.
func renamed(files map[string]string, name string) map[string]string {
	out := map[string]string{}
	for path, text := range files {
		out[strings.ReplaceAll(path, "python-reviewer", name)] = strings.ReplaceAll(text, "python-reviewer", name)
	}
	return out
}

// TestABaseStackMemberOffersAnExclude is a checkout whose qory.yaml extends the base
// stack software, whose bind alone adds an agent of its module factory to the group,
// and the agent lacks a part of the contract: the fix offers to exclude the agent in
// qory.yaml or to ask the base stack's maintainer, and the cause line names the base
// stack and the contract. The fix line names the module when it stays within 100
// characters, as for the agent pr (100), and the cause line names it otherwise, as for
// prr (101).
func TestABaseStackMemberOffersAnExclude(t *testing.T) {
	const bad = "---\nname: python-reviewer\n---\n"
	for _, c := range []struct{ agent, want string }{
		{"python-reviewer", "Exclude agents/python-reviewer in qory.yaml, or ask the base stack's maintainer to complete it.\n" +
			"  The base stack software@working-tree binds python-reviewer of module factory to agents/reviewers, and the agent lacks these parts of the contract roles/reviewer.yaml of module factory:\n" +
			"  - the tool Read"},
		{"pr", "Exclude agents/pr of module factory in qory.yaml, or ask the base stack's maintainer to complete it.\n" +
			"  The base stack software@working-tree binds pr to agents/reviewers, and the agent lacks these parts of the contract roles/reviewer.yaml of module factory:\n" +
			"  - the tool Read"},
		{"prr", "Exclude agents/prr in qory.yaml, or ask the base stack's maintainer to complete it.\n" +
			"  The base stack software@working-tree binds prr of module factory to agents/reviewers, and the agent lacks these parts of the contract roles/reviewer.yaml of module factory:\n" +
			"  - the tool Read"},
	} {
		_, err := composeExtending(t, writeTree(t, renamed(baseGroupFiles(bad, false), c.agent)))
		if err == nil || err.Error() != c.want {
			t.Errorf("error:\n%v\nwant:\n%s", err, c.want)
		}
	}
	long := "Exclude agents/prr of module factory in qory.yaml, or ask the base stack's maintainer to complete it."
	if n := utf8.RuneCountInString(long); n != 101 {
		t.Fatalf("the long fix line has %d characters, want 101", n)
	}
}

// TestABaseStackMemberThatNoExcludeReachesAsksTheMaintainer is the base stack's member
// of TestABaseStackMemberOffersAnExclude, with a base that lets an extending module ship
// no agents: a checkout's exclude of that agent fails, so the fix line asks the base
// stack's maintainer.
func TestABaseStackMemberThatNoExcludeReachesAsksTheMaintainer(t *testing.T) {
	files := baseGroupFiles("---\nname: python-reviewer\n---\n", false)
	files["base/"+stack.FileName] = strings.Replace(files["base/"+stack.FileName], "extending: {kinds: [agents]}", "extending: {}", 1)
	want := "Ask the base stack's maintainer to complete agent python-reviewer.\n" +
		"  The base stack software@working-tree binds python-reviewer of module factory to agents/reviewers, and the agent lacks these parts of the contract roles/reviewer.yaml of module factory:\n" +
		"  - the tool Read"
	if _, err := composeExtending(t, writeTree(t, files)); err == nil || err.Error() != want {
		t.Errorf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestTwoContractsOfABaseStackMemberGetTwoBlocks is the base stack's member of
// TestABaseStackMemberOffersAnExclude with a second contract for the group, of the base
// module quality: each block names its contract.
func TestTwoContractsOfABaseStackMemberGetTwoBlocks(t *testing.T) {
	files := baseGroupFiles("---\nname: python-reviewer\n---\n", false)
	files["base/"+stack.FileName] = strings.Replace(files["base/"+stack.FileName], "bind:", "  - name: quality\n    source: {path: modules/quality}\nbind:", 1)
	files["base/modules/quality/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: quality\nroles:\n  agents:\n    reviewers: {fill: many, contract: roles/reviewer.yaml}\n"
	files["base/modules/quality/roles/reviewer.yaml"] = "front_matter: [description]\n"
	files["base/modules/quality/agents/quality-other.md"] = "---\nname: quality-other\n---\n"
	want := "Exclude agents/python-reviewer in qory.yaml, or ask the base stack's maintainer to complete it.\n" +
		"  The base stack software@working-tree binds python-reviewer of module factory to agents/reviewers, and the agent lacks these parts of the contract roles/reviewer.yaml of module factory:\n" +
		"  - the tool Read\n" +
		"Exclude agents/python-reviewer in qory.yaml, or ask the base stack's maintainer to complete it.\n" +
		"  The base stack software@working-tree binds python-reviewer of module factory to agents/reviewers, and the agent lacks these parts of the contract roles/reviewer.yaml of module quality:\n" +
		"  - the front-matter key description"
	if _, err := composeExtending(t, writeTree(t, files)); err == nil || err.Error() != want {
		t.Errorf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestABaseStackMemberOfACheckoutModuleIsCompletedThere is the base stack's bind adding
// python-reviewer, which the module python of the checkout's qory.yaml ships and does not
// provide: the reader can complete the agent, and the fix line offers that alone, with
// the base's bind on the cause line.
func TestABaseStackMemberOfACheckoutModuleIsCompletedThere(t *testing.T) {
	files := baseGroupFiles("---\nname: python-reviewer\n---\n", true)
	files["python/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: python\n"
	want := "Complete agent python-reviewer in module python.\n" +
		"  The base stack software@working-tree binds python-reviewer to agents/reviewers, and the agent lacks these parts of the contract roles/reviewer.yaml of module factory:\n" +
		"  - the tool Read"
	if _, err := composeExtending(t, writeTree(t, files)); err == nil || err.Error() != want {
		t.Errorf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestAModuleOfTheCheckoutShipsABaseMember is the base stack's bind adding
// python-reviewer, which no base module ships, and the module python of the checkout's
// qory.yaml, which ships it and provides nothing: the agent fills the member, and the
// compose succeeds, so shipping the agent in a module of qory.yaml is a fix.
func TestAModuleOfTheCheckoutShipsABaseMember(t *testing.T) {
	files := baseGroupFiles("---\nname: python-reviewer\ntools: Read\n---\n", true)
	files["python/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: python\n"
	res, err := composeExtending(t, writeTree(t, files))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Groups["agents/reviewers"]; !slices.Equal(got, []string{"python-reviewer"}) {
		t.Fatalf("members %v, want [python-reviewer]", got)
	}
}

// TestAnUnshippedBaseMemberAsksTheMaintainerWithoutAgents is the base stack's bind
// adding python-reviewer, which no module ships, in a base that lets an extending module
// ship no agents: no module of qory.yaml can ship it, and the fix asks the base stack's
// maintainer alone.
func TestAnUnshippedBaseMemberAsksTheMaintainerWithoutAgents(t *testing.T) {
	files := baseGroupFiles("", false)
	files["base/"+stack.FileName] = strings.Replace(files["base/"+stack.FileName], "extending: {kinds: [agents]}", "extending: {}", 1)
	want := "Ask the base stack's maintainer to ship agent python-reviewer.\n" +
		"  No module ships agent python-reviewer, and this list adds it:\n" +
		"  - bind.agents.reviewers in the base stack"
	if _, err := composeExtending(t, writeTree(t, files)); err == nil || err.Error() != want {
		t.Errorf("error:\n%v\nwant:\n%s", err, want)
	}
}

// baseProvidesFiles is the checkout of [baseGroupFiles] whose base stack binds nothing,
// and whose base module factory provides python-reviewer to the group, with member as
// the agent's text in factory, none when "". With agents unset, the base lets an
// extending module ship no agents.
func baseProvidesFiles(member string, agents bool) map[string]string {
	files := baseGroupFiles(member, false)
	files["base/"+stack.FileName] = strings.Replace(files["base/"+stack.FileName], "bind:\n  agents:\n    reviewers: [python-reviewer]\n", "", 1)
	files["base/modules/factory/qory-module.yaml"] += "provides:\n  agents:\n    reviewers: [other, python-reviewer]\n"
	if !agents {
		files["base/"+stack.FileName] = strings.Replace(files["base/"+stack.FileName], "extending: {kinds: [agents]}", "extending: {}", 1)
	}
	return files
}

// TestABaseModulesProvidedMemberOffersOnlyTheReadersSteps is a member that only the
// provides of the base module factory adds. Lacking a part, it gets the base stack's
// exclude, which composes, or, in a base that lets an extending module ship no agents,
// the request to the maintainer. Shipped by no module, it gets the fix to ship it in a
// module of qory.yaml or to ask the maintainer, or the request alone.
func TestABaseModulesProvidedMemberOffersOnlyTheReadersSteps(t *testing.T) {
	const bad = "---\nname: python-reviewer\n---\n"
	cause := "  Module factory of the base stack software@working-tree provides python-reviewer to agents/reviewers, and the agent lacks these parts of the contract roles/reviewer.yaml of module factory:\n" +
		"  - the tool Read"
	unshipped := "  No module ships agent python-reviewer, and this list adds it:\n" +
		"  - provides.agents.reviewers of module factory"
	for _, c := range []struct {
		files map[string]string
		want  string
	}{
		{baseProvidesFiles(bad, true), "Exclude agents/python-reviewer in qory.yaml, or ask the base stack's maintainer to complete it.\n" + cause},
		{baseProvidesFiles(bad, false), "Ask the base stack's maintainer to complete agent python-reviewer.\n" + cause},
		{baseProvidesFiles("", true), "Ship agent python-reviewer in a module of qory.yaml, or ask the base stack's maintainer to ship it.\n" + unshipped},
		{baseProvidesFiles("", false), "Ask the base stack's maintainer to ship agent python-reviewer.\n" + unshipped},
	} {
		if _, err := composeExtending(t, writeTree(t, c.files)); err == nil || err.Error() != c.want {
			t.Errorf("error:\n%v\nwant:\n%s", err, c.want)
		}
	}
	files := baseProvidesFiles(bad, true)
	files["app/"+config.FileName] += "  modules:\n    - name: factory\n      exclude: {agents: [python-reviewer]}\n"
	if _, err := composeExtending(t, writeTree(t, files)); err != nil {
		t.Errorf("with the exclude: %v", err)
	}
}

// TestABaseStackAndModuleMemberOffersAnExclude is a member that the base stack's bind and
// the provides of the checkout's module python both add, and that lacks a part: the fix
// offers to exclude it in qory.yaml or to complete it in python, which ships it, and the
// lists follow the parts. When a base module ships it, the fix asks the base stack's
// maintainer in place of the change to the agent.
func TestABaseStackAndModuleMemberOffersAnExclude(t *testing.T) {
	const bad = "---\nname: python-reviewer\n---\n"
	want := "Exclude agents/python-reviewer in qory.yaml, or complete it in module python.\n" +
		"  The contract roles/reviewer.yaml of module factory requires these parts, which agents/python-reviewer.md of module python lacks:\n" +
		"  - the tool Read\n" +
		"  These lists add it:\n" +
		"  - bind.agents.reviewers in the base stack\n" +
		"  - provides.agents.reviewers of module python"
	if _, err := composeExtending(t, writeTree(t, baseGroupFiles(bad, true))); err == nil || err.Error() != want {
		t.Errorf("error:\n%v\nwant:\n%s", err, want)
	}
	files := baseGroupFiles(bad, true)
	delete(files, "python/agents/python-reviewer.md")
	files["base/modules/factory/agents/python-reviewer.md"] = bad
	want = "Exclude agents/python-reviewer in qory.yaml, or ask the base stack's maintainer to complete it.\n" +
		"  The contract roles/reviewer.yaml of module factory requires these parts, which agents/python-reviewer.md of module factory lacks:\n" +
		"  - the tool Read\n" +
		"  These lists add it:\n" +
		"  - bind.agents.reviewers in the base stack\n" +
		"  - provides.agents.reviewers of module python"
	if _, err := composeExtending(t, writeTree(t, files)); err == nil || err.Error() != want {
		t.Errorf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestACheckoutBindOverABaseKeepsItsRemoveLine is the base stack's member of
// TestABaseStackMemberOffersAnExclude, which the checkout's qory.yaml binds itself: the
// bind is in the document composed, and the fix offers to remove the member from it.
func TestACheckoutBindOverABaseKeepsItsRemoveLine(t *testing.T) {
	files := baseGroupFiles("---\nname: python-reviewer\n---\n", false)
	files["app/"+config.FileName] += "  bind:\n    agents:\n      reviewers: [python-reviewer]\n"
	want := "Complete agent python-reviewer, or remove it from bind.agents.reviewers in qory.yaml.\n" +
		"  The contract roles/reviewer.yaml of module factory requires these parts, which agents/python-reviewer.md of module factory lacks:\n" +
		"  - the tool Read"
	if _, err := composeExtending(t, writeTree(t, files)); err == nil || err.Error() != want {
		t.Errorf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestALongSingleListLineMovesTheList is a member that one list of the module
// software-factory adds, where the fix line naming the list would pass 100 characters:
// the fix offers to remove it from the list that adds it, and a line names the list.
func TestALongSingleListLineMovesTheList(t *testing.T) {
	files := groupFiles("---\nname: python-reviewer\n---\n")
	files[stack.FileName] = strings.Replace(files[stack.FileName], "name: factory\n    source: {path: modules/factory}", "name: software-factory\n    source: {path: modules/factory}", 1)
	files["modules/factory/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: software-factory\nroles:\n  agents:\n    reviewers: {fill: many, contract: roles/reviewer.yaml}\nprovides:\n  agents:\n    reviewers: [code-reviewer, python-reviewer]\n"
	files["modules/python/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: python\n"
	want := "Complete agent python-reviewer, or remove it from the list that adds it.\n" +
		"  This list adds it:\n" +
		"  - provides.agents.reviewers of module software-factory\n" +
		"  The contract roles/reviewer.yaml of module software-factory requires these parts, which agents/python-reviewer.md of module python lacks:\n" +
		"  - the heading \"## Findings\"\n" +
		"  - the heading \"## Stop conditions\"\n" +
		"  - the front-matter key description\n" +
		"  - the tool Read"
	if _, err := composeTree(t, files); err == nil || err.Error() != want {
		t.Errorf("error:\n%v\nwant:\n%s", err, want)
	}
	files = renamed(groupFiles(""), "typescript-reviewer")
	files[stack.FileName] = strings.Replace(files[stack.FileName], "name: factory\n    source: {path: modules/factory}", "name: software-factory\n    source: {path: modules/factory}", 1)
	files["modules/factory/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: software-factory\nroles:\n  agents:\n    reviewers: {fill: many, contract: roles/reviewer.yaml}\nprovides:\n  agents:\n    reviewers: [code-reviewer, typescript-reviewer]\n"
	files["modules/python/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: python\n"
	delete(files, "modules/python/agents/typescript-reviewer.md")
	files["modules/python/agents/other.md"] = "---\nname: other\n---\n"
	want = "Ship agent typescript-reviewer, or remove it from the list that adds it.\n" +
		"  No module ships agent typescript-reviewer, and this list adds it:\n" +
		"  - provides.agents.reviewers of module software-factory"
	if _, err := composeTree(t, files); err == nil || err.Error() != want {
		t.Errorf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestALeftOutBindMemberHasItsFixFirst is a member that only the stack's bind adds and
// that the exclude of module python leaves out: the fix offers to compose it or to remove
// it from the bind, and the cause line names the module.
func TestALeftOutBindMemberHasItsFixFirst(t *testing.T) {
	files := groupFiles("")
	files[stack.FileName] = strings.Replace(files[stack.FileName], "source: {path: modules/python}\n", "source: {path: modules/python}\n    exclude: {agents: [python-reviewer]}\n", 1)
	files[stack.FileName] += "bind:\n  agents:\n    reviewers: [code-reviewer, python-reviewer]\n"
	files["modules/python/agents/other.md"] = "---\nname: other\n---\n"
	want := "Compose agent python-reviewer, or remove it from bind.agents.reviewers in qory-stack.yaml.\n" +
		"  Module python leaves out agent python-reviewer."
	if _, err := composeTree(t, files); err == nil || err.Error() != want {
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

// TestABaseDocumentWithAnEmptyGroupOffersAModule is a checkout whose qory.yaml extends
// the base stack software, whose skill review of module factory references a group that
// has no member: the reader edits neither, so the fix offers a module in qory.yaml that
// provides members, or asks the base stack's maintainer. Such a module fills the group,
// and the compose succeeds. A base that lets an extending module ship no agents leaves
// only the request to its maintainer.
func TestABaseDocumentWithAnEmptyGroupOffersAModule(t *testing.T) {
	const fixed = "---\nname: python-reviewer\ntools: Read\n---\n"
	unbound := func(python bool) map[string]string {
		files := baseGroupFiles(fixed, python)
		files["base/"+stack.FileName] = strings.Replace(files["base/"+stack.FileName], "bind:\n  agents:\n    reviewers: [python-reviewer]\n", "", 1)
		if !python {
			delete(files, "base/modules/factory/agents/python-reviewer.md")
		}
		return files
	}
	want := "Add a module to qory.yaml that provides agents/reviewers, or ask the base stack's maintainer.\n" +
		"  skills/review/SKILL.md of module factory references agents/reviewers, and the group has no member."
	if _, err := composeExtending(t, writeTree(t, unbound(false))); err == nil || err.Error() != want {
		t.Errorf("error:\n%v\nwant:\n%s", err, want)
	}
	if _, err := composeExtending(t, writeTree(t, unbound(true))); err != nil {
		t.Errorf("with a module that provides a member: %v", err)
	}
	files := unbound(false)
	files["base/"+stack.FileName] = strings.Replace(files["base/"+stack.FileName], "extending: {kinds: [agents]}", "extending: {}", 1)
	want = "Ask the base stack's maintainer to fill agents/reviewers.\n" +
		"  skills/review/SKILL.md of module factory references agents/reviewers, and the group has no member."
	if _, err := composeExtending(t, writeTree(t, files)); err == nil || err.Error() != want {
		t.Errorf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestACheckoutDocumentWithAnEmptyGroupKeepsTheFillLine is a checkout whose module
// python ships a skill that references the base's group, which has no member: the reader
// edits the skill, and the fix offers to fill the group or to remove the reference.
func TestACheckoutDocumentWithAnEmptyGroupKeepsTheFillLine(t *testing.T) {
	files := baseGroupFiles("", true)
	files["base/"+stack.FileName] = strings.Replace(files["base/"+stack.FileName], "bind:\n  agents:\n    reviewers: [python-reviewer]\n", "", 1)
	files["base/"+stack.FileName] = strings.Replace(files["base/"+stack.FileName], "kinds: [agents]", "kinds: [agents, skills]", 1)
	files["base/modules/factory/skills/review/SKILL.md"] = "---\nname: review\ndescription: review\n---\n\nReview.\n"
	files["python/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: python\n"
	files["python/skills/check/SKILL.md"] = "---\nname: check\ndescription: check\n---\n\nStart ${qory:agents/reviewers}.\n"
	want := "Fill agents/reviewers, or remove it from skills/check/SKILL.md of module python.\n  The group has no member."
	if _, err := composeExtending(t, writeTree(t, files)); err == nil || err.Error() != want {
		t.Errorf("error:\n%v\nwant:\n%s", err, want)
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

// TestABaseModuleIsExcludedByTheNameQoryYamlListsItBy is a base stack that lists the
// export core of a git source, whose manifest declares the name core-x, and binds its
// agent pr, which lacks a part of the contract: the fix offers the exclude of module
// core, the name the checkout's qory.yaml lists it by, and that exclude composes. No
// line of the refusal names the module core-x, also when the module's provides adds pr.
func TestABaseModuleIsExcludedByTheNameQoryYamlListsItBy(t *testing.T) {
	isolateAll(t)
	manifest := "apiVersion: qory.dev/v1alpha1\nname: core-x\nroles:\n  agents:\n    reviewers: {fill: many, contract: roles/reviewer.yaml}\n"
	provided := gitRepo(t, "", map[string]string{
		"qory.yaml":                           "apiVersion: qory.dev/v1alpha1\nexports:\n  modules: [core]\n",
		"modules/core/qory-module.yaml":       manifest + "provides:\n  agents:\n    reviewers: [pr]\n",
		"modules/core/roles/reviewer.yaml":    "tools: [Read]\n",
		"modules/core/skills/review/SKILL.md": "---\nname: review\ndescription: review\n---\n\nStart ${qory:agents/reviewers}.\n",
		"modules/core/agents/other.md":        "---\nname: other\ntools: Read\n---\n",
		"modules/core/agents/pr.md":           "---\nname: pr\n---\n",
	})
	url := gitRepo(t, "", map[string]string{
		"qory.yaml":                              "apiVersion: qory.dev/v1alpha1\nexports:\n  modules: [core]\n",
		"modules/core/qory-module.yaml":          manifest,
		"modules/core/roles/reviewer.yaml":       "tools: [Read]\n",
		"modules/core/skills/review/SKILL.md":    "---\nname: review\ndescription: review\n---\n\nStart ${qory:agents/reviewers}.\n",
		"modules/core/agents/other.md":           "---\nname: other\ntools: Read\n---\n",
		"modules/core/agents/pr.md":              "---\nname: pr\n---\n",
		"modules/core/agents/python-reviewer.md": "---\nname: python-reviewer\ntools: Read\n---\n",
	})
	tree := func(url string) map[string]string {
		return map[string]string{
			"base/" + stack.FileName: "apiVersion: qory.dev/v1alpha1\nname: software\nextending: {kinds: [agents]}\nmodules:\n  - source: {git: \"" + url + "\", ref: main, module: core}\nbind:\n  agents:\n    reviewers: [other, pr]\n",
			"app/" + config.FileName: "apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {path: ../base}\n  target: {runtime: claude}\n",
		}
	}
	_, err := composeExtending(t, writeTree(t, tree(provided)))
	fix := "Exclude agents/pr in qory.yaml, or ask the base stack's maintainer to complete it."
	if err == nil || !strings.HasPrefix(err.Error(), fix+"\n") || !strings.HasSuffix(err.Error(), "\n  - provides.agents.reviewers of module core") || strings.Contains(err.Error(), "core-x") {
		t.Fatalf("with the module's provides, error:\n%v\nwant the fix line:\n%s\nand no line with core-x", err, fix)
	}
	files := tree(url)
	dir := writeTree(t, files)
	_, err = composeExtending(t, dir)
	fix = "Exclude agents/pr of module core in qory.yaml, or ask the base stack's maintainer to complete it."
	if err == nil || !strings.HasPrefix(err.Error(), fix+"\n  The base stack software@") || !strings.Contains(err.Error(), " binds pr to agents/reviewers, ") || strings.Contains(err.Error(), "core-x") {
		t.Fatalf("error:\n%v\nwant the fix line:\n%s\nand no line with core-x", err, fix)
	}
	doc := filepath.Join(dir, "app", config.FileName)
	if err := os.WriteFile(doc, []byte(files["app/"+config.FileName]+"  modules:\n    - name: core\n      exclude: {agents: [pr]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := composeExtending(t, dir); err != nil {
		t.Fatalf("with the exclude of module core: %v", err)
	}
}

// TestABaseModuleQoryYamlCannotNameAsksTheMaintainer is the base stack's member of
// TestABaseStackMemberOffersAnExclude, of a base module that the base lists by its path
// alone: the checkout's qory.yaml cannot name the module to exclude from it, and the fix
// asks the base stack's maintainer.
func TestABaseModuleQoryYamlCannotNameAsksTheMaintainer(t *testing.T) {
	files := baseGroupFiles("---\nname: python-reviewer\n---\n", false)
	files["base/"+stack.FileName] = strings.Replace(files["base/"+stack.FileName], "  - name: factory\n    source:", "  - source:", 1)
	want := "Ask the base stack's maintainer to complete agent python-reviewer.\n" +
		"  The base stack software@working-tree binds python-reviewer of module factory to agents/reviewers, and the agent lacks these parts of the contract roles/reviewer.yaml of module factory:\n" +
		"  - the tool Read"
	if _, err := composeExtending(t, writeTree(t, files)); err == nil || err.Error() != want {
		t.Errorf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestLongBaseFixLinesNameTheAgentOnTheCauseLine is each base stack fix line that names
// the agent, at 100 characters and at 101, where the line names "the agent" and the cause
// line names it: an unshipped member, a member that only the base's bind adds, and one
// that the base's bind and its module factory's provides add.
func TestLongBaseFixLinesNameTheAgentOnTheCauseLine(t *testing.T) {
	const tail = ", and the agent lacks these parts of the contract roles/reviewer.yaml of module factory:\n  - the tool Read"
	both := func(name string) map[string]string {
		files := renamed(baseGroupFiles("---\nname: python-reviewer\n---\n", false), name)
		files["base/modules/factory/qory-module.yaml"] += "provides:\n  agents:\n    reviewers: [" + name + "]\n"
		return files
	}
	lists := "\n  These lists add it:\n  - bind.agents.reviewers in the base stack\n  - provides.agents.reviewers of module factory"
	for _, c := range []struct {
		files map[string]string
		want  string
	}{
		{renamed(baseGroupFiles("", false), "python-reviewers"), "Ship agent python-reviewers in a module of qory.yaml, or ask the base stack's maintainer to ship it.\n" +
			"  No module ships agent python-reviewers, and this list adds it:\n  - bind.agents.reviewers in the base stack"},
		{renamed(baseGroupFiles("", false), "security-reviewer"), "Ship the agent in a module of qory.yaml, or ask the base stack's maintainer to ship it.\n" +
			"  No module ships agent security-reviewer, and this list adds it:\n  - bind.agents.reviewers in the base stack"},
		{renamed(baseGroupFiles("---\nname: python-reviewer\n---\n", false), "python-code-reviewer"), "Exclude agents/python-code-reviewer in qory.yaml, or ask the base stack's maintainer to complete it.\n" +
			"  The base stack software@working-tree binds python-code-reviewer of module factory to agents/reviewers" + tail},
		{renamed(baseGroupFiles("---\nname: python-reviewer\n---\n", false), "python-style-reviewer"), "Exclude the agent in qory.yaml, or ask the base stack's maintainer to complete it.\n" +
			"  The base stack software@working-tree binds agents/python-style-reviewer of module factory to agents/reviewers" + tail},
		{both("python-code-reviewer"), "Exclude agents/python-code-reviewer in qory.yaml, or ask the base stack's maintainer to complete it.\n" +
			"  The contract roles/reviewer.yaml of module factory requires these parts, which agents/python-code-reviewer.md of module factory lacks:\n  - the tool Read" + lists},
		{both("python-style-reviewer"), "Exclude the agent in qory.yaml, or ask the base stack's maintainer to complete it.\n" +
			"  The contract roles/reviewer.yaml of module factory requires these parts, which agents/python-style-reviewer.md of module factory lacks:\n  - the tool Read" + lists},
	} {
		_, err := composeExtending(t, writeTree(t, c.files))
		if err == nil || err.Error() != c.want {
			t.Errorf("error:\n%v\nwant:\n%s", err, c.want)
			continue
		}
		first, _, _ := strings.Cut(c.want, "\n")
		if n := utf8.RuneCountInString(first); n > 100 {
			t.Errorf("fix line has %d characters: %s", n, first)
		}
	}
	for _, line := range []string{
		"Ship agent security-reviewer in a module of qory.yaml, or ask the base stack's maintainer to ship it.",
		"Exclude agents/python-style-reviewer in qory.yaml, or ask the base stack's maintainer to complete it.",
	} {
		if n := utf8.RuneCountInString(line); n != 101 {
			t.Errorf("%d characters, want 101: %s", n, line)
		}
	}
}

// TestABaseListsMemberWithoutAgentsAsksTheMaintainer is a member of the base module
// factory that the base's bind and factory's provides both add, in a base that lets an
// extending module ship no agents: the exclude fails there, and the fix asks the base
// stack's maintainer.
func TestABaseListsMemberWithoutAgentsAsksTheMaintainer(t *testing.T) {
	files := baseGroupFiles("---\nname: python-reviewer\n---\n", false)
	files["base/modules/factory/qory-module.yaml"] += "provides:\n  agents:\n    reviewers: [python-reviewer]\n"
	files["base/"+stack.FileName] = strings.Replace(files["base/"+stack.FileName], "extending: {kinds: [agents]}", "extending: {}", 1)
	want := "Ask the base stack's maintainer to complete agent python-reviewer.\n" +
		"  The contract roles/reviewer.yaml of module factory requires these parts, which agents/python-reviewer.md of module factory lacks:\n" +
		"  - the tool Read\n" +
		"  These lists add it:\n  - bind.agents.reviewers in the base stack\n  - provides.agents.reviewers of module factory"
	if _, err := composeExtending(t, writeTree(t, files)); err == nil || err.Error() != want {
		t.Errorf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestALongCompleteInModuleLineDropsTheModule is the member of
// TestABaseStackAndModuleMemberOffersAnExclude in a module of qory.yaml with a long name:
// the fix line offers to complete the agent without the module, which the cause line
// names.
func TestALongCompleteInModuleLineDropsTheModule(t *testing.T) {
	const long = "python-security-and-style-checks"
	files := baseGroupFiles("---\nname: python-reviewer\n---\n", true)
	files["python/qory-module.yaml"] = strings.Replace(files["python/qory-module.yaml"], "name: python\n", "name: "+long+"\n", 1)
	files["app/"+config.FileName] = strings.Replace(files["app/"+config.FileName], "- name: python\n", "- name: "+long+"\n", 1)
	want := "Exclude agents/python-reviewer in qory.yaml, or complete it.\n" +
		"  The contract roles/reviewer.yaml of module factory requires these parts, which agents/python-reviewer.md of module " + long + " lacks:\n" +
		"  - the tool Read\n" +
		"  These lists add it:\n  - bind.agents.reviewers in the base stack\n  - provides.agents.reviewers of module " + long
	if _, err := composeExtending(t, writeTree(t, files)); err == nil || err.Error() != want {
		t.Errorf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestALeftOutBindMemberNamesTheExcludeOfQoryYaml is the checkout's qory.yaml binding
// the group to other and python-reviewer, and excluding python-reviewer from the base
// module factory: the cause line names that exclude.
func TestALeftOutBindMemberNamesTheExcludeOfQoryYaml(t *testing.T) {
	files := baseGroupFiles("---\nname: python-reviewer\ntools: Read\n---\n", false)
	files["app/"+config.FileName] += "  bind:\n    agents:\n      reviewers: [other, python-reviewer]\n  modules:\n    - name: factory\n      exclude: {agents: [python-reviewer]}\n"
	want := "Compose agent python-reviewer, or remove it from bind.agents.reviewers in qory.yaml.\n" +
		"  The exclude of module factory in qory.yaml leaves out agent python-reviewer."
	if _, err := composeExtending(t, writeTree(t, files)); err == nil || err.Error() != want {
		t.Errorf("error:\n%v\nwant:\n%s", err, want)
	}
}

// TestALongLeftOutLineMovesTheBind is the left-out member of
// TestALeftOutBindMemberHasItsFixFirst with a name that makes the fix line 100
// characters, and one that makes it 101: the bind moves to the next line.
func TestALongLeftOutLineMovesTheBind(t *testing.T) {
	leftOut := func(name string) map[string]string {
		files := renamed(groupFiles(""), name)
		files[stack.FileName] = strings.Replace(files[stack.FileName], "source: {path: modules/python}\n", "source: {path: modules/python}\n    exclude: {agents: ["+name+"]}\n", 1)
		files[stack.FileName] += "bind:\n  agents:\n    reviewers: [code-reviewer, " + name + "]\n"
		files["modules/python/agents/other.md"] = "---\nname: other\n---\n"
		return files
	}
	for _, c := range []struct{ name, want string }{
		{"python-codestyle-reviewer", "Compose agent python-codestyle-reviewer, or remove it from bind.agents.reviewers in qory-stack.yaml.\n" +
			"  Module python leaves out agent python-codestyle-reviewer."},
		{"python-code-style-reviewer", "Compose agent python-code-style-reviewer, or remove it from the list that adds it.\n" +
			"  Module python leaves out agent python-code-style-reviewer.\n" +
			"  This list adds it:\n  - bind.agents.reviewers in qory-stack.yaml"},
	} {
		if _, err := composeTree(t, leftOut(c.name)); err == nil || err.Error() != c.want {
			t.Errorf("error:\n%v\nwant:\n%s", err, c.want)
		}
	}
}
