package source_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/profile"
	"github.com/qoryai/qory/internal/source"
)

// hermetic points git at an empty global configuration and away from the system one, so no
// test reads the machine's git identity.
func hermetic(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// run runs git in dir and fails the test with its output.
func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// gitOut runs git in dir and returns its trimmed output.
func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// write creates a file and the directories above it.
func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// committedRepo returns a checkout with a layer directory in it and nothing uncommitted.
func committedRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	run(t, root, "init", "-q")
	run(t, root, "config", "user.name", "Test User")
	run(t, root, "config", "user.email", "test@example.com")
	write(t, filepath.Join(root, "layers", "core", "AGENTS.md"), "# Core\n")
	write(t, filepath.Join(root, "README.md"), "app\n")
	run(t, root, "add", "-A")
	run(t, root, "commit", "-q", "-m", "first")
	return root
}

// TestResolveReportsTheDirectoryAndThePin resolves a relative path against the base
// directory and reports the working-tree pin.
func TestResolveReportsTheDirectoryAndThePin(t *testing.T) {
	hermetic(t)
	root := committedRepo(t)
	got, err := source.Resolve(root, profile.Source{Path: filepath.Join("layers", "core")}, source.Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := source.Resolved{Dir: filepath.Join(root, "layers", "core"), Pin: source.WorkingTree}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if source.WorkingTree != "working-tree" {
		t.Errorf("WorkingTree is %q, want working-tree", source.WorkingTree)
	}
}

// TestResolveTakesAnAbsolutePathAsItIs ignores the base directory for an absolute source.
func TestResolveTakesAnAbsolutePathAsItIs(t *testing.T) {
	hermetic(t)
	root := committedRepo(t)
	dir := filepath.Join(root, "layers", "core")
	got, err := source.Resolve(t.TempDir(), profile.Source{Path: dir}, source.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Dir != dir {
		t.Errorf("got %s, want %s", got.Dir, dir)
	}
}

// TestResolveReportsADirtyWorkingTree covers the dirty flag: it is set by a change under the
// layer directory and by nothing else.
func TestResolveReportsADirtyWorkingTree(t *testing.T) {
	hermetic(t)
	root := committedRepo(t)
	layer := profile.Source{Path: filepath.Join("layers", "core")}

	got, err := source.Resolve(root, layer, source.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Dirty {
		t.Error("a committed layer is reported dirty")
	}

	write(t, filepath.Join(root, "README.md"), "changed\n")
	got, err = source.Resolve(root, layer, source.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Dirty {
		t.Error("a change outside the layer directory is reported dirty")
	}

	write(t, filepath.Join(root, "layers", "core", "skills", "review", "SKILL.md"), "review\n")
	got, err = source.Resolve(root, layer, source.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Dirty {
		t.Error("an untracked file under the layer directory is not reported dirty")
	}
}

// TestResolveOutsideGitIsClean covers a layer directory that no checkout covers: git cannot
// answer, so the layer is reported clean.
func TestResolveOutsideGitIsClean(t *testing.T) {
	hermetic(t)
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "core"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := source.Resolve(base, profile.Source{Path: "core"}, source.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Dirty {
		t.Error("a directory outside git is reported dirty")
	}
	if got.Pin != source.WorkingTree {
		t.Errorf("got pin %q, want %q", got.Pin, source.WorkingTree)
	}
}

// TestResolveRefusesWhatIsNoDirectory covers the two ways a source fails: it is not there,
// and it is a file. A caller matches the first with errors.Is and os.ErrNotExist.
func TestResolveRefusesWhatIsNoDirectory(t *testing.T) {
	hermetic(t)
	base := t.TempDir()
	write(t, filepath.Join(base, "core.md"), "not a layer\n")

	if _, err := source.Resolve(base, profile.Source{Path: "missing"}, source.Options{}); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("got %v, want an os.ErrNotExist", err)
	}
	_, err := source.Resolve(base, profile.Source{Path: "core.md"}, source.Options{})
	if err == nil {
		t.Fatal("resolved a file as a layer directory")
	}
	if want := filepath.Join(base, "core.md") + " is not a directory"; err.Error() != want {
		t.Errorf("got %q, want %q", err, want)
	}
}
