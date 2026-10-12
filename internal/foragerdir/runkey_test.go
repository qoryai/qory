package foragerdir_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/foragerdir"
)

// TestARunKeyIsKeptPrivately is the run key a machine behind gateway.server.via keeps:
// in run-keys beside forager.yaml, made mode 0700, a file mode 0600 that replaces one an
// earlier attempt left, read back as written, and removed; one that is not kept is
// ErrNoRunKey, and removing it is no error. A file others may read, a link and a
// directory that is a link are refused, and no refusal holds the key.
func TestARunKeyIsKeptPrivately(t *testing.T) {
	d := newDir(t)
	const id, key = "0192f0c1-7d4e-7a2b-8c3d-4e5f6a7b8c9d", "qrk_payload.signature"
	if _, err := d.ReadRunKey(id); !errors.Is(err, foragerdir.ErrNoRunKey) {
		t.Errorf("no kept key: %v", err)
	}
	for range 2 {
		if err := d.WriteRunKey(id, key); err != nil {
			t.Fatal(err)
		}
	}
	path := d.RunKeyPath(id)
	if path != filepath.Join(string(d), foragerdir.RunKeysDir, id) {
		t.Errorf("kept at %s", path)
	}
	for p, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700 | os.ModeDir} {
		if info, err := os.Lstat(p); err != nil || info.Mode() != want {
			t.Errorf("%s: %v, %v; want %v", p, info.Mode(), err, want)
		}
	}
	if got, err := d.ReadRunKey(id); err != nil || got != key+"\n" {
		t.Errorf("read back %q, %v", got, err)
	}

	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ReadRunKey(id); err == nil || !strings.Contains(err.Error(), "is mode 0640, which grants access to the group or others") || strings.Contains(err.Error(), key) {
		t.Errorf("a key the group may read: %v", err)
	}
	os.Chmod(path, 0o600)

	other := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(other, []byte(key), 0o600); err != nil {
		t.Fatal(err)
	}
	const linked = "0192f0c1-7d4e-7a2b-8c3d-000000000001"
	if err := os.Symlink(other, d.RunKeyPath(linked)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ReadRunKey(linked); err == nil || strings.Contains(err.Error(), key) {
		t.Errorf("a link: %v", err)
	}
	if err := d.WriteRunKey(linked, key); err != nil {
		t.Errorf("a link left where a key goes is replaced: %v", err)
	}
	if info, err := os.Lstat(d.RunKeyPath(linked)); err != nil || !info.Mode().IsRegular() {
		t.Errorf("the link was followed: %v", err)
	}

	for _, run := range []string{id, linked, id} {
		if err := d.RemoveRunKey(run); err != nil {
			t.Errorf("remove %s: %v", run, err)
		}
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("the key is still kept: %v", err)
	}

	e := newDir(t)
	if err := os.Symlink(t.TempDir(), filepath.Join(string(e), foragerdir.RunKeysDir)); err != nil {
		t.Fatal(err)
	}
	if err := e.WriteRunKey(id, key); err == nil || !strings.Contains(err.Error(), "is a symbolic link") {
		t.Errorf("run-keys as a link: %v", err)
	}
}

// TestARunKeysDirectoryThatIsALinkIsNotFollowed is run-keys a link to a directory that
// holds a run's key: reading the kept key and removing it refuse the link, as keeping
// one does, in the same words, without the key; the key where the link leads is neither
// read nor removed. A run-keys that is not there is still no kept key, and nothing to
// remove.
func TestARunKeysDirectoryThatIsALinkIsNotFollowed(t *testing.T) {
	const id, key = "0192f0c1-7d4e-7a2b-8c3d-4e5f6a7b8c9d", "qrk_payload.signature"
	d := newDir(t)
	if err := d.RemoveRunKey(id); err != nil {
		t.Errorf("remove with no run-keys: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(string(d), foragerdir.RunKeysDir)); !os.IsNotExist(err) {
		t.Errorf("removing made run-keys: %v", err)
	}

	elsewhere := t.TempDir()
	if err := os.Chmod(elsewhere, 0o700); err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(elsewhere, id)
	if err := os.WriteFile(kept, []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(string(d), foragerdir.RunKeysDir)
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Fatal(err)
	}
	want := link + " is a symbolic link; it must be the directory itself"
	if got, err := d.ReadRunKey(id); err == nil || err.Error() != want || strings.Contains(got, key) {
		t.Errorf("read through a linked run-keys: %q, %v\nwant %q", got, err, want)
	}
	if err := d.RemoveRunKey(id); err == nil || err.Error() != want {
		t.Errorf("remove through a linked run-keys: %v\nwant %q", err, want)
	}
	if err := d.WriteRunKey(id, key); err == nil || err.Error() != want {
		t.Errorf("write through a linked run-keys: %v\nwant %q", err, want)
	}
	if b, err := os.ReadFile(kept); err != nil || string(b) != key+"\n" {
		t.Errorf("the key where the link leads: %q, %v", b, err)
	}
}
