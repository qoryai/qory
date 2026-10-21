//go:build unix

package devcontainer_test

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/qoryai/qory/internal/devcontainer"
)

// TestFindTakesThePlacesInOrder is a repository with no configuration, with each place,
// and with both: Find returns the spec's first, and nothing is no error.
func TestFindTakesThePlacesInOrder(t *testing.T) {
	root := t.TempDir()
	if got, err := devcontainer.Find(root); got != "" || err != nil {
		t.Fatalf("empty repository: %q, %v", got, err)
	}
	if c, err := devcontainer.Load(root); c != nil || err != nil {
		t.Fatalf("empty repository: Load %v, %v", c, err)
	}
	write(t, root, ".devcontainer.json", `{"image": "second"}`)
	if got, err := devcontainer.Find(root); got != ".devcontainer.json" || err != nil {
		t.Fatalf("second place: %q, %v", got, err)
	}
	write(t, root, ".devcontainer/devcontainer.json", `{"image": "first"}`)
	if got, err := devcontainer.Find(root); got != ".devcontainer/devcontainer.json" || err != nil {
		t.Fatalf("both places: %q, %v", got, err)
	}
	c, err := devcontainer.Load(root)
	if err != nil || c.Image != "first" || c.Path != ".devcontainer/devcontainer.json" {
		t.Fatalf("Load: %+v, %v", c, err)
	}
}

// TestFindRefusesWhatIsNotAFileOfTheRepository is each way a place may leave the
// repository or be no regular file: a link to a file inside or outside, a linked
// folder, a file where a folder is, a folder or a pipe where the file is, and a file
// too big.
func TestFindRefusesWhatIsNotAFileOfTheRepository(t *testing.T) {
	outside := t.TempDir()
	write(t, outside, "devcontainer.json", `{}`)
	write(t, outside, "folder/devcontainer.json", `{}`)
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, root string)
		want  string
	}{
		{"link to a file outside", func(t *testing.T, root string) {
			link(t, filepath.Join(outside, "devcontainer.json"), filepath.Join(root, ".devcontainer.json"))
		}, ".devcontainer.json is a symbolic link"},
		{"link to a file inside", func(t *testing.T, root string) {
			write(t, root, "other.json", `{}`)
			link(t, "other.json", filepath.Join(root, ".devcontainer.json"))
		}, ".devcontainer.json is a symbolic link"},
		{"linked folder outside", func(t *testing.T, root string) {
			link(t, filepath.Join(outside, "folder"), filepath.Join(root, ".devcontainer"))
		}, ".devcontainer is a symbolic link"},
		{"linked file in the folder", func(t *testing.T, root string) {
			if err := os.Mkdir(filepath.Join(root, ".devcontainer"), 0o755); err != nil {
				t.Fatal(err)
			}
			link(t, "../../outside.json", filepath.Join(root, ".devcontainer", "devcontainer.json"))
		}, ".devcontainer/devcontainer.json is a symbolic link"},
		{"file where the folder is", func(t *testing.T, root string) {
			write(t, root, ".devcontainer", "")
		}, ".devcontainer is not a folder"},
		{"folder where the file is", func(t *testing.T, root string) {
			if err := os.Mkdir(filepath.Join(root, ".devcontainer.json"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, ".devcontainer.json is not a regular file"},
		{"pipe where the file is", func(t *testing.T, root string) {
			if err := syscall.Mkfifo(filepath.Join(root, ".devcontainer.json"), 0o644); err != nil {
				t.Skip(err)
			}
		}, ".devcontainer.json is not a regular file"},
		{"too big", func(t *testing.T, root string) {
			write(t, root, ".devcontainer/devcontainer.json", "{}"+strings.Repeat(" ", devcontainer.MaxSize))
		}, ".devcontainer/devcontainer.json is more than 1 MiB"},
	} {
		root := t.TempDir()
		c.setup(t, root)
		if _, err := devcontainer.Find(root); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: Find %v, want %q", c.name, err, c.want)
		}
		if _, err := devcontainer.Load(root); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: Load %v, want %q", c.name, err, c.want)
		}
	}
}

// link makes newname a symbolic link to target.
func link(t *testing.T, target, newname string) {
	t.Helper()
	if err := os.Symlink(target, newname); err != nil {
		t.Fatal(err)
	}
}
