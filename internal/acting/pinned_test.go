package acting_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/acting"
)

// hashOf is an entry's hash as qory has computed it since acting keys were first kept:
// the SHA-256 of "qory." and the key, a zero byte, and the value with, for a path
// outside the place, a line break and where the path resolves.
func hashOf(id, value string) string {
	h := sha256.New()
	h.Write([]byte("qory." + id))
	h.Write([]byte{0})
	h.Write([]byte(value))
	return hex.EncodeToString(h.Sum(nil))
}

// TestEntriesAndBaseKeepTheirResults pins what Entries and Base return: the hashes of
// keys that are no path, as fixed values; a path that leads out through a link, with
// where it leads; a path inside the place written in another case, on a file system
// that ignores case; and the stack file a qory.yaml extends.
func TestEntriesAndBaseKeepTheirResults(t *testing.T) {
	dir, file := place(t, document)
	entries, err := acting.Entries(file, []byte(document), dir)
	if err != nil {
		t.Fatal(err)
	}
	fixed := map[string]string{
		"worktree.run.add":              "a895d347fd7fcce4b16e1136be8db1283b6dfa5cf3ff02e004b3388b0f61edc7",
		"git.cache":                     "f03882e4ee0da8d0bb55c928f0853c3c0c7780466ebd2aea780ae4becd8fbd22",
		"harness.modules[2].source.git": "ef63fe1ec9513747e045530cb95ff206e101e52e55d5b0cd806ba09109b7b446",
	}
	for _, e := range entries {
		if want, ok := fixed[e.Key]; ok {
			if e.Hash() != want {
				t.Errorf("%s hashes to %s, want %s", e.Key, e.Hash(), want)
			}
			delete(fixed, e.Key)
		}
	}
	if len(fixed) != 0 {
		t.Errorf("no entry for %v", fixed)
	}

	content := "harness:\n  extends:\n    path: ./base\n  modules:\n    - name: linked\n      source:\n        path: ./x\n"
	dir, file = place(t, content)
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"x", "base"} {
		if err := os.Symlink(outside, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	entries, err = acting.Entries(file, []byte(content), dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ key, id, value string }{
		{"harness.extends.path", "harness.extends.path", `"./base"`},
		{"harness.modules[0].source.path", "harness.modules.source.path", `"./x"`},
	}
	if len(entries) != len(want) {
		t.Fatalf("entries %+v, want %d", entries, len(want))
	}
	for i, w := range want {
		e := entries[i]
		if e.Key != w.key || e.Value != w.value || e.Leads != outside || e.Hash() != hashOf(w.id, w.value+"\n"+outside) {
			t.Errorf("entry %d is %+v with hash %s, want %s = %s leading to %s", i, e, e.Hash(), w.key, w.value, outside)
		}
	}
	if got, want := acting.Base(file, []byte(content)), filepath.Join(dir, "base", "qory-stack.yaml"); got != want {
		t.Errorf("Base is %q, want %q", got, want)
	}
	if got := acting.Base(file, []byte("harness:\n  model: a-model\n")); got != "" {
		t.Errorf("Base of a qory.yaml that extends no path is %q, want none", got)
	}

	// A path into the place written in another case is inside it where the file system
	// ignores case, so it is no acting key there.
	content = "harness:\n  modules:\n    - name: cased\n      source:\n        path: ../SHOP/modules\n"
	dir, file = place(t, content)
	if err := os.MkdirAll(filepath.Join(dir, "modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "SHOP")); err != nil {
		t.Log("the file system tells cases apart; the case of a cased path is not pinned here")
		return
	}
	entries, err = acting.Entries(file, []byte(content), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a path into the place in another case is an acting key: %+v", entries)
	}
}
