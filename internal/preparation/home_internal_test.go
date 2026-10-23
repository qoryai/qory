package preparation

import (
	"os"
	"path/filepath"
	"testing"
)

// realTemp is a temporary directory by its real path, so no folder above it is a link,
// as on a machine whose temporary directory is under one.
func realTemp(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// wantFreshHome fails the test unless path is an empty real directory of mode 0700.
func wantFreshHome(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Errorf("%s is %v, want a directory of mode 0700", path, fi.Mode())
	}
	if entries, _ := os.ReadDir(path); len(entries) != 0 {
		t.Errorf("%s holds %d entries", path, len(entries))
	}
}

// TestFreshHomeMakesAnEmptyHome is the agent's home made where nothing is: an empty
// directory of mode 0700, whatever the umask.
func TestFreshHomeMakesAnEmptyHome(t *testing.T) {
	path := filepath.Join(realTemp(t), "qory-agent-home")
	if err := freshHome(path); err != nil {
		t.Fatal(err)
	}
	wantFreshHome(t, path)
}

// TestFreshHomeRefusesWhatIsThere is anything at the agent's home, a file, an empty
// folder, a full one, a link to a folder and a link to nothing: each is refused in one
// line naming the path, never what is there, and stays as it was.
func TestFreshHomeRefusesWhatIsThere(t *testing.T) {
	for _, c := range []struct {
		name  string
		setUp func(t *testing.T, path string)
	}{
		{"a file", func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"an empty folder", func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"a full folder", func(t *testing.T, path string) {
			if err := os.MkdirAll(filepath.Join(path, "plugins"), 0o755); err != nil {
				t.Fatal(err)
			}
			for _, f := range []string{"CLAUDE.md", "settings.json", ".claude.json"} {
				if err := os.WriteFile(filepath.Join(path, f), []byte("{}"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"a link to a folder", func(t *testing.T, path string) {
			target := filepath.Join(filepath.Dir(path), "elsewhere")
			if err := os.Mkdir(target, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{"a link to nothing", func(t *testing.T, path string) {
			if err := os.Symlink(filepath.Join(filepath.Dir(path), "missing"), path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(realTemp(t), "qory-agent-home")
			c.setUp(t, path)
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			err = freshHome(path)
			if want := "remove " + path + " from the image and from the setup commands: qory makes the agent's home only where nothing is, so a run given a run key does not start"; err == nil || err.Error() != want {
				t.Errorf("%v, want %q", err, want)
			}
			after, err := os.Lstat(path)
			if err != nil || after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) {
				t.Errorf("what was there changed: %v, %v", after, err)
			}
		})
	}
}

// TestFreshHomeRefusesALinkAbove is a symbolic link in a folder above the agent's home:
// refused in one line naming both, and nothing is made where the link leads.
func TestFreshHomeRefusesALinkAbove(t *testing.T) {
	dir := realTemp(t)
	target := filepath.Join(dir, "elsewhere")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(dir, "tmp")
	if err := os.Symlink(target, parent); err != nil {
		t.Fatal(err)
	}
	below := filepath.Join(parent, "qory-agent-home")
	err := freshHome(below)
	if want := "replace the symbolic link " + parent + " with a folder in the image: the agent's home " + below + " is made only where no link leads"; err == nil || err.Error() != want {
		t.Errorf("%v, want %q", err, want)
	}
	if entries, _ := os.ReadDir(target); len(entries) != 0 {
		t.Errorf("the folder the link leads to holds %d entries", len(entries))
	}
}

// TestFreshHomeIsOnlyForTheAgentHome is the preparation of a run whose agent has no
// CLAUDE_CONFIG_DIR, or another one: nothing is made, removed or refused.
func TestFreshHomeIsOnlyForTheAgentHome(t *testing.T) {
	other := filepath.Join(realTemp(t), "home")
	for _, v := range []string{"", other, AgentHome + "/"} {
		getenv := func(name string) string {
			if name == EnvAgentHome {
				return v
			}
			return ""
		}
		if err := FreshHome(getenv); err != nil {
			t.Errorf("CLAUDE_CONFIG_DIR=%q: %v", v, err)
		}
	}
	if _, err := os.Lstat(other); !os.IsNotExist(err) {
		t.Errorf("another CLAUDE_CONFIG_DIR was made: %v", err)
	}
}

// TestAgentHomeRefusesManagedSettings is Claude Code's managed settings in the image, as
// a folder, a file or a link: refused in one line naming the path, and no home is made.
// With nothing there, the home is made.
func TestAgentHomeRefusesManagedSettings(t *testing.T) {
	for _, c := range []struct {
		name  string
		setUp func(t *testing.T, path string)
	}{
		{"a folder", func(t *testing.T, path string) {
			if err := os.MkdirAll(filepath.Join(path, "managed-settings.d"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"a file", func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"a link to nothing", func(t *testing.T, path string) {
			if err := os.Symlink(filepath.Join(filepath.Dir(path), "missing"), path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := realTemp(t)
			managed := filepath.Join(dir, "claude-code")
			c.setUp(t, managed)
			before := managedSettings
			managedSettings = managed
			t.Cleanup(func() { managedSettings = before })
			home := filepath.Join(dir, "qory-agent-home")
			err := agentHome(home)
			if want := "use an image without " + managed + ": Claude Code reads managed settings there above every other source, so a run given a run key does not start"; err == nil || err.Error() != want {
				t.Errorf("%v, want %q", err, want)
			}
			if _, err := os.Lstat(home); !os.IsNotExist(err) {
				t.Errorf("the home was made: %v", err)
			}
		})
	}

	dir := realTemp(t)
	before := managedSettings
	managedSettings = filepath.Join(dir, "claude-code")
	t.Cleanup(func() { managedSettings = before })
	home := filepath.Join(dir, "qory-agent-home")
	if err := agentHome(home); err != nil {
		t.Fatal(err)
	}
	wantFreshHome(t, home)
}

// TestFreshHomeRefusesAFolderPutThereAfterMkdir is a filled folder renamed over the
// agent's home between its Mkdir and its open: refused in one line naming the path, and
// the folder keeps its mode and its file.
func TestFreshHomeRefusesAFolderPutThereAfterMkdir(t *testing.T) {
	dir := realTemp(t)
	path := filepath.Join(dir, "qory-agent-home")
	filled := filepath.Join(dir, "filled")
	if err := os.Mkdir(filled, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filled, "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := madeHome
	t.Cleanup(func() { madeHome = before })
	madeHome = func(p string) {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filled, p); err != nil {
			t.Fatal(err)
		}
	}
	err := freshHome(path)
	if want := "leave no program of the image or the setup commands running that writes into " + path + ": the agent's home held something once qory made it, so a run given a run key does not start"; err == nil || err.Error() != want {
		t.Errorf("%v, want %q", err, want)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("the folder put there changed: %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(path, "settings.json")); err != nil {
		t.Error(err)
	}
}
