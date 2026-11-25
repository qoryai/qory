package worktree

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestNetAloneReachesARemote: a git of [runner.git] fetches from no path, and one of [runner.net]
// does.
func TestNetAloneReachesARemote(t *testing.T) {
	from, to := repository(t), filepath.Join(t.TempDir(), "to")
	gitIn(t, from, "init", "--quiet", to)
	r := runner{main: to}
	if _, err := r.git(to, "fetch", "--quiet", from, "main"); err == nil || !strings.Contains(err.Error(), "transport 'file' not allowed") {
		t.Errorf("git: %v", err)
	}
	if _, err := r.net(to, "fetch", "--quiet", from, "main"); err != nil {
		t.Errorf("net: %v", err)
	}
}

// TestOnlyOnJoinsTheClauses is the definition the remove trace gives of the commits it
// counts, with and without the main checkout's HEAD and a recorded base: the clauses
// joined by ", ", with " and " before the last.
func TestOnlyOnJoinsTheClauses(t *testing.T) {
	head := "0123456789abcdef0123456789abcdef01234567"
	for _, c := range []struct{ head, base, want string }{
		{head, "origin/main", "on no remote branch, not in the main checkout's HEAD 0123456 and not on its recorded base origin/main"},
		{head, "", "on no remote branch, not in the main checkout's HEAD 0123456 and on no base, since none is recorded for it"},
		{"", "origin/main", "on no remote branch and not on its recorded base origin/main"},
		{"", "", "on no remote branch and on no base, since none is recorded for it"},
	} {
		if got := onlyOn(c.head, c.base); got != c.want {
			t.Errorf("onlyOn(%q, %q) = %q, want %q", c.head, c.base, got, c.want)
		}
	}
}
