package module

import (
	"reflect"
	"strings"
	"testing"
)

// TestTierOfReadsOnlyAWholePlaceholder reads a tier from a value that is the placeholder
// and nothing else, and no tier from a reference, a bare name or a placeholder inside
// other text.
func TestTierOfReadsOnlyAWholePlaceholder(t *testing.T) {
	isolate(t)
	for value, want := range map[string]string{
		"${qory:light}":    "light",
		"${qory:thinking}": "thinking",
		"${qory:heavy}":    "heavy",
	} {
		if got, ok := TierOf(value); !ok || got != want {
			t.Errorf("TierOf(%q) = %q, %v; want %q, true", value, got, ok, want)
		}
	}
	for _, value := range []string{"${qory:agents/coder}", "light", "${qory:}", "x-${qory:light}", "${qory:light} ", "claude-haiku-5-5"} {
		if got, ok := TierOf(value); ok {
			t.Errorf("TierOf(%q) = %q, true; want no tier", value, got)
		}
	}
}

// TestReadSetsTheTierOfAnAgent reads an agent whose model is a tier placeholder, plain and
// quoted: the entry has the tier, and the agent's references are read as before. An agent
// with a model id has no tier.
func TestReadSetsTheTierOfAnAgent(t *testing.T) {
	isolate(t)
	dir := tree(t, map[string]string{
		ManifestName:           "apiVersion: qory.dev/v1alpha1\nname: core\n",
		"agents/explorer.md":   "---\nname: explorer\nmodel: ${qory:light}\n---\nRun ${qory:skills/test}.\n",
		"agents/planner.md":    "---\nname: planner\nmodel: \"${qory:thinking}\"\neffort: high\n---\nPlans.\n",
		"agents/reviewer.md":   "---\nname: reviewer\nmodel: claude-opus-5-5\n---\nReviews.\n",
		"skills/test/SKILL.md": "---\nname: test\n---\nTests.\n",
	})
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	l, err := Read("core", dir, m, "")
	if err != nil {
		t.Fatal(err)
	}
	tiers := map[string]string{}
	refs := map[string][]string{}
	for _, e := range l.Entries {
		tiers[e.Kind+"/"+e.Name] = e.Tier
		refs[e.Kind+"/"+e.Name] = e.References
	}
	want := map[string]string{"agents/explorer": "light", "agents/planner": "thinking", "agents/reviewer": "", "skills/test": ""}
	if !reflect.DeepEqual(tiers, want) {
		t.Errorf("tiers %v, want %v", tiers, want)
	}
	if !reflect.DeepEqual(refs["agents/explorer"], []string{"skills/test"}) {
		t.Errorf("explorer references %v, want [skills/test]", refs["agents/explorer"])
	}
}

// TestReadRefusesAModelThatNamesNoTier refuses an agent whose model is a placeholder with
// a name that is not a tier, and lists the tiers.
func TestReadRefusesAModelThatNamesNoTier(t *testing.T) {
	isolate(t)
	dir := tree(t, map[string]string{
		ManifestName:         "apiVersion: qory.dev/v1alpha1\nname: core\n",
		"agents/explorer.md": "---\nname: explorer\nmodel: ${qory:heavy}\n---\nExplores.\n",
	})
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Read("core", dir, m, "")
	want := "module core: agents/explorer.md: model ${qory:heavy}: use one of the tiers light, fast and thinking; heavy is not a tier"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant %s", err, want)
	}
}

// TestReadRefusesATierOutsideAnAgentsModel refuses a tier placeholder in each place a
// document is read other than an agent's model: an agent's body and another key, a model
// with more text around the placeholder, a skill's and a command's model, and AGENTS.md.
func TestReadRefusesATierOutsideAnAgentsModel(t *testing.T) {
	isolate(t)
	const tail = "${qory:light} is a tier placeholder; a tier goes only in an agent's model, as model: ${qory:light}"
	for _, c := range []struct{ file, body, where string }{
		{"agents/explorer.md", "---\nname: explorer\n---\nUse ${qory:light}.\n", "agents/explorer.md"},
		{"agents/explorer.md", "---\nname: explorer\ndescription: on ${qory:light}\n---\nExplores.\n", "agents/explorer.md"},
		{"agents/explorer.md", "---\nname: explorer\nmodel: x-${qory:light}\n---\nExplores.\n", "agents/explorer.md"},
		{"skills/deploy/SKILL.md", "---\nname: deploy\nmodel: ${qory:light}\n---\nDeploys.\n", "skills/deploy/SKILL.md"},
		{"commands/ship.md", "---\nmodel: ${qory:light}\n---\nShips.\n", "commands/ship.md"},
		{"AGENTS.md", "Agents run on ${qory:light}.\n", "AGENTS.md"},
		{"settings/claude/settings.json", `{"model": "${qory:light}"}`, "settings/claude/settings.json"},
		{"mcp/srv.json", `{"command":"srv","args":["--model","${qory:light}"]}`, "mcp/srv.json"},
		{"files/claude/notes/run.txt", "Run on ${qory:light}.\n", "files/claude/notes/run.txt"},
		{"hooks/check.sh", "#!/bin/sh\necho ${qory:light}\n", "hooks/check.sh"},
	} {
		dir := tree(t, map[string]string{
			ManifestName: "apiVersion: qory.dev/v1alpha1\nname: core\n",
			c.file:       c.body,
		})
		m, err := ReadManifest(dir)
		if err != nil {
			t.Fatal(err)
		}
		_, err = Read("core", dir, m, "")
		want := "module core: " + c.where + ": " + tail
		if err == nil || err.Error() != want {
			t.Errorf("%s:\n%q\nerr = %v\nwant %s", c.file, c.body, err, want)
		}
	}
}

// TestReferencesStillRefuseANameThatIsNoTier keeps the refusal of a placeholder-shaped
// text that is neither a reference nor a tier.
func TestReferencesStillRefuseANameThatIsNoTier(t *testing.T) {
	isolate(t)
	_, err := References("Use ${qory:heavy}.")
	if err == nil || !strings.Contains(err.Error(), "${qory:heavy} is not a reference") {
		t.Fatalf("err = %v", err)
	}
}

// TestSetFrontReplacesAddsAndRemovesAKey sets a top-level frontmatter key: the line and
// its indented continuation replaced, a missing key added before the closing fence, an
// empty value removing the line, a value YAML must quote quoted, and CRLF read as LF. The
// other lines stay as written.
func TestSetFrontReplacesAddsAndRemovesAKey(t *testing.T) {
	isolate(t)
	for _, c := range []struct{ in, key, value, want string }{
		{"---\nname: a\nmodel: ${qory:light}\n---\nBody.\n", "model", "claude-haiku-5-5", "---\nname: a\nmodel: claude-haiku-5-5\n---\nBody.\n"},
		{"---\nname: a\nmodel: ${qory:light}\n---\nBody.\n", "model", "", "---\nname: a\n---\nBody.\n"},
		{"---\nname: a\nmodel: x\n---\nBody.\n", "effort", "max", "---\nname: a\nmodel: x\neffort: max\n---\nBody.\n"},
		{"---\neffort: low\nname: a\n---\nBody.\n", "effort", "max", "---\neffort: max\nname: a\n---\nBody.\n"},
		{"---\ndescription: >\n  two\n  lines\nname: a\n---\n", "description", "one", "---\ndescription: one\nname: a\n---\n"},
		{"---\nmodels: keep\n---\n", "model", "x", "---\nmodels: keep\nmodel: x\n---\n"},
		{"---\nmodel: a\n---\n", "model", "us.vendor:model@1: x", "---\nmodel: 'us.vendor:model@1: x'\n---\n"},
		{"---\r\nmodel: a\r\n---\r\nBody.\r\n", "model", "b", "---\nmodel: b\n---\nBody.\n"},
		{"Body only.\n", "model", "b", "---\nmodel: b\n---\nBody only.\n"},
	} {
		if got := string(SetFront([]byte(c.in), c.key, c.value)); got != c.want {
			t.Errorf("SetFront(%q, %s, %q) =\n%q\nwant\n%q", c.in, c.key, c.value, got, c.want)
		}
	}
}

// TestReadEscapesAControlCharacterInATierName refuses an agent's model that names no tier
// with a control character in the name, and prints the character escaped.
func TestReadEscapesAControlCharacterInATierName(t *testing.T) {
	isolate(t)
	dir := tree(t, map[string]string{
		ManifestName:         "apiVersion: qory.dev/v1alpha1\nname: core\n",
		"agents/explorer.md": "---\nname: explorer\nmodel: \"${qory:he\\eavy}\"\n---\nExplores.\n",
	})
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Read("core", dir, m, "")
	want := `module core: agents/explorer.md: model ${qory:he\x1bavy}: use one of the tiers light, fast and thinking; he\x1bavy is not a tier`
	if err == nil || err.Error() != want {
		t.Errorf("err = %q\nwant %q", err, want)
	}
}
