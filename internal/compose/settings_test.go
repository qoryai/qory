package compose_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/profile"
)

// writeTree writes a checkout under a fresh temporary directory and returns it. A key ending
// in "/" is an empty directory; every other key is a file holding its value.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if strings.HasSuffix(name, "/") {
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// composeTree loads the profile of a written checkout and composes it. The profile is named
// by its absolute path, so the test needs no working directory of its own.
func composeTree(t *testing.T, files map[string]string) (*compose.Result, error) {
	t.Helper()
	dir := writeTree(t, files)
	p, err := profile.Load(filepath.Join(dir, profile.FileName))
	if err != nil {
		return nil, err
	}
	return compose.Compose(p)
}

// twoLayers is a profile over layers a and b, in that order.
const twoLayers = `apiVersion: qory.ai/v1alpha1
kind: HarnessProfile
target:
  runtime: claude
layers:
  - name: a
    source:
      path: layers/a
  - name: b
    source:
      path: layers/b
`

// jsonValue decodes a JSON document into the types the merge works on.
func jsonValue(t *testing.T, doc string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestSettingsMergeTwoLayersIntoOneTargetFile concatenates permissions without a duplicate,
// concatenates hooks, lets the later layer win an env key, merges a nested map deeply and
// replaces any other list.
func TestSettingsMergeTwoLayersIntoOneTargetFile(t *testing.T) {
	res, err := composeTree(t, map[string]string{
		profile.FileName: twoLayers,
		"layers/a/settings/claude/settings.json": `{
			"permissions": {"allow": ["Bash(git status:*)", "Read"], "deny": ["Bash(rm:*)"]},
			"hooks": {"PreToolUse": [{"matcher": "Bash"}]},
			"env": {"QORY_A": "a", "SHARED": "a"},
			"model": "opus",
			"nested": {"deep": {"one": 1}},
			"other": ["a"]
		}`,
		"layers/b/settings/claude/settings.json": `{
			"permissions": {"allow": ["Read", "Bash(git diff:*)"]},
			"hooks": {"PreToolUse": [{"matcher": "Write"}]},
			"env": {"SHARED": "b"},
			"nested": {"deep": {"two": 2}},
			"other": ["b"]
		}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := jsonValue(t, `{
		"permissions": {"allow": ["Bash(git status:*)", "Read", "Bash(git diff:*)"], "deny": ["Bash(rm:*)"]},
		"hooks": {"PreToolUse": [{"matcher": "Bash"}, {"matcher": "Write"}]},
		"env": {"QORY_A": "a", "SHARED": "b"},
		"model": "opus",
		"nested": {"deep": {"one": 1, "two": 2}},
		"other": ["b"]
	}`)
	got := res.Settings["claude"]["settings.json"]
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("settings:\n%s\nwant:\n%s", indent(t, got), indent(t, want))
	}
}

// TestSettingsFilesAreSorted lists the target files a runtime's layers contributed to, and
// nothing for a runtime no layer wrote for.
func TestSettingsFilesAreSorted(t *testing.T) {
	res, err := composeTree(t, map[string]string{
		profile.FileName:                         twoLayers,
		"layers/a/settings/claude/settings.json": "{}",
		"layers/a/settings/claude/mcp.json":      "{}",
		"layers/b/settings/claude/agents.json":   "{}",
		"layers/b/settings/claude/settings.json": "{}",
		"layers/b/settings/codex/config.toml":    "\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"agents.json", "mcp.json", "settings.json"}
	if got := res.SettingsFiles("claude"); !reflect.DeepEqual(got, want) {
		t.Fatalf("files %v, want %v", got, want)
	}
	if got := res.SettingsFiles("codex"); !reflect.DeepEqual(got, []string{"config.toml"}) {
		t.Fatalf("codex files %v", got)
	}
	if got := res.SettingsFiles("gemini"); got != nil {
		t.Fatalf("gemini files %v, want none", got)
	}
}

// TestSettingsForSubstitutesTheHarnessHome rewrites every "$QORY_HARNESS_HOME" in a string,
// inside a map and inside a list, and leaves the merged settings themselves alone.
func TestSettingsForSubstitutesTheHarnessHome(t *testing.T) {
	res, err := composeTree(t, map[string]string{
		profile.FileName: twoLayers,
		"layers/a/settings/claude/settings.json": `{
			"hooks": {"PreToolUse": [{"command": "$QORY_HARNESS_HOME/hooks/pre.sh"}]},
			"env": {"QORY_HARNESS_HOME": "$QORY_HARNESS_HOME"},
			"paths": ["$QORY_HARNESS_HOME/skills", "plain"],
			"count": 1
		}`,
		"layers/b/": "",
	})
	if err != nil {
		t.Fatal(err)
	}
	home := "/checkout/.qory/harness"
	got := res.SettingsFor("claude", "settings.json", home)
	want := jsonValue(t, `{
		"hooks": {"PreToolUse": [{"command": "`+home+`/hooks/pre.sh"}]},
		"env": {"QORY_HARNESS_HOME": "`+home+`"},
		"paths": ["`+home+`/skills", "plain"],
		"count": 1
	}`)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("settings:\n%s\nwant:\n%s", indent(t, got), indent(t, want))
	}
	merged := res.Settings["claude"]["settings.json"]["env"].(map[string]any)
	if merged["QORY_HARNESS_HOME"] != "$QORY_HARNESS_HOME" {
		t.Fatalf("the merged settings were rewritten: %v", merged)
	}
	empty := res.SettingsFor("claude", "mcp.json", home)
	if empty == nil || len(empty) != 0 {
		t.Fatalf("a file no layer contributed to is %v, want an empty map", empty)
	}
}

// TestSettingsMergeATomlFragment reads a TOML fragment for the runtime that takes one, with
// its array of tables turned into the list the merge works on.
func TestSettingsMergeATomlFragment(t *testing.T) {
	res, err := composeTree(t, map[string]string{
		profile.FileName: `apiVersion: qory.ai/v1alpha1
kind: HarnessProfile
target:
  runtime: codex
layers:
  - name: a
    source:
      path: layers/a
  - name: b
    source:
      path: layers/b
`,
		"layers/a/settings/codex/config.toml": "model = \"gpt-5\"\n\n[permissions]\nallow = [\"read\", \"write\"]\n\n[[hooks]]\nmatcher = \"Bash\"\n\n[profiles.review]\nmodel = \"o3\"\n",
		"layers/b/settings/codex/config.toml": "[permissions]\nallow = [\"write\", \"exec\"]\n\n[[hooks]]\nmatcher = \"Write\"\n\n[profiles.ship]\nmodel = \"gpt-5\"\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"model":       "gpt-5",
		"permissions": map[string]any{"allow": []any{"read", "write", "exec"}},
		"hooks": []any{
			map[string]any{"matcher": "Bash"},
			map[string]any{"matcher": "Write"},
		},
		"profiles": map[string]any{
			"review": map[string]any{"model": "o3"},
			"ship":   map[string]any{"model": "gpt-5"},
		},
	}
	got := res.Settings["codex"]["config.toml"]
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("settings:\n%#v\nwant:\n%#v", got, want)
	}
}

// TestSettingsRefuseAFragmentQoryCannotRead covers an extension that is neither .json nor
// .toml, and a fragment that does not parse. Every message names the layer and the file.
func TestSettingsRefuseAFragmentQoryCannotRead(t *testing.T) {
	cases := []struct {
		name string
		file string
		body string
		want string
	}{
		{"another extension", "settings.yaml", "model: opus\n", "settings fragments are .json or .toml"},
		{"broken json", "settings.json", "{\n", "unexpected end of JSON input"},
		{"broken toml", "settings.toml", "model = \n", "expected value but found"},
		{"a json list", "settings.json", "[]\n", "cannot unmarshal array into Go value"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := composeTree(t, map[string]string{
				profile.FileName:                     twoLayers,
				"layers/a/settings/claude/" + c.file: c.body,
				"layers/b/":                          "",
			})
			if err == nil {
				t.Fatalf("read %s without an error", c.file)
			}
			if !strings.HasPrefix(err.Error(), "layer a: ") {
				t.Fatalf("error %q does not name the layer", err)
			}
			if !strings.Contains(err.Error(), c.file) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not name %q", err, c.want)
			}
		})
	}
}

// TestExcludeNamesNothingTheLayerShips refuses an exclude that drops nothing, so a renamed
// entry does not leave a stale exclude behind.
func TestExcludeNamesNothingTheLayerShips(t *testing.T) {
	_, err := composeTree(t, map[string]string{
		profile.FileName: `apiVersion: qory.ai/v1alpha1
kind: HarnessProfile
target:
  runtime: claude
layers:
  - name: core
    source:
      path: layers/core
    exclude:
      skills: [gone]
`,
		"layers/core/skills/greet/SKILL.md": "# greet\n",
	})
	if err == nil {
		t.Fatal("composed with an exclude that names nothing")
	}
	if want := "layer core: exclude skills/gone names nothing the layer ships"; err.Error() != want {
		t.Fatalf("error %q, want %q", err, want)
	}
}

// TestCollisionSuggestKeepsTheLastLayer resolves every collision by excluding it from every
// layer but the last, and returns the layers to change in the order the caller gives.
func TestCollisionSuggestKeepsTheLastLayer(t *testing.T) {
	_, err := composeTree(t, map[string]string{
		profile.FileName: `apiVersion: qory.ai/v1alpha1
kind: HarnessProfile
target:
  runtime: claude
layers:
  - name: a
    source:
      path: layers/a
  - name: b
    source:
      path: layers/b
  - name: c
    source:
      path: layers/c
`,
		"layers/a/skills/greet/SKILL.md": "# greet\n",
		"layers/b/skills/greet/SKILL.md": "# greet\n",
		"layers/c/skills/greet/SKILL.md": "# greet\n",
		"layers/a/agents/reviewer.md":    "reviewer\n",
		"layers/c/agents/reviewer.md":    "reviewer\n",
		"layers/b/commands/ship.md":      "ship\n",
	})
	var ce *compose.CollisionError
	if !errors.As(err, &ce) {
		t.Fatalf("error %v, want a *compose.CollisionError", err)
	}
	wantCollisions := []compose.Collision{
		{Kind: "agents", Name: "reviewer", Layers: []string{"a", "c"}},
		{Kind: "skills", Name: "greet", Layers: []string{"a", "b", "c"}},
	}
	if !reflect.DeepEqual(ce.Collisions, wantCollisions) {
		t.Fatalf("collisions %+v, want %+v", ce.Collisions, wantCollisions)
	}
	layers, excludes := ce.Suggest([]string{"a", "b", "c"})
	if !reflect.DeepEqual(layers, []string{"a", "b"}) {
		t.Fatalf("layers %v, want [a b]", layers)
	}
	wantExcludes := map[string]map[string][]string{
		"a": {"agents": {"reviewer"}, "skills": {"greet"}},
		"b": {"skills": {"greet"}},
	}
	if !reflect.DeepEqual(excludes, wantExcludes) {
		t.Fatalf("excludes %v, want %v", excludes, wantExcludes)
	}
	if layers, _ := ce.Suggest([]string{"c", "b", "a"}); !reflect.DeepEqual(layers, []string{"b", "a"}) {
		t.Fatalf("layers %v, want [b a] for the order the caller gave", layers)
	}
	want := "agents/reviewer is provided by 2 layers: a@working-tree, c@working-tree\n" +
		"  keep one and exclude the others, for example\n" +
		"    a: exclude: {agents: [reviewer]}\n" +
		"\n" +
		"skills/greet is provided by 3 layers: a@working-tree, b@working-tree, c@working-tree\n" +
		"  keep one and exclude the others, for example\n" +
		"    a: exclude: {skills: [greet]}\n" +
		"    b: exclude: {skills: [greet]}"
	if ce.Error() != want {
		t.Fatalf("error:\n%s\nwant:\n%s", ce.Error(), want)
	}
}

// TestInstructionsAreConcatenatedInLayerOrder joins every AGENTS.md a layer ships, in the
// order the profile lists the layers, with one blank line between them.
func TestInstructionsAreConcatenatedInLayerOrder(t *testing.T) {
	res, err := composeTree(t, map[string]string{
		profile.FileName: `apiVersion: qory.ai/v1alpha1
kind: HarnessProfile
target:
  runtime: claude
layers:
  - name: b
    source:
      path: layers/b
  - name: a
    source:
      path: layers/a
  - name: c
    source:
      path: layers/c
`,
		"layers/a/AGENTS.md": "From a.\n\n\n",
		"layers/b/AGENTS.md": "From b.",
		"layers/c/":          "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "From b.\n\nFrom a.\n"; res.Instructions != want {
		t.Fatalf("instructions:\n%s\nwant:\n%s", res.Instructions, want)
	}
}

// TestComposeWithoutInstructions leaves the instructions empty when no layer ships one.
func TestComposeWithoutInstructions(t *testing.T) {
	res, err := composeTree(t, map[string]string{
		profile.FileName:               twoLayers,
		"layers/a/commands/ship.md":    "ship\n",
		"layers/b/hooks/pre-commit.sh": "#!/bin/sh\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Instructions != "" {
		t.Fatalf("instructions %q, want none", res.Instructions)
	}
	// The entries are sorted by kind and name, whatever order the layers come in.
	if len(res.Entries) != 2 ||
		res.Entries[0].Kind != "commands" || res.Entries[0].Layer != "a" ||
		res.Entries[1].Kind != "hooks" || res.Entries[1].Layer != "b" {
		t.Fatalf("entries %+v", res.Entries)
	}
}

// TestComposeRecordsEveryLayer keeps the profile's name for a layer, the layer's own name
// from its manifest, the source as the profile writes it and the pin.
func TestComposeRecordsEveryLayer(t *testing.T) {
	res, err := composeTree(t, map[string]string{
		profile.FileName:               twoLayers,
		"layers/a/harness.yaml":        "apiVersion: qory.ai/v1alpha1\nkind: HarnessLayer\nname: acme-core\n",
		"layers/a/commands/ship.md":    "ship\n",
		"layers/b/hooks/pre-commit.sh": "#!/bin/sh\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Layers) != 2 {
		t.Fatalf("%d layers", len(res.Layers))
	}
	a, b := res.Layers[0], res.Layers[1]
	if a.Name != "a" || a.ManifestName != "acme-core" || a.Source != "layers/a" || a.Pin != "working-tree" || a.Variant != "" {
		t.Fatalf("layers[0] %+v", a)
	}
	if b.Name != "b" || b.ManifestName != "" || b.Source != "layers/b" {
		t.Fatalf("layers[1] %+v", b)
	}
}

// TestComposeRefuses covers the errors a layer raises before its entries are read: a source
// that is not there, a manifest qory turns down, and a runtime no variant serves.
func TestComposeRefuses(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			name:  "a source that is not there",
			files: map[string]string{profile.FileName: twoLayers, "layers/b/": ""},
			want:  "layer a: ",
		},
		{
			name: "a manifest qory turns down",
			files: map[string]string{
				profile.FileName:        twoLayers,
				"layers/a/harness.yaml": "apiVersion: qory.ai/v1alpha1\nkind: HarnessLayer\n",
				"layers/b/":             "",
			},
			want: "name is required",
		},
		{
			name: "a runtime no variant serves",
			files: map[string]string{
				profile.FileName:        twoLayers,
				"layers/a/harness.yaml": "apiVersion: qory.ai/v1alpha1\nkind: HarnessLayer\nname: multi\nvariants:\n  codex:\n    agents: agents/codex\n  default: fail\n",
				"layers/b/":             "",
			},
			want: "layer a: the layer has no variant for runtime claude and its default is fail; variants: codex",
		},
		{
			name: "a forced variant the layer has not",
			files: map[string]string{
				profile.FileName:        strings.Replace(twoLayers, "      path: layers/a\n", "      path: layers/a\n    variant: amp\n", 1),
				"layers/a/harness.yaml": "apiVersion: qory.ai/v1alpha1\nkind: HarnessLayer\nname: multi\nvariants:\n  codex:\n    agents: agents/codex\n",
				"layers/b/":             "",
			},
			want: `layer a: variant "amp" is forced, and the layer has no such variant; variants: codex`,
		},
		{
			name: "a skill without a SKILL.md",
			files: map[string]string{
				profile.FileName:             twoLayers,
				"layers/a/skills/greet/x.md": "x\n",
				"layers/b/":                  "",
			},
			want: "layer a: skills/greet has no SKILL.md",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := composeTree(t, c.files)
			if err == nil {
				t.Fatal("composed without an error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not name %q", err, c.want)
			}
		})
	}
}

// indent renders a merged settings file for a failure message.
func indent(t *testing.T, v any) string {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
