package module

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAnUnknownManifestKeyWithASecretInItsNameIsReportedByItsLine is an unknown key of
// qory-module.yaml whose name contains an access key secret or a run key, with a space
// in it or not: the error contains the key's line and not its name. A plain name is
// quoted as before.
func TestAnUnknownManifestKeyWithASecretInItsNameIsReportedByItsLine(t *testing.T) {
	isolate(t)
	for _, c := range []struct{ name, secret, want string }{
		{"qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA", "qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA", "line 3: key is not one qory-module.yaml reads"},
		{"qrk_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA x", "qrk_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA", "line 3: key is not one qory-module.yaml reads"},
		{"modules", "", `line 3: key "modules" is not one qory-module.yaml reads`},
	} {
		dir := tree(t, map[string]string{ManifestName: "apiVersion: qory.dev/v1alpha1\nname: core\n" + c.name + ": 1\n"})
		_, err := ReadManifest(dir)
		if want := filepath.Join(dir, ManifestName) + ": " + c.want; err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", c.name, err, want)
		}
		if c.secret != "" && err != nil && strings.Contains(err.Error(), c.secret) {
			t.Errorf("%s: the error contains the secret: %v", c.name, err)
		}
	}
}

// TestAnUnknownManifestKeyWithANewlineInItsNameIsReportedByItsLine is an unknown key of
// qory-module.yaml whose name contains an access key secret and a newline: the error is
// the key's line alone.
func TestAnUnknownManifestKeyWithANewlineInItsNameIsReportedByItsLine(t *testing.T) {
	isolate(t)
	dir := tree(t, map[string]string{ManifestName: "apiVersion: qory.dev/v1alpha1\nname: core\n\"qak_SECRET\\nb\": 1\n"})
	_, err := ReadManifest(dir)
	if want := filepath.Join(dir, ManifestName) + ": line 3: key is not one qory-module.yaml reads"; err == nil || err.Error() != want {
		t.Errorf("%v, want %q", err, want)
	}
	if err != nil && strings.Contains(err.Error(), "qak_SECRET") {
		t.Errorf("the error contains the secret: %v", err)
	}
}

// isolate sets HOME and the XDG directories qory reads to directories of the test, so a
// test reads no file of the machine it runs on.
func isolate(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
}

// The phrases a message prints in place of a name that contains an access key secret
// or a run key.
const (
	accessKeyPhrase = "(a name that contains an access key secret)"
	runKeyPhrase    = "(a name that contains a run key)"
)

// TestAManifestNameWithASecretIsLeftOutOfItsRefusal is each refusal of qory-module.yaml
// that prints a name the file writes, a key of env, of variants or of a requires item,
// an entry requires lists, and the variant default selects, when the name contains an
// access key secret or a run key: the refusal prints the phrase in its place.
func TestAManifestNameWithASecretIsLeftOutOfItsRefusal(t *testing.T) {
	isolate(t)
	head := "apiVersion: qory.dev/v1alpha1\nname: core\n"
	for _, c := range []struct{ body, want string }{
		{head + "env:\n  qak_x-y: bin\n", "env: " + accessKeyPhrase + " is not an environment variable name"},
		{head + "env:\n  QAK_X: /bin\n", "env." + accessKeyPhrase + ": /bin is not a path inside the module"},
		{head + "env:\n  QRK_X: ../bin\n", "env." + runKeyPhrase + ": ../bin is not a path inside the module"},
		{head + "variants:\n  qak_v: [x]\n", "variants." + accessKeyPhrase + ": "},
		{head + "variants:\n  claude: {skills: s}\n  default: qrk_v\n", "variants.default selects " + runKeyPhrase + ", which is not a variant"},
		{head + "requires:\n  - skill: deploy\n    qak_x: [a]\n", "requires[0]: key " + accessKeyPhrase + " is not one requires reads; an item selects its entry as skill, agent, command, output-style, hook, mcp or file, and what it needs under skills, agents, commands, output-styles, hooks, mcp and files"},
		{head + "requires:\n  - skill: qak_x\n    agents: [a]\n  - skill: qak_x\n    agents: [b]\n", "requires lists skill " + accessKeyPhrase + " twice"},
	} {
		dir := tree(t, map[string]string{ManifestName: c.body})
		_, err := ReadManifest(dir)
		want := filepath.Join(dir, ManifestName) + ": " + c.want
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%q: %v, want %q", c.body, err, want)
		}
		if err != nil && (strings.Contains(strings.ToLower(err.Error()), "qak_") || strings.Contains(strings.ToLower(err.Error()), "qrk_")) {
			t.Errorf("%q: the error contains the secret: %v", c.body, err)
		}
	}
}

// TestAVariantNameWithASecretIsLeftOutOfItsRefusal is each refusal of a variant whose
// name, or a name it lists, contains an access key secret or a run key: a forced
// variant the module does not have, the variant names the refusal lists, a runtime
// with no variant, a kind a variant sets, and an entry requires lists that the module
// does not ship. Plain names print as before.
func TestAVariantNameWithASecretIsLeftOutOfItsRefusal(t *testing.T) {
	isolate(t)
	m := &Manifest{Variants: map[string]Variant{"qak_v": {}, "claude": {}}}
	if _, err := SelectVariant(m, "QRK_f", "codex"); err == nil || err.Error() != "variant "+runKeyPhrase+" is forced, and the module has no such variant; variants: claude, "+accessKeyPhrase {
		t.Errorf("forced: %v", err)
	}
	if _, err := SelectVariant(&Manifest{}, "qak_f", "codex"); err == nil || err.Error() != "variant "+accessKeyPhrase+" is forced, and the module declares no variants" {
		t.Errorf("forced without variants: %v", err)
	}
	if _, err := SelectVariant(m, "", "qrk_rt"); err == nil || err.Error() != "the module has no variant for runtime "+runKeyPhrase+" and declares no default; variants: claude, "+accessKeyPhrase {
		t.Errorf("runtime: %v", err)
	}
	m.Default = "fail"
	if _, err := SelectVariant(m, "", "qrk_rt"); err == nil || err.Error() != "the module has no variant for runtime "+runKeyPhrase+" and its default is fail; variants: claude, "+accessKeyPhrase {
		t.Errorf("runtime, default fail: %v", err)
	}
	dir := tree(t, map[string]string{"skills/deploy/SKILL.md": "---\nname: deploy\ndescription: d\n---\n"})
	m = &Manifest{Variants: map[string]Variant{"qak_v": {"qrk_kind": "x"}}}
	if _, err := Read("core", dir, m, "qak_v"); err == nil || err.Error() != "module core: variant "+accessKeyPhrase+" sets kind "+runKeyPhrase {
		t.Errorf("kind: %v", err)
	}
	m = &Manifest{Requires: map[string][]string{"skills/qak_x": {"agents/a"}}}
	if _, err := Read("core", dir, m, ""); err == nil || err.Error() != "module core: requires lists skill "+accessKeyPhrase+", which the module does not ship" {
		t.Errorf("requires: %v", err)
	}
}

// TestAnMCPServerKeyWithASecretIsLeftOutOfItsRefusal is an mcp/<name>.json object
// whose key, or a key of its env, contains an access key secret or a run key: the
// refusal prints the phrase in its place.
func TestAnMCPServerKeyWithASecretIsLeftOutOfItsRefusal(t *testing.T) {
	isolate(t)
	for _, c := range []struct{ server, want string }{
		{`{"command": "x", "qak_x": 1}`, "mcp/s.json: key " + accessKeyPhrase + " is not one of an MCP server's; keys: type, command, args, env, url, headers, description"},
		{`{"command": "x", "env": {"QRK_X": 1}}`, "mcp/s.json: env." + runKeyPhrase + " is 1, which is not a string"},
	} {
		dir := tree(t, map[string]string{"mcp/s.json": c.server})
		_, err := Read("core", dir, nil, "")
		if err == nil || !strings.HasSuffix(err.Error(), c.want) {
			t.Errorf("%s: %v, want one that ends in %q", c.server, err, c.want)
		}
	}
}

// TestAModuleNameWithASecretIsLeftOutOfItsRefusals is the refusals of a module whose
// name, as the stack lists it, contains an access key secret or a run key, and of an
// MCP server whose file name contains one: a variant that sets a kind the module does
// not have, a variant that reads a kind from outside the module, an entry requires lists
// that the module does not ship, a skill without SKILL.md, and a server file that is not
// a JSON object or that the server check refuses. Each prints the phrase in place of the
// name.
func TestAModuleNameWithASecretIsLeftOutOfItsRefusals(t *testing.T) {
	isolate(t)
	dir := tree(t, map[string]string{"skills/deploy/SKILL.md": "---\nname: deploy\ndescription: d\n---\n"})
	for _, c := range []struct {
		name, variant string
		m             *Manifest
		want          string
	}{
		{"qak_m", "v", &Manifest{Variants: map[string]Variant{"v": {"prompts": "x"}}}, "module " + accessKeyPhrase + `: variant v sets kind "prompts"`},
		{"QRK_m", "v", &Manifest{Variants: map[string]Variant{"v": {"skills": "../x"}}}, "module " + runKeyPhrase + `: variant v reads skills from "../x", which is not a directory inside the module`},
		{"qak_m", "", &Manifest{Requires: map[string][]string{"skills/absent": {"agents/a"}}}, "module " + accessKeyPhrase + ": requires lists skill absent, which the module does not ship"},
	} {
		if _, err := Read(c.name, dir, c.m, c.variant); err == nil || err.Error() != c.want {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	bare := tree(t, map[string]string{"skills/greet/x.md": "x\n"})
	if _, err := Read("qak_m", bare, nil, ""); err == nil || !strings.HasPrefix(err.Error(), "module "+accessKeyPhrase+": skills/") {
		t.Errorf("a skill without SKILL.md: %v", err)
	}
	for _, c := range []struct{ body, want string }{
		{"[1]", "module core: mcp/" + accessKeyPhrase + ".json does not contain a JSON object: "},
		{"null", "module core: mcp/" + accessKeyPhrase + ".json does not contain a JSON object"},
		{`{"command": "x", "bad": 1}`, "module core: mcp/" + accessKeyPhrase + `.json: key "bad" is not one of an MCP server's`},
	} {
		d := tree(t, map[string]string{"mcp/qak_srv.json": c.body})
		if _, err := Read("core", d, nil, ""); err == nil || !strings.HasPrefix(err.Error(), c.want) {
			t.Errorf("%s: %v, want one that starts with %q", c.body, err, c.want)
		}
	}
}

// TestAHookDirectoryRefusalLeavesOutAModuleNameWithASecret is a module whose name
// contains an access key secret and whose hooks directory holds a directory: the
// refusal's path under $QORY_HARNESS_HOME prints the phrase in place of the name.
func TestAHookDirectoryRefusalLeavesOutAModuleNameWithASecret(t *testing.T) {
	isolate(t)
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "share"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := tree(t, map[string]string{"hooks/sub/x.sh": "#!/bin/sh\n"})
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	_, err := Read("qak_mod", dir, nil, "")
	want := "module " + accessKeyPhrase + ": hooks/sub is a directory; a hook is one file. Keep its helpers elsewhere in the module, reached as $QORY_HARNESS_HOME/modules/" + accessKeyPhrase + "/<path>"
	if err == nil || err.Error() != want {
		t.Errorf("%v, want %q", err, want)
	}
}

// TestAManifestThatIsNotThereLeavesOutASecretInItsDirectory is a module directory with
// no qory-module.yaml, whose name contains an access key secret, with a space in it or
// not: the refusal prints the phrase in place of the directory's name.
func TestAManifestThatIsNotThereLeavesOutASecretInItsDirectory(t *testing.T) {
	isolate(t)
	for _, name := range []string{"qak_m", "a qak_m"} {
		root := tree(t, map[string]string{name + "/AGENTS.md": "# M\n"})
		_, err := ReadManifest(filepath.Join(root, name))
		shown := strings.Replace(name, "qak_m", accessKeyPhrase, 1)
		if want := filepath.Join(root, shown) + " has no qory-module.yaml; a module has one that defines its name"; err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", name, err, want)
		}
	}
}

// TestAManifestQoryCannotReadLeavesOutASecretInItsPath is a qory-module.yaml of mode
// 000 in a directory whose name contains an access key secret: the refusal prints no
// secret and still matches the *fs.PathError under it.
func TestAManifestQoryCannotReadLeavesOutASecretInItsPath(t *testing.T) {
	isolate(t)
	if os.Geteuid() == 0 {
		t.Skip("root reads a file of mode 000")
	}
	root := tree(t, map[string]string{"qak_m/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: qak_m\n"})
	manifest := filepath.Join(root, "qak_m", "qory-module.yaml")
	if err := os.Chmod(manifest, 0); err != nil {
		t.Fatal(err)
	}
	_, err := ReadManifest(filepath.Join(root, "qak_m"))
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) {
		t.Errorf("%v, want a *fs.PathError", err)
	}
	if want := filepath.Join(root, accessKeyPhrase, "qory-module.yaml") + ": permission denied"; err == nil || !strings.HasSuffix(err.Error(), want) || strings.Contains(err.Error(), "qak_") {
		t.Errorf("%v, want the suffix %q", err, want)
	}
}
