package cmd_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/report"
)

// tierCheckout is [newCheckout] with git kept from reading any directory above the test's
// temporary directories.
func tierCheckout(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(t.TempDir()))
	return newCheckout(t)
}

// tierModule writes the module core under root/harness/core with the agents given, name
// to file body, and the repository's qory.yaml composing it for the runtimes, with the
// harness lines added to its harness section.
func tierModule(t *testing.T, root, runtimes string, agents map[string]string, harness ...string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "harness", "core", "qory-module.yaml"), "apiVersion: qory.dev/v1alpha1\nname: core\n")
	for name, body := range agents {
		writeFile(t, filepath.Join(root, "harness", "core", "agents", name+".md"), body)
	}
	writeOwnStack(t, root, "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: "+runtimes+"\nmodules:\n  - name: core\n    source: {path: ./harness/core}\n")
	if len(harness) > 0 {
		configure(t, root, harness, nil)
	}
}

// readText returns the file's content.
func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// tierAgents are four agents: three set a tier, explorer with effort: low as well, and
// one sets a model id.
var tierAgents = map[string]string{
	"explorer": "---\nname: explorer\nmodel: ${qory:light}\neffort: low\n---\nExplores.\n",
	"planner":  "---\nname: planner\nmodel: ${qory:thinking}\n---\nPlans.\n",
	"reviewer": "---\nname: reviewer\nmodel: ${qory:balanced}\n---\nReviews.\n",
	"writer":   "---\nname: writer\nmodel: claude-opus-5-5\n---\nWrites.\n",
}

// TestComposeWritesEachAgentsTier composes for claude with a tier from each source: the
// flag, the machine's map over the repository's for one tier, and the repository's alone.
// Each agent with a tier is written with the mapped model, and with the entry's effort in
// place of the agent's when the entry sets one; an agent with a model id stays a link.
// The rows and the report contain each tier, its model and where it came from.
func TestComposeWritesEachAgentsTier(t *testing.T) {
	root := tierCheckout(t)
	machineConfig(t, "harness:\n  tiers:\n    balanced: claude-sonnet-5-5\n")
	tierModule(t, root, "claude", tierAgents,
		"tiers:",
		"  balanced: repo-balanced",
		"  thinking: {model: claude-opus-5-5, effort: max}",
	)
	out, err := run(t, "harness", "compose", "--tier", "light=claude-haiku-5-5")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got, want := fieldRows(out)["tier"], []string{
		"light  claude-haiku-5-5  (agent explorer; from --tier)",
		"balanced  claude-sonnet-5-5  (agent reviewer; from ~/.config/qory/qory.yaml)",
		"thinking  claude-opus-5-5, effort max  (agent planner; from qory.yaml)",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("tier rows %q\nwant %q\n%s", got, want, out)
	}
	agents := filepath.Join(root, ".qory", "harness", "claude", "agents")
	for name, want := range map[string]string{
		"explorer": "---\nname: explorer\nmodel: claude-haiku-5-5\neffort: low\n---\nExplores.\n",
		"planner":  "---\nname: planner\nmodel: claude-opus-5-5\neffort: max\n---\nPlans.\n",
		"reviewer": "---\nname: reviewer\nmodel: claude-sonnet-5-5\n---\nReviews.\n",
	} {
		if got := readText(t, filepath.Join(agents, name+".md")); got != want {
			t.Errorf("agents/%s.md:\n%s\nwant:\n%s", name, got, want)
		}
		plugin := filepath.Join(root, ".qory", "harness", "claude", "plugin", "agents", name+".md")
		if got := readText(t, plugin); got != want {
			t.Errorf("plugin/agents/%s.md:\n%s\nwant:\n%s", name, got, want)
		}
	}
	if fi, err := os.Lstat(filepath.Join(agents, "writer.md")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("agents/writer.md is not a link: %v, %v", fi, err)
	}
	rep, err := report.Read(filepath.Join(root, ".qory", "harness-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	tiers := map[string]string{}
	for _, e := range rep.Entries {
		if e.Kind == "agents" {
			tiers[e.Name] = e.Tier
		}
	}
	if want := map[string]string{"explorer": "light", "planner": "thinking", "reviewer": "balanced", "writer": ""}; !reflect.DeepEqual(tiers, want) {
		t.Errorf("report entry tiers %v, want %v", tiers, want)
	}
	want := map[string]report.Tier{
		"light":    {Model: "claude-haiku-5-5", From: "--tier"},
		"balanced": {Model: "claude-sonnet-5-5", From: filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "qory.yaml")},
		"thinking": {Model: "claude-opus-5-5", Effort: "max", From: filepath.Join(root, "qory.yaml")},
	}
	if !reflect.DeepEqual(rep.Tiers, want) {
		t.Errorf("report tiers %+v\nwant %+v", rep.Tiers, want)
	}

	// Without the flag the light tier has no map: --check finds the agent changed, and a
	// compose writes it without a model, keeping effort: low.
	_, err = run(t, "harness", "compose", "--check")
	if cmd.ExitCode(err) != cmd.ExitStale {
		t.Errorf("--check after the map changed: %v, exit %d", err, cmd.ExitCode(err))
	}
	out, err = run(t, "harness", "compose")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := fieldRows(out)["tier"][0]; got != "light  not mapped  (agent explorer gets no model and runs on the main model; map it with --tier light=<model id> or harness.tiers)" {
		t.Errorf("unmapped row %q\n%s", got, out)
	}
	if got, want := readText(t, filepath.Join(agents, "explorer.md")), "---\nname: explorer\neffort: low\n---\nExplores.\n"; got != want {
		t.Errorf("unmapped agents/explorer.md:\n%s\nwant:\n%s", got, want)
	}
}

// TestComposeWritesATierForEachRuntime composes one map for every runtime that writes
// agents: claude gets the model and the effort, copilot the model and reasoning-effort;
// cursor, opencode and goose get the model and a row for the effort they do not write, so
// the agents run at the session's effort; codex and gemini get a row for the model they
// do not write.
func TestComposeWritesATierForEachRuntime(t *testing.T) {
	root := tierCheckout(t)
	tierModule(t, root, "[claude, copilot, cursor, opencode, goose, gemini, codex]", map[string]string{
		"planner": "---\nname: planner\ndescription: Plans.\nmodel: ${qory:thinking}\n---\nPlans.\n",
		"scout":   "---\nname: scout\ndescription: Scouts.\nmodel: ${qory:thinking}\n---\nScouts.\n",
	}, "tiers: {thinking: {model: claude-opus-5-5, effort: max}}")
	out, err := run(t, "harness", "compose")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantsRow(t, out, "tier", "thinking  claude-opus-5-5, effort max  (agents planner, scout; from qory.yaml)")
	if got, want := fieldRows(out)["skipped"], []string{
		"effort max of tier thinking for cursor  (qory writes no effort into cursor agents; agents planner, scout run at the session's effort)",
		"effort max of tier thinking for opencode  (qory writes no effort into opencode agents; agents planner, scout run at the session's effort)",
		"effort max of tier thinking for goose  (qory writes no effort into goose agents; agents planner, scout run at the session's effort)",
		"tier thinking for gemini  (gemini agents have no model setting; agents planner, scout run on the main model)",
		"tier thinking for codex  (codex agents have no model setting; agents planner, scout run on the main model)",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("skipped rows %q\nwant %q\n%s", got, want, out)
	}
	home := filepath.Join(root, ".qory", "harness")
	for _, path := range []string{"claude/agents/planner.md", "claude/plugin/agents/planner.md"} {
		if got := readText(t, filepath.Join(home, path)); !strings.Contains(got, "model: claude-opus-5-5\neffort: max\n") {
			t.Errorf("%s:\n%s", path, got)
		}
	}
	for _, path := range []string{"copilot/agents/planner.agent.md", "copilot/workspace/.github/agents/planner.agent.md"} {
		if got := readText(t, filepath.Join(home, path)); !strings.Contains(got, "model: claude-opus-5-5\n") || !strings.Contains(got, "reasoning-effort: max\n") || strings.Contains(got, "${qory:") {
			t.Errorf("%s:\n%s", path, got)
		}
	}
	for _, path := range []string{"cursor/agents/planner.md", "opencode/agents/planner.md", "goose/agents/planner.md"} {
		if got := readText(t, filepath.Join(home, path)); !strings.Contains(got, "model: claude-opus-5-5\n") || strings.Contains(got, "effort") || strings.Contains(got, "${qory:") {
			t.Errorf("%s:\n%s", path, got)
		}
	}
	if got := readText(t, filepath.Join(home, "gemini", "agents", "planner.md")); strings.Contains(got, "model") || strings.Contains(got, "effort") || strings.Contains(got, "${qory:") {
		t.Errorf("gemini/agents/planner.md:\n%s", got)
	}
	codex := readText(t, filepath.Join(root, ".qory", "harness", "codex", "agents", "planner.toml"))
	if strings.Contains(codex, "model") || strings.Contains(codex, "${qory:") {
		t.Errorf("codex agents/planner.toml:\n%s", codex)
	}
}

// TestComposeRefusesATierMistake refuses each mistake of the flag and the main model with
// exit status 2, before anything is written.
func TestComposeRefusesATierMistake(t *testing.T) {
	for _, c := range []struct {
		args    []string
		harness []string
		want    string
	}{
		{[]string{"--tier", "light"}, nil, "--tier light: write --tier <tier>=<model id>, such as --tier light=claude-haiku-5-5"},
		{[]string{"--tier", "light="}, nil, "--tier light=: write --tier <tier>=<model id>, such as --tier light=claude-haiku-5-5"},
		{[]string{"--tier", "heavy=claude-haiku-5-5"}, nil, "Use one of the tiers light, balanced, and thinking.\n  heavy is not a tier.\n  The name is in --tier heavy=claude-haiku-5-5."},
		{[]string{"--tier", "fast=claude-haiku-5-5"}, nil, "Use one of the tiers light, balanced, and thinking.\n  fast is not a tier.\n  The name is in --tier fast=claude-haiku-5-5."},
		{[]string{"--tier", "light=a", "--tier", "light=b"}, nil, "--tier light is given twice; give each tier once"},
		{[]string{"--tier", "light=${qory:balanced}"}, nil, "--tier light=${qory:balanced}: write a model id; ${qory:balanced} is a tier placeholder, which goes only in an agent's model"},
		{[]string{"--model", "${qory:light}"}, nil, "--model ${qory:light} is a tier placeholder; set the main model by its id, and a tier only in an agent's model"},
		{nil, []string{"model: ${qory:light}"}, "harness.model ${qory:light} is a tier placeholder; set the main model by its id, and a tier only in an agent's model"},
	} {
		root := tierCheckout(t)
		tierModule(t, root, "claude", tierAgents, c.harness...)
		out, err := run(t, append([]string{"harness", "compose"}, c.args...)...)
		if cmd.ExitCode(err) != 2 || err == nil || !strings.HasSuffix(err.Error(), c.want) {
			t.Errorf("%v %v: exit %d, err %v\nwant suffix %s\n%s", c.args, c.harness, cmd.ExitCode(err), err, c.want, out)
		}
		if _, err := os.Stat(filepath.Join(root, ".qory")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%v: .qory was written", c.args)
		}
	}
	root := tierCheckout(t)
	writeFile(t, filepath.Join(root, "harness", "core", "qory-module.yaml"), "apiVersion: qory.dev/v1alpha1\nname: core\n")
	writeOwnStack(t, root, "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\n  model: ${qory:light}\nmodules:\n  - name: core\n    source: {path: ./harness/core}\n")
	_, err := run(t, "harness", "compose")
	want := filepath.Join(root, "qory.yaml") + ": target.model ${qory:light} is a tier placeholder; set the main model by its id, and a tier only in an agent's model"
	if cmd.ExitCode(err) != 2 || err == nil || err.Error() != want {
		t.Errorf("target.model: exit %d, err %v\nwant %s", cmd.ExitCode(err), err, want)
	}
}

// TestComposeRefusesAnEffortCopilotAgentsDoNotTake refuses a tier's effort that a copilot
// agent does not take, the fix first, in a first line of at most 100 characters.
func TestComposeRefusesAnEffortCopilotAgentsDoNotTake(t *testing.T) {
	root := tierCheckout(t)
	tierModule(t, root, "copilot", tierAgents, "tiers: {thinking: {model: example-model, effort: ultra}}")
	_, err := run(t, "harness", "compose")
	want := "set tier thinking's effort to one of minimal, low, medium, high, xhigh, max for copilot agents\nultra is not one (from qory.yaml)"
	if cmd.ExitCode(err) != 2 || err == nil || err.Error() != want {
		t.Errorf("exit %d, err %v\nwant %s", cmd.ExitCode(err), err, want)
	}
	if first, _, _ := strings.Cut(want, "\n"); len(first) > 100 {
		t.Errorf("first line is %d characters, more than 100", len(first))
	}
}

// TestComposeRefusesAnEffortClaudeAgentsDoNotTake refuses a tier's effort that a claude
// agent does not take, with the values it does and the file the entry came from.
func TestComposeRefusesAnEffortClaudeAgentsDoNotTake(t *testing.T) {
	root := tierCheckout(t)
	tierModule(t, root, "claude", tierAgents, "tiers: {thinking: {model: claude-opus-5-5, effort: ultra}}")
	_, err := run(t, "harness", "compose")
	want := "set tier thinking's effort to one of low, medium, high, xhigh, max for claude agents\nultra is not one (from qory.yaml)"
	if cmd.ExitCode(err) != 2 || err == nil || err.Error() != want {
		t.Errorf("exit %d, err %v\nwant %s", cmd.ExitCode(err), err, want)
	}
}
