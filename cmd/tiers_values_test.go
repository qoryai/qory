package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/cmd"
)

// agentFront parses the frontmatter of the agent file at path.
func agentFront(t *testing.T, path string) map[string]any {
	t.Helper()
	text := readText(t, path)
	body, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		t.Fatalf("%s has no frontmatter:\n%s", path, text)
	}
	front, _, ok := strings.Cut(body, "\n---\n")
	if !ok {
		t.Fatalf("%s has no closing fence:\n%s", path, text)
	}
	var m map[string]any
	if err := yaml.Unmarshal([]byte(front), &m); err != nil {
		t.Fatalf("%s: %v\n%s", path, err, text)
	}
	return m
}

// TestComposeWritesAModelIdAsTheSameString maps tiers to model ids that change type or
// end the value as plain YAML scalars: "null", "yes", an id with # and an id with ": ",
// "0x10", and an id with ; $ ` " \ { [ and #.
// Each reads back from the written claude and opencode agent files as the same string.
func TestComposeWritesAModelIdAsTheSameString(t *testing.T) {
	root := tierCheckout(t)
	tierModule(t, root, "[claude, opencode]", map[string]string{
		"a": "---\nname: a\ndescription: A.\nmodel: ${qory:light}\n---\nA.\n",
		"b": "---\nname: b\ndescription: B.\nmodel: ${qory:fast}\n---\nB.\n",
		"c": "---\nname: c\ndescription: C.\nmodel: ${qory:thinking}\n---\nC.\n",
	}, `tiers: {light: "null", fast: "yes"}`)
	for _, c := range []struct{ tier, model string }{
		{"thinking", "acme#1"},
		{"thinking", "acme: model"},
		{"thinking", "0x10"},
		{"thinking", "a;$`\"\\{[#b"},
	} {
		out, err := run(t, "harness", "compose", "--tier", c.tier+"="+c.model)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		for name, want := range map[string]string{"a": "null", "b": "yes", "c": c.model} {
			for _, path := range []string{
				filepath.Join(root, ".qory", "harness", "claude", "agents", name+".md"),
				filepath.Join(root, ".qory", "harness", "claude", "plugin", "agents", name+".md"),
				filepath.Join(root, ".qory", "harness", "opencode", "agents", name+".md"),
			} {
				if got, ok := agentFront(t, path)["model"].(string); !ok || got != want {
					t.Errorf("%s: model %#v, want the string %q\n%s", path, agentFront(t, path)["model"], want, readText(t, path))
				}
			}
		}
		_ = os.Remove(filepath.Join(root, ".qory", "harness-report.json"))
	}
}

// TestComposeRefusesAModelIdWithALineEnd refuses a model id or an effort with a line end
// or another control character, from --tier and from harness.tiers, with the value's
// control character escaped.
func TestComposeRefusesAModelIdWithALineEnd(t *testing.T) {
	for _, c := range []struct {
		args    []string
		harness string
		want    string
	}{
		{[]string{"--tier", "light=a\nmodel: b"}, "", `--tier light=a\nmodel: b: write --tier <tier>=<model id>, such as --tier light=claude-haiku-5-5`},
		{[]string{"--tier", "light=a\tb"}, "", `--tier light=a\tb: write --tier <tier>=<model id>, such as --tier light=claude-haiku-5-5`},
		{[]string{"--tier", "light=a\x1b[2Jb"}, "", `--tier light=a\x1b[2Jb: write --tier <tier>=<model id>, such as --tier light=claude-haiku-5-5`},
		{nil, `tiers: {light: "a\nb"}`, "harness.tiers.light: write a model id, or {model: <model id>, effort: <value>}"},
		{nil, `tiers: {light: {model: a, effort: "max\r"}}`, "harness.tiers.light: write a model id, or {model: <model id>, effort: <value>}"},
		{nil, `tiers: {light: "a b"}`, "harness.tiers.light: write a model id, or {model: <model id>, effort: <value>}"},
	} {
		root := tierCheckout(t)
		var harness []string
		if c.harness != "" {
			harness = []string{c.harness}
		}
		tierModule(t, root, "claude", tierAgents, harness...)
		_, err := run(t, append([]string{"harness", "compose"}, c.args...)...)
		if cmd.ExitCode(err) != 2 || err == nil || !strings.HasSuffix(err.Error(), c.want) {
			t.Errorf("%q %s: exit %d, err %v\nwant suffix %s", c.args, c.harness, cmd.ExitCode(err), err, c.want)
		}
	}
}
