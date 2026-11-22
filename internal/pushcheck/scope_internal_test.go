package pushcheck

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestScopeUserIsNeverRoot is the user of a scope check: the process's uid:gid when it is
// not root; for root, the wall's user when its uid is a number above 0, else
// 65534:65534.
func TestScopeUserIsNeverRoot(t *testing.T) {
	for _, c := range []struct {
		uid, gid   int
		wall, want string
	}{
		{1000, 1001, "", "1000:1001"},
		{1000, 1001, "2000:2000", "1000:1001"},
		{0, 0, "2000:2001", "2000:2001"},
		{0, 0, "2000", "2000"},
		{0, 0, "", "65534:65534"},
		{0, 0, "0:0", "65534:65534"},
		{0, 0, "0", "65534:65534"},
		{0, 0, "00:0", "65534:65534"},
		{0, 0, "+0", "65534:65534"},
		{0, 0, "-0:5", "65534:65534"},
		{0, 0, "agent", "65534:65534"},
		{0, 0, " 1000", "65534:65534"},
		{0, 0, "-1:-1", "65534:65534"},
	} {
		if got := scopeUser(c.uid, c.gid, c.wall); got != c.want {
			t.Errorf("scopeUser(%d, %d, %q) = %s, want %s", c.uid, c.gid, c.wall, got, c.want)
		}
	}
}

// TestChownTakesANumericUser is Chown of a folder to this process's user, which needs no
// root, and of a user that is no number, which fails.
func TestChownTakesANumericUser(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Chown(scopeUser(os.Getuid(), os.Getgid(), ""), dir); err != nil {
		t.Errorf("Chown to this user: %v", err)
	}
	for _, user := range []string{"agent", "1000:staff", ""} {
		if err := Chown(user, dir); err == nil {
			t.Errorf("Chown to %q passed", user)
		}
	}
}

// TestChownGivesTheFoldersToNobody is the root path: qory as root gives the clone and the
// snapshot, files and links included, to 65534:65534, and a link's target keeps its
// owner. It runs only as root.
func TestChownGivesTheFoldersToNobody(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("only root can give a folder to another user")
	}
	user := scopeUser(0, 0, "")
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("o"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "a"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := Chown(user, dir); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dir, filepath.Join(dir, "sub"), filepath.Join(dir, "sub", "a"), filepath.Join(dir, "link"), outside} {
		info, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		want := uint32(65534)
		if p == outside {
			want = 0
		}
		if st := info.Sys().(*syscall.Stat_t); st.Uid != want || st.Gid != want {
			t.Errorf("%s is owned by %d:%d, want %d", p, st.Uid, st.Gid, want)
		}
	}
}
