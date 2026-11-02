package exports_test

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// TestVerifyRefusesAWorkflowThatIsNoString is a workflow written as a number, a boolean
// or a date, which YAML reads as no string: each is refused with the entry's place, and
// the same text quoted is a name the stack must declare.
func TestVerifyRefusesAWorkflowThatIsNoString(t *testing.T) {
	for _, c := range []struct{ value, why string }{
		{"123", "it is a number"},
		{"1.5", "it is a number"},
		{"true", "it is a boolean"},
		{"2026-11-02", "it is not a string"},
	} {
		e := factory(t, strings.Replace(process, "workflow: draft-an-answer", "workflow: "+c.value, 1))
		file := filepath.Join(e.Root, "modules", "desk", exports.ProcessFileName)
		want := file + ": set the workflow of entry 1 in stage Answer to a workflow name: " + c.why
		if err := e.Verify(); err == nil || err.Error() != want {
			t.Errorf("%s: %v\nwant %s", c.value, err, want)
		}
	}
	e := factory(t, strings.Replace(process, "workflow: draft-an-answer", `workflow: "123"`, 1))
	if err := e.Verify(); err == nil || !strings.Contains(err.Error(), ": declare 123 under workflows in ") {
		t.Errorf("a quoted number: %v", err)
	}
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
	} {
		if err := factory(t, withTypes(body)).Verify(); err != nil {
			t.Errorf("%q: %v", body, err)
		}
	}
}

// TestVerifyChecksArtifactTypes is each mistake in artifact_types. Each refusal starts
// with the file and then the fix.
func TestVerifyChecksArtifactTypes(t *testing.T) {
	const structured = "size_limit: 65536, max_files: 1, json_schema: "
	const name = "artifact type a-type"
	for _, c := range []struct{ body, want string }{
		{" ~", "set artifact_types to a list of artifact types, or remove it: artifact_types is null"},
		{" {name: mockup}", "set artifact_types to a list of artifact types, or remove it: artifact_types is an object"},
		{" mockup", "set artifact_types to a list of artifact types, or remove it: artifact_types is a string"},
		{"\n  - mockup", "set entry 1 of artifact_types to an object with a name: the entry is a string"},
		{"\n  - ~", "set entry 1 of artifact_types to an object with a name: the entry is null"},
		{"\n  - {base_view: file}", "add name to entry 1 of artifact_types: the entry has no name"},
		{"\n  - {name: 3}", "set the name of entry 1 of artifact_types to a string of lowercase letters and digits in groups joined by single hyphens: the name is a number"},
		{"\n  - {name: [a]}", "set the name of entry 1 of artifact_types to a string of lowercase letters and digits in groups joined by single hyphens: the name is a list"},
		{"\n  - {name: ~}", "set the name of entry 1 of artifact_types to a string of lowercase letters and digits in groups joined by single hyphens: the name is null"},
		{"\n  - {name: \"\"}", `set the name of entry 1 of artifact_types to lowercase letters and digits in groups joined by single hyphens, at most 64 bytes: the name is ""`},
		{"\n  - {name: a--b}", `set the name of entry 1 of artifact_types to lowercase letters and digits in groups joined by single hyphens, at most 64 bytes: the name is "a--b"`},
		{"\n  - {name: -a}", `set the name of entry 1 of artifact_types to lowercase letters and digits in groups joined by single hyphens, at most 64 bytes: the name is "-a"`},
		{"\n  - {name: \"a\\n\"}", `set the name of entry 1 of artifact_types to lowercase letters and digits in groups joined by single hyphens, at most 64 bytes: the name is "a\n"`},
		{"\n  - {name: " + strings.Repeat("a", 65) + "}", `set the name of entry 1 of artifact_types to lowercase letters and digits in groups joined by single hyphens, at most 64 bytes: the name is "` + strings.Repeat("a", 65) + `"`},
		{"\n  - {name: mockup, owner: acme}", `remove the key "owner" from entry 1 of artifact_types: an artifact type has only name, base_view, size_limit, max_files and json_schema`},
		{"\n  - {name: mockup, [a]: b}", `remove the key a list from entry 1 of artifact_types: an artifact type has only name, base_view, size_limit, max_files and json_schema`},
		{"\n  - {name: mockup, name: screenshot}", "remove the second name from entry 1 of artifact_types: the entry has the key twice"},
		{"\n  - {name: a-type, size_limit: 1}", "add base_view and max_files to " + name + ", or leave only its name: a custom type has base_view, size_limit and max_files"},
		{"\n  - {name: a-type, base_view: file}", "add size_limit and max_files to " + name + ", or leave only its name: a custom type has base_view, size_limit and max_files"},
		{"\n  - {name: a-type, base_view: file, size_limit: 1}", "add max_files to " + name + ", or leave only its name: a custom type has base_view, size_limit and max_files"},
		{"\n  - {name: a-type, json_schema: {}}", "add base_view, size_limit and max_files to " + name + ", or leave only its name: a custom type has base_view, size_limit and max_files"},
		{custom("text", "size_limit: 1, max_files: 1"), `set base_view of ` + name + ` to document, image, video, file or structured: base_view is "text"`},
		{custom("[a]", "size_limit: 1, max_files: 1"), "set base_view of " + name + " to document, image, video, file or structured: base_view is a list"},
		{custom("document", "size_limit: 0, max_files: 1"), "set size_limit of " + name + " to a whole number from 1 to 10485760, the most for base_view document: size_limit is 0"},
		{custom("document", "size_limit: 10485761, max_files: 1"), "set size_limit of " + name + " to a whole number from 1 to 10485760, the most for base_view document: size_limit is 10485761"},
		{custom("image", "size_limit: 52428801, max_files: 1"), "set size_limit of " + name + " to a whole number from 1 to 52428800, the most for base_view image: size_limit is 52428801"},
		{custom("video", "size_limit: 2147483649, max_files: 1"), "set size_limit of " + name + " to a whole number from 1 to 2147483648, the most for base_view video: size_limit is 2147483649"},
		{custom("file", "size_limit: 1073741825, max_files: 1"), "set size_limit of " + name + " to a whole number from 1 to 1073741824, the most for base_view file: size_limit is 1073741825"},
		{custom("structured", "size_limit: 1048577, max_files: 1, json_schema: {}"), "set size_limit of " + name + " to a whole number from 1 to 1048576, the most for base_view structured: size_limit is 1048577"},
		{custom("file", "size_limit: 1.5, max_files: 1"), "set size_limit of " + name + " to a whole number from 1 to 1073741824, the most for base_view file: size_limit is 1.5"},
		{custom("file", "size_limit: 1.0, max_files: 1"), "set size_limit of " + name + " to a whole number from 1 to 1073741824, the most for base_view file: size_limit is 1.0"},
		{custom("file", `size_limit: "1", max_files: 1`), "set size_limit of " + name + " to a whole number from 1 to 1073741824, the most for base_view file: size_limit is a string"},
		{custom("file", "size_limit: ~, max_files: 1"), "set size_limit of " + name + " to a whole number from 1 to 1073741824, the most for base_view file: size_limit is null"},
		{custom("file", "size_limit: 99999999999999999999999, max_files: 1"), "set size_limit of " + name + " to a whole number from 1 to 1073741824, the most for base_view file: size_limit is 99999999999999999999999"},
		{custom("file", "size_limit: 1, max_files: 0"), "set max_files of " + name + " to a whole number from 1 to 20: max_files is 0"},
		{custom("file", "size_limit: 1, max_files: 21"), "set max_files of " + name + " to a whole number from 1 to 20: max_files is 21"},
		{custom("structured", "size_limit: 1, max_files: 2, json_schema: {}"), "set max_files of " + name + " to 1: a structured type has one file, and max_files is 2"},
		{custom("structured", "size_limit: 1, max_files: 1"), "add json_schema to " + name + ": a structured type has a JSON Schema"},
		{custom("document", "size_limit: 1, max_files: 1, json_schema: {}"), "remove json_schema from " + name + ", or set base_view to structured: only a structured type has a JSON Schema, and base_view is document"},
		{custom("image", "size_limit: 1, max_files: 1, json_schema: ~"), "remove json_schema from " + name + ", or set base_view to structured: only a structured type has a JSON Schema, and base_view is image"},
		{custom("structured", structured+"[a]"), "set json_schema of " + name + " to an object, a JSON Schema: json_schema is a list"},
		{custom("structured", structured+"true"), "set json_schema of " + name + " to an object, a JSON Schema: json_schema is a boolean"},
		{custom("structured", structured+"~"), "set json_schema of " + name + " to an object, a JSON Schema: json_schema is null"},
		{custom("structured", structured+"{1: a}"), `quote the key "1" in json_schema of ` + name + ": a JSON key is a string"},
		{custom("structured", structured+"{a: 1, a: 2}"), `remove the second "a" from its object in json_schema of ` + name + ": the object has the key twice"},
		{custom("structured", structured+"{const: .inf}"), "replace .inf in json_schema of " + name + " with a JSON number: JSON has no such number"},
		{custom("structured", structured+"{$schema: 'http://json-schema.org/draft-04/schema#'}"), `set $schema in json_schema of ` + name + ` to https://json-schema.org/draft/2020-12/schema or http://json-schema.org/draft-07/schema, or remove it: $schema is "http://json-schema.org/draft-04/schema#"`},
		{custom("structured", structured+"{$schema: 3}"), `set $schema in json_schema of ` + name + ` to https://json-schema.org/draft/2020-12/schema or http://json-schema.org/draft-07/schema, or remove it: $schema is "3"`},
		{custom("structured", structured+"{type: objekt}"), "fix json_schema of " + name + " so that it compiles as a JSON Schema: at '/type': value must be one of 'array', 'boolean', 'integer', 'null', 'number', 'object', 'string'; at '/type': got string, want array"},
		{custom("structured", structured+"{$ref: '#/$defs/gone'}"), "fix json_schema of " + name + ` so that it compiles as a JSON Schema: json-pointer in "#/$defs/gone" not found`},
	} {
		e := factory(t, withTypes(c.body))
		file := filepath.Join(e.Root, "modules", "desk", exports.ProcessFileName)
		if err := e.Verify(); err == nil || err.Error() != file+": "+c.want {
			t.Errorf("%q: %v\nwant %s: %s", c.body, err, file, c.want)
		}
	}
}

// TestVerifyPrintsOneLineForEachArtifactType is two entries that fail and a name listed
// twice: one line for each, and the workflows of the stages first.
func TestVerifyPrintsOneLineForEachArtifactType(t *testing.T) {
	e := factory(t, withTypes("\n  - name: mockup\n  - name: Mockup\n  - {name: a-type, size_limit: 1}\n  - name: mockup\n  - name: mockup"))
	file := filepath.Join(e.Root, "modules", "desk", exports.ProcessFileName)
	want := strings.Join([]string{
		file + `: set the name of entry 2 of artifact_types to lowercase letters and digits in groups joined by single hyphens, at most 64 bytes: the name is "Mockup"`,
		file + ": add base_view and max_files to artifact type a-type, or leave only its name: a custom type has base_view, size_limit and max_files",
		file + ": rename or remove entry 4 of artifact_types: entry 1 has the name mockup too",
		file + ": rename or remove entry 5 of artifact_types: entry 1 has the name mockup too",
	}, "\n")
	if err := e.Verify(); err == nil || err.Error() != want {
		t.Errorf("%v\nwant:\n%s", err, want)
	}
	bad := strings.Replace(withTypes("\n  - name: Mockup"), "workflow: draft-an-answer", "workflow: send-a-reply", 1)
	if err := factory(t, bad).Verify(); err == nil || !strings.Contains(err.Error(), "declare send-a-reply under workflows") || strings.Contains(err.Error(), "Mockup") {
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
	const where = "shorten json_schema of artifact type a-type"
	for _, c := range []struct{ schema, want string }{
		{list(1024), ""},
		{list(1025), where + ": it has a list of more than 1024 entries"},
		{members(1024), ""},
		{members(1025), where + ": it has an object of more than 1024 members"},
		{`{title: "` + strings.Repeat("s", 16384) + `"}`, ""},
		{`{title: "` + strings.Repeat("s", 16385) + `"}`, where + ": it has a string of more than 16384 bytes"},
		{`{? "` + strings.Repeat("s", 16384) + `" : 1}`, ""},
		{`{? "` + strings.Repeat("s", 16385) + `" : 1}`, where + ": it has a member name of more than 16384 bytes"},
		{nested(64), ""},
		{nested(65), where + ": it nests lists and objects more than 64 deep"},
		{values(4096), ""},
		{values(4097), where + ": it has more than 4096 values"},
		{sized(65536), ""},
		{sized(65537), where + " to at most 65536 bytes of JSON: it has 65537"},
	} {
		e := factory(t, entry(c.schema))
		err := e.Verify()
		file := filepath.Join(e.Root, "modules", "desk", exports.ProcessFileName)
		if c.want == "" && err != nil {
			t.Errorf("%.60s: %v", c.schema, err)
		}
		if c.want != "" && (err == nil || err.Error() != file+": "+c.want) {
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
		if err == nil || !strings.Contains(err.Error(), "so that it compiles as a JSON Schema: ") || strings.Contains(err.Error(), "TARGET-CONTENT") {
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
	} {
		err := factory(t, withTypes(body)).Verify()
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Errorf("%q: %v", body, err)
		}
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

// TestBranchSchemaPassesAsACustomType is the contract's branch.schema.json as the
// json_schema of a custom type: it passes the check and process.schema.json, with the
// limits that a factory sets for the type. The same type with a schema that does not
// compile fails, so the check reads the entry.
func TestBranchSchemaPassesAsACustomType(t *testing.T) {
	raw, err := os.ReadFile("../../contracts/harness/v1/branch.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var one bytes.Buffer
	if err := json.Compact(&one, raw); err != nil {
		t.Fatal(err)
	}
	entry := func(schema string) string {
		return withTypes("\n  - name: branch\n    base_view: structured\n    size_limit: 65536\n    max_files: 1\n    json_schema: " + schema)
	}
	body := entry(one.String())
	if err := factory(t, body).Verify(); err != nil {
		t.Errorf("branch.schema.json: %v", err)
	}
	schema, err := jsonschema.NewCompiler().Compile("../../contracts/harness/v1/process.schema.json")
	if err != nil {
		t.Fatal(err)
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
	if err := schema.Validate(doc); err != nil {
		t.Errorf("process.schema.json: %v", err)
	}
	broken := entry(strings.Replace(one.String(), `"type":"object"`, `"type":"objekt"`, 1))
	if err := factory(t, broken).Verify(); err == nil || !strings.Contains(err.Error(), "so that it compiles as a JSON Schema: ") {
		t.Errorf("a schema that does not compile: %v", err)
	}
}
