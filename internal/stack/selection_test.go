package stack

import (
	"strings"
	"testing"
)

// selectionStack wraps a module block into a stack document.
func selectionStack(block string) string {
	return "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    " + strings.ReplaceAll(block, "\n", "\n    ") + "\n"
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

// TestSelectionStopsAtAMistake is the errors a block returns, each with a fix that contains the
// module and the block, so a person finds the line, and the two ways an exclude beside an only
// contradicts it.
func TestSelectionStopsAtAMistake(t *testing.T) {
	cases := map[string]string{
		"exclude:\n  prompts: [greet]":                         "Rename \"prompts\" in the exclude of module core to a kind or a part.\n  Kinds: skills, agents, commands, output-styles, hooks, mcp, files.\n  Parts: instructions, settings, env.\n  The exclude is in {path}.",
		"only:\n  skills: []":                                  "Set skills in the only of module core to a list of entry names.\n  The value is not a list, or the list is empty.\n  The only is in {path}.",
		"only:\n  skills: greet":                               "Set skills in the only of module core to a list of entry names.\n  The value is not a list, or the list is empty.\n  The only is in {path}.",
		"exclude:\n  instructions: false":                      "Set instructions in the exclude of module core to true.\n  instructions stands for the module's AGENTS.md: exclude leaves it out, only keeps it.\n  The exclude is in {path}.",
		"exclude:\n  settings: false":                          "Set settings in the exclude of module core to true or a list of names.\n  The value is false.\n  The exclude is in {path}.",
		"exclude:\n  env: []":                                  "Set env in the exclude of module core to true or a list of names.\n  The value is not true, and not a list with a name in it.\n  The exclude is in {path}.",
		"exclude:\n  env: [\"\"]":                              "Remove the empty name from env in the exclude of module core.\n  A name is not empty.\n  The exclude is in {path}.",
		"exclude: [skills]":                                    "Set the exclude of module core to a mapping of kinds and parts to what they select.\n  The block is not a mapping.\n  The exclude is in {path}.",
		"only:\n  skills: [a]\nexclude:\n  instructions: true": "Remove instructions, settings, and env from the exclude of module core.\n  Only leaves every part it does not select out, so an exclude beside only selects entries that only brings in.\n  The blocks are in {path}.",
		"only:\n  skills: [a]\nexclude:\n  skills: [a]":        "Remove skills/a from the only or the exclude of module core.\n  Both blocks list skills/a.\n  The blocks are in {path}.",
	}
	for block, want := range cases {
		path := write(t, selectionStack(block))
		_, err := Load(path)
		if want := strings.ReplaceAll(want, "{path}", path); err == nil || !strings.HasSuffix(err.Error(), want) {
			t.Errorf("%q: err = %v, want suffix %q", block, err, want)
		}
	}
}
