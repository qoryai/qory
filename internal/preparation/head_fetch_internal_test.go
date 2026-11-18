package preparation

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// fetchesIn is a git runner that runs every git through inner and counts the fetches.
type fetchesIn struct {
	inner gitRunner
	count int
}

func (f *fetchesIn) run(ctx context.Context, dir string, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "fetch" {
		f.count++
	}
	return f.inner(ctx, dir, args...)
}

// TestAHeadThatNoRecordListsIsRefusedWithNoFetch is a branch with a head that 20 commits lead
// to: whoever can push the branch makes that history long. The head check refuses the head that
// no record lists, and the head after the run's first push, with no fetch of the history, and
// replaces the branch at a recorded head with one fetch.
func TestAHeadThatNoRecordListsIsRefusedWithNoFetch(t *testing.T) {
	for _, c := range []struct {
		name       string
		record     bool // the record lists the head
		pushed     bool // the run has pushed already: r.Commit is its last push
		wantFetch  bool
		wantLeased bool
	}{
		{"a head that no record lists", false, false, false, false},
		{"a recorded head after the run's first push", true, true, false, false},
		{"a recorded head before the first push", true, false, true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, b, base := onBranchIn(t, "acme/shop")
			bare := filepath.Join(base, "acme", "shop.git")
			start := b.Repositories[0].Commit
			head := start
			for i := 0; i < 20; i++ {
				head = bareCommit(t, bare, b.Ref, head, "another push")
			}
			if c.record {
				b.Cancelled = append(b.Cancelled, Landed{System: "git.example.com", Path: "acme/shop", Commit: head})
			}
			r := b.Repositories[0]
			if c.pushed {
				r.Commit = run(t, bare, "rev-parse", start+"^")
			}
			leave(t, dir)
			commitHere(t, dir)
			read := &fetchesIn{inner: stepGit(&testProxy, "")}
			check, err := checkHead(context.Background(), read.run, dir, b, r)
			if (read.count > 0) != c.wantFetch {
				t.Errorf("%d fetches of the history, want fetches: %v", read.count, c.wantFetch)
			}
			if (check.lease == head) != c.wantLeased {
				t.Errorf("lease %q, want the head %s: %v (error %v)", check.lease, head, c.wantLeased, err)
			}
			if !c.wantLeased {
				changed := &HeadChanged{}
				if !errors.As(err, &changed) || changed.Head != head {
					t.Fatalf("error %v, want a refusal with the head %s", err, head)
				}
			}
			if got := run(t, bare, "rev-parse", b.Ref); got != head {
				t.Errorf("the branch moved to %s", got)
			}
		})
	}
}

// TestARecordOfAnotherRepositoryNeverListsTheHead is a branch whose head a record lists for
// another path, for another system, and for a path that differs in case on a system that is not
// GitHub: the check compares the system, the path and the commit, so the head is one that no
// record lists, and the check refuses it with no fetch.
func TestARecordOfAnotherRepositoryNeverListsTheHead(t *testing.T) {
	for _, c := range []struct{ name, system, path string }{
		{"another path", "git.example.com", "acme/lib"},
		{"another system", "other.example.com", "acme/shop"},
		{"a path in another case", "git.example.com", "ACME/shop"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, b, base := onBranchIn(t, "acme/shop")
			bare := filepath.Join(base, "acme", "shop.git")
			head := bareCommit(t, bare, b.Ref, b.Repositories[0].Commit, "cancelled elsewhere")
			b.Cancelled = []Landed{{System: c.system, Path: c.path, Commit: head}}
			leave(t, dir)
			commitHere(t, dir)
			read := &fetchesIn{inner: stepGit(&testProxy, "")}
			check, err := checkHead(context.Background(), read.run, dir, b, b.Repositories[0])
			changed := &HeadChanged{}
			if !errors.As(err, &changed) || changed.Head != head || check.lease != "" {
				t.Fatalf("lease %q, error %v, want a refusal with the head %s", check.lease, err, head)
			}
			if read.count != 0 {
				t.Errorf("%d fetches of the history, want none", read.count)
			}
			if got := run(t, bare, "rev-parse", b.Ref); got != head {
				t.Errorf("the branch moved to %s", got)
			}
		})
	}
}
