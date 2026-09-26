package config

import (
	"os"
	"strings"
	"testing"
)

// TestTheRuleOfWhoMayChangeAProgram holds synthetic owners and modes to the rule: the
// installs a machine has, Homebrew's admin group, a user's private group, root's own
// directories and a sticky /tmp or /nix/store, pass; another user's file, a directory every user may
// write and a group that is neither root's nor the owner's are refused, each named.
func TestTheRuleOfWhoMayChangeAProgram(t *testing.T) {
	const me, other = 501, 502
	n := names{
		User: func(uid uint32) string {
			return map[uint32]string{0: "root", me: "dev", other: "guest"}[uid]
		},
		Group: func(gid uint32) string {
			return map[uint32]string{0: "wheel", 20: "staff", 30: "nixbld", 80: "admin", 1000: "dev", 1001: "guest", 1002: "builders", 1003: "dev"}[gid]
		},
		Primary: func(uid uint32) (uint32, bool) {
			gid, ok := map[uint32]uint32{0: 0, me: 1000, other: 1001}[uid]
			return gid, ok
		},
	}
	dir := func(perm os.FileMode) os.FileMode { return os.ModeDir | perm }
	for _, c := range []struct {
		what string
		o    fileOwner
		want string
	}{
		{"root's /usr/local/bin", fileOwner{0, 0, dir(0o755)}, ""},
		{"Homebrew's bin, the user's with the admin group", fileOwner{me, 80, dir(0o775)}, ""},
		{"a directory the wheel group may write", fileOwner{0, 0, dir(0o775)}, ""},
		{"~/go/bin under a user private group", fileOwner{me, 1000, dir(0o775)}, ""},
		{"a sticky /tmp root owns", fileOwner{0, 0, dir(0o777) | os.ModeSticky}, ""},
		{"/nix/store, root's with the nixbld group, sticky", fileOwner{0, 30, dir(0o775) | os.ModeSticky}, ""},
		{"a directory root owns with the nixbld group, not sticky", fileOwner{0, 30, dir(0o775)}, "the group nixbld (30)"},
		{"a group named as the owner that is not the owner's primary one", fileOwner{me, 1003, dir(0o775)}, "the group dev (1003)"},
		{"the user's program", fileOwner{me, 20, 0o755}, ""},
		{"a program another user owns", fileOwner{other, 1001, 0o755}, "/x is owned by guest (502), neither root nor the user running qory"},
		{"a program an unnamed user owns", fileOwner{9999, 20, 0o755}, "/x is owned by 9999, neither root nor"},
		{"a directory every user may write", fileOwner{me, 20, dir(0o777)}, "/x may be written by every user"},
		{"a sticky directory the user owns", fileOwner{me, 20, dir(0o777) | os.ModeSticky}, "/x may be written by every user"},
		{"a program every user may write", fileOwner{0, 0, 0o757}, "/x may be written by every user"},
		{"a group of others", fileOwner{me, 1002, dir(0o775)}, "/x may be written by the group builders (1002), which is neither root's, wheel, admin nor its owner's own"},
		{"another user's private group", fileOwner{me, 1001, 0o775}, "the group guest (1001)"},
		{"the user's primary group, staff", fileOwner{me, 20, 0o775}, "the group staff (20)"},
		{"an unnamed group", fileOwner{me, 4242, 0o775}, "the group 4242,"},
	} {
		err := trusted("/x", c.o, me, n)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: %v, want %q", c.what, err, c.want)
		}
	}
}

// TestEveryDirectoryAboveAProgramIsHeldToTheRule walks a program's directories up to /:
// a writable directory anywhere above it refuses it, named by its path.
func TestEveryDirectoryAboveAProgramIsHeldToTheRule(t *testing.T) {
	tree := map[string]fileOwner{
		"/":                    {0, 0, os.ModeDir | 0o755},
		"/opt":                 {0, 0, os.ModeDir | 0o755},
		"/opt/homebrew":        {501, 80, os.ModeDir | 0o775},
		"/opt/homebrew/bin":    {501, 80, os.ModeDir | 0o775},
		"/opt/homebrew/bin/qg": {501, 80, 0o755},
	}
	stat := func(p string) (fileOwner, error) { return tree[p], nil }
	n := names{User: func(uint32) string { return "" }, Group: func(gid uint32) string { return map[uint32]string{80: "admin"}[gid] }, Primary: func(uint32) (uint32, bool) { return 20, true }}
	if err := chainTrusted("/opt/homebrew/bin/qg", stat, 501, n); err != nil {
		t.Errorf("a Homebrew tree: %v", err)
	}
	tree["/opt"] = fileOwner{501, 20, os.ModeDir | 0o777}
	if err := chainTrusted("/opt/homebrew/bin/qg", stat, 501, n); err == nil || !strings.HasPrefix(err.Error(), "/opt may be written by every user") {
		t.Errorf("a writable ancestor: %v", err)
	}
}

// TestALinkOnTheWayIsOwnedByRootOrTheUser takes a link root or the user running qory
// owns, whatever its mode, and refuses one another user owns, named as a link.
func TestALinkOnTheWayIsOwnedByRootOrTheUser(t *testing.T) {
	n := names{User: func(uid uint32) string { return map[uint32]string{502: "guest"}[uid] }}
	for _, o := range []fileOwner{{0, 0, os.ModeSymlink | 0o777}, {501, 20, os.ModeSymlink | 0o777}} {
		if err := ownedBy("/l", "a link ", o, 501, n); err != nil {
			t.Errorf("%+v: %v", o, err)
		}
	}
	if err := ownedBy("/l", "a link ", fileOwner{502, 20, os.ModeSymlink | 0o777}, 501, n); err == nil || err.Error() != "/l is a link owned by guest (502), neither root nor the user running qory" {
		t.Errorf("another user's link: %v", err)
	}
}
