package pushcheck

import (
	"os"
	"path/filepath"
	"testing"
)

// TestScopeUserIsNeverRoot is the user of a scope check: the process's uid:gid when it is
// not root; for root, the wall's user unless it is root or unset, else 65534:65534.
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
