package preparation

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
		if err := FreshHome(getenv, true); err != nil {
			t.Errorf("CLAUDE_CONFIG_DIR=%q: %v", v, err)
		}
	}
	if _, err := os.Lstat(other); !os.IsNotExist(err) {
		t.Errorf("another CLAUDE_CONFIG_DIR was made: %v", err)
	}
}

// TestFreshFolderOfTheStore is the folder of a read run's artifact files: made with mode
// 0700 where nothing is, and with anything at the path, or a link above it, the line of
// the store and not the line of the agent's home.
func TestFreshFolderOfTheStore(t *testing.T) {
	path := filepath.Join(realTemp(t), "qory-artifacts")
	if err := freshFolder(path, storeWords); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Errorf("the folder has mode %v", info.Mode())
	}

	want := "remove " + path + " from the image: qory makes the folder of a run's artifact files only where nothing is, so a read run does not start"
	if err := freshFolder(path, storeWords); err == nil || err.Error() != want {
		t.Errorf("a folder at the path: %v, want %q", err, want)
	}

	above := filepath.Join(realTemp(t), "above")
	if err := os.Symlink(realTemp(t), above); err != nil {
		t.Fatal(err)
	}
	under := filepath.Join(above, "qory-artifacts")
	want = "replace the symbolic link " + above + " with a folder in the image: the folder of a run's artifact files, " + under + ", is made only where no link leads"
	if err := freshFolder(under, storeWords); err == nil || err.Error() != want {
		t.Errorf("a link above: %v, want %q", err, want)
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

// mountsAt has [readMountinfo] list a mount at each of paths, as the kernel writes
// /proc/self/mountinfo, for the rest of the test.
func mountsAt(t *testing.T, paths ...string) {
	t.Helper()
	text := "22 1 0:21 / / rw,relatime - overlay overlay rw\n"
	for i, p := range paths {
		p = strings.NewReplacer(`\`, `\134`, " ", `\040`).Replace(p)
		text += fmt.Sprintf("%d 22 0:%d / %s rw,relatime - ext4 /dev/vda1 rw\n", 30+i, 30+i, p)
	}
	before := readMountinfo
	readMountinfo = func() ([]byte, error) { return []byte(text), nil }
	t.Cleanup(func() { readMountinfo = before })
}

// TestTheTranscriptsLinkToTheBind is the agent's home made fresh with a mount at
// .claude/projects below HOME, where Forager binds the run's transcript folder: projects
// of the home becomes a link to it, and a file written through the link lands in the
// mounted folder.
func TestTheTranscriptsLinkToTheBind(t *testing.T) {
	dir := realTemp(t)
	agentHome, home := filepath.Join(dir, "qory-agent-home"), filepath.Join(dir, "home agent")
	bind := filepath.Join(home, ".claude", "projects")
	if err := os.MkdirAll(bind, 0o700); err != nil {
		t.Fatal(err)
	}
	mountsAt(t, bind)
	if err := freshHome(agentHome); err != nil {
		t.Fatal(err)
	}
	if err := linkTranscripts(agentHome, home, "projects", ".claude/projects"); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(filepath.Join(agentHome, "projects")); err != nil || target != bind {
		t.Fatalf("the link leads to %q (%v), want %s", target, err, bind)
	}
	if err := os.MkdirAll(filepath.Join(agentHome, "projects", "-work"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentHome, "projects", "-work", "s.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(bind, "-work", "s.jsonl")); err != nil {
		t.Errorf("the transcript is not in the bound folder: %v", err)
	}
}

// TestTheTranscriptsStayInTheHomeWithoutABind is a run whose transcript folder Forager
// does not bind, or a launch with no HOME or no folder to link: the agent's home stays
// empty. Each case but "no mount" lists a mount at the place it would link to, so the
// check under test is what stops the link: with no mount at the place, even where the
// image has a folder there and a mount is above it; with no HOME; with a relative HOME;
// and with no folder to link.
func TestTheTranscriptsStayInTheHomeWithoutABind(t *testing.T) {
	for name, c := range map[string]struct{ home, folder string }{
		"no mount":   {"home", "projects"},
		"no HOME":    {"", "projects"},
		"no folder":  {"home", ""},
		"a relative": {"relative-home", "projects"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := realTemp(t)
			agentHome := filepath.Join(dir, "qory-agent-home")
			home := c.home
			if home == "home" {
				home = filepath.Join(dir, "home")
				if err := os.MkdirAll(filepath.Join(home, ".claude", "projects"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if name == "no mount" {
				mountsAt(t, home)
			} else {
				mountsAt(t, filepath.Join(home, ".claude", "projects"))
			}
			if err := freshHome(agentHome); err != nil {
				t.Fatal(err)
			}
			if err := linkTranscripts(agentHome, home, c.folder, ".claude/projects"); err != nil {
				t.Fatal(err)
			}
			wantFreshHome(t, agentHome)
		})
	}
}

// TestTheTranscriptsLinkStopsAtSomethingInTheHome is a program that wrote into the
// agent's home after qory made it fresh: a file at the link's path, or at a folder above
// it. The link is not made, and the error's fix is to leave no such program running.
func TestTheTranscriptsLinkStopsAtSomethingInTheHome(t *testing.T) {
	for name, c := range map[string]struct{ file, folder string }{
		"at the link":    {"projects", "projects"},
		"above the link": {"a", "a/projects"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := realTemp(t)
			agentHome, home := filepath.Join(dir, "qory-agent-home"), filepath.Join(dir, "home")
			bind := filepath.Join(home, ".claude", "projects")
			if err := os.MkdirAll(bind, 0o700); err != nil {
				t.Fatal(err)
			}
			mountsAt(t, bind)
			if err := freshHome(agentHome); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(agentHome, c.file), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			err := linkTranscripts(agentHome, home, c.folder, ".claude/projects")
			want := fmt.Sprintf(textLinkExists, agentHome, filepath.Join(agentHome, c.folder))
			if err == nil || err.Error() != want {
				t.Errorf("%v, want %s", err, want)
			}
			if fi, err := os.Lstat(filepath.Join(agentHome, c.file)); err != nil || !fi.Mode().IsRegular() {
				t.Errorf("the file at %s is gone or replaced: %v", c.file, err)
			}
		})
	}
}

// TestTheTranscriptsLinkStopsAtAPathOutside is a folder or a place with a .. name, or an
// absolute one, which qory run does not pass: one error, and no link.
func TestTheTranscriptsLinkStopsAtAPathOutside(t *testing.T) {
	dir := realTemp(t)
	agentHome := filepath.Join(dir, "qory-agent-home")
	if err := freshHome(agentHome); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][2]string{{"../projects", ".claude/projects"}, {"projects", "../.claude"}, {"projects", "/etc"}, {"projects", ""}, {"a/./b", ".claude"}} {
		err := linkTranscripts(agentHome, dir, c[0], c[1])
		if err == nil || !strings.HasPrefix(err.Error(), "point wall.helper at the Linux build of the qory that starts the run: ") {
			t.Errorf("%q: %v", c, err)
		}
	}
	wantFreshHome(t, agentHome)
}

// TestLinkTranscriptsNeedsTheAgentHome is a launch whose CLAUDE_CONFIG_DIR is not the
// agent's home, as on a run that is not dispatched or not walled: no link is made.
func TestLinkTranscriptsNeedsTheAgentHome(t *testing.T) {
	getenv := func(name string) string {
		if name == "HOME" {
			return "/nonexistent-home"
		}
		return ""
	}
	if err := LinkTranscripts(getenv, "../x", "/y"); err != nil {
		t.Error(err)
	}
}
