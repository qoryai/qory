package exports_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

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
	return factoryIn(t, t.TempDir(), body)
}

// factoryIn is factory with the repository in the directory root.
func factoryIn(t *testing.T, root, body string) *exports.Exports {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, exports.FileName), []byte("apiVersion: qory.dev/v1alpha1\nexports:\n  stacks: [support]\n  modules: [desk]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
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
// not have and another apiVersion. Each refusal names the file, see refusalIn.
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
		{"", "Add apiVersion: qory.dev/v1alpha1 to process.yaml.\n  The file has no apiVersion.\n  The file is {file}."},
		{without("apiVersion"), "Add apiVersion: qory.dev/v1alpha1 to process.yaml.\n  The file has no apiVersion.\n  The file is {file}."},
		{with("apiVersion", "qory.dev/v2"), "Set apiVersion to qory.dev/v1alpha1.\n  This qory reads qory.dev/v1alpha1, and apiVersion is \"qory.dev/v2\".\n  apiVersion is in {file}."},
		{without("name"), "Add name to process.yaml, at most 60 characters.\n  The file has no name.\n  The file is {file}."},
		{with("name", strings.Repeat("é", 61)), "Shorten name in process.yaml to at most 60 characters.\n  The name has 61 characters.\n  The file is {file}."},
		{without("summary"), "Add summary to process.yaml, at most 200 characters.\n  The file has no summary.\n  The file is {file}."},
		{with("summary", strings.Repeat("s", 201)), "Shorten summary in process.yaml to at most 200 characters.\n  The summary has 201 characters.\n  The file is {file}."},
		{with("description", `"  "`), "Add description to process.yaml, at most 1200 characters.\n  The file has no description.\n  The file is {file}."},
		{with("description", strings.Repeat("d", 1201)), "Shorten description in process.yaml to at most 1200 characters.\n  The description has 1201 characters.\n  The file is {file}."},
		{with("description", words), "Shorten description in process.yaml to at most 80 words.\n  The description has 81 words.\n  The file is {file}."},
		{without("type"), "Add type to process.yaml, the workspace type the template sets up.\n  The file has no type.\n  The file is {file}."},
		{with("type", `" "`), "Add type to process.yaml, the workspace type the template sets up.\n  The file has no type.\n  The file is {file}."},
		{without("stack"), "Add stack to process.yaml, a name that exports.stacks lists.\n  The file has no stack.\n  The file is {file}."},
		{with("stack", "web"), "Set stack in process.yaml to a name that exports.stacks lists.\n  The value is web, and exports.stacks lists support.\n  The file is {file}."},
		{with("stack", `"web\nqory: all checks passed"`), "Set stack in process.yaml to one line of text.\n  The value has a line break.\n  The file is {file}."},
		{with("stack", `"web\rx"`), "Set stack in process.yaml to one line of text.\n  The value has a line break.\n  The file is {file}."},
		{with("stack", `"web\tx"`), "Set stack in process.yaml to one line of text.\n  The value has a control character.\n  The file is {file}."},
		{without("process"), "Add process to process.yaml, the template's process document as an object.\n  The file has no process.\n  The file is {file}."},
		{with("process", "~"), "Add process to process.yaml, the template's process document as an object.\n  The file has no process.\n  The file is {file}."},
		{with("process", "[a]"), "Set process in process.yaml to an object.\n  process is the template's process document, and the value is a list.\n  The file is {file}."},
		{with("process", "stages"), "Set process in process.yaml to an object.\n  process is the template's process document, and the value is a string.\n  The file is {file}."},
		{with("process", "3"), "Set process in process.yaml to an object.\n  process is the template's process document, and the value is a number.\n  The file is {file}."},
		{with("process", "{stages: []}"), "Add schema: qory.process/v1 under process.\n  process has no schema.\n  The file is {file}."},
		{with("process", "{schema: qory.process/v2, stages: []}"), "Set process.schema to qory.process/v1.\n  process.schema is \"qory.process/v2\".\n  The file is {file}."},
		{with("process", "{schema: [a], stages: []}"), "Set process.schema to qory.process/v1.\n  process.schema is a list.\n  The file is {file}."},
		{with("process", "{schema: qory.process/v1}"), "Add stages under process, the list of the process's stages.\n  process has no stages.\n  The file is {file}."},
		{with("process", "{schema: qory.process/v1, stages: {a: 1}}"), "Set process.stages to a list of stages.\n  process.stages is an object.\n  The file is {file}."},
		{process + "owner: acme\n", "Remove the key \"owner\" from line 15 of process.yaml.\n  The key is not one process.yaml reads.\n  The file is {file}."},
	} {
		e := factory(t, c.body)
		file := filepath.Join(e.Root, "modules", "desk", exports.ProcessFileName)
		want := refusalIn(file, c.want)
		if err := e.Verify(); err == nil || err.Error() != want {
			t.Errorf("%q: %v\nwant %s", c.body, err, want)
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

// TestVerifyKeepsTheFixLineShortUnderALongPath is each refusal of a process.yaml that
// names the file, with the repository under a directory of 150 characters: the first
// line, the fix, stays at most 100 characters and has no path; the path comes below it.
func TestVerifyKeepsTheFixLineShortUnderALongPath(t *testing.T) {
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
	for name, body := range map[string]string{
		"a field missing":         without("name"),
		"a field too long":        with("summary", strings.Repeat("s", 201)),
		"a stack not listed":      with("stack", "web"),
		"a process not object":    with("process", "[a]"),
		"an unknown key":          process + "owner: acme\n",
		"a workflow undeclared":   strings.Replace(process, "workflow: draft-an-answer", "workflow: send-a-reply", 1),
		"a bad type name":         withTypes("\n  - name: Mockup"),
		"a type key unknown":      withTypes("\n  - {name: mockup, owner: acme}"),
		"a type name twice":       withTypes("\n  - name: mockup\n  - name: mockup"),
		"a type left incomplete":  withTypes("\n  - {name: a-type, size_limit: 1}"),
		"apiVersion missing":      without("apiVersion"),
		"type missing":            without("type"),
		"stack missing":           without("stack"),
		"stack with a line break": with("stack", `"web\nqory: all checks passed"`),
		"process missing":         without("process"),
		"description too long":    with("description", strings.TrimSpace(strings.Repeat("word ", 81))),
		"schema missing":          with("process", "{stages: []}"),
		"schema wrong":            with("process", "{schema: qory.process/v2, stages: []}"),
		"stages missing":          with("process", "{schema: qory.process/v1}"),
		"stages not a list":       with("process", "{schema: qory.process/v1, stages: {a: 1}}"),
		"a workflow empty":        strings.Replace(process, "workflow: draft-an-answer", `workflow: ""`, 1),
		"a stage name two lines":  strings.Replace(process, "    - name: Check\n", "    - name: \"Check\\nx\"\n", 1),
		"types not a list":        withTypes(" mockup"),
		"a type not an object":    withTypes("\n  - mockup"),
		"a type without a name":   withTypes("\n  - {base_view: file}"),
		"a type name not text":    withTypes("\n  - {name: 3}"),
		"a type name unquoted":    withTypes("\n  - {name: 2026-11-02}"),
		"a type key twice":        withTypes("\n  - {name: mockup, name: screenshot}"),
		"a base_view unknown":     withTypes(custom("text", "size_limit: 1, max_files: 1")),
		"max_files too many":      withTypes(custom("file", "size_limit: 1, max_files: 21")),
		"max_files of structure":  withTypes(custom("structured", "size_limit: 1, max_files: 2, json_schema: {}")),
		"json_schema missing":     withTypes(custom("structured", "size_limit: 1, max_files: 1")),
		"json_schema not object":  withTypes(custom("structured", "size_limit: 1, max_files: 1, json_schema: [a]")),
		"a schema key not text":   withTypes(custom("structured", "size_limit: 1, max_files: 1, json_schema: {1: a}")),
		"a schema key twice":      withTypes(custom("structured", "size_limit: 1, max_files: 1, json_schema: {a: 1, a: 2}")),
		"a schema number":         withTypes(custom("structured", "size_limit: 1, max_files: 1, json_schema: {const: .inf}")),
		"a schema draft":          withTypes(custom("structured", "size_limit: 1, max_files: 1, json_schema: {$schema: 'http://json-schema.org/draft-04/schema#'}")),
		"a schema too big":        withTypes(custom("structured", "size_limit: 1, max_files: 1, json_schema: {x-a: ["+strings.Repeat("1, ", 1100)+"1]}")),
	} {
		root := filepath.Join(t.TempDir(), strings.Repeat("d", 150))
		err := factoryIn(t, root, body).Verify()
		if err == nil {
			t.Errorf("%s: no refusal", name)
			continue
		}
		lines := strings.Split(err.Error(), "\n")
		if n := utf8.RuneCountInString(lines[0]); n > 100 {
			t.Errorf("%s: the first line has %d characters, past 100: %s", name, n, lines[0])
		}
		if strings.Contains(lines[0], root) || !strings.Contains(err.Error(), root) {
			t.Errorf("%s: want the path below the first line: %v", name, err)
		}
	}
	// A process.yaml that is a link to a place outside the module.
	root := filepath.Join(t.TempDir(), strings.Repeat("d", 150))
	e := factoryIn(t, root, process)
	file := filepath.Join(e.Root, "modules", "desk", exports.ProcessFileName)
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/gone.yaml", file); err != nil {
		t.Fatal(err)
	}
	err := e.Verify()
	if err == nil {
		t.Fatal("a link outside the module: no refusal")
	}
	if first := strings.Split(err.Error(), "\n")[0]; utf8.RuneCountInString(first) > 100 || strings.Contains(first, root) || !strings.Contains(err.Error(), root) {
		t.Errorf("a link outside the module: want the path below the first line: %v", err)
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
		"Declare send-a-reply in stack support, or remove it from stage Answer.",
		"  Stage Answer of " + file + " runs send-a-reply, and stack support does not declare it under workflows.",
		"  The stack is in " + stackFile + ".",
		"Declare send-a-reply in stack support, or remove it from stage Check.",
		"  Stage Check of " + file + " runs send-a-reply, and stack support does not declare it under workflows.",
		"  The stack is in " + stackFile + ".",
		"Declare close-a-ticket in stack support, or remove it from stage 3.",
		"  Stage 3 of " + file + " runs close-a-ticket, and stack support does not declare it under workflows.",
		"  The stack is in " + stackFile + ".",
	}, "\n")
	if err := e.Verify(); err == nil || err.Error() != want {
		t.Errorf("%v\nwant:\n%s", err, want)
	}
	// A stack with no workflows declares none of them.
	if err := os.WriteFile(filepath.Join(e.Root, stackFile), []byte("apiVersion: qory.dev/v1alpha1\nmodules: [{name: desk}]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := e.Verify()
	if err == nil || strings.Count(err.Error(), "\n") != 14 || !strings.Contains(err.Error(), "Declare draft-an-answer in stack support, or remove it from stage Answer.\n") {
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
	want := "Set stack in process.yaml to a name that exports.stacks lists.\n  The value is support, and exports.stacks lists no stacks.\n  The file is " + file + "."
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
	want := "Declare write-a-reply in stack support, or remove it from stage Answer.\n" +
		"  Stage Answer of " + file + " runs write-a-reply, and stack support does not declare it under workflows.\n" +
		"  The stack is in " + filepath.Join("lib", "stacks", "support", exports.StackFileName) + "."
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
		{strings.Replace(process, "workflow: draft-an-answer", `workflow: ""`, 1), false},
		{strings.Replace(process, "workflow: draft-an-answer", `workflow: " "`, 1), false},
		{strings.Replace(process, "workflow: draft-an-answer", "workflow: ~", 1), false},
		{strings.Replace(process, "workflow: draft-an-answer", "workflow: 123", 1), false},
		{strings.Replace(process, "workflow: draft-an-answer", "workflow: true", 1), false},
		{strings.Replace(process, "workflow: draft-an-answer", "workflow: [a]", 1), false},
		{strings.Replace(process, "workflow: draft-an-answer", `workflow: "a\nb"`, 1), false},
		{strings.Replace(process, "workflow: check-the-tone", `workflow: ""`, 1), false},
		{strings.Replace(process, "workflow: check-the-tone", `workflow: "a\tb"`, 1), false},
		{strings.Replace(process, "name: Answer", `name: "Answer\nx"`, 1), false},
		{strings.Replace(process, "name: Answer", `name: "\t"`, 1), false},
		{strings.Replace(process, "name: Answer", "name: 3", 1), true},
		{strings.Replace(process, "name: Answer", `name: ""`, 1), true},
		{process + structured("submission") + "submit: {type: submission, send: end_of_run}\n", true},
		{process + structured("submission") + "submit: {type: submission, send: after_gate}\n", true},
		{process + structured("submission") + "submit: {type: submission, send: end_of_run, command: open-a-draft}\n", false},
		{process + structured("submission") + "submit: {type: submission}\n", false},
		{process + structured("submission") + "submit: {send: end_of_run}\n", false},
		{process + structured("submission") + "submit: {type: submission, send: later}\n", false},
		{process + structured("submission") + "submit: {type: submission, send: [end_of_run]}\n", false},
		{process + structured("submission") + "submit: {type: Submission, send: end_of_run}\n", false},
		{process + structured("submission") + "submit: {type: 3, send: end_of_run}\n", false},
		{process + structured(strings.Repeat("a", 64)) + "submit: {type: " + strings.Repeat("a", 64) + ", send: end_of_run}\n", true},
		{process + structured("submission") + "submit: {type: " + strings.Repeat("a", 65) + ", send: end_of_run}\n", false},
		{process + structured("submission") + "submit:\n  - {type: submission, send: end_of_run}\n", false},
		{process + structured("submission") + "submit: ~\n", false},
		{process + structured("submission") + "submit: submission\n", false},
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
	want := "Replace process.yaml with a file inside the module.\n  It is a link to a place outside the module.\n  The file is " + file + "."
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
	if err := e.Verify(); err == nil || err.Error() != "Remove the key \"owner\" from line 15 of process.yaml.\n  The key is not one process.yaml reads.\n  The file is "+file+"." {
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
	refuse := func(fix, cause string) string {
		return fix + "\n  " + cause + "\n  The file is " + file + "."
	}
	want := strings.Join([]string{
		refuse("Set the workflow of entry 2 in stage Answer to a workflow name.", "The workflow is empty."),
		refuse("Set the workflow of entry 3 in stage Answer to a workflow name.", "The workflow is null."),
		refuse("Set the workflow of entry 4 in stage Answer to a workflow name.", "The workflow is a list."),
		refuse("Set the workflow of entry 5 in stage Answer to a workflow name.", "The workflow has a line break."),
		refuse("Set the workflow of entry 2 in the loop of entry 6 in stage Answer to a workflow name.", "The workflow is empty."),
		refuse("Set the workflow of entry 3 in the loop of entry 6 in stage Answer to a workflow name.", "The workflow has a control character."),
		refuse("Set the name of stage 2 to one line of text.", "The name has a line break."),
		refuse("Set the workflow of entry 1 in stage 2 to a workflow name.", "The workflow is empty."),
	}, "\n")
	err := e.Verify()
	if err == nil || err.Error() != want {
		t.Fatalf("%v\nwant:\n%s", err, want)
	}
	for _, line := range strings.Split(err.Error(), "\n") {
		if !strings.HasPrefix(line, "Set the ") && !strings.HasPrefix(line, "  ") {
			t.Errorf("a line that starts with no fix and no indent: %q", line)
		}
	}
}

// TestVerifyRefusesAWorkflowThatIsNoString is a workflow written as a number, a boolean
// or a date, which YAML reads as no string: each is refused with the entry's place, and
// the same text quoted is a name the stack must declare.
func TestVerifyRefusesAWorkflowThatIsNoString(t *testing.T) {
	for _, c := range []struct{ value, why string }{
		{"123", "is a number"},
		{"1.5", "is a number"},
		{"true", "is a boolean"},
		{"2026-11-02", "is not a string"},
	} {
		e := factory(t, strings.Replace(process, "workflow: draft-an-answer", "workflow: "+c.value, 1))
		file := filepath.Join(e.Root, "modules", "desk", exports.ProcessFileName)
		want := "Set the workflow of entry 1 in stage Answer to a workflow name.\n  The workflow " + c.why + ".\n  The file is " + file + "."
		if err := e.Verify(); err == nil || err.Error() != want {
			t.Errorf("%s: %v\nwant %s", c.value, err, want)
		}
	}
	e := factory(t, strings.Replace(process, "workflow: draft-an-answer", `workflow: "123"`, 1))
	if err := e.Verify(); err == nil || !strings.Contains(err.Error(), "Declare 123 in ") {
		t.Errorf("a quoted number: %v", err)
	}
}

// refusalIn is want, a refusal of the process.yaml at file: one written fix first has the
// file at {file}, and any other starts with the file.
func refusalIn(file, want string) string {
	if strings.Contains(want, "{file}") {
		return strings.ReplaceAll(want, "{file}", file)
	}
	return file + ": " + want
}

// withTypes is process with artifact_types set to the YAML list body.
func withTypes(body string) string { return process + "artifact_types:" + body + "\n" }

// custom is an entry of artifact_types: a type of the base view with the keys given.
func custom(view, rest string) string {
	return "\n  - {name: a-type, base_view: " + view + ", " + rest + "}"
}

// TestVerifyPassesArtifactTypes is each shape of artifact_types that passes: a name
// alone, a custom type of each base view at its limits, a list with no entries, and a
// JSON Schema in each draft that a custom type may use.
func TestVerifyPassesArtifactTypes(t *testing.T) {
	for _, body := range []string{
		" []",
		"\n  - name: mockup",
		"\n  - name: \"123\"\n  - name: a-1-b",
		"\n  - name: " + strings.Repeat("a", 64),
		custom("document", "size_limit: 10485760, max_files: 20"),
		custom("image", "size_limit: 52428800, max_files: 1"),
		custom("video", "size_limit: 2147483648, max_files: 5"),
		custom("file", "size_limit: 1073741824, max_files: 2"),
		custom("structured", "size_limit: 1048576, max_files: 1, json_schema: {}"),
		custom("structured", "size_limit: 1, max_files: 1, json_schema: {$schema: 'http://json-schema.org/draft-07/schema#', type: object}"),
		custom("structured", "size_limit: 1, max_files: 1, json_schema: {$schema: 'https://json-schema.org/draft/2020-12/schema', type: object}"),
		custom("structured", "size_limit: 1, max_files: 1, json_schema: {$defs: {a: {type: string}}, properties: {a: {$ref: '#/$defs/a'}}}"),
		custom("structured", "size_limit: 1, max_files: 1, json_schema: {const: 2026-11-02}"),
		// The metaschemas are built in, and a reference the schema does not reach is not
		// followed.
		custom("structured", "size_limit: 1, max_files: 1, json_schema: {$ref: 'https://json-schema.org/draft/2020-12/schema'}"),
		custom("structured", "size_limit: 1, max_files: 1, json_schema: {$ref: 'http://json-schema.org/draft-07/schema'}"),
		custom("structured", "size_limit: 1, max_files: 1, json_schema: {$defs: {a: {$ref: 'https://schema.example/a.json'}}}"),
		// A URL that is the schema's $id is inside the schema.
		custom("structured", "size_limit: 1, max_files: 1, json_schema: {$id: 'https://a.example/x', $defs: {b: {}}, properties: {a: {$ref: 'https://a.example/x#/$defs/b'}}}"),
	} {
		if err := factory(t, withTypes(body)).Verify(); err != nil {
			t.Errorf("%q: %v", body, err)
		}
	}
}

// TestVerifyChecksArtifactTypes is each mistake in artifact_types. Each refusal names the
// file, see refusalIn.
func TestVerifyChecksArtifactTypes(t *testing.T) {
	const structured = "size_limit: 65536, max_files: 1, json_schema: "
	const name = "artifact type a-type"
	for _, c := range []struct{ body, want string }{
		{" ~", "Set artifact_types to a list of artifact types, or remove it.\n  artifact_types is null.\n  The file is {file}."},
		{" {name: mockup}", "Set artifact_types to a list of artifact types, or remove it.\n  artifact_types is an object.\n  The file is {file}."},
		{" mockup", "Set artifact_types to a list of artifact types, or remove it.\n  artifact_types is a string.\n  The file is {file}."},
		{"\n  - mockup", "Set entry 1 of artifact_types to an object with a name.\n  The entry is a string.\n  The entry is in {file}."},
		{"\n  - ~", "Set entry 1 of artifact_types to an object with a name.\n  The entry is null.\n  The entry is in {file}."},
		{"\n  - {base_view: file}", "Add name to entry 1 of artifact_types.\n  The entry has no name.\n  The entry is in {file}."},
		{"\n  - {name: 3}", "Set the name of entry 1 of artifact_types to a string.\n  A name is lowercase letters and digits in groups joined by single hyphens, and the name is a number.\n  The entry is in {file}."},
		{"\n  - {name: [a]}", "Set the name of entry 1 of artifact_types to a string.\n  A name is lowercase letters and digits in groups joined by single hyphens, and the name is a list.\n  The entry is in {file}."},
		{"\n  - {name: ~}", "Set the name of entry 1 of artifact_types to a string.\n  A name is lowercase letters and digits in groups joined by single hyphens, and the name is null.\n  The entry is in {file}."},
		{"\n  - {name: 2026-11-02}", "Set the name of entry 1 of artifact_types to a quoted string.\n  YAML reads \"2026-11-02\" as a value that is not a string.\n  The entry is in {file}."},
		{"\n  - {name: !!binary aGk=}", "Set the name of entry 1 of artifact_types to a quoted string.\n  YAML reads \"aGk=\" as a value that is not a string.\n  The entry is in {file}."},
		{"\n  - {name: \"\"}", "Rename entry 1 of artifact_types.\n  A name is lowercase letters and digits in groups joined by single hyphens, at most 64 bytes.\n  The name is " + `""` + ".\n  The entry is in {file}."},
		{"\n  - {name: a--b}", "Rename entry 1 of artifact_types.\n  A name is lowercase letters and digits in groups joined by single hyphens, at most 64 bytes.\n  The name is " + `"a--b"` + ".\n  The entry is in {file}."},
		{"\n  - {name: -a}", "Rename entry 1 of artifact_types.\n  A name is lowercase letters and digits in groups joined by single hyphens, at most 64 bytes.\n  The name is " + `"-a"` + ".\n  The entry is in {file}."},
		{"\n  - {name: \"a\\n\"}", "Rename entry 1 of artifact_types.\n  A name is lowercase letters and digits in groups joined by single hyphens, at most 64 bytes.\n  The name is " + `"a\n"` + ".\n  The entry is in {file}."},
		{"\n  - {name: " + strings.Repeat("a", 65) + "}", "Rename entry 1 of artifact_types.\n  A name is lowercase letters and digits in groups joined by single hyphens, at most 64 bytes.\n  The name is " + `"` + strings.Repeat("a", 65) + `"` + ".\n  The entry is in {file}."},
		{"\n  - {name: mockup, owner: acme}", "Remove the key \"owner\" from entry 1 of artifact_types.\n  An artifact type has only name, base_view, size_limit, max_files, and json_schema.\n  The entry is in {file}."},
		{"\n  - {name: mockup, [a]: b}", "Remove the key a list from entry 1 of artifact_types.\n  An artifact type has only name, base_view, size_limit, max_files, and json_schema.\n  The entry is in {file}."},
		{"\n  - {name: mockup, name: screenshot}", "Remove the second name from entry 1 of artifact_types.\n  The entry has the key twice.\n  The entry is in {file}."},
		{"\n  - {name: a-type, size_limit: 1}", "Add base_view and max_files to " + name + ", or leave only its name.\n  A custom type has base_view, size_limit, and max_files.\n  The type is in {file}."},
		{"\n  - {name: a-type, base_view: file}", "Add size_limit and max_files to " + name + ", or leave only its name.\n  A custom type has base_view, size_limit, and max_files.\n  The type is in {file}."},
		{"\n  - {name: a-type, base_view: file, size_limit: 1}", "Add max_files to " + name + ", or leave only its name.\n  A custom type has base_view, size_limit, and max_files.\n  The type is in {file}."},
		{"\n  - {name: a-type, json_schema: {}}", "Add base_view, size_limit, and max_files to " + name + ", or leave only its name.\n  A custom type has base_view, size_limit, and max_files.\n  The type is in {file}."},
		{custom("text", "size_limit: 1, max_files: 1"), "Set base_view of " + name + " to document, image, video, file, or structured.\n  base_view is \"text\".\n  The type is in {file}."},
		{custom("[a]", "size_limit: 1, max_files: 1"), "Set base_view of " + name + " to document, image, video, file, or structured.\n  base_view is a list.\n  The type is in {file}."},
		{custom("document", "size_limit: 0, max_files: 1"), "Set size_limit of " + name + " to a whole number from 1 to 10485760.\n  10485760 is the most for base_view document, and size_limit is 0.\n  The type is in {file}."},
		{custom("document", "size_limit: 10485761, max_files: 1"), "Set size_limit of " + name + " to a whole number from 1 to 10485760.\n  10485760 is the most for base_view document, and size_limit is 10485761.\n  The type is in {file}."},
		{custom("image", "size_limit: 52428801, max_files: 1"), "Set size_limit of " + name + " to a whole number from 1 to 52428800.\n  52428800 is the most for base_view image, and size_limit is 52428801.\n  The type is in {file}."},
		{custom("video", "size_limit: 2147483649, max_files: 1"), "Set size_limit of " + name + " to a whole number from 1 to 2147483648.\n  2147483648 is the most for base_view video, and size_limit is 2147483649.\n  The type is in {file}."},
		{custom("file", "size_limit: 1073741825, max_files: 1"), "Set size_limit of " + name + " to a whole number from 1 to 1073741824.\n  1073741824 is the most for base_view file, and size_limit is 1073741825.\n  The type is in {file}."},
		{custom("structured", "size_limit: 1048577, max_files: 1, json_schema: {}"), "Set size_limit of " + name + " to a whole number from 1 to 1048576.\n  1048576 is the most for base_view structured, and size_limit is 1048577.\n  The type is in {file}."},
		{custom("file", "size_limit: 1.5, max_files: 1"), "Set size_limit of " + name + " to a whole number from 1 to 1073741824.\n  1073741824 is the most for base_view file, and size_limit is 1.5.\n  The type is in {file}."},
		{custom("file", "size_limit: 1.0, max_files: 1"), "Set size_limit of " + name + " to a whole number from 1 to 1073741824.\n  1073741824 is the most for base_view file, and size_limit is 1.0.\n  The type is in {file}."},
		{custom("file", `size_limit: "1", max_files: 1`), "Set size_limit of " + name + " to a whole number from 1 to 1073741824.\n  1073741824 is the most for base_view file, and size_limit is a string.\n  The type is in {file}."},
		{custom("file", "size_limit: ~, max_files: 1"), "Set size_limit of " + name + " to a whole number from 1 to 1073741824.\n  1073741824 is the most for base_view file, and size_limit is null.\n  The type is in {file}."},
		{custom("file", "size_limit: 99999999999999999999999, max_files: 1"), "Set size_limit of " + name + " to a whole number from 1 to 1073741824.\n  1073741824 is the most for base_view file, and size_limit is 99999999999999999999999.\n  The type is in {file}."},
		{custom("file", `size_limit: !!int "1\n/src/process.yaml: all checks passed", max_files: 1`), "Set size_limit of " + name + " to a whole number from 1 to 1073741824.\n  1073741824 is the most for base_view file, and size_limit is " + `"1\n/src/process.yaml: all checks passed"` + ".\n  The type is in {file}."},
		{custom("file", `size_limit: 1, max_files: !!float "1\n/src/process.yaml: all checks passed"`), "Set max_files of " + name + " to a whole number from 1 to 20.\n  max_files is " + `"1\n/src/process.yaml: all checks passed"` + ".\n  The type is in {file}."},
		{custom("file", "size_limit: 1, max_files: 0"), "Set max_files of " + name + " to a whole number from 1 to 20.\n  max_files is 0.\n  The type is in {file}."},
		{custom("file", "size_limit: 1, max_files: 21"), "Set max_files of " + name + " to a whole number from 1 to 20.\n  max_files is 21.\n  The type is in {file}."},
		{custom("structured", "size_limit: 1, max_files: 2, json_schema: {}"), "Set max_files of " + name + " to 1.\n  A structured type has one file, and max_files is 2.\n  The type is in {file}."},
		{custom("structured", "size_limit: 1, max_files: 1"), "Add json_schema to " + name + ".\n  A structured type has a JSON Schema.\n  The type is in {file}."},
		{custom("document", "size_limit: 1, max_files: 1, json_schema: {}"), "Remove json_schema from " + name + ", or set its base_view to structured.\n  Only a structured type has a JSON Schema, and base_view is document.\n  The type is in {file}."},
		{custom("image", "size_limit: 1, max_files: 1, json_schema: ~"), "Remove json_schema from " + name + ", or set its base_view to structured.\n  Only a structured type has a JSON Schema, and base_view is image.\n  The type is in {file}."},
		{custom("structured", structured+"[a]"), "Set json_schema of " + name + " to an object, a JSON Schema.\n  json_schema is a list.\n  The type is in {file}."},
		{custom("structured", structured+"true"), "Set json_schema of " + name + " to an object, a JSON Schema.\n  json_schema is a boolean.\n  The type is in {file}."},
		{custom("structured", structured+"~"), "Set json_schema of " + name + " to an object, a JSON Schema.\n  json_schema is null.\n  The type is in {file}."},
		{custom("structured", structured+"{1: a}"), `Quote the key "1" in json_schema of ` + name + ".\n  A JSON key is a string.\n  The type is in {file}."},
		{custom("structured", structured+"{a: 1, a: 2}"), `Remove the second "a" from its object in json_schema of ` + name + ".\n  The object has the key twice.\n  The type is in {file}."},
		{custom("structured", structured+"{const: .inf}"), "Replace .inf in json_schema of " + name + " with a JSON number.\n  JSON has no such number.\n  The type is in {file}."},
		{custom("structured", structured+"{const: !!int a}"), `Replace "a" in json_schema of ` + name + " with a JSON value.\n  The value does not decode as !!int.\n  The type is in {file}."},
		{custom("structured", structured+"{$schema: 'http://json-schema.org/draft-04/schema#'}"), "Set $schema in json_schema of " + name + " to a draft qory reads, or remove it.\n  $schema is \"http://json-schema.org/draft-04/schema#\", and qory reads https://json-schema.org/draft/2020-12/schema and http://json-schema.org/draft-07/schema.\n  The type is in {file}."},
		{custom("structured", structured+"{$schema: 3}"), "Set $schema in json_schema of " + name + " to a draft qory reads, or remove it.\n  $schema is \"3\", and qory reads https://json-schema.org/draft/2020-12/schema and http://json-schema.org/draft-07/schema.\n  The type is in {file}."},
		{custom("structured", structured+"{type: objekt}"), "Fix json_schema of " + name + " so that it compiles as a JSON Schema.\n  The schema does not compile: at '/type': value must be one of 'array', 'boolean', 'integer', 'null', 'number', 'object', 'string'; at '/type': got string, want array\n  The type is in {file}."},
		{custom("structured", structured+"{$ref: '#/$defs/gone'}"), "Fix json_schema of " + name + " so that it compiles as a JSON Schema.\n  The schema does not compile: " + `json-pointer in "#/$defs/gone" not found` + "\n  The type is in {file}."},
	} {
		e := factory(t, withTypes(c.body))
		file := filepath.Join(e.Root, "modules", "desk", exports.ProcessFileName)
		want := refusalIn(file, c.want)
		if err := e.Verify(); err == nil || err.Error() != want {
			t.Errorf("%q: %v\nwant %s", c.body, err, want)
		}
	}
}

// structured is artifact_types with one entry: a custom structured type named name.
func structured(name string) string {
	return "artifact_types:\n  - {name: " + name + ", base_view: structured, size_limit: 65536, max_files: 1, json_schema: {}}\n"
}

// submitTypes is artifact_types with a structured type submission, a document type
// reply-draft and a standard type screenshot.
const submitTypes = `artifact_types:
  - {name: submission, base_view: structured, size_limit: 65536, max_files: 1, json_schema: {}}
  - {name: reply-draft, base_view: document, size_limit: 65536, max_files: 1}
  - name: screenshot
`

// TestVerifyChecksSubmit is each mistake in submit, next to submitTypes. Each refusal
// starts with the fix, in a first line of at most 100 characters, then the cause, then
// the file. Both values of send pass with the structured type; a type that no entry has,
// a custom type of another base view and a standard type fail, as does any type with no
// artifact_types.
func TestVerifyChecksSubmit(t *testing.T) {
	for _, body := range []string{"{type: submission, send: end_of_run}", "{send: after_gate, type: submission}"} {
		if err := factory(t, process+submitTypes+"submit: "+body+"\n").Verify(); err != nil {
			t.Errorf("%q: %v", body, err)
		}
	}
	const sends = "end_of_run or after_gate"
	const fix = "Set submit.type to a structured type of artifact_types.\n  "
	// refusal is the refusal with fix and cause of the submit in file.
	refusal := func(file, fixAndCause string) string { return fixAndCause + "\n  submit is in " + file + "." }
	noTypes := factory(t, process+"submit: {type: submission, send: end_of_run}\n")
	want := refusal(filepath.Join(noTypes.Root, "modules", "desk", exports.ProcessFileName), fix+"No entry of artifact_types is named submission.")
	if err := noTypes.Verify(); err == nil || err.Error() != want {
		t.Errorf("no artifact_types: %v\nwant %s", err, want)
	}
	for _, c := range []struct{ body, want string }{
		{" {type: submision, send: end_of_run}", fix + "No entry of artifact_types is named submision."},
		{" {type: reply-draft, send: end_of_run}", fix + "reply-draft has base_view document."},
		{" {type: screenshot, send: after_gate}", fix + "screenshot is a standard type."},
		{" ~", "Set submit to an object with type and send, or remove it.\n  submit is null."},
		{" submission", "Set submit to an object with type and send, or remove it.\n  submit is a string."},
		{"\n  - {type: submission, send: end_of_run}", "Set submit to an object with type and send, or remove it.\n  submit is a list."},
		{" {type: submission, send: end_of_run, command: open-a-draft}", "Remove the key \"command\" from submit.\n  submit has only type and send."},
		{" {type: submission, send: end_of_run, [a]: b}", "Remove the key a list from submit.\n  submit has only type and send."},
		{" {type: submission, type: reply, send: end_of_run}", "Remove the second type from submit.\n  submit has the key type twice."},
		{" {send: end_of_run}", "Add type to submit, the name of a structured entry of artifact_types.\n  submit has no type."},
		{" {type: 3, send: end_of_run}", "Set submit.type to the name of a structured entry of artifact_types.\n  submit.type is a number."},
		{" {type: ~, send: end_of_run}", "Set submit.type to the name of a structured entry of artifact_types.\n  submit.type is null."},
		{" {type: [a], send: end_of_run}", "Set submit.type to the name of a structured entry of artifact_types.\n  submit.type is a list."},
		{" {type: 2026-11-02, send: end_of_run}", "Set submit.type to a quoted string.\n  YAML reads \"2026-11-02\" as a type that is not a string."},
		{" {type: Submission, send: end_of_run}", "Set submit.type to a name such as reply-draft, at most 64 bytes.\n  submit.type is \"Submission\"."},
		{" {type: \"a\\nb\", send: end_of_run}", "Set submit.type to a name such as reply-draft, at most 64 bytes.\n  submit.type is \"a\\nb\"."},
		{" {type: a--b, send: end_of_run}", "Set submit.type to a name such as reply-draft, at most 64 bytes.\n  submit.type is \"a--b\"."},
		{" {type: submission}", "Add send to submit, " + sends + ".\n  submit has no send."},
		{" {type: submission, send: later}", "Set submit.send to " + sends + ".\n  submit.send is \"later\"."},
		{" {type: submission, send: End_of_run}", "Set submit.send to " + sends + ".\n  submit.send is \"End_of_run\"."},
		{" {type: submission, send: [end_of_run]}", "Set submit.send to " + sends + ".\n  submit.send is a list."},
		{" {type: submission, send: ~}", "Set submit.send to " + sends + ".\n  submit.send is null."},
		{" {type: submission, send: 1}", "Set submit.send to " + sends + ".\n  submit.send is a number."},
		{" {type: submission, send: true}", "Set submit.send to " + sends + ".\n  submit.send is a boolean."},
		{" {type: submission, send: {a: b}}", "Set submit.send to " + sends + ".\n  submit.send is an object."},
		{" {type: submission, send: !foo end_of_run}", "Set submit.send to a quoted string.\n  YAML reads \"end_of_run\" as a type that is not a string."},
		{" {type: submission, send: !!binary ZW5kX29mX3J1bg==}", "Set submit.send to a quoted string.\n  YAML reads \"ZW5kX29mX3J1bg==\" as a type that is not a string."},
		{" {type: submission, send: \"end_of_run\\nok\"}", "Set submit.send to " + sends + ".\n  submit.send is \"end_of_run\\nok\"."},
	} {
		e := factory(t, process+submitTypes+"submit:"+c.body+"\n")
		want := refusal(filepath.Join(e.Root, "modules", "desk", exports.ProcessFileName), c.want)
		err := e.Verify()
		if err == nil || err.Error() != want {
			t.Errorf("%q: %v\nwant %s", c.body, err, want)
			continue
		}
		if first, _, _ := strings.Cut(err.Error(), "\n"); utf8.RuneCountInString(first) > 100 {
			t.Errorf("%q: the first line has %d characters, past 100: %s", c.body, utf8.RuneCountInString(first), first)
		}
	}
}

// TestVerifyPrintsOneRefusalForEachArtifactType is two entries that fail and a name listed
// twice: one refusal for each, and the workflows of the stages first.
func TestVerifyPrintsOneRefusalForEachArtifactType(t *testing.T) {
	e := factory(t, withTypes("\n  - name: mockup\n  - name: Mockup\n  - {name: a-type, size_limit: 1}\n  - name: mockup\n  - name: mockup"))
	file := filepath.Join(e.Root, "modules", "desk", exports.ProcessFileName)
	want := strings.Join([]string{
		"Rename entry 2 of artifact_types.",
		"  A name is lowercase letters and digits in groups joined by single hyphens, at most 64 bytes.",
		`  The name is "Mockup".`,
		"  The entry is in " + file + ".",
		"Add base_view and max_files to artifact type a-type, or leave only its name.",
		"  A custom type has base_view, size_limit, and max_files.",
		"  The type is in " + file + ".",
		"Rename or remove entry 4 of artifact_types.",
		"  Entry 1 has the name mockup too.",
		"  The entry is in " + file + ".",
		"Rename or remove entry 5 of artifact_types.",
		"  Entry 1 has the name mockup too.",
		"  The entry is in " + file + ".",
	}, "\n")
	if err := e.Verify(); err == nil || err.Error() != want {
		t.Errorf("%v\nwant:\n%s", err, want)
	}
	bad := strings.Replace(withTypes("\n  - name: Mockup"), "workflow: draft-an-answer", "workflow: send-a-reply", 1)
	if err := factory(t, bad).Verify(); err == nil || !strings.Contains(err.Error(), "Declare send-a-reply in ") || strings.Contains(err.Error(), "Mockup") {
		t.Errorf("a workflow and a type: %v", err)
	}
}

// TestVerifyBoundsAJSONSchema is a custom type's JSON Schema at each limit, which passes,
// and one past it, which fails with the fix first.
func TestVerifyBoundsAJSONSchema(t *testing.T) {
	entry := func(schema string) string {
		return withTypes("\n  - {name: a-type, base_view: structured, size_limit: 65536, max_files: 1, json_schema: " + schema + "}")
	}
	items := func(n int, item string) string {
		return strings.TrimSuffix(strings.Repeat(item+", ", n), ", ")
	}
	list := func(n int) string { return "{x-a: [" + items(n, "1") + "]}" }
	members := func(n int) string {
		var b strings.Builder
		for i := range n {
			fmt.Fprintf(&b, "a%d: 1, ", i)
		}
		return "{x-a: {" + b.String() + "}}"
	}
	nested := func(n int) string { return strings.Repeat("{x-a: ", n-1) + "{}" + strings.Repeat("}", n-1) }
	// values is a schema of n values: itself, the list, 4 lists in it and the numbers.
	values := func(n int) string {
		full := "[" + items(1024, "1") + "]"
		return "{x-a: [" + items(3, full) + ", [" + items(n-6-3*1024, "1") + "]]}"
	}
	// sized is a schema of n bytes of compact JSON: 4 long strings and a shorter one.
	sized := func(n int) string {
		long := `"` + strings.Repeat("s", 16000) + `"`
		return `{x-a: [` + items(4, long) + `, "` + strings.Repeat("s", n-64024) + `"]}`
	}
	const where = "Shorten json_schema of artifact type a-type.\n  "
	for _, c := range []struct{ schema, want string }{
		{list(1024), ""},
		{list(1025), where + "It has a list of more than 1024 entries.\n  The type is in {file}."},
		{members(1024), ""},
		{members(1025), where + "It has an object of more than 1024 members.\n  The type is in {file}."},
		{`{title: "` + strings.Repeat("s", 16384) + `"}`, ""},
		{`{title: "` + strings.Repeat("s", 16385) + `"}`, where + "It has a string of more than 16384 bytes.\n  The type is in {file}."},
		{`{? "` + strings.Repeat("s", 16384) + `" : 1}`, ""},
		{`{? "` + strings.Repeat("s", 16385) + `" : 1}`, where + "It has a member name of more than 16384 bytes.\n  The type is in {file}."},
		{nested(64), ""},
		{nested(65), where + "It nests lists and objects more than 64 deep.\n  The type is in {file}."},
		{values(4096), ""},
		{values(4097), where + "It has more than 4096 values.\n  The type is in {file}."},
		{sized(65536), ""},
		{sized(65537), "Shorten json_schema of artifact type a-type to at most 65536 bytes of JSON.\n  It has 65537 bytes.\n  The type is in {file}."},
	} {
		e := factory(t, entry(c.schema))
		err := e.Verify()
		file := filepath.Join(e.Root, "modules", "desk", exports.ProcessFileName)
		if c.want == "" && err != nil {
			t.Errorf("%.60s: %v", c.schema, err)
		}
		if c.want != "" && (err == nil || err.Error() != refusalIn(file, c.want)) {
			t.Errorf("%.60s: %v\nwant %s", c.schema, err, c.want)
		}
	}
}

// TestVerifyReadsNothingForAJSONSchema is a custom type's JSON Schema that refers to a
// file, to a URL and to a schema beside it: each fails, and no file is read.
func TestVerifyReadsNothingForAJSONSchema(t *testing.T) {
	place := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.WriteFile(place, []byte(`{"title": "TARGET-CONTENT"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"file://" + place, "https://schema.example/a.json", "branch.schema.json"} {
		body := withTypes("\n  - {name: a-type, base_view: structured, size_limit: 1, max_files: 1, json_schema: {$ref: '" + ref + "'}}")
		err := factory(t, body).Verify()
		if err == nil || !strings.Contains(err.Error(), "so that it compiles as a JSON Schema.\n  The schema does not compile: ") || strings.Contains(err.Error(), "TARGET-CONTENT") {
			t.Errorf("%s: %v", ref, err)
		}
	}
}

// TestVerifyHidesASecretInArtifactTypes is an access key secret where a name, a key or a
// schema's value belongs: no line prints it.
func TestVerifyHidesASecretInArtifactTypes(t *testing.T) {
	const secret = "qak_" + "0123456789abcdef"
	for _, body := range []string{
		"\n  - name: " + secret,
		"\n  - {name: mockup, " + secret + ": 1}",
		"\n  - {name: a-type, base_view: " + secret + ", size_limit: 1, max_files: 1}",
		"\n  - {name: a-type, base_view: structured, size_limit: 1, max_files: 1, json_schema: {$schema: " + secret + "}}",
		"\n  - {name: a-type, base_view: structured, size_limit: 1, max_files: 1, json_schema: {$ref: '" + secret + "'}}",
		"\n  - {name: a-type, base_view: structured, size_limit: 1, max_files: 1, json_schema: {type: " + secret + "}}",
		"\n  - {name: a-type, base_view: structured, size_limit: 1, max_files: 1, json_schema: {pattern: '(" + secret + "'}}",
		"\n  - {name: !!binary " + secret + "}",
		"\n  - {name: a-type, base_view: file, size_limit: !!int " + secret + ", max_files: 1}",
		"\n  - {name: a-type, base_view: file, size_limit: !!float " + secret + ", max_files: 1}",
		"\n  - {name: a-type, base_view: file, size_limit: 1, max_files: !!int " + secret + "}",
		"\n  - {name: a-type, base_view: structured, size_limit: 1, max_files: !!int " + secret + ", json_schema: {}}",
		"\n  - {name: a-type, base_view: structured, size_limit: 1, max_files: 1, json_schema: {const: !!int " + secret + "}}",
		"\n  - {name: a-type, base_view: structured, size_limit: 1, max_files: 1, json_schema: {const: !!float " + secret + "}}",
		"\n  - {name: a-type, base_view: structured, size_limit: 1, max_files: 1, json_schema: {const: !!bool " + secret + "}}",
		"\n  - {name: a-type, base_view: structured, size_limit: 1, max_files: 1, json_schema: {const: !!null " + secret + "}}",
	} {
		err := factory(t, withTypes(body)).Verify()
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Errorf("%q: %v", body, err)
		}
	}
}

// structuredWith is an entry of artifact_types, a structured type with the name and the
// JSON Schema in flow style.
func structuredWith(name, schema string) string {
	return "\n  - {name: " + name + ", base_view: structured, size_limit: 65536, max_files: 1, json_schema: " + schema + "}"
}

// repeated is a pattern of n copies of text, in a group repeated 1000 times.
func repeated(n int, text string) string {
	return "(?:" + strings.Repeat(text, n) + "){1000}"
}

// TestVerifyBoundsThePatterns is the limit on compiled patterns: the most that passes,
// one character more, and the limit shared by the entries of a file, in each place a
// pattern can be. The message starts with the fix.
func TestVerifyBoundsThePatterns(t *testing.T) {
	const fix = "Shorten the patterns in json_schema of artifact type "
	const limit = ".\n  The estimate for the patterns of all artifact types in the file is at most 16777216 bytes, and this one brings it to "
	half := repeated(207, "a")
	for _, c := range []struct{ body, want string }{
		{structuredWith("a-type", "{pattern: '"+repeated(417, "a")+"'}"), ""},
		{structuredWith("a-type", "{pattern: '"+repeated(418, "a")+"'}"), fix + "a-type" + limit + "16801104" + "." + "\n  The type is in {file}."},
		{structuredWith("a-type", "{patternProperties: {'"+repeated(418, "a")+"': {}}}"), fix + "a-type" + limit + "16801104" + "." + "\n  The type is in {file}."},
		{structuredWith("a-type", "{$defs: {x: {pattern: '"+repeated(418, "a")+"'}}}"), fix + "a-type" + limit + "16801104" + "." + "\n  The type is in {file}."},
		{structuredWith("a-type", "{$schema: 'http://json-schema.org/draft-07/schema#', pattern: '"+repeated(418, "a")+"'}"), fix + "a-type" + limit + "16801104" + "." + "\n  The type is in {file}."},
		// The same source twice counts once, and two sources count twice.
		{structuredWith("a-type", "{pattern: '"+repeated(417, "a")+"'}") + structuredWith("b-type", "{pattern: '"+repeated(417, "a")+"'}"), ""},
		{structuredWith("a-type", "{pattern: '"+half+"'}") + structuredWith("b-type", "{pattern: '"+repeated(208, "b")+"'}"), ""},
		{structuredWith("a-type", "{pattern: '"+half+"'}") + structuredWith("b-type", "{pattern: '"+repeated(209, "b")+"'}"), fix + "b-type" + limit + "16802208" + "." + "\n  The type is in {file}."},
	} {
		e := factory(t, withTypes(c.body))
		err := e.Verify()
		file := filepath.Join(e.Root, "modules", "desk", exports.ProcessFileName)
		if c.want == "" && err != nil {
			t.Errorf("%.80s: %v", c.body, err)
		}
		if c.want != "" && (err == nil || err.Error() != refusalIn(file, c.want)) {
			t.Errorf("%.80s: %v\nwant %s", c.body, err, c.want)
		}
	}
}

// onePass is a pattern of 300 alternatives of a class and a character, anchored at both
// ends. The character is different for each i, so that each source counts. Go's compiler
// builds a one-pass program for such a pattern, and its memory grows with the square of
// the alternatives.
func onePass(i int) string {
	alternatives := make([]string, 300)
	for a := range alternatives {
		var class strings.Builder
		for c := range 16 {
			class.WriteRune(rune(0x4e00 + 2*(16*a+c)))
		}
		alternatives[a] = "[" + class.String() + "]" + string(rune(0x3400+i))
	}
	return "^(?:" + strings.Join(alternatives, "|") + ")$"
}

// TestVerifyCompilesNoPatternPastTheLimit is two process.yaml files whose patterns a
// compiler would write out: 18 patterns of about 3300 characters, each in a group
// repeated 1000 times, past the limit, and 104 anchored alternations within it. The
// bytes the check allocates stay far under the gigabytes that compiling them takes.
func TestVerifyCompilesNoPatternPastTheLimit(t *testing.T) {
	allocated := func(e *exports.Exports) (uint64, error) {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		err := e.Verify()
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc, err
	}
	var body strings.Builder
	for i := range 18 {
		name := fmt.Sprintf("a-type-%d", i)
		body.WriteString(structuredWith(name, "{pattern: '"+repeated(3300, string(rune('a'+i)))+"'}"))
	}
	used, err := allocated(factory(t, withTypes(body.String())))
	if err == nil || strings.Count(err.Error(), "\n") != 53 || !strings.Contains(err.Error(), "Shorten the patterns in json_schema of artifact type a-type-17.\n") {
		t.Errorf("%.300v", err)
	}
	if used > 64<<20 {
		t.Errorf("the repeats: the check allocated %d bytes, and the limit is 64 MiB", used)
	}
	body.Reset()
	for i := range 104 {
		name := fmt.Sprintf("a-type-%d", i)
		body.WriteString(structuredWith(name, "{pattern: '"+onePass(i)+"'}"))
	}
	used, err = allocated(factory(t, withTypes(body.String())))
	if err != nil {
		t.Errorf("%.300v", err)
	}
	if used > 512<<20 {
		t.Errorf("the alternations: the check allocated %d bytes, and the limit is 512 MiB", used)
	}
}

// TestVerifyMatchesNoPattern is each place in a JSON Schema where the compiler reads a
// pattern, and each way to bring a metaschema into a schema. No check matches a pattern:
// [exports] panics if one does, which fails this test.
func TestVerifyMatchesNoPattern(t *testing.T) {
	for _, schema := range []string{
		"{pattern: a, patternProperties: {b: {pattern: c}}, propertyNames: {pattern: d}}",
		"{default: a, examples: [b], const: c, properties: {a: {pattern: '^a$'}}, required: [a]}",
		"{$ref: 'https://json-schema.org/draft/2020-12/schema', pattern: a}",
		"{$schema: 'http://json-schema.org/draft-07/schema#', pattern: a, patternProperties: {b: {}}}",
		"{$id: 'https://a.example/x', $defs: {b: {pattern: a}}, $ref: '#/$defs/b'}",
		"{$dynamicAnchor: m, $defs: {b: {$dynamicRef: '#m', pattern: a}}, $ref: '#/$defs/b'}",
		"{if: {pattern: a}, then: {pattern: b}, else: {pattern: c}, not: {pattern: d}}",
		"{contentMediaType: application/json, contentEncoding: base64, contentSchema: {pattern: a}}",
	} {
		if err := factory(t, withTypes(structuredWith("a-type", schema))).Verify(); err != nil {
			t.Errorf("%s: %v", schema, err)
		}
	}
	// A metaschema inside the schema: the compile fails or passes, and matches nothing.
	for _, schema := range []string{
		"{$defs: {m: {$id: 'https://a.example/m', pattern: a}, n: {$id: 'https://a.example/n', $schema: 'https://a.example/m', pattern: b}}}",
		"{$id: 'https://a.example/m', $vocabulary: {'https://json-schema.org/draft/2020-12/vocab/core': true}, $defs: {n: {$id: 'n', $schema: 'https://a.example/m', pattern: b}}}",
	} {
		_ = factory(t, withTypes(structuredWith("a-type", schema))).Verify()
	}
}

// siblings is n members of a JSON object, each a nested object, as a list of them with a
// comma at the end: each one writes to the slice in which the library keeps a place.
func siblings(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, `"x-%d":{"a":{"b":{"c":1}}},`, i)
	}
	return b.String()
}

// TestVerifyPrintsNoPlaceForAMemberName is a member name that a metaschema does not take,
// among sibling members: the line has the name and no place, and it is the same in each
// run. Go's maps change the order of the members in each compile.
func TestVerifyPrintsNoPlaceForAMemberName(t *testing.T) {
	const fix = "Fix json_schema of artifact type a-type so that it compiles as a JSON Schema.\n  The schema does not compile: "
	const regex = fix + "invalid propertyName '('; '(' is not valid regex: error parsing regexp: missing closing ): `(`\n  The type is in {file}."
	const uri = fix + "invalid propertyName 'not a uri'; 'not a uri' is not valid uri: relative url\n  The type is in {file}."
	many := siblings(30)
	for _, c := range []struct{ schema, want string }{
		{`{"title":"t","properties":{"u":{"title":"v","patternProperties":{"(":{}}}}}`, regex},
		{`{"title":"t",` + many + `"properties":{"u":{"title":"v",` + many + `"patternProperties":{"(":{}}},"w":{` + many + `"title":"z"}}}`, regex},
		{`{"contentMediaType":"application/json",` + many + `"contentSchema":{` + many + `"patternProperties":{"(":{}}}}`, regex},
		{`{` + many + `"$vocabulary":{"not a uri":true}}`, uri},
	} {
		e := factory(t, withTypes(structuredWith("a-type", c.schema)))
		file := filepath.Join(e.Root, "modules", "desk", exports.ProcessFileName)
		for range 30 {
			if err := e.Verify(); err == nil || err.Error() != refusalIn(file, c.want) {
				t.Fatalf("%.80s: %v\nwant %s", c.schema, err, c.want)
			}
		}
	}
}

// TestSchemaRulesPrintsNoPlaceForContent is a string whose content its schema does not
// take, among sibling members, with the library's content assertion on: the line has no
// place, and it is the same in each run. The process.yaml check has that assertion off.
func TestSchemaRulesPrintsNoPlaceForContent(t *testing.T) {
	c := jsonschema.NewCompiler()
	c.AssertContent()
	const url = "https://schema.example/content"
	if err := c.AddResource(url, map[string]any{"properties": map[string]any{
		"a": map[string]any{"contentMediaType": "application/json", "contentSchema": map[string]any{"required": []any{"b"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	schema, err := c.Compile(url)
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{"a": "{}"}
	for i := range 30 {
		doc[fmt.Sprintf("x-%d", i)] = map[string]any{"b": map[string]any{"c": 1}}
	}
	const want = "missing property 'b'"
	for range 30 {
		if got := exports.SchemaRules(schema.Validate(doc)); got != want {
			t.Fatalf("%s\nwant %s", got, want)
		}
	}
}

// TestSchemaRulesPrintsARuleWithNothingUnderIt is a 'not', an 'enum' and a 'const' whose
// library text ends as the text of an 'allOf' does, with no rule under them: each prints
// as a rule, and the text of an 'allOf' with a rule under it does not print.
func TestSchemaRulesPrintsARuleWithNothingUnderIt(t *testing.T) {
	member := func(rule map[string]any) map[string]any {
		return map[string]any{"properties": map[string]any{"a": rule}}
	}
	object := map[string]any{"b": "c"}
	for _, tc := range []struct {
		name   string
		schema map[string]any
		want   string
	}{
		{"not", member(map[string]any{"not": map[string]any{"type": "string"}}), "at '/a': 'not' failed"},
		{"enum of an object", member(map[string]any{"enum": []any{object, "c"}}), "at '/a': 'enum' failed"},
		{"enum of an array", member(map[string]any{"enum": []any{[]any{"b"}}}), "at '/a': 'enum' failed"},
		{"const of an object", member(map[string]any{"const": object}), "at '/a': 'const' failed"},
		{"const of an array", member(map[string]any{"const": []any{"b"}}), "at '/a': 'const' failed"},
		{"not under a member name", map[string]any{"propertyNames": map[string]any{"not": map[string]any{"const": "a"}}}, "invalid propertyName 'a'; 'not' failed"},
		{"not under allOf", map[string]any{"allOf": []any{member(map[string]any{"not": map[string]any{}})}}, "at '/a': 'not' failed"},
		{"allOf with a rule under it", map[string]any{"allOf": []any{map[string]any{"required": []any{"b"}}}}, "at '': missing property 'b'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := jsonschema.NewCompiler()
			const url = "https://schema.example/rule"
			if err := c.AddResource(url, tc.schema); err != nil {
				t.Fatal(err)
			}
			schema, err := c.Compile(url)
			if err != nil {
				t.Fatal(err)
			}
			if got := exports.SchemaRules(schema.Validate(map[string]any{"a": "x"})); got != tc.want {
				t.Errorf("%s\nwant %s", got, tc.want)
			}
		})
	}
}

// TestArtifactTypesSchemaMatchesTheReader keeps artifact_types in process.schema.json and
// the reader in step: each document is accepted or refused by both.
func TestArtifactTypesSchemaMatchesTheReader(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile("../../contracts/harness/v1/process.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	const limits = "size_limit: 65536, max_files: 1"
	for _, c := range []struct {
		body  string
		valid bool
	}{
		{" []", true},
		{" ~", false},
		{" {}", false},
		{"\n  - mockup", false},
		{"\n  - name: mockup", true},
		{"\n  - {}", false},
		{"\n  - {name: 3}", false},
		{"\n  - {name: Mockup}", false},
		{"\n  - {name: a--b}", false},
		{"\n  - name: " + strings.Repeat("a", 64), true},
		{"\n  - name: " + strings.Repeat("a", 65), false},
		{"\n  - {name: mockup, owner: acme}", false},
		{"\n  - {name: a-type, base_view: file}", false},
		{"\n  - {name: a-type, size_limit: 1}", false},
		{"\n  - {name: a-type, json_schema: {}}", false},
		{custom("text", limits), false},
		{custom("document", limits), true},
		{custom("document", "size_limit: 10485760, max_files: 20"), true},
		{custom("document", "size_limit: 10485761, max_files: 1"), false},
		{custom("image", "size_limit: 52428800, max_files: 1"), true},
		{custom("image", "size_limit: 52428801, max_files: 1"), false},
		{custom("video", "size_limit: 2147483648, max_files: 1"), true},
		{custom("video", "size_limit: 2147483649, max_files: 1"), false},
		{custom("file", "size_limit: 1073741824, max_files: 1"), true},
		{custom("file", "size_limit: 1073741825, max_files: 1"), false},
		{custom("structured", "size_limit: 1048576, max_files: 1, json_schema: {}"), true},
		{custom("structured", "size_limit: 1048577, max_files: 1, json_schema: {}"), false},
		{custom("file", "size_limit: 0, max_files: 1"), false},
		{custom("file", "size_limit: 1.5, max_files: 1"), false},
		{custom("file", `size_limit: "1", max_files: 1`), false},
		{custom("file", "size_limit: 1, max_files: 0"), false},
		{custom("file", "size_limit: 1, max_files: 20"), true},
		{custom("file", "size_limit: 1, max_files: 21"), false},
		{custom("structured", limits), false},
		{custom("structured", "size_limit: 1, max_files: 2, json_schema: {}"), false},
		{custom("structured", limits+", json_schema: [a]"), false},
		{custom("structured", limits+", json_schema: {type: object}"), true},
		{custom("document", limits+", json_schema: {}"), false},
		{custom("image", limits+", json_schema: {}"), false},
		{custom("video", limits+", json_schema: {}"), false},
		{custom("file", limits+", json_schema: {}"), false},
	} {
		body := withTypes(c.body)
		var doc any
		if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
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
		if err := factory(t, body).Verify(); (err == nil) != c.valid {
			t.Errorf("reader: %q valid=%v err=%v", c.body, c.valid, err)
		}
	}
}

// TestContractSchemasPassAsCustomTypes is each structured format of the contract,
// branch.schema.json, questions.schema.json, review-comments.schema.json,
// proposal-sources.schema.json and verdict.schema.json, as the json_schema of a custom
// type: it passes the check and process.schema.json, with a size limit that a structured
// type can have. The same type with a schema that does not compile fails, so the check
// reads the entry.
func TestContractSchemasPassAsCustomTypes(t *testing.T) {
	processSchema, err := jsonschema.NewCompiler().Compile("../../contracts/harness/v1/process.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, sizeLimit string }{
		{"branch", "65536"},
		{"questions", "1048576"},
		{"review-comments", "1048576"},
		{"proposal-sources", "1048576"},
		{"verdict", "65536"},
	} {
		file := c.name + ".schema.json"
		raw, err := os.ReadFile("../../contracts/harness/v1/" + file)
		if err != nil {
			t.Fatal(err)
		}
		var one bytes.Buffer
		if err := json.Compact(&one, raw); err != nil {
			t.Fatal(err)
		}
		entry := func(schema string) string {
			return withTypes("\n  - name: " + c.name + "\n    base_view: structured\n    size_limit: " + c.sizeLimit + "\n    max_files: 1\n    json_schema: " + schema)
		}
		body := entry(one.String())
		if err := factory(t, body).Verify(); err != nil {
			t.Errorf("%s: %v", file, err)
		}
		var doc any
		if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
			t.Fatal(err)
		}
		j, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(j, &doc); err != nil {
			t.Fatal(err)
		}
		if err := processSchema.Validate(doc); err != nil {
			t.Errorf("%s in process.schema.json: %v", file, err)
		}
		broken := entry(strings.Replace(one.String(), `"type":"object"`, `"type":"objekt"`, 1))
		if err := factory(t, broken).Verify(); err == nil || !strings.Contains(err.Error(), "so that it compiles as a JSON Schema.\n  The schema does not compile: ") {
			t.Errorf("%s, a schema that does not compile: %v", file, err)
		}
	}
}
