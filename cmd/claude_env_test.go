package cmd_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// modelVariables are the variables a compose for claude refuses: Claude Code ranks them
// above the model and effort qory writes, except that an agent file's effort ranks above
// CLAUDE_CODE_SUBAGENT_EFFORT.
var modelVariables = []string{
	"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"ANTHROPIC_DEFAULT_FABLE_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL_FORCE", "CLAUDE_CODE_EFFORT_LEVEL",
	"CLAUDE_CODE_SUBAGENT_EFFORT", "CLAUDE_CODE_WORKFLOW_SUBAGENT_MODEL",
}

// envModule writes the module core with the settings fragment and the manifest lines
// given, and the repository's qory.yaml composing it for the runtime.
func envModule(t *testing.T, root, runtime, fragment, manifest string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "harness", "core", "qory-module.yaml"), "apiVersion: qory.dev/v1alpha1\nname: core\n"+manifest)
	writeFile(t, filepath.Join(root, "harness", "core", "AGENTS.md"), "# core\n")
	if fragment != "" {
		writeFile(t, filepath.Join(root, "harness", "core", "settings", "claude", "settings.json"), fragment)
	}
	writeOwnStack(t, root, "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: "+runtime+"\nmodules:\n  - name: core\n    source: {path: ./harness/core}\n")
}

// TestComposeRefusesAModelVariableFromEverySource sets one of the variables in each place
// that reaches Claude Code: a module's settings fragment and its manifest's export, the
// configuration's env and harness.launch.claude.env, the checkout's settings files and
// the user's Claude Code settings. The compose, and a dry run, refuses with one line per
// variable and writes nothing.
func TestComposeRefusesAModelVariableFromEverySource(t *testing.T) {
	root := tierCheckout(t)
	machineConfig(t, "harness:\n  launch:\n    claude: {env: {ANTHROPIC_DEFAULT_HAIKU_MODEL: h}}\nenv:\n  CLAUDE_CODE_EFFORT_LEVEL: high\n")
	envModule(t, root, "claude", `{"env": {"OTHER": "kept", "ANTHROPIC_MODEL": "m"}}`, "env:\n  CLAUDE_CODE_SUBAGENT_MODEL: .\n")
	writeFile(t, filepath.Join(root, ".claude", "settings.json"), `{"env": {"ANTHROPIC_DEFAULT_SONNET_MODEL": "s"}}`)
	writeFile(t, filepath.Join(root, ".claude", "settings.local.json"), `{"env": {"ANTHROPIC_DEFAULT_OPUS_MODEL": "o"}}`)
	writeFile(t, filepath.Join(os.Getenv("HOME"), ".claude", "settings.json"), `{"env": {"CLAUDE_CODE_SUBAGENT_MODEL_FORCE": "1"}}`)
	want := strings.Join([]string{
		"Remove env.ANTHROPIC_MODEL from settings/claude/settings.json in module core.",
		"Remove env.CLAUDE_CODE_SUBAGENT_MODEL from qory-module.yaml in module core.",
		"Remove CLAUDE_CODE_EFFORT_LEVEL from env in ~/.config/qory/qory.yaml.",
		"Remove env.ANTHROPIC_DEFAULT_HAIKU_MODEL from harness.launch.claude.env in ~/.config/qory/qory.yaml.",
		"Remove env.ANTHROPIC_DEFAULT_SONNET_MODEL from .claude/settings.json in the checkout.",
		"Remove env.ANTHROPIC_DEFAULT_OPUS_MODEL from .claude/settings.local.json in the checkout.",
		"Remove env.CLAUDE_CODE_SUBAGENT_MODEL_FORCE from ~/.claude/settings.json.",
		"Claude Code ranks these variables above the model and effort that qory writes into settings.json and the agent files, except CLAUDE_CODE_SUBAGENT_EFFORT: an agent file's effort ranks above it.",
	}, "\n")
	for _, args := range [][]string{{"harness", "compose", "--dry-run"}, {"harness", "compose"}} {
		out, err := run(t, args...)
		if cmd.ExitCode(err) != 2 || err == nil || err.Error() != want {
			t.Errorf("%v: exit %d, err:\n%v\nwant:\n%s\n%s", args, cmd.ExitCode(err), err, want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".qory")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".qory was written: %v", err)
	}
}

// TestComposeRefusesEachModelVariable refuses every name of modelVariables in a module's
// settings fragment.
func TestComposeRefusesEachModelVariable(t *testing.T) {
	for _, name := range modelVariables {
		root := tierCheckout(t)
		envModule(t, root, "claude", `{"env": {"`+name+`": "x"}}`, "")
		_, err := run(t, "harness", "compose")
		want := "Remove env." + name + " from settings/claude/settings.json in module core.\nClaude Code ranks these variables above the model and effort that qory writes into settings.json and the agent files, except CLAUDE_CODE_SUBAGENT_EFFORT: an agent file's effort ranks above it."
		if cmd.ExitCode(err) != 2 || err == nil || err.Error() != want {
			t.Errorf("%s: exit %d, err %v", name, cmd.ExitCode(err), err)
		}
	}
}

// TestComposeChecksModelVariablesOnlyWhereClaudeReadsThem composes without claude, where
// no source is checked, and into a home outside the checkout, where the checkout's
// settings files and the user's are not checked and a module's fragment still is.
func TestComposeChecksModelVariablesOnlyWhereClaudeReadsThem(t *testing.T) {
	root := tierCheckout(t)
	machineConfig(t, "env:\n  ANTHROPIC_MODEL: m\n")
	envModule(t, root, "codex", `{"env": {"ANTHROPIC_MODEL": "m"}}`, "")
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("codex: %v\n%s", err, out)
	}

	root = tierCheckout(t)
	envModule(t, root, "claude", "", "")
	writeFile(t, filepath.Join(root, ".claude", "settings.local.json"), `{"env": {"ANTHROPIC_MODEL": "o"}}`)
	writeFile(t, filepath.Join(os.Getenv("HOME"), ".claude", "settings.json"), `{"env": {"ANTHROPIC_MODEL": "u"}}`)
	home := t.TempDir()
	if out, err := run(t, "harness", "compose", "--home", home); err != nil {
		t.Fatalf("home outside: %v\n%s", err, out)
	}
	envModule(t, root, "claude", `{"env": {"ANTHROPIC_MODEL": "m"}}`, "")
	_, err := run(t, "harness", "compose", "--home", home)
	if err == nil || !strings.HasPrefix(err.Error(), "Remove env.ANTHROPIC_MODEL from settings/claude/settings.json in module core.\n") {
		t.Errorf("home outside, module fragment: %v", err)
	}
}

// TestComposeReadsTheUsersClaudeSettingsFromClaudeConfigDir checks the settings file
// under CLAUDE_CONFIG_DIR in place of ~/.claude, and prints a row for a settings file that
// is not JSON and goes on.
func TestComposeReadsTheUsersClaudeSettingsFromClaudeConfigDir(t *testing.T) {
	root := tierCheckout(t)
	dir := filepath.Join(os.Getenv("HOME"), "claude-config")
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	envModule(t, root, "claude", "", "")
	writeFile(t, filepath.Join(os.Getenv("HOME"), ".claude", "settings.json"), `{"env": {"ANTHROPIC_MODEL": "ignored"}}`)
	writeFile(t, filepath.Join(dir, "settings.json"), `{"env": {"CLAUDE_CODE_EFFORT_LEVEL": "max"}}`)
	_, err := run(t, "harness", "compose")
	want := "Remove env.CLAUDE_CODE_EFFORT_LEVEL from ~/claude-config/settings.json.\nClaude Code ranks these variables above the model and effort that qory writes into settings.json and the agent files, except CLAUDE_CODE_SUBAGENT_EFFORT: an agent file's effort ranks above it."
	if err == nil || err.Error() != want {
		t.Errorf("err %v\nwant %s", err, want)
	}

	writeFile(t, filepath.Join(dir, "settings.json"), `{}`)
	writeFile(t, filepath.Join(root, ".claude", "settings.local.json"), `{"env": `)
	out, err := run(t, "harness", "compose")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantsRow(t, out, "skipped", ".claude/settings.local.json  (not JSON: unexpected end of JSON input; qory did not check it for model variables)")
}

// TestComposeRefusesAClaudeConfigDirAModuleSets refuses CLAUDE_CONFIG_DIR from a module's
// export and from a settings fragment's env, one line each, with the home inside the
// checkout and outside it.
func TestComposeRefusesAClaudeConfigDirAModuleSets(t *testing.T) {
	const why = " A module does not choose where Claude Code reads its user settings, because qory checks those settings for model variables."
	root := tierCheckout(t)
	envModule(t, root, "claude", `{"env":{"CLAUDE_CONFIG_DIR":"$QORY_HARNESS_HOME/modules/core/cfg"}}`, "env:\n  CLAUDE_CONFIG_DIR: cfg\n")
	writeFile(t, filepath.Join(root, "harness", "core", "cfg", "settings.json"), `{"env":{"ANTHROPIC_MODEL":"x"}}`)
	want := "Remove env.CLAUDE_CONFIG_DIR from settings/claude/settings.json in module core." + why +
		"\nRemove env.CLAUDE_CONFIG_DIR from qory-module.yaml in module core." + why
	for _, args := range [][]string{{"harness", "compose"}, {"harness", "compose", "--home", t.TempDir()}, {"harness", "compose", "--dry-run"}} {
		_, err := run(t, args...)
		if cmd.ExitCode(err) != 2 || err == nil || err.Error() != want {
			t.Errorf("%v: exit %d, err %v\nwant %s", args, cmd.ExitCode(err), err, want)
		}
	}
}

// TestComposeRefusesAClaudeConfigDirItCannotCheck refuses a CLAUDE_CONFIG_DIR from the
// configuration that is relative or inside the harness home, as written, through .., or
// through a link, and names the file and the key that set it.
func TestComposeRefusesAClaudeConfigDirItCannotCheck(t *testing.T) {
	const why = " to an absolute path outside the harness home. qory checks the Claude Code settings there for model variables, and cannot check "
	user := "~/.config/qory/qory.yaml"
	home := filepath.Join(t.TempDir(), "home")
	// link is under the HOME each case's checkout sets, which is a new directory per case.
	const link = "$TEST_LINK"
	for _, c := range []struct{ config, value, where string }{
		{"env:\n  CLAUDE_CONFIG_DIR: cfg\n", "cfg", "env in " + user},
		{"env:\n  CLAUDE_CONFIG_DIR: \"cfg\\e[2J\"\n", `cfg\x1b[2J`, "env in " + user},
		{"env:\n  CLAUDE_CONFIG_DIR: $QORY_HARNESS_HOME/modules/core/../other/cfg\n", "$QORY_HARNESS_HOME/modules/core/../other/cfg", "env in " + user},
		{"env:\n  CLAUDE_CONFIG_DIR: " + home + "/claude\n", home + "/claude", "env in " + user},
		{"env:\n  CLAUDE_CONFIG_DIR: " + link + "/cfg\n", link + "/cfg", "env in " + user},
		{"harness:\n  launch:\n    claude: {env: {CLAUDE_CONFIG_DIR: \"${home}/cfg\"}}\n", "${home}/cfg", "harness.launch.claude.env in " + user},
		{"harness:\n  launch:\n    claude: {env: {CLAUDE_CONFIG_DIR: rel}}\n", "rel", "harness.launch.claude.env in " + user},
	} {
		root := tierCheckout(t)
		if err := os.MkdirAll(home, 0o755); err != nil {
			t.Fatal(err)
		}
		at := filepath.Join(os.Getenv("HOME"), "to-home")
		if err := os.Symlink(home, at); err != nil {
			t.Fatal(err)
		}
		c.config = strings.ReplaceAll(c.config, link, at)
		c.value = strings.ReplaceAll(c.value, link, at)
		machineConfig(t, c.config)
		envModule(t, root, "claude", "", "")
		_, err := run(t, "harness", "compose", "--home", home)
		want := "Set CLAUDE_CONFIG_DIR in " + c.where + why + c.value + "."
		if cmd.ExitCode(err) != 2 || err == nil || err.Error() != want {
			t.Errorf("%q: exit %d, err %v\nwant %s", c.config, cmd.ExitCode(err), err, want)
		}
	}
}

// TestComposeRefusesAClaudeConfigDirThatDoesNotResolve refuses a CLAUDE_CONFIG_DIR from the
// configuration that does not resolve whole on a first compose: a path through the
// checkout's .claude, which the compose then links into the home; a link whose target in
// the home is not written yet; and a path that does not exist. On a later compose, a path
// through the checkout's .claude links is refused too, both to a skill the home writes as
// a directory, as it does for a skill whose SKILL.md has a reference, and to one the
// home links to the module's directory.
func TestComposeRefusesAClaudeConfigDirThatDoesNotResolve(t *testing.T) {
	const why = " to an absolute path outside the harness home. qory checks the Claude Code settings there for model variables, and cannot check "
	skill := func(root string) {
		writeFile(t, filepath.Join(root, "harness", "core", "skills", "foo", "SKILL.md"), "---\nname: foo\ndescription: Foo.\n---\nFoo.\n")
		writeFile(t, filepath.Join(root, "harness", "core", "skills", "foo", "settings.json"), `{"env":{"ANTHROPIC_MODEL":"x"}}`)
		writeFile(t, filepath.Join(root, "harness", "core", "skills", "bar", "SKILL.md"), "---\nname: bar\ndescription: Bar.\n---\nRun ${qory:skills/foo}.\n")
		writeFile(t, filepath.Join(root, "harness", "core", "skills", "bar", "settings.json"), `{"env":{"ANTHROPIC_MODEL":"x"}}`)
	}

	root := tierCheckout(t)
	envModule(t, root, "claude", "", "")
	skill(root)
	value := "$QORY_HARNESS_HOME/../../.claude/skills/foo"
	configure(t, root, nil, []string{"env:", "  CLAUDE_CONFIG_DIR: " + value})
	if _, err := run(t, "harness", "compose"); err == nil || err.Error() != "Set CLAUDE_CONFIG_DIR in env in qory.yaml"+why+value+"." {
		t.Errorf("through .claude, first compose: err %v", err)
	}

	root = tierCheckout(t)
	envModule(t, root, "claude", "", "")
	skill(root)
	dang := filepath.Join(os.Getenv("HOME"), "dang")
	if err := os.Symlink(filepath.Join(root, ".qory", "harness", "claude", "skills", "foo"), dang); err != nil {
		t.Fatal(err)
	}
	machineConfig(t, "env:\n  CLAUDE_CONFIG_DIR: "+dang+"\n")
	if _, err := run(t, "harness", "compose"); err == nil || err.Error() != "Set CLAUDE_CONFIG_DIR in env in ~/.config/qory/qory.yaml"+why+dang+"." {
		t.Errorf("link into the home not written yet: err %v", err)
	}

	nowhere := filepath.Join(os.Getenv("HOME"), "nowhere")
	machineConfig(t, "env:\n  CLAUDE_CONFIG_DIR: "+nowhere+"\n")
	if _, err := run(t, "harness", "compose"); err == nil || err.Error() != "Set CLAUDE_CONFIG_DIR in env in ~/.config/qory/qory.yaml"+why+nowhere+"." {
		t.Errorf("a path that does not exist: err %v", err)
	}

	machineConfig(t, "")
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if fi, err := os.Lstat(filepath.Join(root, ".qory", "harness", "claude", "skills", "bar")); err != nil || !fi.IsDir() {
		t.Fatalf("skills/bar in the home is not a directory: %v, %v", fi, err)
	}
	through := filepath.Join(root, ".claude", "skills", "bar")
	machineConfig(t, "env:\n  CLAUDE_CONFIG_DIR: "+through+"\n")
	if _, err := run(t, "harness", "compose"); err == nil || err.Error() != "Set CLAUDE_CONFIG_DIR in env in ~/.config/qory/qory.yaml"+why+through+"." {
		t.Errorf("through .claude into the home, second compose: err %v", err)
	}
	through = filepath.Join(root, ".claude", "skills", "foo")
	machineConfig(t, "env:\n  CLAUDE_CONFIG_DIR: "+through+"\n")
	if _, err := run(t, "harness", "compose"); err == nil || err.Error() != "Set CLAUDE_CONFIG_DIR in env in ~/.config/qory/qory.yaml"+why+through+"." {
		t.Errorf("through .claude to the module, second compose: err %v", err)
	}
}

// TestComposeRefusesAClaudeConfigDirAtACheckoutLink refuses a CLAUDE_CONFIG_DIR at or under
// a checkout path a runtime links into the home, such as .claude/skills, even while a
// tracked directory stands there that --force replaces with the link.
func TestComposeRefusesAClaudeConfigDirAtACheckoutLink(t *testing.T) {
	root := tierCheckout(t)
	envModule(t, root, "claude", "", "")
	writeFile(t, filepath.Join(root, "harness", "core", "skills", "foo", "SKILL.md"), "---\nname: foo\ndescription: Foo.\n---\nFoo.\n")
	writeFile(t, filepath.Join(root, "harness", "core", "skills", "foo", "settings.json"), `{"env":{"ANTHROPIC_MODEL":"x"}}`)
	writeFile(t, filepath.Join(root, ".claude", "skills", "foo", "settings.json"), `{}`)
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "tracked skills")
	value := filepath.Join(root, ".claude", "skills", "foo")
	machineConfig(t, "env:\n  CLAUDE_CONFIG_DIR: "+value+"\n")
	_, err := run(t, "harness", "compose", "--force")
	want := "Set CLAUDE_CONFIG_DIR in env in ~/.config/qory/qory.yaml to an absolute path outside the harness home. qory checks the Claude Code settings there for model variables, and cannot check " + value + "."
	if cmd.ExitCode(err) != 2 || err == nil || err.Error() != want {
		t.Errorf("exit %d, err %v\nwant %s", cmd.ExitCode(err), err, want)
	}
	if fi, err := os.Lstat(filepath.Join(root, ".claude", "skills")); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf(".claude/skills was replaced: %v, %v", fi, err)
	}
}

// TestComposeRefusesAClaudeConfigDirAtAModuleLink refuses a CLAUDE_CONFIG_DIR under the
// checkout-root name a stack links a module's directory as, while a tracked directory
// that --force would replace with the link stands there.
func TestComposeRefusesAClaudeConfigDirAtAModuleLink(t *testing.T) {
	root := tierCheckout(t)
	writeFile(t, filepath.Join(root, "harness", "core", "qory-module.yaml"), "apiVersion: qory.dev/v1alpha1\nname: core\n")
	writeFile(t, filepath.Join(root, "harness", "core", "AGENTS.md"), "# core\n")
	writeFile(t, filepath.Join(root, "harness", "core", "cfg", "settings.json"), `{"env":{"ANTHROPIC_MODEL":"x"}}`)
	writeOwnStack(t, root, "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n    source: {path: ./harness/core}\n    link: core-dir\n")
	writeFile(t, filepath.Join(root, "core-dir", "cfg", "settings.json"), `{}`)
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "tracked core-dir")
	value := filepath.Join(root, "core-dir", "cfg")
	machineConfig(t, "env:\n  CLAUDE_CONFIG_DIR: "+value+"\n")
	_, err := run(t, "harness", "compose", "--force")
	want := "Set CLAUDE_CONFIG_DIR in env in ~/.config/qory/qory.yaml to an absolute path outside the harness home. qory checks the Claude Code settings there for model variables, and cannot check " + value + "."
	if cmd.ExitCode(err) != 2 || err == nil || err.Error() != want {
		t.Errorf("exit %d, err %v\nwant %s", cmd.ExitCode(err), err, want)
	}
	if fi, err := os.Lstat(filepath.Join(root, "core-dir")); err != nil || !fi.IsDir() {
		t.Errorf("core-dir was replaced: %v, %v", fi, err)
	}
}

// TestComposeRefusesAClaudeConfigDirWhoseSettingsLinkIntoTheHome refuses a
// CLAUDE_CONFIG_DIR whose settings.json is a link into a path the compose writes: a
// tracked file under .claude/skills, which --force replaces with a link, and a file in
// the home, which the compose rewrites when the stack takes the skill from another module.
func TestComposeRefusesAClaudeConfigDirWhoseSettingsLinkIntoTheHome(t *testing.T) {
	const why = " to an absolute path outside the harness home. qory checks the Claude Code settings there for model variables, and cannot check "
	skill := func(root, module, settings string) {
		writeFile(t, filepath.Join(root, "harness", module, "qory-module.yaml"), "apiVersion: qory.dev/v1alpha1\nname: "+module+"\n")
		writeFile(t, filepath.Join(root, "harness", module, "skills", "foo", "SKILL.md"), "---\nname: foo\ndescription: Foo.\n---\nFoo.\n")
		writeFile(t, filepath.Join(root, "harness", module, "skills", "foo", "settings.json"), settings)
	}
	stack := func(root, module string) {
		writeOwnStack(t, root, "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: "+module+"\n    source: {path: ./harness/"+module+"}\n")
	}
	link := func(dir, target string) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, "settings.json")); err != nil {
			t.Fatal(err)
		}
	}

	root := tierCheckout(t)
	skill(root, "core", `{"env":{"ANTHROPIC_MODEL":"x"}}`)
	stack(root, "core")
	writeFile(t, filepath.Join(root, ".claude", "skills", "foo", "settings.json"), `{}`)
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "tracked skills")
	dir := filepath.Join(os.Getenv("HOME"), "cfgQ4")
	link(dir, filepath.Join(root, ".claude", "skills", "foo", "settings.json"))
	machineConfig(t, "env:\n  CLAUDE_CONFIG_DIR: "+dir+"\n")
	if _, err := run(t, "harness", "compose", "--force"); cmd.ExitCode(err) != 2 || err == nil || err.Error() != "Set CLAUDE_CONFIG_DIR in env in ~/.config/qory/qory.yaml"+why+dir+"." {
		t.Errorf("into .claude/skills: exit %d, err %v", cmd.ExitCode(err), err)
	}
	if fi, err := os.Lstat(filepath.Join(root, ".claude", "skills")); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf(".claude/skills was replaced: %v, %v", fi, err)
	}

	root = tierCheckout(t)
	skill(root, "m1", `{}`)
	skill(root, "m2", `{"env":{"ANTHROPIC_MODEL":"x"}}`)
	stack(root, "m1")
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	dir = filepath.Join(os.Getenv("HOME"), "cfgQ7")
	link(dir, filepath.Join(root, ".qory", "harness", "claude", "skills", "foo", "settings.json"))
	machineConfig(t, "env:\n  CLAUDE_CONFIG_DIR: "+dir+"\n")
	stack(root, "m2")
	if _, err := run(t, "harness", "compose"); cmd.ExitCode(err) != 2 || err == nil || err.Error() != "Set CLAUDE_CONFIG_DIR in env in ~/.config/qory/qory.yaml"+why+dir+"." {
		t.Errorf("into the home: exit %d, err %v", cmd.ExitCode(err), err)
	}
}

// TestComposeRefusesASettingsFileThatLinksWhereTheComposeWrites refuses the checkout's
// .claude/settings.local.json and the user's settings file when each is a link into a path
// the compose writes: .claude/skills/foo, under claude's link path .claude, where the
// repository's force: true replaces .claude/skills, and the home. qory's own .claude/settings.json link passes a second compose.
func TestComposeRefusesASettingsFileThatLinksWhereTheComposeWrites(t *testing.T) {
	const why = ", with a file. qory checks that file for model variables before the compose writes where the link points."
	root := tierCheckout(t)
	envModule(t, root, "claude", "", "")
	writeFile(t, filepath.Join(root, "harness", "core", "skills", "foo", "SKILL.md"), "---\nname: foo\ndescription: Foo.\n---\nFoo.\n")
	writeFile(t, filepath.Join(root, "harness", "core", "skills", "foo", "settings.json"), `{"env":{"ANTHROPIC_MODEL":"x"}}`)
	configure(t, root, []string{"force: true"}, nil)
	writeFile(t, filepath.Join(root, ".claude", "skills", "foo", "settings.json"), `{}`)
	if err := os.Symlink(filepath.Join("skills", "foo", "settings.json"), filepath.Join(root, ".claude", "settings.local.json")); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "tracked settings link")
	_, err := run(t, "harness", "compose")
	// claude's link path is .claude, whose entries the compose links one by one.
	if want := "Replace .claude/settings.local.json, a link into .claude" + why; cmd.ExitCode(err) != 2 || err == nil || err.Error() != want {
		t.Errorf("q5: exit %d, err %v\nwant %s", cmd.ExitCode(err), err, want)
	}
	if fi, err := os.Lstat(filepath.Join(root, ".claude", "skills")); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf(".claude/skills was replaced: %v, %v", fi, err)
	}

	root = tierCheckout(t)
	envModule(t, root, "claude", "", "")
	user := filepath.Join(os.Getenv("HOME"), ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(user), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, ".qory", "harness", "claude", "settings.json"), user); err != nil {
		t.Fatal(err)
	}
	_, err = run(t, "harness", "compose")
	if want := "Replace ~/.claude/settings.json, a link into .qory/harness" + why; cmd.ExitCode(err) != 2 || err == nil || err.Error() != want {
		t.Errorf("user file: exit %d, err %v\nwant %s", cmd.ExitCode(err), err, want)
	}

	if err := os.Remove(user); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if out, err := run(t, "harness", "compose"); err != nil {
			t.Fatalf("compose %d: %v\n%s", i+1, err, out)
		}
	}
	if target, err := os.Readlink(filepath.Join(root, ".claude", "settings.json")); err != nil || !strings.Contains(target, "harness/claude/settings.json") {
		t.Errorf(".claude/settings.json is not qory's link: %q, %v", target, err)
	}
}

// TestComposeFollowsARelativeSettingsLinkFromItsRealFolder follows a settings.json link
// with a relative target from the folder the link really is in, when that folder is
// reached through another link, as the system does: the target is the checkout's tracked
// .claude/skills/foo/settings.json, under claude's link path .claude. It is refused for a
// CLAUDE_CONFIG_DIR and for the user's ~/.claude.
func TestComposeFollowsARelativeSettingsLinkFromItsRealFolder(t *testing.T) {
	// tracked makes a checkout whose repository tracks .claude/skills/foo/settings.json.
	tracked := func() string {
		root := tierCheckout(t)
		envModule(t, root, "claude", "", "")
		writeFile(t, filepath.Join(root, "harness", "core", "skills", "foo", "SKILL.md"), "---\nname: foo\ndescription: Foo.\n---\nFoo.\n")
		writeFile(t, filepath.Join(root, "harness", "core", "skills", "foo", "settings.json"), `{"env":{"ANTHROPIC_MODEL":"x"}}`)
		writeFile(t, filepath.Join(root, ".claude", "skills", "foo", "settings.json"), `{}`)
		runGit(t, root, "add", "-A")
		runGit(t, root, "commit", "-q", "-m", "tracked skills")
		return root
	}
	// deep makes the real folder dir, reached through the link at, with a settings.json
	// that links by a relative path to the root's tracked file.
	deep := func(root, dir, at string) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		realDir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			t.Fatal(err)
		}
		rel, err := filepath.Rel(realDir, filepath.Join(realRoot, ".claude", "skills", "foo", "settings.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(rel, filepath.Join(dir, "settings.json")); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(dir, at); err != nil {
			t.Fatal(err)
		}
	}

	root := tracked()
	value := filepath.Join(os.Getenv("HOME"), "n1", "vlink")
	deep(root, filepath.Join(os.Getenv("HOME"), "n1", "b", "c", "realdir"), value)
	machineConfig(t, "env:\n  CLAUDE_CONFIG_DIR: "+value+"\n")
	_, err := run(t, "harness", "compose", "--force")
	if want := "Set CLAUDE_CONFIG_DIR in env in ~/.config/qory/qory.yaml to an absolute path outside the harness home. qory checks the Claude Code settings there for model variables, and cannot check " + value + "."; cmd.ExitCode(err) != 2 || err == nil || err.Error() != want {
		t.Errorf("CLAUDE_CONFIG_DIR: exit %d, err %v\nwant %s", cmd.ExitCode(err), err, want)
	}

	root = tracked()
	deep(root, filepath.Join(os.Getenv("HOME"), "dot", "x", "y", "dotclaude"), filepath.Join(os.Getenv("HOME"), ".claude"))
	_, err = run(t, "harness", "compose", "--force")
	if want := "Replace ~/.claude/settings.json, a link into .claude, with a file. qory checks that file for model variables before the compose writes where the link points."; cmd.ExitCode(err) != 2 || err == nil || err.Error() != want {
		t.Errorf("~/.claude: exit %d, err %v\nwant %s", cmd.ExitCode(err), err, want)
	}
	if fi, err := os.Lstat(filepath.Join(root, ".claude", "skills")); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf(".claude/skills was replaced: %v, %v", fi, err)
	}
}

// TestComposeChecksAFolderLinkInTheMiddleOfAChain refuses a settings file whose chain of
// links passes through a folder link into the checkout's .claude, although the file at
// the end of the chain is outside it: a CLAUDE_CONFIG_DIR, the checkout's
// .claude/settings.local.json with only committed files, and the user's settings file.
func TestComposeChecksAFolderLinkInTheMiddleOfAChain(t *testing.T) {
	const replace = ", with a file. qory checks that file for model variables before the compose writes where the link points."
	symlink := func(target, at string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, at); err != nil {
			t.Fatal(err)
		}
	}
	// tracked makes a checkout whose repository commits .claude/skills/foo/settings.json as
	// a link to outside.json at the root, and the files given, path to content or, for a
	// path ending in @, link target.
	tracked := func(files map[string]string) string {
		root := tierCheckout(t)
		envModule(t, root, "claude", "", "")
		writeFile(t, filepath.Join(root, "harness", "core", "skills", "foo", "SKILL.md"), "---\nname: foo\ndescription: Foo.\n---\nFoo.\n")
		writeFile(t, filepath.Join(root, "harness", "core", "skills", "foo", "settings.json"), `{"env":{"ANTHROPIC_MODEL":"x"}}`)
		writeFile(t, filepath.Join(root, "outside.json"), `{}`)
		symlink(filepath.Join("..", "..", "..", "outside.json"), filepath.Join(root, ".claude", "skills", "foo", "settings.json"))
		for path, content := range files {
			if at, ok := strings.CutSuffix(path, "@"); ok {
				symlink(content, filepath.Join(root, at))
				continue
			}
			writeFile(t, filepath.Join(root, path), content)
		}
		runGit(t, root, "add", "-A")
		runGit(t, root, "commit", "-q", "-m", "tracked")
		return root
	}
	unreplaced := func(root string) {
		t.Helper()
		if fi, err := os.Lstat(filepath.Join(root, ".claude", "skills")); err != nil || fi.Mode()&os.ModeSymlink != 0 {
			t.Errorf(".claude/skills was replaced: %v, %v", fi, err)
		}
	}

	root := tracked(nil)
	home := os.Getenv("HOME")
	symlink(filepath.Join(root, ".claude", "skills", "foo"), filepath.Join(home, "d4", "a"))
	symlink(filepath.Join(home, "d4", "a", "settings.json"), filepath.Join(home, "d4", "cfg", "settings.json"))
	value := filepath.Join(home, "d4", "cfg")
	machineConfig(t, "env:\n  CLAUDE_CONFIG_DIR: "+value+"\n")
	_, err := run(t, "harness", "compose", "--force")
	if want := "Set CLAUDE_CONFIG_DIR in env in ~/.config/qory/qory.yaml to an absolute path outside the harness home. qory checks the Claude Code settings there for model variables, and cannot check " + value + "."; cmd.ExitCode(err) != 2 || err == nil || err.Error() != want {
		t.Errorf("CLAUDE_CONFIG_DIR: exit %d, err %v\nwant %s", cmd.ExitCode(err), err, want)
	}
	unreplaced(root)

	root = tracked(map[string]string{
		"hop/a@":                       filepath.Join("..", ".claude", "skills", "foo"),
		".claude/settings.local.json@": filepath.Join("..", "hop", "a", "settings.json"),
	})
	configure(t, root, []string{"force: true"}, nil)
	_, err = run(t, "harness", "compose")
	if want := "Replace .claude/settings.local.json, a link into .claude" + replace; cmd.ExitCode(err) != 2 || err == nil || err.Error() != want {
		t.Errorf("committed files: exit %d, err %v\nwant %s", cmd.ExitCode(err), err, want)
	}
	unreplaced(root)

	root = tracked(nil)
	home = os.Getenv("HOME")
	symlink(filepath.Join(root, ".claude", "skills", "foo"), filepath.Join(home, "mid", "a"))
	symlink(filepath.Join(home, "mid", "a", "settings.json"), filepath.Join(home, ".claude", "settings.json"))
	_, err = run(t, "harness", "compose", "--force")
	if want := "Replace ~/.claude/settings.json, a link into .claude" + replace; cmd.ExitCode(err) != 2 || err == nil || err.Error() != want {
		t.Errorf("user file: exit %d, err %v\nwant %s", cmd.ExitCode(err), err, want)
	}
	unreplaced(root)
}

// TestComposeStopsOnALinkLoop refuses a CLAUDE_CONFIG_DIR that is a loop of links and
// stops on a checkout settings file that is one, without hanging.
func TestComposeStopsOnALinkLoop(t *testing.T) {
	root := tierCheckout(t)
	envModule(t, root, "claude", "", "")
	loop := filepath.Join(os.Getenv("HOME"), "loop")
	if err := os.MkdirAll(loop, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, l := range [][2]string{{"b", "a"}, {"a", "b"}} {
		if err := os.Symlink(filepath.Join(loop, l[0]), filepath.Join(loop, l[1])); err != nil {
			t.Fatal(err)
		}
	}
	value := filepath.Join(loop, "a")
	machineConfig(t, "env:\n  CLAUDE_CONFIG_DIR: "+value+"\n")
	_, err := run(t, "harness", "compose")
	if want := "Set CLAUDE_CONFIG_DIR in env in ~/.config/qory/qory.yaml to an absolute path outside the harness home. qory checks the Claude Code settings there for model variables, and cannot check " + value + "."; err == nil || err.Error() != want {
		t.Errorf("CLAUDE_CONFIG_DIR: err %v\nwant %s", err, want)
	}

	machineConfig(t, "")
	local := filepath.Join(root, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(value, local); err != nil {
		t.Fatal(err)
	}
	_, err = run(t, "harness", "compose")
	if want := "open " + local + ": too many levels of symbolic links"; err == nil || err.Error() != want {
		t.Errorf(".claude/settings.local.json: err %v\nwant %s", err, want)
	}
}

// TestComposeStopsOnASettingsLinkThatDoesNotResolve stops the compose with the read error
// for a settings.json that is a link to a path not written yet, in the CLAUDE_CONFIG_DIR
// the configuration sets and in the checkout.
func TestComposeStopsOnASettingsLinkThatDoesNotResolve(t *testing.T) {
	root := tierCheckout(t)
	envModule(t, root, "claude", "", "")
	dir := filepath.Join(os.Getenv("HOME"), "cfgA")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, ".qory", "harness", "claude", "skills", "foo", "settings.json"), filepath.Join(dir, "settings.json")); err != nil {
		t.Fatal(err)
	}
	machineConfig(t, "env:\n  CLAUDE_CONFIG_DIR: "+dir+"\n")
	_, err := run(t, "harness", "compose", "--home", t.TempDir())
	if want := "open " + filepath.Join(dir, "settings.json") + ": no such file or directory"; err == nil || err.Error() != want {
		t.Errorf("CLAUDE_CONFIG_DIR: err %v\nwant %s", err, want)
	}

	root = tierCheckout(t)
	envModule(t, root, "claude", "", "")
	local := filepath.Join(root, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, ".qory", "later.json"), local); err != nil {
		t.Fatal(err)
	}
	_, err = run(t, "harness", "compose")
	if want := "open " + local + ": no such file or directory"; err == nil || err.Error() != want {
		t.Errorf(".claude/settings.local.json: err %v\nwant %s", err, want)
	}
}

// TestComposeChecksTheSettingsOfTheClaudeConfigDirTheLaunchSets sets CLAUDE_CONFIG_DIR to
// an absolute directory outside the home, through the configuration's env and through
// harness.launch.claude.env: the compose refuses a model variable in its settings.json
// and passes when it has none.
func TestComposeChecksTheSettingsOfTheClaudeConfigDirTheLaunchSets(t *testing.T) {
	const why = "\nClaude Code ranks these variables above the model and effort that qory writes into settings.json and the agent files, except CLAUDE_CODE_SUBAGENT_EFFORT: an agent file's effort ranks above it."
	root := tierCheckout(t)
	launchDir := filepath.Join(os.Getenv("HOME"), "claude-from-launch")
	writeFile(t, filepath.Join(launchDir, "settings.json"), `{"env":{"ANTHROPIC_DEFAULT_HAIKU_MODEL":"h"}}`)
	machineConfig(t, "harness:\n  launch:\n    claude: {env: {CLAUDE_CONFIG_DIR: "+launchDir+"}}\n")
	envModule(t, root, "claude", "", "")
	if _, err := run(t, "harness", "compose", "--home", t.TempDir()); err == nil || err.Error() != "Remove env.ANTHROPIC_DEFAULT_HAIKU_MODEL from ~/claude-from-launch/settings.json."+why {
		t.Errorf("harness.launch.claude.env: err %v", err)
	}

	root = tierCheckout(t)
	dir := filepath.Join(os.Getenv("HOME"), "claude-launch")
	writeFile(t, filepath.Join(dir, "settings.json"), `{"env":{"CLAUDE_CODE_EFFORT_LEVEL":"max"}}`)
	machineConfig(t, "env:\n  CLAUDE_CONFIG_DIR: "+dir+"\n")
	envModule(t, root, "claude", "", "")
	for _, args := range [][]string{{"harness", "compose"}, {"harness", "compose", "--home", t.TempDir()}} {
		_, err := run(t, args...)
		if want := "Remove env.CLAUDE_CODE_EFFORT_LEVEL from ~/claude-launch/settings.json." + why; cmd.ExitCode(err) != 2 || err == nil || err.Error() != want {
			t.Errorf("%v, configuration env: exit %d, err %v\nwant %s", args, cmd.ExitCode(err), err, want)
		}
	}
	writeFile(t, filepath.Join(dir, "settings.json"), `{"env":{"OTHER":"kept"}}`)
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Errorf("clean settings: %v\n%s", err, out)
	}
}

// TestComposeStopsOnASettingsFileItCannotRead stops the compose with the read error for a
// settings file qory cannot read, where a file that is not JSON gets a row.
func TestComposeStopsOnASettingsFileItCannotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file of mode 000")
	}
	root := tierCheckout(t)
	envModule(t, root, "claude", "", "")
	path := filepath.Join(root, ".claude", "settings.local.json")
	writeFile(t, path, `{}`)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	out, err := run(t, "harness", "compose")
	if want := "open " + path + ": permission denied"; err == nil || err.Error() != want {
		t.Errorf("err %v\nwant %s\n%s", err, want, out)
	}
	wantsNoRow(t, out, "skipped")
}
