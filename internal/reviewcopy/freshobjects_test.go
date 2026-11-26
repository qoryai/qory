package reviewcopy

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// packOf writes a pack of the objects ids of the repository at dir into pack/ of the git
// folder dest, with no deltas, and returns the path of its index.
func packOf(t *testing.T, dir, dest string, ids ...string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dest, "objects", "pack"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := gitCommand(t, dir, "pack-objects", "--quiet", "--window=0", "--depth=0", filepath.Join(dest, "objects", "pack", "pack"))
	c.Stdin = strings.NewReader(strings.Join(ids, "\n") + "\n")
	out, err := c.Output()
	if err != nil {
		t.Fatalf("pack-objects: %v", err)
	}
	return filepath.Join(dest, "objects", "pack", "pack-"+strings.TrimSpace(string(out))+".idx")
}

// forgeIndex writes the index idx, a SHA-1 index of version 2 with no large offsets, again
// with the id from in place of to: the pack then has the bytes of from under the id to, as
// a program that can write a git folder could make it.
func forgeIndex(t *testing.T, idx, from, to string) {
	t.Helper()
	data, err := os.ReadFile(idx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("\377tOc\x00\x00\x00\x02")) {
		t.Fatal("the index is not of version 2")
	}
	n := int(binary.BigEndian.Uint32(data[8+255*4:]))
	ids, crcs, offs := 8+256*4, 8+256*4+n*20, 8+256*4+n*24
	type entry struct{ id, crc, off []byte }
	entries := make([]entry, n)
	for i := range entries {
		entries[i] = entry{slices.Clone(data[ids+i*20 : ids+i*20+20]), data[crcs+i*4 : crcs+i*4+4], data[offs+i*4 : offs+i*4+4]}
		if hex.EncodeToString(entries[i].id) == from {
			entries[i].id, _ = hex.DecodeString(to)
		}
	}
	slices.SortFunc(entries, func(a, b entry) int { return bytes.Compare(a.id, b.id) })
	var b bytes.Buffer
	b.Write(data[:8])
	for i := 0; i < 256; i++ {
		count := 0
		for _, e := range entries {
			if int(e.id[0]) <= i {
				count++
			}
		}
		binary.Write(&b, binary.BigEndian, uint32(count))
	}
	for _, e := range entries {
		b.Write(e.id)
	}
	for _, e := range entries {
		b.Write(e.crc)
	}
	for _, e := range entries {
		b.Write(e.off)
	}
	b.Write(data[len(data)-40 : len(data)-20])
	sum := sha1.Sum(b.Bytes())
	b.Write(sum[:])
	if err := os.Chmod(idx, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(idx, b.Bytes(), 0o444); err != nil {
		t.Fatal(err)
	}
}

// TestTwoPacksWithOneIdStopTheReview is a rerun whose source has two packs: the first has
// the project base and every real object but the pushed commit; the second has the pushed
// commit and, under the project base's tree id, the bytes of the pushed commit's tree. git
// reads an object from the pack that it found the last object in, without hashing it, so
// the check of the project base's tree reads the real tree, and the diff after the pushed
// commit reads the false one and finds .claude unchanged: the script before the fresh copy
// listed no path. Now index-pack hashes each object into the fresh git folder, so the copy
// stops or lists .claude/x.
func TestTwoPacksWithOneIdStopTheReview(t *testing.T) {
	sessionGit(t)
	dir := t.TempDir()
	testGit(t, dir, "init", "-q", "--object-format=sha1", "-b", "main")
	commit := func(rel, text string) string {
		write(t, dir, rel, text, 0o644)
		testGit(t, dir, "add", "-A")
		testGit(t, dir, "commit", "-q", "-m", rel)
		return testGit(t, dir, "rev-parse", "HEAD")
	}
	pbase := commit("README.md", "a\n")
	start := commit(".claude/x", "x\n")
	head := commit("README.md", "b\n")
	ptree := testGit(t, dir, "rev-parse", pbase+"^{tree}")
	htree := testGit(t, dir, "rev-parse", head+"^{tree}")
	var real []string
	for _, id := range strings.Fields(testGit(t, dir, "rev-list", "--objects", "--no-object-names", "--all")) {
		if id != head {
			real = append(real, id)
		}
	}
	source := filepath.Join(t.TempDir(), "source.git")
	packOf(t, dir, source, real...)
	forgeIndex(t, packOf(t, dir, source, head, htree), htree, ptree)
	for _, sub := range []string{"refs/heads", "objects/info"} {
		if err := os.MkdirAll(filepath.Join(source, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, source, "HEAD", "ref: refs/heads/main\n", 0o644)
	write(t, source, "config", "", 0o644)

	// Which pack the fresh copy reads the tree id from depends on the order of the packs:
	// with the false bytes, the fresh folder lacks the id and the copy stops; with the real
	// bytes, every read finds them, and the list has .claude/x, which stops the review.
	f, out, err := copyFrom(t, "", source, head, start, pbase, []string{".claude"})
	if err == nil {
		if got, err := f.Changed(); err != nil || !slices.Equal(got, []string{".claude/x"}) {
			t.Errorf("the copy session ended with no error, and listed %q, %v:\n%s", got, err, out)
		}
		return
	}
	if _, err := f.Result(); err == nil || !strings.Contains(err.Error(), copyFailed) {
		t.Errorf("the result is %v, want %q\n%s", err, copyFailed, out)
	}
}

// TestFreshObjectsHashesEachObject runs FreshObjects alone: a commit's objects land in a
// new git folder with no alternates, and a blob whose bytes the source changed lands under
// the id of its bytes, so the copy returns non-zero and the folder lacks the blob's id.
func TestFreshObjectsHashesEachObject(t *testing.T) {
	sessionGit(t)
	dir := t.TempDir()
	testGit(t, dir, "init", "-q", "--object-format=sha1", "-b", "main")
	write(t, dir, "a.txt", "real\n", 0o644)
	testGit(t, dir, "add", "-A")
	testGit(t, dir, "commit", "-q", "-m", "c")
	commit := testGit(t, dir, "rev-parse", "HEAD")
	blob := testGit(t, dir, "rev-parse", "HEAD:a.txt")
	run := func(target string) error {
		c := exec.Command("/bin/sh", "-c", "set -eu\n"+FreshObjects+`fresh_objects "$1" "$2"`, "sh", target, commit)
		c.Env = []string{"PATH=" + sessionPath, "GIT_DIR=" + filepath.Join(dir, ".git"), "GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=/dev/null", "HOME=" + t.TempDir(), "LC_ALL=C"}
		out, err := c.CombinedOutput()
		if err != nil {
			t.Logf("fresh_objects: %v\n%s", err, out)
		}
		return err
	}
	has := func(target, id string) bool {
		return gitCommand(t, target, "--git-dir="+target, "cat-file", "-e", id).Run() == nil
	}

	good := filepath.Join(t.TempDir(), "good")
	if err := run(good); err != nil {
		t.Fatal(err)
	}
	if !has(good, commit) || !has(good, blob) {
		t.Error("the fresh folder lacks the commit or its blob")
	}
	if _, err := os.Stat(filepath.Join(good, "objects", "info", "alternates")); err == nil {
		t.Error("the fresh folder has alternates")
	}

	plant(t, filepath.Join(dir, ".git"), blob, "blob", []byte("fake\n"))
	fake := filepath.Join(t.TempDir(), "fake")
	if err := run(fake); err == nil {
		t.Error("fresh_objects returned 0 for a blob whose bytes have another id")
	}
	if has(fake, blob) {
		t.Error("the fresh folder has the blob's id with the false bytes")
	}
	c := gitCommand(t, dir, "hash-object", "--stdin")
	c.Stdin = strings.NewReader("fake\n")
	other, err := c.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !has(fake, strings.TrimSpace(string(other))) {
		t.Error("the fresh folder lacks the false bytes under the id of those bytes")
	}
}
