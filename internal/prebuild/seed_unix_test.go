//go:build unix

package prebuild_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/prebuild"
)

// seedPaths makes a workspace holding the configuration config and a file, an empty
// seed folder, and no hooks' folder.
func seedPaths(t *testing.T, config string) prebuild.SeedPaths {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := prebuild.SeedPaths{Workspace: filepath.Join(root, "workspace"), Seed: filepath.Join(root, "seed"), Hooks: filepath.Join(root, "hooks"),
		ManagedSettings: filepath.Join(root, "etc", "claude-code")}
	for _, dir := range []string{filepath.Join(p.Workspace, ".devcontainer"), p.Seed} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(p.Workspace, ".devcontainer", "devcontainer.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Workspace, "README.md"), []byte("the shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestSeedRunsTheCommandsAndStoresTheTree runs onCreateCommand, then
// updateContentCommand, in the workspace, with the container's environment and the
// configuration's, expanded, and moves the tree to the seed folder; postCreateCommand
// does not run.
func TestSeedRunsTheCommandsAndStoresTheTree(t *testing.T) {
	p := seedPaths(t, `{
		"image": "alpine",
		"containerEnv": {"SHOP_MODE": "test", "SHOP_HOME": "${containerWorkspaceFolder}/home"},
		"remoteEnv": {"SHOP_LABEL": "${containerEnv:SHOP_MODE}-${containerEnv:BASE}"},
		"onCreateCommand": "echo \"$BASE $SHOP_MODE $SHOP_HOME $SHOP_LABEL\" > created.txt; mkdir -p cache && chmod 555 cache",
		"updateContentCommand": ["sh", "-c", "pwd > updated.txt; echo ${containerWorkspaceFolderBasename} >> updated.txt"],
		"postCreateCommand": "touch post-created.txt",
	}`)
	var out, log bytes.Buffer
	env := []string{"PATH=" + os.Getenv("PATH"), "BASE=image"}
	if err := prebuild.Seed(t.Context(), p, env, &out, &log); err != nil {
		t.Fatalf("Seed: %v\n%s", err, log.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "prebuild: ran onCreateCommand in ") ||
		!strings.HasPrefix(lines[1], "prebuild: ran updateContentCommand in ") ||
		!strings.HasPrefix(lines[2], "prebuild: moved /workspace to /qory/seed in ") {
		t.Errorf("printed\n%s", out.String())
	}
	if entries, _ := os.ReadDir(p.Workspace); len(entries) != 0 {
		t.Errorf("the workspace holds %d entries", len(entries))
	}
	created, _ := os.ReadFile(filepath.Join(p.Seed, "created.txt"))
	if got := string(created); got != "image test /workspace/home test-image\n" {
		t.Errorf("onCreateCommand saw %q", got)
	}
	updated, _ := os.ReadFile(filepath.Join(p.Seed, "updated.txt"))
	if got := string(updated); got != p.Workspace+"\nworkspace\n" {
		t.Errorf("updateContentCommand saw %q", got)
	}
	for _, name := range []string{"README.md", ".devcontainer/devcontainer.json", "cache"} {
		if _, err := os.Stat(filepath.Join(p.Seed, name)); err != nil {
			t.Errorf("the seed lacks %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(p.Seed, "post-created.txt")); err == nil {
		t.Error("postCreateCommand ran")
	}
}

// TestSeedNamesAFailedCommand is a command that fails: named, never its text, and the
// tree is not moved.
func TestSeedNamesAFailedCommand(t *testing.T) {
	p := seedPaths(t, `{"image": "alpine", "onCreateCommand": "true", "updateContentCommand": {"deps": "exit 3", "lint": "true"}}`)
	var out bytes.Buffer
	err := prebuild.Seed(t.Context(), p, []string{"PATH=" + os.Getenv("PATH")}, &out, &bytes.Buffer{})
	if err == nil || err.Error() != "updateContentCommand's command deps exited with status 3" {
		t.Fatalf("Seed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.Workspace, "README.md")); err != nil {
		t.Error("the tree was moved")
	}
}

// TestSeedSaysWhatIsNotSet is a configuration with no commands: a line for each, and
// the tree moved.
func TestSeedSaysWhatIsNotSet(t *testing.T) {
	p := seedPaths(t, `{"image": "alpine"}`)
	var out bytes.Buffer
	if err := prebuild.Seed(t.Context(), p, nil, &out, &bytes.Buffer{}); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	want := "prebuild: onCreateCommand is not set; nothing to run\nprebuild: updateContentCommand is not set; nothing to run\n"
	if !strings.HasPrefix(out.String(), want) {
		t.Errorf("printed\n%s", out.String())
	}
}

// TestSeedRefusesWhatIsInTheWay is something at the hooks' path, a seed folder that
// holds files, and a variable of the host: each refused before a command runs.
func TestSeedRefusesWhatIsInTheWay(t *testing.T) {
	cases := []struct {
		name   string
		config string
		setup  func(t *testing.T, p prebuild.SeedPaths)
		want   string
	}{
		{name: "hooks", setup: func(t *testing.T, p prebuild.SeedPaths) {
			if err := os.Symlink("/tmp", p.Hooks); err != nil {
				t.Fatal(err)
			}
		}, want: "the image has something at /qory/hooks, where a run mounts its hook socket's folder; nothing may be there"},
		{name: "seed", setup: func(t *testing.T, p prebuild.SeedPaths) {
			if err := os.WriteFile(filepath.Join(p.Seed, "x"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}, want: "/qory/seed is not an empty folder; the seed step stores the tree there"},
		{name: "host variable", config: `{"image": "alpine", "containerEnv": {"A": "${localEnv:HOME}"}, "onCreateCommand": "touch ran"}`,
			want: "containerEnv.A uses ${localEnv:…}, a value of the host; no value of the host reaches the container"},
		{name: "id", config: `{"image": "alpine", "onCreateCommand": "echo ${devcontainerId} > ran"}`, want: "onCreateCommand uses ${devcontainerId}, which is given no value here"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			config := c.config
			if config == "" {
				config = `{"image": "alpine", "onCreateCommand": "touch ran"}`
			}
			p := seedPaths(t, config)
			if c.setup != nil {
				c.setup(t, p)
			}
			err := prebuild.Seed(t.Context(), p, []string{"PATH=" + os.Getenv("PATH")}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || err.Error() != c.want {
				t.Fatalf("Seed: %v, want %q", err, c.want)
			}
			if _, err := os.Stat(filepath.Join(p.Workspace, "ran")); err == nil {
				t.Error("the command ran")
			}
		})
	}
}

// TestSeedRefusesManagedSettings is anything at /etc/claude-code, which Claude Code
// reads above every other source: in the image, before the commands run, or made by a
// command, after.
func TestSeedRefusesManagedSettings(t *testing.T) {
	want := "the image has something at /etc/claude-code, where Claude Code reads managed settings above every other source; no image is made with anything there"
	kinds := map[string]func(t *testing.T, path string){
		"a folder": func(t *testing.T, path string) {
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"a file": func(t *testing.T, path string) {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"a link that leads nowhere": func(t *testing.T, path string) {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/nowhere", path); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mk := range kinds {
		t.Run(name, func(t *testing.T) {
			p := seedPaths(t, `{"image": "alpine", "onCreateCommand": "touch ran"}`)
			mk(t, p.ManagedSettings)
			err := prebuild.Seed(t.Context(), p, []string{"PATH=" + os.Getenv("PATH")}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || err.Error() != want {
				t.Fatalf("Seed: %v", err)
			}
			if _, err := os.Stat(filepath.Join(p.Workspace, "ran")); err == nil {
				t.Error("the command ran")
			}
		})
	}
	t.Run("made by a command", func(t *testing.T) {
		p := seedPaths(t, `{"image": "alpine"}`)
		if err := os.WriteFile(filepath.Join(p.Workspace, ".devcontainer", "devcontainer.json"),
			[]byte(`{"image": "alpine", "updateContentCommand": ["mkdir", "-p", "`+p.ManagedSettings+`"]}`), 0o644); err != nil {
			t.Fatal(err)
		}
		err := prebuild.Seed(t.Context(), p, []string{"PATH=" + os.Getenv("PATH")}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || err.Error() != want {
			t.Fatalf("Seed: %v", err)
		}
	})
}

// TestSeedRunsWithTheImagesHome is the commands with HOME as the container has it: a
// command writes ~/x there, and a HOME the wall user cannot write in is a line, and
// the commands run all the same.
func TestSeedRunsWithTheImagesHome(t *testing.T) {
	config := `{"image": "alpine", "onCreateCommand": "echo hi > ~/x || true; touch ran"}`
	t.Run("writable", func(t *testing.T) {
		p := seedPaths(t, config)
		home := t.TempDir()
		var out bytes.Buffer
		if err := prebuild.Seed(t.Context(), p, []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}, &out, &bytes.Buffer{}); err != nil {
			t.Fatalf("Seed: %v", err)
		}
		if data, err := os.ReadFile(filepath.Join(home, "x")); err != nil || string(data) != "hi\n" {
			t.Errorf("~/x is %q, %v", data, err)
		}
		if strings.Contains(out.String(), "HOME") {
			t.Errorf("printed\n%s", out.String())
		}
		if entries, _ := os.ReadDir(home); len(entries) != 1 {
			t.Errorf("HOME holds %d entries", len(entries))
		}
	})
	t.Run("not writable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root writes in any folder")
		}
		p := seedPaths(t, config)
		home := t.TempDir()
		if err := os.Chmod(home, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(home, 0o700) })
		var out bytes.Buffer
		if err := prebuild.Seed(t.Context(), p, []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}, &out, &bytes.Buffer{}); err != nil {
			t.Fatalf("Seed: %v", err)
		}
		want := "prebuild: HOME, " + home + ", is not a folder the wall user writes in; the commands run with it all the same\n"
		if !strings.HasPrefix(out.String(), want) {
			t.Errorf("printed\n%s\nwant first\n%s", out.String(), want)
		}
		if _, err := os.Stat(filepath.Join(p.Seed, "ran")); err != nil {
			t.Error("the command did not run")
		}
	})
}

// TestSeedRunsWithTheConfigurationsHome is a HOME containerEnv or remoteEnv sets, the
// last one: the commands run with it, and it is the HOME checked for writing.
func TestSeedRunsWithTheConfigurationsHome(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes in any folder")
	}
	locked := t.TempDir()
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	open := t.TempDir()
	t.Run("remoteEnv's, not writable", func(t *testing.T) {
		p := seedPaths(t, `{"image": "alpine", "containerEnv": {"HOME": "`+open+`"}, "remoteEnv": {"HOME": "`+locked+`"}, "onCreateCommand": "echo $HOME > home.txt"}`)
		var out bytes.Buffer
		if err := prebuild.Seed(t.Context(), p, []string{"PATH=" + os.Getenv("PATH"), "HOME=" + open}, &out, &bytes.Buffer{}); err != nil {
			t.Fatalf("Seed: %v", err)
		}
		want := "prebuild: HOME, " + locked + ", is not a folder the wall user writes in; the commands run with it all the same\n"
		if !strings.HasPrefix(out.String(), want) {
			t.Errorf("printed\n%s", out.String())
		}
		if data, _ := os.ReadFile(filepath.Join(p.Seed, "home.txt")); string(data) != locked+"\n" {
			t.Errorf("the command ran with HOME %q", data)
		}
	})
	t.Run("containerEnv's, writable, over the image's", func(t *testing.T) {
		p := seedPaths(t, `{"image": "alpine", "containerEnv": {"HOME": "`+open+`"}, "onCreateCommand": "echo $HOME > home.txt"}`)
		var out bytes.Buffer
		if err := prebuild.Seed(t.Context(), p, []string{"PATH=" + os.Getenv("PATH"), "HOME=" + locked}, &out, &bytes.Buffer{}); err != nil {
			t.Fatalf("Seed: %v", err)
		}
		if strings.Contains(out.String(), "HOME") {
			t.Errorf("printed\n%s", out.String())
		}
		if data, _ := os.ReadFile(filepath.Join(p.Seed, "home.txt")); string(data) != open+"\n" {
			t.Errorf("the command ran with HOME %q", data)
		}
	})
}

// TestSeedRefusesAHomeInTheTree is a HOME in the workspace or the seed folder, from the
// image or the configuration, or a relative one: refused before a command runs.
func TestSeedRefusesAHomeInTheTree(t *testing.T) {
	cases := []struct {
		name  string
		home  func(p prebuild.SeedPaths) string
		where string
	}{
		{name: "the image's, in the workspace", home: func(p prebuild.SeedPaths) string { return p.Workspace + "/.cache" }, where: "image"},
		{name: "the image's, the workspace", home: func(p prebuild.SeedPaths) string { return p.Workspace + "/" }, where: "image"},
		{name: "containerEnv's, the seed", home: func(p prebuild.SeedPaths) string { return p.Seed }, where: "containerEnv"},
		{name: "remoteEnv's, in the seed", home: func(p prebuild.SeedPaths) string { return p.Seed + "/../seed/home" }, where: "remoteEnv"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := seedPaths(t, `{"image": "alpine"}`)
			home := c.home(p)
			env := []string{"PATH=" + os.Getenv("PATH")}
			config := `{"image": "alpine", "onCreateCommand": "touch ran"}`
			switch c.where {
			case "image":
				env = append(env, "HOME="+home)
			default:
				config = `{"image": "alpine", "` + c.where + `": {"HOME": "` + home + `"}, "onCreateCommand": "touch ran"}`
			}
			if err := os.WriteFile(filepath.Join(p.Workspace, ".devcontainer", "devcontainer.json"), []byte(config), 0o644); err != nil {
				t.Fatal(err)
			}
			err := prebuild.Seed(t.Context(), p, env, &bytes.Buffer{}, &bytes.Buffer{})
			want := "HOME, " + home + ", is in /workspace or /qory/seed, whose files the image keeps as the repository's tree; the commands do not run with it"
			if err == nil || err.Error() != want {
				t.Fatalf("Seed: %v, want %q", err, want)
			}
			if _, err := os.Stat(filepath.Join(p.Workspace, "ran")); err == nil {
				t.Error("the command ran")
			}
		})
	}
}

// TestSeedRefusesHooksACommandMade is a command that puts something at /qory/hooks:
// refused after the commands, as /etc/claude-code is.
func TestSeedRefusesHooksACommandMade(t *testing.T) {
	p := seedPaths(t, `{"image": "alpine"}`)
	if err := os.WriteFile(filepath.Join(p.Workspace, ".devcontainer", "devcontainer.json"),
		[]byte(`{"image": "alpine", "onCreateCommand": ["mkdir", "`+p.Hooks+`"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	err := prebuild.Seed(t.Context(), p, []string{"PATH=" + os.Getenv("PATH")}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || err.Error() != "the image has something at /qory/hooks, where a run mounts its hook socket's folder; nothing may be there" {
		t.Fatalf("Seed: %v", err)
	}
}

// TestSeedRefusesARelativeHome is a HOME that is not an absolute path, from the image
// or the configuration: refused, as what it is, before a command runs.
func TestSeedRefusesARelativeHome(t *testing.T) {
	for _, c := range []struct{ home, where string }{{"..", "image"}, {"home", "image"}, {"..", "remoteEnv"}, {"./cache", "containerEnv"}} {
		t.Run(c.where+" "+c.home, func(t *testing.T) {
			p := seedPaths(t, `{"image": "alpine"}`)
			env := []string{"PATH=" + os.Getenv("PATH")}
			config := `{"image": "alpine", "onCreateCommand": "touch ran"}`
			if c.where == "image" {
				env = append(env, "HOME="+c.home)
			} else {
				config = `{"image": "alpine", "` + c.where + `": {"HOME": "` + c.home + `"}, "onCreateCommand": "touch ran"}`
			}
			if err := os.WriteFile(filepath.Join(p.Workspace, ".devcontainer", "devcontainer.json"), []byte(config), 0o644); err != nil {
				t.Fatal(err)
			}
			err := prebuild.Seed(t.Context(), p, env, &bytes.Buffer{}, &bytes.Buffer{})
			want := "HOME, " + c.home + ", is not an absolute path; the commands do not run with it"
			if err == nil || err.Error() != want {
				t.Fatalf("Seed: %v, want %q", err, want)
			}
			if _, err := os.Stat(filepath.Join(p.Workspace, "ran")); err == nil {
				t.Error("the command ran")
			}
		})
	}
}
