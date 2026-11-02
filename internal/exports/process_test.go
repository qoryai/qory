package exports_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/exports"
)

// process is a valid process.yaml whose stack is support, which declares draft-an-answer
// and check-the-tone.
const process = `apiVersion: qory.dev/v1alpha1
name: Support desk
summary: Agents draft answers to support tickets.
description: An agent drafts each answer, and a person checks it.
type: customer-care
stack: support
process:
  schema: qory.process/v1
  stages:
    - name: Answer
      sequence:
        - workflow: draft-an-answer
        - loop: {rounds: 2, body: [{workflow: check-the-tone}]}
    - name: Check
`

// supportStack is the stack file of support, with the two workflows process runs.
const supportStack = "apiVersion: qory.dev/v1alpha1\nmodules: [{name: desk}]\nworkflows:\n  draft-an-answer: {access: read}\n  check-the-tone: {}\n"

// factory is a repository that exports the stack support and the module desk, with the
// stack's file set to supportStack and the module's process.yaml set to body; it
// returns the exports read.
func factory(t *testing.T, body string) *exports.Exports {
	t.Helper()
	root := write(t, "exports:\n  stacks: [support]\n  modules: [desk]\n")
	if err := os.MkdirAll(filepath.Join(root, "stacks", "support"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "stacks", "support", exports.StackFileName), []byte(supportStack), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(root, "modules", "desk", exports.ModuleFileName))
	if err := os.WriteFile(filepath.Join(root, "modules", "desk", exports.ProcessFileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := exports.Read(root)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// TestVerifyLeavesAModuleWithoutProcess is a module with no process.yaml: nothing more
// is checked. One with a valid file passes.
func TestVerifyLeavesAModuleWithoutProcess(t *testing.T) {
	e := factory(t, process)
	if err := e.Verify(); err != nil {
		t.Fatalf("a valid process.yaml: %v", err)
	}
	if err := os.Remove(filepath.Join(e.Root, "modules", "desk", exports.ProcessFileName)); err != nil {
		t.Fatal(err)
	}
	if err := e.Verify(); err != nil {
		t.Fatalf("no process.yaml: %v", err)
	}
}

// TestVerifyChecksTheProcessFields is each mistake in a process.yaml: a field left out,
// a text over its limit, a stack the section does not export, a process that is not an
// object, a process document without its schema or a list of stages, a key the file does
// not have and another apiVersion. Each refusal starts with the file and then the fix.
func TestVerifyChecksTheProcessFields(t *testing.T) {
	without := func(key string) string {
		var lines []string
		for _, line := range strings.Split(process, "\n") {
			if !strings.HasPrefix(line, key+":") && !(key == "process" && strings.HasPrefix(line, "  ")) {
				lines = append(lines, line)
			}
		}
		return strings.Join(lines, "\n")
	}
	with := func(key, value string) string {
		return strings.Replace(without(key), "\n", "\n"+key+": "+value+"\n", 1)
	}
	words := strings.TrimSpace(strings.Repeat("word ", 81))
	for _, c := range []struct{ body, want string }{
		{"", "add apiVersion: qory.dev/v1alpha1: the file has no apiVersion"},
		{without("apiVersion"), "add apiVersion: qory.dev/v1alpha1: the file has no apiVersion"},
		{with("apiVersion", "qory.dev/v2"), `apiVersion "qory.dev/v2" is not one this qory reads; versions: qory.dev/v1alpha1`},
		{without("name"), "add name, at most 60 characters: the file has no name"},
		{with("name", strings.Repeat("é", 61)), "shorten name to at most 60 characters: name has 61"},
		{without("summary"), "add summary, at most 200 characters: the file has no summary"},
		{with("summary", strings.Repeat("s", 201)), "shorten summary to at most 200 characters: summary has 201"},
		{with("description", `"  "`), "add description, at most 1200 characters: the file has no description"},
		{with("description", strings.Repeat("d", 1201)), "shorten description to at most 1200 characters: description has 1201"},
		{with("description", words), "shorten description to at most 80 words: description has 81"},
		{without("type"), "add type, the workspace type the template sets up: the file has no type"},
		{with("type", `" "`), "add type, the workspace type the template sets up: the file has no type"},
		{without("stack"), "add stack, a name that exports.stacks lists: the file has no stack"},
		{with("stack", "web"), "set stack to a name that exports.stacks lists: stack is web, and exports.stacks lists support"},
		{with("stack", `"web\nqory: all checks passed"`), "set stack to one line of text: it has a line break"},
		{with("stack", `"web\rx"`), "set stack to one line of text: it has a line break"},
		{with("stack", `"web\tx"`), "set stack to one line of text: it has a control character"},
		{without("process"), "add process, the template's process document as an object: the file has no process"},
		{with("process", "~"), "add process, the template's process document as an object: the file has no process"},
		{with("process", "[a]"), "set process to an object, the template's process document: process is a list"},
		{with("process", "stages"), "set process to an object, the template's process document: process is a string"},
		{with("process", "3"), "set process to an object, the template's process document: process is a number"},
		{with("process", "{stages: []}"), "add schema: qory.process/v1 under process: process has no schema"},
		{with("process", "{schema: qory.process/v2, stages: []}"), `set process.schema to qory.process/v1: process.schema is "qory.process/v2"`},
		{with("process", "{schema: [a], stages: []}"), "set process.schema to qory.process/v1: process.schema is a list"},
		{with("process", "{schema: qory.process/v1}"), "add stages under process, the list of the process's stages: process has no stages"},
		{with("process", "{schema: qory.process/v1, stages: {a: 1}}"), "set process.stages to a list of stages: process.stages is an object"},
		{process + "owner: acme\n", `line 15: key "owner" is not one process.yaml reads`},
	} {
		e := factory(t, c.body)
		file := filepath.Join(e.Root, "modules", "desk", exports.ProcessFileName)
		if err := e.Verify(); err == nil || err.Error() != file+": "+c.want {
			t.Errorf("%q: %v\nwant %s: %s", c.body, err, file, c.want)
		}
	}
	// An alias is refused as in every file qory reads.
	e := factory(t, strings.Replace(process, "summary: Agents", "summary: &s Agents", 1)+"x: *s\n")
	if err := e.Verify(); err == nil || !strings.Contains(err.Error(), "*s is a YAML alias, which process.yaml may not contain") {
		t.Errorf("an alias: %v", err)
	}
	// A description of exactly 80 words, and a name of 60 characters, pass.
	body := strings.Replace(with("name", strings.Repeat("n", 60)), "description: An agent drafts each answer, and a person checks it.", "description: "+strings.TrimSpace(strings.Repeat("word ", 80)), 1)
	if err := factory(t, body).Verify(); err != nil {
		t.Errorf("the limits: %v", err)
	}
}

// TestVerifyWantsEachWorkflowDeclared is a process whose stages run workflows the stack
// does not declare, in a sequence and in a loop's body: one line per stage and workflow,
// each with the stage, the workflow and the stack file. A workflow named twice in one
// stage is one line.
func TestVerifyWantsEachWorkflowDeclared(t *testing.T) {
	body := strings.Replace(process, "check-the-tone}]}", "check-the-tone}, {workflow: send-a-reply}, {workflow: send-a-reply}]}", 1) +
		"      sequence: [{workflow: send-a-reply}]\n    - sequence: [{workflow: close-a-ticket}]\n"
	e := factory(t, body)
	file := filepath.Join(e.Root, "modules", "desk", exports.ProcessFileName)
	stackFile := filepath.Join("stacks", "support", exports.StackFileName)
	want := strings.Join([]string{
		file + ": declare send-a-reply under workflows in " + stackFile + ", or remove it from stage Answer: stack support does not declare it",
		file + ": declare send-a-reply under workflows in " + stackFile + ", or remove it from stage Check: stack support does not declare it",
		file + ": declare close-a-ticket under workflows in " + stackFile + ", or remove it from stage 3: stack support does not declare it",
	}, "\n")
	if err := e.Verify(); err == nil || err.Error() != want {
		t.Errorf("%v\nwant:\n%s", err, want)
	}
	// A stack with no workflows declares none of them.
	if err := os.WriteFile(filepath.Join(e.Root, stackFile), []byte("apiVersion: qory.dev/v1alpha1\nmodules: [{name: desk}]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := e.Verify()
	if err == nil || strings.Count(err.Error(), "\n") != 4 || !strings.Contains(err.Error(), "declare draft-an-answer under workflows in "+stackFile+", or remove it from stage Answer") {
		t.Errorf("no workflows: %v", err)
	}
}

// TestVerifyWantsTheProcessStackListed is a process.yaml whose repository exports no
// stack: the refusal says that exports.stacks lists none.
func TestVerifyWantsTheProcessStackListed(t *testing.T) {
	root := write(t, "exports:\n  modules: [desk]\n")
	touch(t, filepath.Join(root, "modules", "desk", exports.ModuleFileName))
	file := filepath.Join(root, "modules", "desk", exports.ProcessFileName)
	if err := os.WriteFile(file, []byte(process), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := exports.Read(root)
	if err != nil {
		t.Fatal(err)
	}
	want := file + ": set stack to a name that exports.stacks lists: stack is support, and exports.stacks lists no stacks"
	if err := e.Verify(); err == nil || err.Error() != want {
		t.Errorf("%v\nwant %s", err, want)
	}
}

// TestVerifyReadsProcessUnderExportsDir is a module under the modules directory that
// exports.dir sets: its process.yaml is checked there.
func TestVerifyReadsProcessUnderExportsDir(t *testing.T) {
	root := write(t, "exports:\n  dir: {stacks: lib/stacks, modules: lib/modules}\n  stacks: [support]\n  modules: [desk]\n")
	if err := os.MkdirAll(filepath.Join(root, "lib", "stacks", "support"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "lib", "stacks", "support", exports.StackFileName), []byte(supportStack), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(root, "lib", "modules", "desk", exports.ModuleFileName))
	file := filepath.Join(root, "lib", "modules", "desk", exports.ProcessFileName)
	if err := os.WriteFile(file, []byte(strings.Replace(process, "draft-an-answer", "write-a-reply", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := exports.Read(root)
	if err != nil {
		t.Fatal(err)
	}
	want := file + ": declare write-a-reply under workflows in " + filepath.Join("lib", "stacks", "support", exports.StackFileName) + ", or remove it from stage Answer: stack support does not declare it"
	if err := e.Verify(); err == nil || err.Error() != want {
		t.Errorf("%v\nwant %s", err, want)
	}
}

// TestProcessSchemaMatchesTheReader keeps process.schema.json and the reader in step:
// each document is accepted or refused by both.
func TestProcessSchemaMatchesTheReader(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile("../../contracts/harness/v1/process.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		body  string
		valid bool
	}{
		{process, true},
		{strings.Replace(process, "name: Support desk", "name: "+strings.Repeat("n", 61), 1), false},
		{strings.Replace(process, "type: customer-care\n", "", 1), false},
		{strings.Replace(process, "stack: support", `stack: ""`, 1), false},
		{strings.Replace(process, "stack: support", `stack: "support\nx"`, 1), false},
		{strings.Replace(process, "description: An agent", "description: "+strings.Repeat("w ", 80)+"An agent", 1), false},
		{strings.Replace(process, "description: An agent", "description: "+strings.Repeat("w ", 70)+"An agent", 1), true},
		{strings.Replace(process, "  schema: qory.process/v1\n", "", 1), false},
		{strings.Replace(process, "schema: qory.process/v1", "schema: qory.process/v2", 1), false},
		{strings.Replace(process, "  stages:\n", "  stages: {}\n  other:\n", 1), false},
		{strings.Replace(process, "  schema: qory.process/v1\n", "  schema: qory.process/v1\n  machine: small\n", 1), true},
		{process + "owner: acme\n", false},
	} {
		var doc any
		if err := yaml.Unmarshal([]byte(c.body), &doc); err != nil {
			t.Fatal(err)
		}
		j, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(j, &doc); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("schema: %q valid=%v err=%v", c.body, c.valid, err)
		}
		if err := factory(t, c.body).Verify(); (err == nil) != c.valid {
			t.Errorf("reader: %q valid=%v err=%v", c.body, c.valid, err)
		}
	}
}

// TestVerifyRefusesAProcessLinkOutside is a process.yaml that is a link to a file outside
// the module's folder, by an absolute path, by a relative one, and to a file that is not
// there: each is refused with the fix first, and no line contains the target's content
// or its path. A link to a file inside the folder is read as the file.
func TestVerifyRefusesAProcessLinkOutside(t *testing.T) {
	e := factory(t, process)
	folder := filepath.Join(e.Root, "modules", "desk")
	file := filepath.Join(folder, exports.ProcessFileName)
	outside := filepath.Join(t.TempDir(), "elsewhere.yaml")
	if err := os.WriteFile(outside, []byte("owner: TARGET-CONTENT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := file + ": replace process.yaml with a file inside the module: it is a link to a place outside the module"
	for _, target := range []string{outside, filepath.Join("..", "..", "elsewhere.yaml"), "../gone.yaml", "/gone.yaml"} {
		if target == filepath.Join("..", "..", "elsewhere.yaml") {
			if err := os.WriteFile(filepath.Join(e.Root, "elsewhere.yaml"), []byte("owner: TARGET-CONTENT\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := os.Symlink(target, file); err != nil {
			t.Fatal(err)
		}
		err := e.Verify()
		if err == nil || err.Error() != want {
			t.Errorf("a link to %s: %v\nwant %s", target, err, want)
		}
		if err != nil && (strings.Contains(err.Error(), "TARGET-CONTENT") || strings.Contains(err.Error(), "elsewhere") || strings.Contains(err.Error(), "gone")) {
			t.Errorf("a link to %s: the refusal contains the target: %v", target, err)
		}
	}
	// A link inside the folder is followed: a valid file passes, and a wrong one is
	// refused under the link's path.
	if err := os.WriteFile(filepath.Join(folder, "real.yaml"), []byte(process), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.yaml", file); err != nil {
		t.Fatal(err)
	}
	if err := e.Verify(); err != nil {
		t.Errorf("a link inside the module: %v", err)
	}
	if err := os.WriteFile(filepath.Join(folder, "real.yaml"), []byte(process+"owner: acme\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.Verify(); err == nil || err.Error() != file+`: line 15: key "owner" is not one process.yaml reads` {
		t.Errorf("a wrong file behind a link inside the module: %v", err)
	}
	// A link inside the folder to a file that is not there is no process.yaml.
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing.yaml", file); err != nil {
		t.Fatal(err)
	}
	if err := e.Verify(); err != nil {
		t.Errorf("a dangling link inside the module: %v", err)
	}
}

// TestVerifyRefusesBadWorkflowNames is a stage entry whose workflow is empty, null, a
// list, or more than one line, in a sequence and in a loop's body, and a stage name of
// two lines: each is refused on one line with the fix first and the entry's place, and
// no text of the file starts a line of the error.
func TestVerifyRefusesBadWorkflowNames(t *testing.T) {
	body := strings.Replace(process, "        - loop: {rounds: 2, body: [{workflow: check-the-tone}]}\n",
		"        - workflow: \"\"\n        - workflow: ~\n        - workflow: [a]\n        - workflow: \"x\\nqory: all checks passed\"\n"+
			"        - loop: {rounds: 2, body: [{workflow: check-the-tone}, {workflow: \"\"}, {workflow: \"y\\tz\"}]}\n", 1)
	body = strings.Replace(body, "    - name: Check\n", "    - name: \"Check\\nqory: all checks passed\"\n      sequence: [{workflow: \"\"}]\n", 1)
	e := factory(t, body)
	file := filepath.Join(e.Root, "modules", "desk", exports.ProcessFileName)
	want := strings.Join([]string{
		file + ": set the workflow of entry 2 in stage Answer to a workflow name: it is empty",
		file + ": set the workflow of entry 3 in stage Answer to a workflow name: it is null",
		file + ": set the workflow of entry 4 in stage Answer to a workflow name: it is a list",
		file + ": set the workflow of entry 5 in stage Answer to a workflow name: it has a line break",
		file + ": set the workflow of entry 2 in the loop of entry 6 in stage Answer to a workflow name: it is empty",
		file + ": set the workflow of entry 3 in the loop of entry 6 in stage Answer to a workflow name: it has a control character",
		file + ": set the name of stage 2 to one line of text: it has a line break",
		file + ": set the workflow of entry 1 in stage 2 to a workflow name: it is empty",
	}, "\n")
	err := e.Verify()
	if err == nil || err.Error() != want {
		t.Fatalf("%v\nwant:\n%s", err, want)
	}
	for _, line := range strings.Split(err.Error(), "\n") {
		if !strings.HasPrefix(line, file+": ") {
			t.Errorf("a line that does not start with the file: %q", line)
		}
	}
}
