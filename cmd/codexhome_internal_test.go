package cmd

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	if _, err := walledCodexHome(root, filepath.Join(base, "home"), checkout, work, nil, false, codexParts{}); err != nil {
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

// TestStartDirsInWall is each kind of -C or --cd value of a walled Codex run that works
// in the checkout co, at its folder sub: each value gets its path in the wall at the
// checkout's path and at /workspace, a relative value resolved against the working
// folder. Several values all get entries, and a value that escapes the checkout gets
// the path it resolves to at each. A value through a link in the checkout gets its path
// as given and its path with the link resolved, the link before a .. that follows it,
// also for a link that leads out of the checkout or names an absolute path.
func TestStartDirsInWall(t *testing.T) {
	base := t.TempDir()
	co := filepath.Join(base, "co")
	for _, d := range []string{"sub/a", "sub/b", "pkg/inner", "outside"} {
		if err := os.MkdirAll(filepath.Join(co, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		"ln":  filepath.Join("pkg", "inner"),
		"out": filepath.Join("..", "..", "elsewhere"),
		"abs": filepath.Join(co, "pkg"),
	} {
		if err := os.Symlink(target, filepath.Join(co, link)); err != nil {
			t.Fatal(err)
		}
	}
	work := filepath.Join(co, "sub")
	for _, c := range []struct {
		name string
		dirs []string
		want []string
	}{
		{"several values", []string{"a", "b"}, []string{
			filepath.Join(co, "sub/a"), filepath.Join(co, "sub/b"), "/workspace/sub/a", "/workspace/sub/b",
		}},
		{"a relative value", []string{"../pkg"}, []string{filepath.Join(co, "pkg"), "/workspace/pkg"}},
		{"an absolute value in the checkout", []string{filepath.Join(co, "pkg")}, []string{filepath.Join(co, "pkg"), "/workspace/pkg"}},
		{"a value that escapes the checkout", []string{"../.."}, []string{"/", base}},
		{"a link", []string{"../ln/deep"}, []string{
			filepath.Join(co, "ln/deep"), filepath.Join(co, "pkg/inner/deep"), "/workspace/ln/deep", "/workspace/pkg/inner/deep",
		}},
		{"a link before ..", []string{"../ln/.."}, []string{co, filepath.Join(co, "pkg"), "/workspace", "/workspace/pkg"}},
		{"a link out of the checkout", []string{"../out"}, []string{
			"/elsewhere", filepath.Join(base, "../elsewhere"), filepath.Join(co, "out"), "/workspace/out",
		}},
		{"a link to an absolute path", []string{"../abs"}, []string{
			filepath.Join(co, "abs"), filepath.Join(co, "pkg"), "/workspace/abs", "/workspace/pkg",
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			want := slices.Clone(c.want)
			slices.Sort(want)
			if got := startDirsInWall(co, work, c.dirs); !slices.Equal(got, slices.Compact(want)) {
				t.Errorf("startDirsInWall(%q) = %q, want %q", c.dirs, got, want)
			}
		})
	}
}

// TestWalledCodexHomeLeavesStartDirsUntrusted is a walled Codex run whose arguments name
// two folders with -C and --cd=: the config.toml of the home marks each one untrusted at
// its path and at /workspace, and a folder named after -- gets no entry.
func TestWalledCodexHomeLeavesStartDirsUntrusted(t *testing.T) {
	base := t.TempDir()
	checkout := filepath.Join(base, "co")
	if err := os.MkdirAll(filepath.Join(checkout, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "run")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	args := []string{"exec", "-C", "a", "--cd=../other", "--", "-C", "never", "$explore x"}
	if _, err := walledCodexHome(root, filepath.Join(base, "home"), checkout, checkout, args, false, codexParts{}); err != nil {
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
	for _, p := range []string{filepath.Join(checkout, "a"), "/workspace/a", filepath.Join(base, "other"), "/other"} {
		if config.Projects[p].TrustLevel != "untrusted" {
			t.Errorf("the trust of %s: %q, want untrusted; projects %v", p, config.Projects[p].TrustLevel, config.Projects)
		}
	}
	for p := range config.Projects {
		if strings.Contains(p, "never") {
			t.Errorf("projects have %s, a folder named after --", p)
		}
	}
}
