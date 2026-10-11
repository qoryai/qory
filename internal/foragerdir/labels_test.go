package foragerdir_test

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/foragerdir"
)

// TestACheckoutsLabelsAreKept is the labels qory keeps per checkout: none at first,
// ErrNoLabels; then in labels beside forager.yaml, made mode 0700, a file mode 0600
// named by the SHA-256 of the checkout's real path, read back as written, an absent
// value too, and replaced in one rename, leaving nothing beside it.
func TestACheckoutsLabelsAreKept(t *testing.T) {
	d := newDir(t)
	root := t.TempDir()
	if _, err := d.ReadLabels(root); !errors.Is(err, foragerdir.ErrNoLabels) {
		t.Errorf("no kept labels: %v", err)
	}
	for _, l := range []foragerdir.Labels{
		{"forge": "git.example.com", "repository": "acme/app"},
		{"forge": "git.example.com", "repository": ""},
	} {
		if err := d.WriteLabels(root, l); err != nil {
			t.Fatal(err)
		}
		got, err := d.ReadLabels(root)
		if err != nil || !maps.Equal(got, l) {
			t.Errorf("read back %v, %v; want %v", got, err, l)
		}
	}
	path, err := d.LabelsPath(root)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(path)
	if filepath.Dir(path) != filepath.Join(string(d), foragerdir.LabelsDir) || len(name) != 64+len(".json") || !strings.HasSuffix(name, ".json") {
		t.Errorf("kept at %s", path)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Errorf("labels holds %v, %v; want the one file", entries, err)
	}
	for p, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700 | os.ModeDir} {
		if info, err := os.Lstat(p); err != nil || info.Mode() != want {
			t.Errorf("%s: %v, %v; want %v", p, info.Mode(), err, want)
		}
	}
	if other, err := d.LabelsPath(t.TempDir()); err != nil || other == path {
		t.Errorf("another checkout's labels are at %s, %v", other, err)
	}
}

// TestALinkedCheckoutHasTheSameLabels is a checkout reached through a symbolic link:
// its labels are the ones kept for its real path.
func TestALinkedCheckoutHasTheSameLabels(t *testing.T) {
	d := newDir(t)
	root := t.TempDir()
	link := filepath.Join(t.TempDir(), "app")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	a, errA := d.LabelsPath(root)
	b, errB := d.LabelsPath(link)
	if errA != nil || errB != nil || a != b {
		t.Errorf("the checkout's labels are at %s (%v), through the link at %s (%v)", a, errA, b, errB)
	}
	want := foragerdir.Labels{"forge": "git.example.com", "repository": "acme/app"}
	if err := d.WriteLabels(link, want); err != nil {
		t.Fatal(err)
	}
	if got, err := d.ReadLabels(root); err != nil || !maps.Equal(got, want) {
		t.Errorf("read by the real path: %v, %v", got, err)
	}
}

// TestKeptLabelsRefuseALink is a labels directory, or a file in it, that is a symbolic
// link: writing and reading refuse the directory, and reading refuses the file.
func TestKeptLabelsRefuseALink(t *testing.T) {
	d := newDir(t)
	root := t.TempDir()
	if err := os.Symlink(t.TempDir(), d.Path(foragerdir.LabelsDir)); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteLabels(root, foragerdir.Labels{"forge": "x"}); err == nil || !strings.Contains(err.Error(), "is a symbolic link; it must be the directory itself") {
		t.Errorf("a linked directory: %v", err)
	}
	if _, err := d.ReadLabels(root); err == nil || err.Error() != d.Path(foragerdir.LabelsDir)+" is a symbolic link; it must be the directory itself" {
		t.Errorf("reading through a linked directory: %v", err)
	}

	e := newDir(t)
	path, err := e.LabelsPath(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.WriteFile(other, []byte(`{"forge":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, path); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ReadLabels(root); err == nil || err.Error() != path+" is a symbolic link; it must be the file itself; remove it to keep the labels of the next run the server accepts" {
		t.Errorf("a linked file: %v", err)
	}
}

// TestKeptLabelsMakeTheirDirectoryPrivate is a labels directory of mode 0755, which
// writing makes mode 0700. A labels that is a file is refused, and nothing is
// kept.
func TestKeptLabelsMakeTheirDirectoryPrivate(t *testing.T) {
	d := newDir(t)
	dir := d.Path(foragerdir.LabelsDir)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := d.WriteLabels(root, foragerdir.Labels{"forge": "x"}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(dir); err != nil || info.Mode() != 0o700|os.ModeDir {
		t.Errorf("%s: %v, %v; want mode 0700", dir, info.Mode(), err)
	}

	e := newDir(t)
	if err := os.WriteFile(e.Path(foragerdir.LabelsDir), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.WriteLabels(root, foragerdir.Labels{"forge": "x"}); err == nil {
		t.Error("labels were kept in a file")
	}
	if _, err := e.ReadLabels(root); err == nil || errors.Is(err, foragerdir.ErrNoLabels) {
		t.Errorf("a labels that is a file reads as %v", err)
	}
}

// TestKeptLabelsTooLargeAreRefused is a checkout's labels file larger than two labels
// could be: it is refused, with how to clear it.
func TestKeptLabelsTooLargeAreRefused(t *testing.T) {
	d := newDir(t)
	root := t.TempDir()
	if err := d.WriteLabels(root, foragerdir.Labels{"forge": strings.Repeat("x", 8<<10)}); err != nil {
		t.Fatal(err)
	}
	path, err := d.LabelsPath(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ReadLabels(root); err == nil || err.Error() != path+" is larger than a checkout's labels; remove it to keep the labels of the next run the server accepts" {
		t.Errorf("a large file: %v", err)
	}
}
