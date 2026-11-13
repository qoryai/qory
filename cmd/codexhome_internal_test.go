package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

// TestWalledCodexHomeLeavesTheWorkingFolderUntrusted is a walled Codex run in a checkout
// whose .git is a file, as a submodule's is, started in the checkout's folder sub: Codex
// finds no git root for that folder, so the config.toml of the home marks the working
// folder untrusted at its path and at /workspace/sub, beside the checkout and /workspace.
func TestWalledCodexHomeLeavesTheWorkingFolderUntrusted(t *testing.T) {
	base := t.TempDir()
	checkout := filepath.Join(base, "lib")
	if err := os.MkdirAll(filepath.Join(checkout, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, ".git"), []byte("gitdir: ../.git/modules/lib\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "run")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(checkout, "sub")
	if _, err := walledCodexHome(root, filepath.Join(base, "home"), checkout, work, false, codexParts{}); err != nil {
		t.Fatal(err)
	}
	var config struct {
		Projects map[string]struct {
			TrustLevel string `toml:"trust_level"`
		}
	}
	if _, err := toml.DecodeFile(filepath.Join(root, codexFixedName, "config.toml"), &config); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{checkout, workspaceInWall, work, workspaceInWall + "/sub"} {
		if config.Projects[p].TrustLevel != "untrusted" {
			t.Errorf("the trust of %s: %q, want untrusted; projects %v", p, config.Projects[p].TrustLevel, config.Projects)
		}
	}
}
