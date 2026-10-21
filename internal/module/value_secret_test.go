package module

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The phrases a message prints in place of a value that contains an access key secret
// or a run key.
const (
	accessKeyValuePhrase = "(a value that contains an access key secret)"
	runKeyValuePhrase    = "(a value that contains a run key)"
)

// containsSecret reports whether text contains qak_ or qrk_ in any letter case.
func containsSecret(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "qak_") || strings.Contains(lower, "qrk_")
}

// TestAnMCPServerValueWithASecretIsLeftOutOfItsRefusal is an mcp/<name>.json object
// whose type, args element, env or headers value, or description is not what the server
// check reads and contains an access key secret or a run key: the refusal prints the
// value phrase in place of the value. A plain value prints as before.
func TestAnMCPServerValueWithASecretIsLeftOutOfItsRefusal(t *testing.T) {
	isolate(t)
	for _, c := range []struct{ server, want string }{
		{`{"command": "x", "type": "qak_t"}`, "mcp/s.json: type " + accessKeyValuePhrase + " is not stdio, http or sse"},
		{`{"command": "x", "type": ["QRK_t"]}`, "mcp/s.json: type " + runKeyValuePhrase + " is not stdio, http or sse"},
		{`{"command": "x", "args": ["a", {"k": "qrk_a"}]}`, "mcp/s.json: args contains " + runKeyValuePhrase + ", which is not a string"},
		{`{"url": "https://git.example", "headers": {"Authorization": ["Bearer qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA"]}}`, "mcp/s.json: headers.Authorization is " + accessKeyValuePhrase + ", which is not a string"},
		{`{"command": "x", "env": {"TOKEN": {"v": "qak_x"}}}`, "mcp/s.json: env.TOKEN is " + accessKeyValuePhrase + ", which is not a string"},
		{`{"command": "x", "description": ["qrk_d"]}`, "mcp/s.json: description is " + runKeyValuePhrase + ", which is not a string"},
		{`{"url": "https://git.example", "headers": {"Authorization": ["Bearer token-123"]}}`, "mcp/s.json: headers.Authorization is [Bearer token-123], which is not a string"},
	} {
		dir := tree(t, map[string]string{"mcp/s.json": c.server})
		_, err := Read("core", dir, nil, "")
		if want := "module core: " + c.want; err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", c.server, err, want)
		}
	}
}

// TestAnMCPFileQoryCannotReadLeavesOutASecretInItsPath is a module directory whose name
// contains an access key secret, with an mcp directory qory cannot list, an MCP server
// file that links to nothing, and one of mode 000: each refusal prints the phrase in
// place of the directory's name and still matches the *fs.PathError under it.
func TestAnMCPFileQoryCannotReadLeavesOutASecretInItsPath(t *testing.T) {
	isolate(t)
	if os.Geteuid() == 0 {
		t.Skip("root reads a file of mode 000")
	}
	for _, c := range []struct {
		name string
		make func(dir string) error
		want string
	}{
		{"a directory of mode 000", func(dir string) error { return os.Chmod(filepath.Join(dir, "mcp"), 0) }, filepath.Join(accessKeyPhrase, "mcp") + ": permission denied"},
		{"a link to nothing", func(dir string) error {
			return os.Symlink(filepath.Join(dir, "absent"), filepath.Join(dir, "mcp", "qrk_s.json"))
		}, filepath.Join(accessKeyPhrase, "mcp", runKeyPhrase) + ": no such file or directory"},
		{"a file of mode 000", func(dir string) error { return os.Chmod(filepath.Join(dir, "mcp", "s.json"), 0) }, filepath.Join(accessKeyPhrase, "mcp", "s.json") + ": permission denied"},
	} {
		root := tree(t, map[string]string{"qak_m/mcp/s.json": `{"command": "x"}`})
		dir := filepath.Join(root, "qak_m")
		if err := c.make(dir); err != nil {
			t.Fatal(err)
		}
		_, err := Read("core", dir, nil, "")
		var pathErr *fs.PathError
		if !errors.As(err, &pathErr) {
			t.Errorf("%s: %v, want a *fs.PathError", c.name, err)
		}
		if err == nil || !strings.HasPrefix(err.Error(), "module core: ") || !strings.HasSuffix(err.Error(), c.want) || containsSecret(err.Error()) {
			t.Errorf("%s: %v, want the suffix %q", c.name, err, c.want)
		}
		_ = os.Chmod(filepath.Join(dir, "mcp"), 0o755)
	}
}

// TestAModuleFileQoryCannotReadLeavesOutASecretInItsPath is a module directory whose
// name contains an access key secret, with a hook, a settings fragment or AGENTS.md of
// mode 000: the refusal prints the phrase in place of the directory's name and still
// matches the *fs.PathError under it.
func TestAModuleFileQoryCannotReadLeavesOutASecretInItsPath(t *testing.T) {
	isolate(t)
	if os.Geteuid() == 0 {
		t.Skip("root reads a file of mode 000")
	}
	for _, file := range []string{"hooks/h.sh", "settings/claude/settings.json", "AGENTS.md"} {
		root := tree(t, map[string]string{"qak_m/" + file: "x\n"})
		dir := filepath.Join(root, "qak_m")
		if err := os.Chmod(filepath.Join(dir, file), 0); err != nil {
			t.Fatal(err)
		}
		_, err := Read("core", dir, nil, "")
		var pathErr *fs.PathError
		if !errors.As(err, &pathErr) {
			t.Errorf("%s: %v, want a *fs.PathError", file, err)
		}
		if want := filepath.Join(accessKeyPhrase, file) + ": permission denied"; err == nil || !strings.HasSuffix(err.Error(), want) || containsSecret(err.Error()) {
			t.Errorf("%s: %v, want the suffix %q", file, err, want)
		}
	}
}

// TestATierRefusalLeavesOutASecretInTheModuleAndItsPath is a hook, a settings fragment
// and AGENTS.md that contain a tier placeholder, in a module whose name, or the file's
// path, contains an access key secret or a run key: the refusal prints the phrase in
// place of each.
func TestATierRefusalLeavesOutASecretInTheModuleAndItsPath(t *testing.T) {
	isolate(t)
	tier := ": ${qory:light} is a tier placeholder; a tier goes only in an agent's model, as model: ${qory:light}"
	for _, c := range []struct{ name, file, want string }{
		{"qak_m", "hooks/h.sh", "module " + accessKeyPhrase + ": hooks/h.sh" + tier},
		{"core", "hooks/qrk_h.sh", "module core: hooks/" + runKeyPhrase + tier},
		{"core", "mcp/qak_s.json", "module core: mcp/" + accessKeyPhrase + tier},
		{"core", "files/claude/qak_f.txt", "module core: files/claude/" + accessKeyPhrase + tier},
		{"qrk_m", "settings/claude/settings.json", "module " + runKeyPhrase + ": settings/claude/settings.json" + tier},
		{"core", "settings/qak_rt/qrk_f.json", "module core: settings/" + accessKeyPhrase + "/" + runKeyPhrase + tier},
		{"qak_m", "AGENTS.md", "module " + accessKeyPhrase + ": AGENTS.md" + tier},
	} {
		body := "${qory:light}\n"
		if strings.HasPrefix(c.file, "mcp/") {
			body = `{"command": "${qory:light}"}`
		}
		dir := tree(t, map[string]string{c.file: body})
		if _, err := Read(c.name, dir, nil, ""); err == nil || err.Error() != c.want {
			t.Errorf("%s %s: %v, want %q", c.name, c.file, err, c.want)
		}
	}
}

// TestAnEntryPathWithASecretIsLeftOutOfItsRefusal is each refusal of an entry whose
// path inside the module contains an access key secret or a run key: a variant that
// reads a kind from outside the module, a skill without SKILL.md, a directory under
// hooks, a file directly under files, a directory under a settings runtime, from the
// module root and from a directory a variant sets. The refusal prints the value phrase
// in place of the variant's directory and the name phrase in place of each path
// segment.
func TestAnEntryPathWithASecretIsLeftOutOfItsRefusal(t *testing.T) {
	isolate(t)
	for _, c := range []struct {
		files map[string]string
		v     Variant
		want  string
	}{
		{map[string]string{"AGENTS.md": "x\n"}, Variant{"skills": "../qak_x"}, "variant v reads skills from " + accessKeyValuePhrase + ", which is not a directory inside the module"},
		{map[string]string{"skills/qak_s/x.md": "x\n"}, nil, "skills/" + accessKeyPhrase + " has no SKILL.md"},
		{map[string]string{"qrk_d/s/x.md": "x\n"}, Variant{"skills": "qrk_d"}, runKeyPhrase + "/s has no SKILL.md"},
		{map[string]string{"hooks/qak_d/x.sh": "x\n"}, nil, "hooks/" + accessKeyPhrase + " is a directory; a hook is one file. Keep its helpers elsewhere in the module, reached as $QORY_HARNESS_HOME/modules/core/<path>"},
		{map[string]string{"files/qak_f.txt": "x\n"}, nil, "files/" + accessKeyPhrase + " is not under a runtime directory; a file ships as files/<runtime>/<path>"},
		{map[string]string{"qrk_d/f.txt": "x\n"}, Variant{"files": "qrk_d"}, runKeyPhrase + "/f.txt is not under a runtime directory; a file ships as " + runKeyPhrase + "/<runtime>/<path>"},
		{map[string]string{"settings/qak_rt/qrk_d/x.json": "{}"}, nil, "settings/" + accessKeyPhrase + "/" + runKeyPhrase + " is a directory; settings contains one fragment per target file, and a runtime's other files go under files/" + accessKeyPhrase + "/" + runKeyPhrase},
	} {
		dir := tree(t, c.files)
		var m *Manifest
		variant := ""
		if c.v != nil {
			m, variant = &Manifest{Variants: map[string]Variant{"v": c.v}}, "v"
		}
		if _, err := Read("core", dir, m, variant); err == nil || err.Error() != "module core: "+c.want {
			t.Errorf("%v: %v, want %q", c.files, err, "module core: "+c.want)
		}
	}
}

// TestALinkOutOfTheModuleWithASecretIsLeftOutOfItsRefusal is an entry, a file inside a
// skill and a settings fragment that link out of the module, where the entry's path or
// the link's target contains an access key secret or a run key: the refusal prints the
// name phrase in place of each such path segment.
func TestALinkOutOfTheModuleWithASecretIsLeftOutOfItsRefusal(t *testing.T) {
	isolate(t)
	outside := tree(t, map[string]string{"qrk_out/SKILL.md": "x\n", "qrk_out/f.json": "{}"})
	// The refusal prints the target with every link resolved, as on macOS, where the
	// temporary directory is a link.
	outside, err := filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "qrk_out")
	shownTarget := filepath.Join(outside, runKeyPhrase)
	for _, c := range []struct{ link, to, want string }{
		{"skills/qak_s", target, "skills/" + accessKeyPhrase + " links to " + shownTarget + ", outside the module"},
		{"skills/s/qak_f", filepath.Join(target, "f.json"), "skills/s/" + accessKeyPhrase + " links to " + filepath.Join(shownTarget, "f.json") + ", outside the module"},
		{"settings/qak_rt/f.json", filepath.Join(target, "f.json"), "settings/" + accessKeyPhrase + "/f.json links to " + filepath.Join(shownTarget, "f.json") + ", outside the module"},
	} {
		dir := tree(t, map[string]string{"skills/s/SKILL.md": "x\n"})
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, c.link)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(c.to, filepath.Join(dir, c.link)); err != nil {
			t.Fatal(err)
		}
		_, err := Read("core", dir, nil, "")
		if err == nil || !strings.HasPrefix(err.Error(), "module core: ") || !strings.HasSuffix(err.Error(), c.want) || containsSecret(err.Error()) {
			t.Errorf("%s: %v, want the suffix %q", c.link, err, c.want)
		}
	}
}

// TestAKindDirectoryQoryCannotReadLeavesOutASecretInItsPath is a module directory whose
// name contains an access key secret, with a kind directory, a settings runtime
// directory, or a directory inside a skill or under files of mode 000: the refusal
// prints the phrase in place of the directory's name and still matches the
// *fs.PathError under it.
func TestAKindDirectoryQoryCannotReadLeavesOutASecretInItsPath(t *testing.T) {
	isolate(t)
	if os.Geteuid() == 0 {
		t.Skip("root reads a directory of mode 000")
	}
	for _, c := range []struct{ file, locked string }{
		{"skills/s/SKILL.md", "skills"},
		{"agents/a.md", "agents"},
		{"hooks/h.sh", "hooks"},
		{"files/claude/f.txt", "files"},
		{"files/claude/sub/f.txt", "files/claude/sub"},
		{"skills/s/sub/x.md", "skills/s/sub"},
		{"settings/claude/settings.json", "settings"},
		{"settings/claude/settings.json", "settings/claude"},
	} {
		files := map[string]string{"qak_m/" + c.file: "x\n"}
		if strings.HasPrefix(c.file, "skills/") {
			files["qak_m/skills/s/SKILL.md"] = "x\n"
		}
		root := tree(t, files)
		dir := filepath.Join(root, "qak_m")
		locked := filepath.Join(dir, c.locked)
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		_, err := Read("core", dir, nil, "")
		_ = os.Chmod(locked, 0o755)
		var pathErr *fs.PathError
		if !errors.As(err, &pathErr) {
			t.Errorf("%s: %v, want a *fs.PathError", c.locked, err)
		}
		if want := filepath.Join(accessKeyPhrase, c.locked) + ": permission denied"; err == nil || !strings.HasSuffix(err.Error(), want) || containsSecret(err.Error()) {
			t.Errorf("%s: %v, want the suffix %q", c.locked, err, want)
		}
	}
}

// TestAManifestValueWithASecretIsLeftOutOfItsRefusal is each refusal of a value
// qory-module.yaml writes, an apiVersion, an env path, an egress host and a variant
// that is not a map, when the value contains an access key secret or a run key: the
// refusal prints the value phrase in its place. A plain value prints as before.
func TestAManifestValueWithASecretIsLeftOutOfItsRefusal(t *testing.T) {
	isolate(t)
	head := "apiVersion: qory.dev/v1alpha1\nname: core\n"
	for _, c := range []struct{ body, want string }{
		{"apiVersion: qak_v1\nname: core\n", "apiVersion " + accessKeyValuePhrase + " is not one this qory reads; versions: qory.dev/v1alpha1"},
		{"apiVersion: v1\nname: core\n", `apiVersion "v1" is not one this qory reads; versions: qory.dev/v1alpha1`},
		{head + "env:\n  BIN: /opt/qak_x\n", "env.BIN: " + accessKeyValuePhrase + " is not a path inside the module"},
		{head + "egress: [QRK_x.example]\n", "egress: " + runKeyValuePhrase + " is not a lower-case host name or a *. suffix; no port, path or scheme"},
		{head + "egress: [Git.example]\n", `egress: "Git.example" is not a lower-case host name or a *. suffix; no port, path or scheme`},
		{head + "variants:\n  v: qak_x\n", "variants.v: yaml: unmarshal errors:\n  line 4: cannot unmarshal !!str " + accessKeyValuePhrase + " into module.Variant"},
		{head + "variants:\n  v: qrk_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA\n", "variants.v: yaml: unmarshal errors:\n  line 4: cannot unmarshal !!str " + runKeyValuePhrase + " into module.Variant"},
	} {
		dir := tree(t, map[string]string{ManifestName: c.body})
		_, err := ReadManifest(dir)
		if want := filepath.Join(dir, ManifestName) + ": " + c.want; err == nil || err.Error() != want {
			t.Errorf("%q: %v, want %q", c.body, err, want)
		}
		var typeErr *yaml.TypeError
		if strings.Contains(c.body, "variants") && !errors.As(err, &typeErr) {
			t.Errorf("%q: %v, want a *yaml.TypeError", c.body, err)
		}
	}
}

// TestAReferenceWithASecretIsLeftOutOfItsRefusal is each refusal of a document's
// reference or tier placeholder, and of a document's path, that contains an access key
// secret or a run key: a tier placeholder whose name is not a tier, a reference of no
// kind, a reference whose name is not one path segment, and a document whose path
// contains one. The refusal prints the value phrase in place of the reference or the
// model, and the name phrase in place of the tier, the name or the path segment.
func TestAReferenceWithASecretIsLeftOutOfItsRefusal(t *testing.T) {
	isolate(t)
	for _, c := range []struct{ file, body, want string }{
		{"agents/a.md", "---\nmodel: ${qory:qak_t}\n---\n", "agents/a.md: model " + accessKeyValuePhrase + ": use one of the tiers light, fast and thinking; " + accessKeyPhrase + " is not a tier"},
		{"skills/s/SKILL.md", "${qory:qrk_kind/x}\n", "skills/s/SKILL.md: " + runKeyValuePhrase + " is not a reference; after ${qory: comes one of agents, commands, skills, then /<name>"},
		{"commands/c.md", "${qory:skills/qak_x y}\n", "commands/c.md: " + accessKeyValuePhrase + " refers to " + accessKeyPhrase + ", which is not an entry name; a name is one path segment"},
		{"agents/qrk_a.md", "${qory:prompts/x}\n", "agents/" + runKeyPhrase + ": ${qory:prompts/x} is not a reference; after ${qory: comes one of agents, commands, skills, then /<name>"},
		{"commands/c.md", "${qory:skills/a b}\n", `commands/c.md: ${qory:skills/a b} refers to "a b", which is not an entry name; a name is one path segment`},
	} {
		files := map[string]string{c.file: c.body}
		if strings.HasPrefix(c.file, "skills/") && !strings.HasSuffix(c.file, "SKILL.md") {
			files["skills/s/SKILL.md"] = "x\n"
		}
		dir := tree(t, files)
		if _, err := Read("core", dir, nil, ""); err == nil || err.Error() != "module core: "+c.want {
			t.Errorf("%s: %v, want %q", c.body, err, "module core: "+c.want)
		}
	}
	if err := NotATier("--tier light=x", "QRK_t"); err == nil || err.Error() != "--tier light=x: use one of the tiers light, fast and thinking; "+runKeyPhrase+" is not a tier" {
		t.Errorf("NotATier: %v", err)
	}
}

// TestADocumentWithASecretInItsPathOrFrontmatterIsLeftOutOfItsRefusal is a document
// whose path contains an access key secret, that qory cannot read, whose frontmatter is
// not closed, or whose frontmatter is a value with a run key in it: the refusal prints
// the name phrase in place of the path segment and the value phrase in place of the
// value, and still matches the error under it.
func TestADocumentWithASecretInItsPathOrFrontmatterIsLeftOutOfItsRefusal(t *testing.T) {
	isolate(t)
	path := filepath.Join("/srv", "qak_d", "a.md")
	shown := filepath.Join("/srv", accessKeyPhrase, "a.md")
	if _, err := ParseDocument([]byte("---\nmodel: x\n"), path); err == nil || err.Error() != shown+": frontmatter is not closed" {
		t.Errorf("not closed: %v", err)
	}
	_, err := ParseDocument([]byte("---\nqrk_x\n---\n"), "a.md")
	if want := "a.md: frontmatter: yaml: unmarshal errors:\n  line 1: cannot unmarshal !!str " + runKeyValuePhrase + " into map[string]interface {}"; err == nil || err.Error() != want {
		t.Errorf("a value: %v, want %q", err, want)
	}
	var typeErr *yaml.TypeError
	if !errors.As(err, &typeErr) {
		t.Errorf("a value: %v, want a *yaml.TypeError", err)
	}
	if os.Geteuid() == 0 {
		return
	}
	root := tree(t, map[string]string{"qak_m/agents/a.md": "x\n", "qak_m/skills/s/SKILL.md": "x\n", "qak_m/skills/s/sub/x.md": "x\n"})
	dir := filepath.Join(root, "qak_m")
	for _, c := range []struct {
		name string
		read func() error
		want string
	}{
		{"an agent of mode 000", func() error {
			if err := os.Chmod(filepath.Join(dir, "agents", "a.md"), 0); err != nil {
				t.Fatal(err)
			}
			_, err := Read("core", dir, nil, "")
			return err
		}, filepath.Join(accessKeyPhrase, "agents", "a.md") + ": permission denied"},
		{"ReadDocument", func() error {
			_, err := ReadDocument(filepath.Join(dir, "agents", "a.md"))
			return err
		}, filepath.Join(accessKeyPhrase, "agents", "a.md") + ": permission denied"},
		{"a skill directory of mode 000", func() error {
			if err := os.Chmod(filepath.Join(dir, "skills", "s", "sub"), 0); err != nil {
				t.Fatal(err)
			}
			defer os.Chmod(filepath.Join(dir, "skills", "s", "sub"), 0o755)
			_, err := Documents(Entry{Kind: "skills", Path: filepath.Join(dir, "skills", "s")})
			return err
		}, filepath.Join(accessKeyPhrase, "skills", "s", "sub") + ": permission denied"},
	} {
		err := c.read()
		var pathErr *fs.PathError
		if !errors.As(err, &pathErr) {
			t.Errorf("%s: %v, want a *fs.PathError", c.name, err)
		}
		if err == nil || !strings.HasSuffix(err.Error(), c.want) || containsSecret(err.Error()) {
			t.Errorf("%s: %v, want the suffix %q", c.name, err, c.want)
		}
	}
}
