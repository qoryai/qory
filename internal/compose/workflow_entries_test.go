package compose_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/stack"
)

const workflowStack = "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source: {path: modules/core}\nworkflows:\n  example: {}\n"

// TestAWorkflowShipsInAnyOfFourForms is the compose's check of a stack's workflows: each
// of skill workflow-<name>, command workflow-<name>, skill <name> and command <name>
// passes, and a harness with none fails with both names in the cause.
func TestAWorkflowShipsInAnyOfFourForms(t *testing.T) {
	for _, path := range []string{
		"modules/core/skills/workflow-example/SKILL.md",
		"modules/core/commands/workflow-example.md",
		"modules/core/skills/example/SKILL.md",
		"modules/core/commands/example.md",
	} {
		res, err := composeTree(t, map[string]string{stack.FileName: workflowStack, path: "text\n"})
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		res.Close()
	}
	_, err := composeTree(t, map[string]string{stack.FileName: workflowStack, "modules/core/skills/other/SKILL.md": "text\n"})
	const want = "add a skill or command workflow-example to a module of the stack in " + stack.FileName + ", or remove example from its workflows: the stack declares the workflow example, and none of its modules ships a skill or command workflow-example or example"
	if err == nil || err.Error() != want {
		t.Errorf("no form: %v\nwant %s", err, want)
	}
}

// TestAnExcludedPrefixedEntryDoesNotShipTheWorkflow is an exclude that leaves out the
// prefixed skill: the plain skill still ships the workflow, and with no plain skill the
// compose fails, since the check reads what the module composes.
func TestAnExcludedPrefixedEntryDoesNotShipTheWorkflow(t *testing.T) {
	excluded := strings.Replace(workflowStack, "source: {path: modules/core}", "source: {path: modules/core}\n    exclude: {skills: [workflow-example]}", 1)
	files := func(extra ...string) map[string]string {
		m := map[string]string{stack.FileName: excluded, "modules/core/skills/workflow-example/SKILL.md": "text\n"}
		for _, path := range extra {
			m[path] = "text\n"
		}
		return m
	}
	res, err := composeTree(t, files("modules/core/skills/example/SKILL.md"))
	if err != nil {
		t.Fatalf("with a plain skill: %v", err)
	}
	res.Close()
	if _, err := composeTree(t, files()); err == nil || !strings.HasPrefix(err.Error(), "add a skill or command workflow-example to a module of ") {
		t.Errorf("with no plain skill: %v", err)
	}
}

// TestTheSchemasCapAWorkflowNameAt55Characters is stack.schema.json and config.schema.json
// at the limit that the stack check has: a name of 55 characters passes, and one of 56
// does not.
func TestTheSchemasCapAWorkflowNameAt55Characters(t *testing.T) {
	c := jsonschema.NewCompiler()
	for file, wrap := range map[string]string{
		"stack.schema.json":  `{"apiVersion": "qory.dev/v1alpha1", "modules": [{"name": "app"}], "workflows": {"%s": {}}}`,
		"config.schema.json": `{"harness": {"modules": [{"name": "app"}], "workflows": {"%s": {}}}}`,
	} {
		schema, err := c.Compile("../../contracts/harness/v1/" + file)
		if err != nil {
			t.Fatal(err)
		}
		for n, valid := range map[int]bool{55: true, 56: false} {
			var doc any
			if err := json.Unmarshal([]byte(strings.Replace(wrap, "%s", strings.Repeat("a", n), 1)), &doc); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(doc); (err == nil) != valid {
				t.Errorf("%s, %d characters: %v, want valid %v", file, n, err, valid)
			}
		}
	}
}

// TestACheckoutsExcludeOfThePrefixedSkillDoesNotCount is a base with the workflow fix and
// the skill workflow-fix, and a checkout that excludes workflow-fix: the check reads the
// base's modules before that exclude, so the compose passes, with no module of the checkout
// and with one that ships the skill instead.
func TestACheckoutsExcludeOfThePrefixedSkillDoesNotCount(t *testing.T) {
	const checkout = "apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {path: ../base}\n  target:\n    runtime: claude\n  modules:\n    - name: a\n      exclude: {skills: [workflow-fix]}\n"
	for more, want := range map[string]string{
		"": "skills/other a",
		"    - name: app\n      source: {path: harness}\n": "skills/other a skills/workflow-fix app",
	} {
		dir := writeTree(t, map[string]string{
			"base/" + stack.FileName:                      "apiVersion: qory.dev/v1alpha1\nname: core\nextending:\n  kinds: [skills]\nmodules:\n  - name: a\n    source: {path: modules/a}\nworkflows:\n  fix: {}\n",
			"base/modules/a/qory-module.yaml":             "apiVersion: qory.dev/v1alpha1\nname: a\n",
			"base/modules/a/skills/workflow-fix/SKILL.md": "base\n",
			"base/modules/a/skills/other/SKILL.md":        "other\n",
			"app/" + config.FileName:                      checkout + more,
			"app/harness/qory-module.yaml":                "apiVersion: qory.dev/v1alpha1\nname: app\n",
			"app/harness/skills/workflow-fix/SKILL.md":    "app\n",
		})
		p, err := config.Compose(dir + "/app/" + config.FileName)
		if err != nil {
			t.Fatal(err)
		}
		merged, base, err := compose.LoadBase(p, "", compose.Options{})
		if err != nil {
			t.Fatal(err)
		}
		res, err := compose.ComposeWith(merged, compose.Options{Base: base})
		if err != nil {
			t.Fatalf("with %q: %v", more, err)
		}
		var got []string
		for _, e := range res.Entries {
			got = append(got, e.Kind+"/"+e.Name+" "+e.Module)
		}
		res.Close()
		if strings.Join(got, " ") != want {
			t.Errorf("with %q: %v, want %s", more, got, want)
		}
	}
}
