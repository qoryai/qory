package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/report"
	"github.com/qoryai/qory/internal/stack"
)

// workflowForms are the four places a harness may ship the workflow example, as a
// module's files.
var workflowForms = map[string]map[string]string{
	"prefixed skill":   {"skills/workflow-example/SKILL.md": "---\nname: workflow-example\ndescription: d\n---\n"},
	"prefixed command": {"commands/workflow-example.md": "---\ndescription: d\n---\nGo.\n"},
	"plain skill":      {"skills/example/SKILL.md": "---\nname: example\ndescription: d\n---\n"},
	"plain command":    {"commands/example.md": "---\ndescription: d\n---\nGo.\n"},
}

// composedWith composes a stack with one module that has the files of forms, through the
// real compose for the runtime, and returns the report a run reads. The stack declares
// no workflow, so the compose passes with no form too.
func composedWith(t *testing.T, runtime string, forms ...string) report.Report {
	t.Helper()
	return composedWithStack(t, runtime, "", forms...)
}

// composedWithStack is [composedWith] with more of the stack, such as its bind.
func composedWithStack(t *testing.T, runtime, more string, forms ...string) report.Report {
	t.Helper()
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
		t.Setenv(name, t.TempDir())
	}
	dir := t.TempDir()
	files := map[string]string{
		stack.FileName:                   "apiVersion: qory.dev/v1alpha1\nname: core\nmodules:\n  - name: core\n    source: {path: modules/core}\n" + more,
		"modules/core/qory-module.yaml":  "apiVersion: qory.dev/v1alpha1\nname: core\n",
		"modules/core/skills/a/SKILL.md": "---\nname: a\ndescription: d\n---\n",
	}
	for _, form := range forms {
		for path, text := range workflowForms[form] {
			files["modules/core/"+path] = text
		}
	}
	for path, text := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, err := stack.Load(filepath.Join(dir, stack.FileName))
	if err != nil {
		t.Fatal(err)
	}
	p.Target = stack.Target{Runtimes: []string{runtime}}
	res, err := compose.Compose(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(res.Close)
	return report.New(res, "core", dir, filepath.Join(dir, ".qory", "harness"))
}

// headlessStart is the arguments of the runtime's headless start for the entry name, for
// the input folder /in. Claude Code registers an entry of the harness plugin.
func headlessStart(runtime, name string) []string {
	const in, file = "/in", "/in/inputs.json"
	switch runtime {
	case "claude":
		return []string{"--output-format", "stream-json", "--verbose", "--add-dir", in, "-p", "/harness:" + name + " " + file}
	case "codex":
		return []string{"exec", "--json", "--add-dir", in, "$" + name + " " + file}
	case "cursor":
		return []string{"--print", "--output-format", "stream-json", "--add-dir", in, "/" + name + " " + file}
	case "gemini":
		return []string{"--output-format", "stream-json", "--include-directories", in, "-p", "/" + name + " " + file}
	case "opencode":
		return []string{"run", "--format", "json", "--command", name, file}
	}
	return nil
}

// TestAWorkflowStartsItsPrefixedEntryFirst is workflowArgs on a harness the real compose
// made, on each runtime with a headless start. For each set of forms, the runtime starts
// the first of skill workflow-<name>, command workflow-<name>, skill <name> and command
// <name> that it can start, with no line and no stop when a harness ships both names. A
// form the runtime cannot start stays a candidate, so a later form runs.
func TestAWorkflowStartsItsPrefixedEntryFirst(t *testing.T) {
	const prefixed, plain = "workflow-example", "example"
	skillsOnly := map[string]bool{"codex": true, "cursor": true}
	commandsOnly := map[string]bool{"gemini": true, "opencode": true}
	for _, c := range []struct {
		name  string
		forms []string
		want  func(runtime string) string
	}{
		{"prefixed skill", []string{"prefixed skill"}, func(rt string) string {
			if commandsOnly[rt] {
				return ""
			}
			return prefixed
		}},
		{"prefixed command", []string{"prefixed command"}, func(rt string) string {
			if skillsOnly[rt] {
				return ""
			}
			return prefixed
		}},
		{"plain skill", []string{"plain skill"}, func(rt string) string {
			if commandsOnly[rt] {
				return ""
			}
			return plain
		}},
		{"plain command", []string{"plain command"}, func(rt string) string {
			if skillsOnly[rt] {
				return ""
			}
			return plain
		}},
		{"prefixed and plain skill", []string{"prefixed skill", "plain skill"}, func(rt string) string {
			if commandsOnly[rt] {
				return ""
			}
			return prefixed
		}},
		{"prefixed and plain command", []string{"prefixed command", "plain command"}, func(rt string) string {
			if skillsOnly[rt] {
				return ""
			}
			return prefixed
		}},
		{"prefixed skill and plain command", []string{"prefixed skill", "plain command"}, func(rt string) string {
			if commandsOnly[rt] {
				return plain
			}
			return prefixed
		}},
		{"prefixed command and plain skill", []string{"prefixed command", "plain skill"}, func(rt string) string {
			if skillsOnly[rt] {
				return plain
			}
			return prefixed
		}},
		{"every form", []string{"prefixed skill", "prefixed command", "plain skill", "plain command"}, func(string) string { return prefixed }},
	} {
		for _, rt := range []string{"claude", "codex", "cursor", "gemini", "opencode"} {
			t.Run(c.name+"/"+rt, func(t *testing.T) {
				r, err := codeRunOf(contracts.Documents{Workflow: "example", RunSettings: &contracts.RunSettings{Runtime: rt}})
				if err != nil {
					t.Fatal(err)
				}
				got, err := r.workflowArgs(rt, composedWith(t, rt, c.forms...), "/in", "")
				want := c.want(rt)
				if want == "" {
					add, runsNo := "command", "skill"
					if skillsOnly[rt] {
						add, runsNo = "skill", "command"
					}
					text := "add a " + add + " workflow-example to the checkout's harness, or set another runtime in Qory Apiary: " + rt + " runs no " + runsNo + " by its name from a headless start, and the composed harness has no " + add + " workflow-example or example; the code is used"
					var ended *reasonError
					if err == nil || err.Error() != text || errors.As(err, &ended) {
						t.Errorf("got %q, %v\nwant %s", got, err, text)
					}
					return
				}
				if err != nil || !reflect.DeepEqual(got, headlessStart(rt, want)) {
					t.Errorf("got %q, %v\nwant %q", got, err, headlessStart(rt, want))
				}
			})
		}
	}
}

// TestAWorkflowInNoFormStopsWithItsReason is workflowArgs on each runtime with a harness
// the real compose made with no form of the workflow: the stop with the reason, whose
// fix adds the prefixed form that the runtime can start, a skill or a command, and whose
// cause has both names.
func TestAWorkflowInNoFormStopsWithItsReason(t *testing.T) {
	for rt, add := range map[string]string{"claude": "skill", "codex": "skill", "cursor": "skill", "gemini": "command", "opencode": "command"} {
		want := "add a " + add + " workflow-example to the checkout's harness, or set another workflow in Qory Apiary: the composed harness has no skill or command workflow-example or example (workflow_not_in_harness); the code is used"
		r, err := codeRunOf(contracts.Documents{Workflow: "example", RunSettings: &contracts.RunSettings{Runtime: rt}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = r.workflowArgs(rt, composedWith(t, rt), "/in", "")
		var ended *reasonError
		if !errors.As(err, &ended) || ended.code != reasonWorkflowNotInHarness || err.Error() != want {
			t.Errorf("%s: %v\nwant %s", rt, err, want)
		}
	}
}

// TestARoleBoundAtThePrefixedNameIsAWorkflowsSkill is a stack that binds the role
// skills/workflow-example to the skill a: claude starts the skill, by its name, and gemini,
// which runs no skill by its name, stops with the add a command line.
func TestARoleBoundAtThePrefixedNameIsAWorkflowsSkill(t *testing.T) {
	const bind = "bind:\n  skills:\n    workflow-example: a\n"
	r, err := codeRunOf(contracts.Documents{Workflow: "example", RunSettings: &contracts.RunSettings{Runtime: "claude"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.workflowArgs("claude", composedWithStack(t, "claude", bind), "/in", "")
	if want := headlessStart("claude", "a"); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("claude: got %q, %v\nwant %q", got, err, want)
	}
	const want = "add a command workflow-example to the checkout's harness, or set another runtime in Qory Apiary: gemini runs no skill by its name from a headless start, and the composed harness has no command workflow-example or example; the code is used"
	if _, err := r.workflowArgs("gemini", composedWithStack(t, "gemini", bind), "/in", ""); err == nil || err.Error() != want {
		t.Errorf("gemini: %v\nwant %s", err, want)
	}
}

// TestAWorkflowOfAPrefixedNameLooksUpTheNameItIsGiven is a workflow whose name already
// starts with the prefix: the plain lookup finds its entry, and nothing strips the
// prefix from a shorter one.
func TestAWorkflowOfAPrefixedNameLooksUpTheNameItIsGiven(t *testing.T) {
	r, err := codeRunOf(contracts.Documents{Workflow: "workflow-example", RunSettings: &contracts.RunSettings{Runtime: "claude"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.workflowArgs("claude", composedWith(t, "claude", "prefixed skill"), "/in", "")
	if err != nil || !strings.Contains(strings.Join(got, " "), "/harness:workflow-example ") {
		t.Errorf("got %q, %v", got, err)
	}
}
