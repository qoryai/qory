package stack

import (
	"strings"
	"testing"
)

// selectionStack wraps a module block into a stack document.
func selectionStack(block string) string {
	return "apiVersion: qory.ai/v1alpha1\ntarget: {runtime: claude}\nmodules:\n  - name: core\n    " + strings.ReplaceAll(block, "\n", "\n    ") + "\n"
}

// TestSelectionReadsKindsAndParts is every key a block takes: entries by kind, the
// instruction section, and the two parts that are true or a list.
func TestSelectionReadsKindsAndParts(t *testing.T) {
	p, err := Load(write(t, selectionStack("only:\n  skills: [deploy, test]\n  instructions: true\n  settings: true\n  env: [HARNESS_PROFILE]")))
	if err != nil {
		t.Fatal(err)
	}
	sel := p.Modules[0].Only
	if !p.Modules[0].Exclude.Empty() || sel.Empty() {
		t.Fatalf("exclude %+v, only %+v", p.Modules[0].Exclude, sel)
	}
	if got := sel.Kinds["skills"]; len(got) != 2 || got[0] != "deploy" || got[1] != "test" {
		t.Errorf("skills = %v", got)
	}
	if !sel.Instructions || !sel.Settings.All || sel.Settings.Names != nil || sel.Env.All || len(sel.Env.Names) != 1 || sel.Env.Names[0] != "HARNESS_PROFILE" {
		t.Errorf("parts = %+v", sel)
	}
	if kinds := sel.KindNames(); len(kinds) != 1 || kinds[0] != "skills" {
		t.Errorf("kinds = %v", kinds)
	}
}

// TestSelectionRefusesAMistake is the errors a block gives, each naming the module and
// the block, so a person finds the line.
func TestSelectionRefusesAMistake(t *testing.T) {
	cases := map[string]string{
		"exclude:\n  prompts: [greet]":                         `module core: exclude names kind "prompts"; kinds: skills, agents, commands, output-styles, hooks, mcp, files; parts: instructions, settings, env`,
		"only:\n  skills: []":                                  "module core: only skills is a list of entry names",
		"only:\n  skills: greet":                               "module core: only skills is a list of entry names",
		"exclude:\n  instructions: false":                      "module core: exclude instructions is true, which names the module's AGENTS.md",
		"exclude:\n  settings: false":                          "module core: exclude settings is true or a list of names",
		"exclude:\n  env: []":                                  "module core: exclude env is true or a list of names",
		"exclude:\n  env: [\"\"]":                              "module core: exclude env names an empty name",
		"exclude: [skills]":                                    "module core: exclude is a mapping of kinds and parts to what they name",
		"exclude:\n  skills: [a]\nonly:\n  instructions: true": "module core: names both exclude and only; a module names one of the two",
	}
	for block, want := range cases {
		_, err := Load(write(t, selectionStack(block)))
		if err == nil || !strings.HasSuffix(err.Error(), want) {
			t.Errorf("%q: err = %v, want suffix %q", block, err, want)
		}
	}
}
