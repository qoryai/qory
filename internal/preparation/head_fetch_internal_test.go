package preparation

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
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

// TestTheProjectBaseIsTheMergeBaseWithTheDefaultBranch is a branch cut from main at the
// version, with main moved on by one commit: a push sets the project base to the version,
// the merge base of the pushed head with main, not to main's head. A rerun starts at the
// pushed commit in a checkout fetched at depth one, which lacks the version: its push sets
// the same project base and fetches it into the checkout. A repository with no default
// branch has none.
func TestTheProjectBaseIsTheMergeBaseWithTheDefaultBranch(t *testing.T) {
	dir, b, base := onBranchIn(t, "acme/shop")
	bare := filepath.Join(base, "acme", "shop.git")
	version := b.Repositories[0].Commit
	bareCommit(t, bare, "main", version, "main moves on")
	leave(t, dir)
	if doc, _, err := pushOf(dir, b); err != nil || doc == nil {
		t.Fatalf("first push: %v", err)
	}
	if got := b.Repositories[0].ProjectBase; got != "" {
		t.Fatalf("the push set the project base %q, which ReadProjectBase sets", got)
	}
	ReadProjectBase(context.Background(), dir, os.Environ(), b, testProxy)
	pushedHead := b.Repositories[0].Commit
	if got := b.Repositories[0].ProjectBase; got != version {
		t.Fatalf("the first push's project base is %q, want the version %s", got, version)
	}

	rerun := t.TempDir()
	run(t, rerun, "init", "--quiet")
	run(t, rerun, "fetch", "--quiet", "--depth", "1", bare, pushedHead)
	run(t, rerun, "checkout", "--quiet", "-b", b.Ref, pushedHead)
	write(t, filepath.Join(rerun, "rerun.txt"), "rerun\n")
	head := commitHere(t, rerun)
	run(t, rerun, "push", "--quiet", bare, head+":refs/heads/"+b.Ref)
	if _, err := exec.Command("git", "-C", rerun, "cat-file", "-e", version+"^{commit}").CombinedOutput(); err == nil {
		t.Fatal("the rerun's checkout has the version before the read")
	}
	r := WorkRepository{Path: "acme/shop", Dir: ".", Commit: head, Start: pushedHead, DefaultBranch: "main"}
	if got := projectBase(context.Background(), stepGit(&testProxy, ""), rerun, "git.example.com", r, head); got != version {
		t.Errorf("the rerun's project base is %q, want the version %s", got, version)
	}
	run(t, rerun, "cat-file", "-e", version+"^{commit}")
	run(t, rerun, "rev-parse", version+"^{tree}")

	r.DefaultBranch = ""
	if got := projectBase(context.Background(), stepGit(&testProxy, ""), rerun, "git.example.com", r, head); got != "" {
		t.Errorf("with no default branch the project base is %q, want none", got)
	}
}

// TestAPushOfNoCommitReadsTheProjectBase is a session that leaves no change: the push
// moves nothing and still sets the project base, the version, which main has moved past,
// so a review after the session counts only what the branch changes. A default branch that
// the forge lacks has none.
func TestAPushOfNoCommitReadsTheProjectBase(t *testing.T) {
	dir, b, base := onBranchIn(t, "acme/shop")
	bare := filepath.Join(base, "acme", "shop.git")
	version := b.Repositories[0].Commit
	bareCommit(t, bare, "main", version, "main moves on")
	if doc, _, err := pushOf(dir, b); err != nil || doc != nil {
		t.Fatalf("the push returned %v, %v, want nothing pushed", doc, err)
	}
	ReadProjectBase(context.Background(), dir, os.Environ(), b, testProxy)
	if got := b.Repositories[0].ProjectBase; got != version {
		t.Errorf("the project base is %q, want the version %s", got, version)
	}
	r := b.Repositories[0]
	r.DefaultBranch = "gone"
	if got := projectBase(context.Background(), stepGit(&testProxy, ""), dir, "git.example.com", r, r.Commit); got != "" {
		t.Errorf("a default branch that the forge lacks has the project base %q", got)
	}
}

// TestAFailedReadKeepsTheEarlierProjectBase is a run whose first push reads the project
// base, the version, and whose second push moves the branch while the forge has lost the
// default branch: the second read finds no base, and the first one stays.
func TestAFailedReadKeepsTheEarlierProjectBase(t *testing.T) {
	dir, b, base := onBranchIn(t, "acme/shop")
	bare := filepath.Join(base, "acme", "shop.git")
	version := b.Repositories[0].Commit
	bareCommit(t, bare, "main", version, "main moves on")
	leave(t, dir)
	if doc, _, err := pushOf(dir, b); err != nil || doc == nil {
		t.Fatalf("first push: %v", err)
	}
	ReadProjectBase(context.Background(), dir, os.Environ(), b, testProxy)
	if got := b.Repositories[0].ProjectBase; got != version {
		t.Fatalf("the first project base is %q, want the version %s", got, version)
	}
	run(t, bare, "update-ref", "-d", "refs/heads/main")
	write(t, filepath.Join(dir, "second.txt"), "second\n")
	if doc, _, err := pushOf(dir, b); err != nil || doc == nil {
		t.Fatalf("second push: %v", err)
	}
	ReadProjectBase(context.Background(), dir, os.Environ(), b, testProxy)
	if got := b.Repositories[0].ProjectBase; got != version {
		t.Errorf("after a read that found no base, the project base is %q, want the version %s", got, version)
	}
}

// TestASlowHistoryFetchLeavesNoProjectBase is a forge whose history fetch for the project
// base does not end: the read ends at [projectBaseLimit] with no project base, and the
// fetch of the history is the call that the limit cut.
func TestASlowHistoryFetchLeavesNoProjectBase(t *testing.T) {
	dir, b, _ := onBranchIn(t, "acme/shop")
	was := projectBaseLimit
	t.Cleanup(func() { projectBaseLimit = was })
	projectBaseLimit = 300 * time.Millisecond
	inner := stepGit(&testProxy, "")
	cut := false
	slow := func(ctx context.Context, dir string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "fetch" && slices.Contains(args, "--filter=tree:0") {
			<-ctx.Done()
			cut = true
			return "", ctx.Err()
		}
		return inner(ctx, dir, args...)
	}
	r := b.Repositories[0]
	began := time.Now()
	if got := projectBase(context.Background(), slow, dir, "git.example.com", r, r.Commit); got != "" {
		t.Errorf("the project base is %q, want none", got)
	}
	if took := time.Since(began); !cut || took > 10*time.Second {
		t.Errorf("the read took %v, and the limit cut the history fetch: %v", took, cut)
	}
}
