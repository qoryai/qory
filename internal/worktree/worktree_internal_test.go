package worktree

import "testing"

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
