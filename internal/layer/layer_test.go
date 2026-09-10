package layer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tree writes a layer under a fresh temporary directory and returns it. A key ending in "/"
// is an empty directory; every other key is a file holding its value.
func tree(t *testing.T, files map[string]string) string {
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

// entryLines renders the entries as "kind/name path" lines, with the layer directory cut off
// the path, so a test can compare them as one string.
func entryLines(l *Layer) string {
	var b strings.Builder
	for _, e := range l.Entries {
		rel, err := filepath.Rel(l.Dir, e.Path)
		if err != nil {
			rel = e.Path
		}
		b.WriteString(e.Kind + "/" + e.Name + " " + rel + "\n")
	}
	return b.String()
}

// TestReadListsEveryKindWithItsPaths reads a layer holding all six kinds, the settings
// fragments and the instruction file.
func TestReadListsEveryKindWithItsPaths(t *testing.T) {
	dir := tree(t, map[string]string{
		"skills/review/SKILL.md":        "# review\n",
		"skills/ship/SKILL.md":          "# ship\n",
		"agents/reviewer.md":            "reviewer\n",
		"commands/ship.md":              "ship\n",
		"output-styles/terse.md":        "terse\n",
		"hooks/pre-commit.sh":           "#!/bin/sh\n",
		"hooks/notify.py":               "print()\n",
		"mcp/db.json":                   "{\"command\": \"db\"}\n",
		"settings/claude/settings.json": "{}\n",
		"settings/claude/mcp.json":      "{}\n",
		"settings/codex/config.toml":    "\n",
		"AGENTS.md":                     "read me\n",
	})
	l, err := Read("core", dir, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if l.Name != "core" || l.Dir != dir || l.Manifest != nil || l.Variant != "" {
		t.Fatalf("got %+v", l)
	}
	want := "agents/reviewer agents/reviewer.md\n" +
		"commands/ship commands/ship.md\n" +
		"hooks/notify.py hooks/notify.py\n" +
		"hooks/pre-commit.sh hooks/pre-commit.sh\n" +
		"mcp/db mcp/db.json\n" +
		"output-styles/terse output-styles/terse.md\n" +
		"skills/review skills/review\n" +
		"skills/ship skills/ship\n"
	if got := entryLines(l); got != want {
		t.Fatalf("entries:\n%swant:\n%s", got, want)
	}
	for _, e := range l.Entries {
		if !filepath.IsAbs(e.Path) {
			t.Fatalf("%s/%s has a relative path %s", e.Kind, e.Name, e.Path)
		}
	}
	if len(l.MCP) != 1 || l.MCP["db"]["command"] != "db" {
		t.Fatalf("mcp servers %v", l.MCP)
	}
	settings := map[string]map[string]string{
		"claude": {
			"settings.json": filepath.Join(dir, "settings", "claude", "settings.json"),
			"mcp.json":      filepath.Join(dir, "settings", "claude", "mcp.json"),
		},
		"codex": {"config.toml": filepath.Join(dir, "settings", "codex", "config.toml")},
	}
	for runtime, files := range settings {
		for file, path := range files {
			if l.Settings[runtime][file] != path {
				t.Fatalf("settings %s/%s is %q, want %q", runtime, file, l.Settings[runtime][file], path)
			}
		}
		if len(l.Settings[runtime]) != len(files) {
			t.Fatalf("settings %s: %v", runtime, l.Settings[runtime])
		}
	}
	if len(l.Settings) != len(settings) {
		t.Fatalf("settings runtimes: %v", l.Settings)
	}
	if l.Instructions != filepath.Join(dir, InstructionsName) {
		t.Fatalf("instructions %q", l.Instructions)
	}
}

// TestReadSkipsWhatIsNotAnEntry ignores dot-directories, dot-files, files of another
// extension, a loose file in skills, and an empty kind directory.
func TestReadSkipsWhatIsNotAnEntry(t *testing.T) {
	dir := tree(t, map[string]string{
		"skills/review/SKILL.md":         "# review\n",
		"skills/.hidden/SKILL.md":        "# hidden\n",
		"skills/README.md":               "not a skill\n",
		"agents/reviewer.md":             "reviewer\n",
		"agents/.draft.md":               "draft\n",
		"agents/notes.txt":               "notes\n",
		"agents/nested/planner.md":       "planner\n",
		"commands/":                      "",
		"output-styles/.keep":            "",
		"hooks/pre-commit.sh":            "#!/bin/sh\n",
		"hooks/.gitignore":               "*\n",
		"mcp/README.md":                  "not a server\n",
		"mcp/.draft.json":                "{}\n",
		"mcp/nested/db.json":             "{}\n",
		"settings/.hidden/settings.json": "{}\n",
		"settings/claude/.settings.json": "{}\n",
		"settings/README.md":             "not a runtime\n",
	})
	l, err := Read("core", dir, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	want := "agents/reviewer agents/reviewer.md\n" +
		"hooks/pre-commit.sh hooks/pre-commit.sh\n" +
		"skills/review skills/review\n"
	if got := entryLines(l); got != want {
		t.Fatalf("entries:\n%swant:\n%s", got, want)
	}
	if len(l.Settings) != 0 {
		t.Fatalf("settings %v, want none", l.Settings)
	}
	if l.Instructions != "" {
		t.Fatalf("instructions %q, want none", l.Instructions)
	}
}

// TestReadRefusesADirectoryUnderHooks names the directory and where its files belong.
func TestReadRefusesADirectoryUnderHooks(t *testing.T) {
	dir := tree(t, map[string]string{
		"hooks/guard.sh":          "#!/bin/sh\n",
		"hooks/scripts/helper.sh": "#!/bin/sh\n",
	})
	_, err := Read("core", dir, nil, "")
	if err == nil {
		t.Fatal("read a layer with a directory under hooks")
	}
	want := "layer core: hooks/scripts is a directory; a hook is one file. Keep its helpers elsewhere in the layer, reached as $QORY_HARNESS_HOME/layers/core/<path>"
	if err.Error() != want {
		t.Fatalf("error %q, want %q", err, want)
	}
}

// TestReadRefusesAnMCPServerThatIsNotAnObject names the file, and keeps the decoder's
// message for a file that does not parse, so the reader learns where the file breaks. A
// null parses and gets no decoder message.
func TestReadRefusesAnMCPServerThatIsNotAnObject(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{"[]\n", "layer core: mcp/db.json does not hold a JSON object: json: cannot unmarshal array into Go value of type map[string]interface {}"},
		{"not json\n", "layer core: mcp/db.json does not hold a JSON object: invalid character 'o' in literal null (expecting 'u')"},
		{"{\"command\": \"x\",}\n", "layer core: mcp/db.json does not hold a JSON object: invalid character '}' looking for beginning of object key string"},
		{"null\n", "layer core: mcp/db.json does not hold a JSON object"},
	} {
		dir := tree(t, map[string]string{"mcp/db.json": c.body})
		_, err := Read("core", dir, nil, "")
		if err == nil {
			t.Fatalf("read %q as a server", c.body)
		}
		if err.Error() != c.want {
			t.Errorf("%q: error %q, want %q", c.body, err, c.want)
		}
	}
}

// TestReadRefusesAnMCPServerNoRuntimeCouldStart covers the shapes the schema refuses: a
// key nothing reads, both transports, neither.
func TestReadRefusesAnMCPServerNoRuntimeCouldStart(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{`{"comand": "x"}`, `layer core: mcp/db.json: key "comand" is not one of an MCP server's; keys: type, command, args, env, url, headers, description`},
		{`{"command": "x", "url": "https://x"}`, "layer core: mcp/db.json: names both a command and a url; a server is one or the other"},
		{`{"args": ["x"]}`, "layer core: mcp/db.json: names neither a command nor a url"},
	} {
		dir := tree(t, map[string]string{"mcp/db.json": c.body})
		_, err := Read("core", dir, nil, "")
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: error %v, want %q", c.body, err, c.want)
		}
	}
}

// TestReadRefusesALinkToADirectoryUnderHooks is hooks/scripts as a symlink to a directory
// elsewhere in the layer, and the message under a variant names the directory the variant
// reads.
func TestReadRefusesALinkToADirectoryUnderHooks(t *testing.T) {
	dir := tree(t, map[string]string{
		"helpers/a.sh":          "#!/bin/sh\n",
		"claude/hooks/guard.sh": "#!/bin/sh\n",
		"harness.yaml":          "apiVersion: qory.ai/v1alpha1\nkind: HarnessLayer\nname: core\nvariants:\n  claude: {hooks: claude/hooks}\n",
	})
	if err := os.Symlink(filepath.Join("..", "..", "helpers"), filepath.Join(dir, "claude", "hooks", "scripts")); err != nil {
		t.Fatal(err)
	}
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Read("core", dir, m, "claude")
	want := "layer core: claude/hooks/scripts is a directory; a hook is one file. Keep its helpers elsewhere in the layer, reached as $QORY_HARNESS_HOME/layers/core/<path>"
	if err == nil || err.Error() != want {
		t.Fatalf("error %v, want %q", err, want)
	}
}

// TestReadRefusesASkillWithoutItsSkillFile names the directory the skill is missing from.
func TestReadRefusesASkillWithoutItsSkillFile(t *testing.T) {
	dir := tree(t, map[string]string{"skills/review/notes.md": "notes\n"})
	_, err := Read("core", dir, nil, "")
	if err == nil {
		t.Fatal("read a skill directory without a SKILL.md")
	}
	if want := "layer core: skills/review has no SKILL.md"; err.Error() != want {
		t.Fatalf("error %q, want %q", err, want)
	}
}

// TestReadEmptyLayer reads a directory holding no kind directory at all.
func TestReadEmptyLayer(t *testing.T) {
	l, err := Read("core", t.TempDir(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != 0 || l.Settings != nil || l.Instructions != "" {
		t.Fatalf("got %+v", l)
	}
}

// TestReadFollowsTheVariantDirectories reads the kinds the variant redirects from its
// directory and every other kind from the layer root.
func TestReadFollowsTheVariantDirectories(t *testing.T) {
	dir := tree(t, map[string]string{
		"harness.yaml":               "apiVersion: qory.ai/v1alpha1\nkind: HarnessLayer\nname: multi\nvariants:\n  codex:\n    agents: agents/codex\n    skills: skills/codex\n",
		"agents/reviewer.md":         "reviewer\n",
		"agents/codex/planner.md":    "planner\n",
		"skills/codex/ship/SKILL.md": "# ship\n",
		"commands/deploy.md":         "deploy\n",
	})
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	l, err := Read("multi", dir, m, "codex")
	if err != nil {
		t.Fatal(err)
	}
	want := "agents/planner agents/codex/planner.md\n" +
		"commands/deploy commands/deploy.md\n" +
		"skills/ship skills/codex/ship\n"
	if got := entryLines(l); got != want {
		t.Fatalf("entries:\n%swant:\n%s", got, want)
	}
	if l.Variant != "codex" || l.Manifest != m {
		t.Fatalf("got variant %q, manifest %v", l.Variant, l.Manifest)
	}
}

// TestReadVariantDirectoryMissingFromDisk records that a variant directory the manifest
// names but the layer does not hold contributes no entries and no error.
func TestReadVariantDirectoryMissingFromDisk(t *testing.T) {
	dir := tree(t, map[string]string{
		"harness.yaml":       "apiVersion: qory.ai/v1alpha1\nkind: HarnessLayer\nname: multi\nvariants:\n  codex:\n    agents: agents/codex\n",
		"agents/reviewer.md": "reviewer\n",
	})
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	l, err := Read("multi", dir, m, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != 0 {
		t.Fatalf("entries %s, want none", entryLines(l))
	}
}

// TestReadRefusesAVariantThatLeavesTheLayer refuses a variant naming an unknown kind or a
// directory outside the layer.
func TestReadRefusesAVariantThatLeavesTheLayer(t *testing.T) {
	cases := []struct {
		name    string
		variant Variant
		want    string
	}{
		{"unknown kind", Variant{"prompts": "prompts"}, `layer multi: variant codex names kind "prompts"`},
		{"empty path", Variant{"agents": ""}, `layer multi: variant codex reads agents from "", which is not a directory inside the layer`},
		{"the layer root", Variant{"agents": "."}, `layer multi: variant codex reads agents from ".", which is not a directory inside the layer`},
		{"a parent", Variant{"agents": "../other"}, `layer multi: variant codex reads agents from "../other", which is not a directory inside the layer`},
		{"an absolute path", Variant{"agents": "/etc"}, `layer multi: variant codex reads agents from "/etc", which is not a directory inside the layer`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := &Manifest{APIVersion: "qory.ai/v1alpha1", Kind: Kind, Name: "multi", Variants: map[string]Variant{"codex": c.variant}}
			_, err := Read("multi", t.TempDir(), m, "codex")
			if err == nil {
				t.Fatal("read a variant that leaves the layer")
			}
			if err.Error() != c.want {
				t.Fatalf("error %q, want %q", err, c.want)
			}
		})
	}
}

// TestReadManifestWithoutAFile returns nil for a layer that carries no manifest.
func TestReadManifestWithoutAFile(t *testing.T) {
	m, err := ReadManifest(t.TempDir())
	if err != nil || m != nil {
		t.Fatalf("got %v, %v", m, err)
	}
}

// TestReadManifestReadsVariantsAndDefault reads a manifest holding a variant per runtime and
// a default naming one of them.
func TestReadManifestReadsVariantsAndDefault(t *testing.T) {
	dir := tree(t, map[string]string{ManifestName: "apiVersion: qory.ai/v1alpha1\n" +
		"kind: HarnessLayer\n" +
		"name: multi\n" +
		"variants:\n" +
		"  claude:\n" +
		"    agents: agents/claude\n" +
		"  codex:\n" +
		"    agents: agents/codex\n" +
		"    skills: skills/codex\n" +
		"  default: claude\n"})
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.APIVersion != "qory.ai/v1alpha1" || m.Kind != Kind || m.Name != "multi" {
		t.Fatalf("got %+v", m)
	}
	if m.Default != "claude" {
		t.Fatalf("default %q, want claude", m.Default)
	}
	if len(m.Variants) != 2 ||
		m.Variants["claude"]["agents"] != "agents/claude" ||
		m.Variants["codex"]["agents"] != "agents/codex" ||
		m.Variants["codex"]["skills"] != "skills/codex" {
		t.Fatalf("variants %v", m.Variants)
	}
}

// TestReadManifestRefuses covers every document the manifest reader turns down. The message
// starts with the manifest path, which the command prints as it comes.
func TestReadManifestRefuses(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{
			"unknown apiVersion",
			"apiVersion: qory.ai/v2\nkind: HarnessLayer\nname: core\n",
			`apiVersion "qory.ai/v2" is not one this qory reads; versions: qory.ai/v1alpha1`,
		},
		{
			"unknown kind",
			"apiVersion: qory.ai/v1alpha1\nkind: HarnessProfile\nname: core\n",
			`kind "HarnessProfile" is not HarnessLayer`,
		},
		{
			"missing name",
			"apiVersion: qory.ai/v1alpha1\nkind: HarnessLayer\n",
			"name is required",
		},
		{
			"unknown field",
			"apiVersion: qory.ai/v1alpha1\nkind: HarnessLayer\nname: core\nlayers: []\n",
			"field layers not found",
		},
		{
			"a default that is not a variant",
			"apiVersion: qory.ai/v1alpha1\nkind: HarnessLayer\nname: core\nvariants:\n  codex:\n    agents: agents/codex\n  default: claude\n",
			`variants.default names "claude", which is not a variant`,
		},
		{
			"a default that is not a string",
			"apiVersion: qory.ai/v1alpha1\nkind: HarnessLayer\nname: core\nvariants:\n  default:\n    agents: agents/claude\n",
			"variants.default:",
		},
		{
			"a variant that is not a map of kinds",
			"apiVersion: qory.ai/v1alpha1\nkind: HarnessLayer\nname: core\nvariants:\n  codex: agents/codex\n",
			"variants.codex:",
		},
		{
			"broken yaml",
			"apiVersion: [\n",
			"did not find expected node content",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := tree(t, map[string]string{ManifestName: c.yaml})
			_, err := ReadManifest(dir)
			if err == nil {
				t.Fatalf("read %q without an error", c.yaml)
			}
			path := filepath.Join(dir, ManifestName)
			if !strings.HasPrefix(err.Error(), path+": ") {
				t.Fatalf("error %q does not start with %q", err, path)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not name %q", err, c.want)
			}
		})
	}
}

// TestReadManifestReadsEnv reads the variables a manifest exports: each value comes back
// cleaned, and the layer root is ".".
func TestReadManifestReadsEnv(t *testing.T) {
	dir := tree(t, map[string]string{ManifestName: "apiVersion: qory.ai/v1alpha1\n" +
		"kind: HarnessLayer\n" +
		"name: core\n" +
		"env:\n" +
		"  CORE_HOME: .\n" +
		"  CORE_SCRIPTS: ./scripts/\n" +
		"  _core_lib: lib/../lib/py\n"})
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"CORE_HOME": ".", "CORE_SCRIPTS": "scripts", "_core_lib": filepath.Join("lib", "py")}
	if len(m.Env) != len(want) {
		t.Fatalf("env %v, want %v", m.Env, want)
	}
	for k, v := range want {
		if m.Env[k] != v {
			t.Errorf("env %s is %q, want %q", k, m.Env[k], v)
		}
	}
	// A manifest without env leaves the map nil.
	dir = tree(t, map[string]string{ManifestName: "apiVersion: qory.ai/v1alpha1\nkind: HarnessLayer\nname: core\n"})
	m, err = ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Env != nil {
		t.Fatalf("env %v, want nil", m.Env)
	}
}

// TestReadManifestRefusesEnv covers every env the manifest reader turns down: a key that is
// not an environment variable name, qory's own variable, and a value that is not a path
// inside the layer. The message starts with the manifest path.
func TestReadManifestRefusesEnv(t *testing.T) {
	head := "apiVersion: qory.ai/v1alpha1\nkind: HarnessLayer\nname: core\nenv:\n"
	cases := []struct {
		name string
		env  string
		want string
	}{
		{"a key with a dash", "  core-home: scripts\n", "env: core-home is not an environment variable name"},
		{"a key starting with a digit", "  1CORE: scripts\n", "env: 1CORE is not an environment variable name"},
		{"an empty key", `  "": scripts` + "\n", "env:  is not an environment variable name"},
		{"qory's own variable", "  QORY_HARNESS_HOME: .\n", "env.QORY_HARNESS_HOME is qory's own; a layer exports another name"},
		{"an empty value", `  CORE_HOME: ""` + "\n", "env.CORE_HOME:  is not a path inside the layer"},
		{"an absolute value", "  CORE_HOME: /etc\n", "env.CORE_HOME: /etc is not a path inside the layer"},
		{"the parent", "  CORE_HOME: ..\n", "env.CORE_HOME: .. is not a path inside the layer"},
		{"a path leaving the layer", "  CORE_HOME: scripts/../../other\n", "env.CORE_HOME: scripts/../../other is not a path inside the layer"},
		{"a value that is not a string", "  CORE_HOME: [a]\n", "cannot unmarshal"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := tree(t, map[string]string{ManifestName: head + c.env})
			_, err := ReadManifest(dir)
			if err == nil {
				t.Fatalf("read %q without an error", c.env)
			}
			path := filepath.Join(dir, ManifestName)
			if !strings.HasPrefix(err.Error(), path+": ") {
				t.Fatalf("error %q does not start with %q", err, path)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not name %q", err, c.want)
			}
		})
	}
}

// TestReadManifestPropagatesAReadError returns the operating system's error for a manifest
// that cannot be read, such as a directory in its place.
func TestReadManifestPropagatesAReadError(t *testing.T) {
	dir := tree(t, map[string]string{ManifestName + "/": ""})
	if _, err := ReadManifest(dir); err == nil {
		t.Fatal("read a directory as a manifest")
	}
}

// TestSelectVariantPicksTheForcedRuntimeOrDefaultOne walks the whole order of preference.
func TestSelectVariantPicksTheForcedRuntimeOrDefaultOne(t *testing.T) {
	two := &Manifest{Variants: map[string]Variant{
		"claude": {"agents": "agents/claude"},
		"codex":  {"agents": "agents/codex"},
	}}
	withDefault := &Manifest{Variants: two.Variants, Default: "claude"}
	failing := &Manifest{Variants: two.Variants, Default: "fail"}
	cases := []struct {
		name     string
		manifest *Manifest
		forced   string
		runtime  string
		want     string
		wantErr  string
	}{
		{name: "the forced variant wins", manifest: withDefault, forced: "codex", runtime: "claude", want: "codex"},
		{name: "the variant named like the runtime", manifest: two, runtime: "codex", want: "codex"},
		{name: "the default", manifest: withDefault, runtime: "gemini", want: "claude"},
		{name: "the layer root without variants", manifest: &Manifest{Name: "core"}, runtime: "claude", want: ""},
		{name: "the layer root without a manifest", runtime: "claude", want: ""},
		{
			name: "a default of fail", manifest: failing, runtime: "gemini",
			wantErr: "the layer has no variant for runtime gemini and its default is fail; variants: claude, codex",
		},
		{
			name: "no default at all", manifest: two, runtime: "gemini",
			wantErr: "the layer has no variant for runtime gemini and declares no default; variants: claude, codex",
		},
		{
			name: "a forced variant the layer has not", manifest: two, forced: "amp", runtime: "claude",
			wantErr: `variant "amp" is forced, and the layer has no such variant; variants: claude, codex`,
		},
		{
			name: "a forced variant on a layer without variants", manifest: &Manifest{Name: "core"}, forced: "codex", runtime: "claude",
			wantErr: `variant "codex" is forced, and the layer declares no variants`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := SelectVariant(c.manifest, c.forced, c.runtime)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("selected %q, want an error", got)
				}
				if err.Error() != c.wantErr {
					t.Fatalf("error %q, want %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("selected %q, want %q", got, c.want)
			}
		})
	}
}

// TestReadPropagatesADirectoryError hands back the operating system's error for a kind
// directory the process cannot list, with the layer named.
func TestReadPropagatesADirectoryError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads a directory whatever its mode")
	}
	for _, kind := range []string{"skills", "agents", "commands", "output-styles", "hooks", "mcp", "settings"} {
		t.Run(kind, func(t *testing.T) {
			dir := tree(t, map[string]string{kind + "/": ""})
			closed := filepath.Join(dir, kind)
			if err := os.Chmod(closed, 0o000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(closed, 0o755) })
			_, err := Read("core", dir, nil, "")
			if err == nil {
				t.Fatalf("read %s without permission", kind)
			}
			if !strings.HasPrefix(err.Error(), "layer core: ") {
				t.Fatalf("error %q does not name the layer", err)
			}
		})
	}
}

// TestReadPropagatesARuntimeDirectoryError covers a settings runtime directory the process
// cannot list.
func TestReadPropagatesARuntimeDirectoryError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads a directory whatever its mode")
	}
	dir := tree(t, map[string]string{"settings/claude/": ""})
	closed := filepath.Join(dir, "settings", "claude")
	if err := os.Chmod(closed, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(closed, 0o755) })
	if _, err := Read("core", dir, nil, ""); err == nil {
		t.Fatal("read settings/claude without permission")
	}
}

// TestReadRefusesADanglingSkillLink covers the stat of a skill entry that leads nowhere.
func TestReadRefusesADanglingSkillLink(t *testing.T) {
	dir := tree(t, map[string]string{"skills/": ""})
	if err := os.Symlink(filepath.Join(dir, "gone"), filepath.Join(dir, "skills", "review")); err != nil {
		t.Fatal(err)
	}
	if _, err := Read("core", dir, nil, ""); err == nil {
		t.Fatal("read a skill link that leads nowhere")
	}
}
