package cmd_test

import (
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// topWebSearchOff matches web_search = "disabled" on a line of its own with no indent,
// where the TOML encoder puts a key of the top level, before the first table.
var topWebSearchOff = regexp.MustCompile(`(?m)\A[^\[]*^web_search = "disabled"$`)

// TestComposeTrustsTheCodexCommandHooks composes for codex a module with a PreToolUse
// command hook in config.toml, and an mcp_tool hook and a Stop command hook with U+2028
// in hooks.json. The home's config.toml has web_search = "disabled" at the top level,
// features.unified_exec_tty = false and a trusted_hash for the PreToolUse hook, keyed by
// the config.toml of the home's codex folder. The other two hooks get no hash and a
// skipped row each. --check then finds the home current.
func TestComposeTrustsTheCodexCommandHooks(t *testing.T) {
	root := tierCheckout(t)
	tierModule(t, root, "codex", nil)
	core := filepath.Join(root, "harness", "core", "settings", "codex")
	writeFile(t, filepath.Join(core, "config.toml"), "[[hooks.PreToolUse]]\nmatcher = \"*\"\n[[hooks.PreToolUse.hooks]]\ntype = \"command\"\ncommand = \"echo guard\"\ntimeout = 120\n")
	writeFile(t, filepath.Join(core, "hooks.json"), `{"hooks": {"Stop": [{"hooks": [{"type": "mcp_tool", "server": "files", "tool": "check"}, {"type": "command", "command": "echo \u2028"}]}]}}`)
	out, err := run(t, "harness", "compose")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got, want := fieldRows(out)["skipped"], []string{
		"hooks.Stop[0].hooks[0] in hooks.json for codex  (qory writes no trusted hash for an mcp_tool hook; Codex skips it in a run without a wall)",
		"hooks.Stop[0].hooks[1] in hooks.json for codex  (qory writes no trusted hash for a hook with U+2028 or U+2029; Codex skips it in a run without a wall)",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("skipped rows %q\nwant %q\n%s", got, want, out)
	}
	codexDir, err := filepath.EvalSymlinks(filepath.Join(root, ".qory", "harness", "codex"))
	if err != nil {
		t.Fatal(err)
	}
	config := readText(t, filepath.Join(codexDir, "config.toml"))
	for _, want := range []string{
		"[features]\n  unified_exec_tty = false\n",
		"[hooks.state.\"" + filepath.Join(codexDir, "config.toml") + ":pre_tool_use:0:0\"]\n      trusted_hash = \"sha256:",
	} {
		if !strings.Contains(config, want) {
			t.Errorf("config.toml lacks %q:\n%s", want, config)
		}
	}
	if !topWebSearchOff.MatchString(config) {
		t.Errorf("config.toml lacks web_search = \"disabled\" at the top level:\n%s", config)
	}
	if strings.Count(config, "trusted_hash") != 1 {
		t.Errorf("config.toml has %d trusted hashes, want 1:\n%s", strings.Count(config, "trusted_hash"), config)
	}
	if out, err := run(t, "harness", "compose", "--check"); err != nil {
		t.Errorf("--check: %v, exit %d\n%s", err, cmd.ExitCode(err), out)
	}
}
