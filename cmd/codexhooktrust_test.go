package cmd_test

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// TestComposeTrustsTheCodexCommandHooks composes for codex a module with a PreToolUse
// command hook in config.toml and an mcp_tool hook in hooks.json. The home's config.toml
// has features.unified_exec_tty = false and a trusted_hash for the command hook, keyed by
// the config.toml of the home's codex folder. The mcp_tool hook gets no hash and a skipped
// row. --check then finds the home current.
func TestComposeTrustsTheCodexCommandHooks(t *testing.T) {
	root := tierCheckout(t)
	tierModule(t, root, "codex", nil)
	core := filepath.Join(root, "harness", "core", "settings", "codex")
	writeFile(t, filepath.Join(core, "config.toml"), "[[hooks.PreToolUse]]\nmatcher = \"*\"\n[[hooks.PreToolUse.hooks]]\ntype = \"command\"\ncommand = \"echo guard\"\ntimeout = 120\n")
	writeFile(t, filepath.Join(core, "hooks.json"), `{"hooks": {"Stop": [{"hooks": [{"type": "mcp_tool", "server": "files", "tool": "check"}]}]}}`)
	out, err := run(t, "harness", "compose")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got, want := fieldRows(out)["skipped"], []string{
		"hooks.Stop[0].hooks[0] in hooks.json for codex  (qory writes no trusted hash for an mcp_tool hook; Codex skips it in a run without a wall)",
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
	if strings.Count(config, "trusted_hash") != 1 {
		t.Errorf("config.toml has %d trusted hashes, want 1:\n%s", strings.Count(config, "trusted_hash"), config)
	}
	if out, err := run(t, "harness", "compose", "--check"); err != nil {
		t.Errorf("--check: %v, exit %d\n%s", err, cmd.ExitCode(err), out)
	}
}
