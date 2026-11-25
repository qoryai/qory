package worktree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// lines are the numbers 1 to 40, one on a line, with each of the old texts, such as "\n5\n",
// replaced by the new one.
func lines(changes ...string) string {
	var b strings.Builder
	b.WriteString("\n")
	for n := range 40 {
		fmt.Fprintf(&b, "%d\n", n+1)
	}
	return strings.NewReplacer(changes...).Replace(b.String())
}

// blobless is a clone of a new repository, made with --filter=blob:none, with the branch old
// checked out, and with the local branches local, rebased, squashed and work, each at the
// remote's. Every file of old has another content on main. The branch rebased is on main by
// its commits' patches. squashed, whose two commits change a file each, is there as one commit,
// and no commit of main changes one of the two files alone, so git reads the files of no commit
// before it looks for that one. It returns the clone and the repository.
func blobless(t *testing.T) (clone, from string) {
	t.Helper()
	hermetic(t)
	for _, who := range []string{"AUTHOR", "COMMITTER"} {
		t.Setenv("GIT_"+who+"_NAME", "Tester")
		t.Setenv("GIT_"+who+"_EMAIL", "tester@example.com")
	}
	from = repository(t)
	gitIn(t, from, "config", "uploadpack.allowFilter", "true")
	gitIn(t, from, "config", "uploadpack.allowAnySHA1InWant", "true")
	commit := func(paths, content, message string) {
		t.Helper()
		for _, path := range strings.Fields(paths) {
			if err := os.WriteFile(filepath.Join(from, path), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			gitIn(t, from, "add", path)
		}
		gitIn(t, from, "commit", "-q", "-m", message)
	}
	commit("a b c d", lines(), "files")
	gitIn(t, from, "branch", "old")
	gitIn(t, from, "checkout", "-q", "-b", "rebased", "old")
	commit("a", lines("\n5\n", "\nfive\n"), "five")
	commit("a", lines("\n5\n", "\nfive\n", "\n30\n", "\nthirty\n"), "thirty")
	gitIn(t, from, "checkout", "-q", "-b", "squashed", "old")
	commit("b", lines("\n7\n", "\nseven\n"), "seven")
	commit("d", lines("\n9\n", "\nnine\n"), "nine")
	gitIn(t, from, "checkout", "-q", "-b", "work", "old")
	commit("c", lines("\n3\n", "\nthree\n"), "three")
	gitIn(t, from, "checkout", "-q", "main")
	commit("b d", lines("\n36\n", "\nthirty-six\n"), "first")
	gitIn(t, from, "cherry-pick", "rebased~1", "rebased")
	gitIn(t, from, "merge", "-q", "--squash", "squashed")
	gitIn(t, from, "commit", "-q", "-m", "squash")
	commit("a b d", lines("\n20\n", "\ntwenty\n"), "later")
	// The change is far from work's, so the rebase of work merges the two contents of c.
	commit("c", lines("\n33\n", "\nthirty-three\n"), "later c")
	gitIn(t, from, "branch", "local")
	gitIn(t, from, "checkout", "-q", "old")
	clone = filepath.Join(filepath.Dir(from), "clone")
	gitIn(t, from, "clone", "-q", "--filter=blob:none", "-b", "old", "file://"+from, clone)
	for _, b := range []string{"local", "rebased", "squashed", "work"} {
		gitIn(t, clone, "branch", "-q", b, "origin/"+b)
	}
	return clone, from
}

// gitOut is the trimmed output of git in dir with no fetch of a missing object, and "" when it
// fails.
func gitOut(dir string, args ...string) string {
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	c.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=none")
	out, err := c.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// TestAddFetchesTheMissingObjects: each way in which a worktree gets its files works in a
// clone that has none of them. With --offline no git fetch runs, and the timeout, which is for
// a git that reaches the remote for its refs, stops no checkout.
func TestAddFetchesTheMissingObjects(t *testing.T) {
	o := Options{Offline: true, Timeout: time.Nanosecond}
	for _, c := range []struct {
		name, branch, how string
		o                 func(o Options) Options
		before            func(t *testing.T, clone string)
	}{
		{name: "a local branch", branch: "local", how: "local"},
		{name: "a branch of the remote", branch: "main", how: "remote"},
		{name: "a new branch", branch: "new", how: "new off origin/main", o: func(o Options) Options {
			o.Base = "main"
			return o
		}},
		{name: "a branch to attach to", branch: "main", how: "fetched from origin", o: func(o Options) Options {
			return Options{Attach: Remote{Branch: "main"}}
		}},
		{name: "a local branch behind the one to attach to", branch: "main", how: "fetched from origin; local branch fast-forwarded to origin/main", before: func(t *testing.T, clone string) {
			gitIn(t, clone, "branch", "-q", "main", "origin/old")
		}, o: func(o Options) Options {
			return Options{Attach: Remote{Branch: "main"}}
		}},
		{name: "--onto, with no commit to move", branch: "here", how: "local; moved onto", before: func(t *testing.T, clone string) {
			gitIn(t, clone, "branch", "-q", "here", "origin/old")
			gitIn(t, clone, "config", "branch.here.qory-base", "origin/old")
		}, o: func(o Options) Options {
			o.Base, o.Onto, o.Rebase = "main", true, true
			return o
		}},
		{name: "--onto, with a commit to move", branch: "work", how: "rebased onto", before: func(t *testing.T, clone string) {
			gitIn(t, clone, "config", "branch.work.qory-base", "origin/old")
		}, o: func(o Options) Options {
			o.Base, o.Onto, o.Rebase = "main", true, true
			return o
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			clone, _ := blobless(t)
			if c.before != nil {
				c.before(t, clone)
			}
			if gitOut(clone, "cat-file", "-t", "origin/main:c") != "" {
				t.Fatal("the clone has the file of main before the add")
			}
			with := o
			if c.o != nil {
				with = c.o(o)
			}
			a, err := Add(clone, c.branch, with)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains("local; "+a.How, c.how) {
				t.Errorf("how %q, want %q", a.How, c.how)
			}
			got, err := os.ReadFile(filepath.Join(a.Path, "c"))
			want := lines("\n33\n", "\nthirty-three\n")
			if c.branch == "work" {
				want = lines("\n33\n", "\nthirty-three\n", "\n3\n", "\nthree\n")
			}
			if err != nil || string(got) != want {
				t.Errorf("c is %q, %v", got, err)
			}
			if status := gitOut(a.Path, "status", "--porcelain"); status != "" {
				t.Errorf("status %q", status)
			}
		})
	}
}

// TestRemoveFetchesTheMissingObjects: a branch that a rebase or squash merge wrote anew is found
// on its base in a clone that has not the files of the base.
func TestRemoveFetchesTheMissingObjects(t *testing.T) {
	for _, branch := range []string{"rebased", "squashed"} {
		t.Run(branch, func(t *testing.T) {
			clone, from := blobless(t)
			a, err := Add(clone, branch, Options{Offline: true})
			if err != nil {
				t.Fatal(err)
			}
			gitIn(t, from, "branch", "-q", "-D", branch)
			gitIn(t, clone, "update-ref", "-d", "refs/remotes/origin/"+branch)
			gitIn(t, clone, "config", "branch."+branch+".qory-base", "origin/main")
			r, err := Remove(clone, a.Path, Options{Offline: true, Timeout: time.Nanosecond})
			if err != nil {
				t.Fatal(err)
			}
			if r.Landed == "" || !r.BranchDeleted {
				t.Errorf("removed: %+v", r)
			}
		})
	}
}

// TestAWorktreeWithARenameOfAMissingFileIsChanged: git status compares a renamed file with the
// one of HEAD, which the clone has not, so a worktree with that change is not removed, and not
// moved onto another base.
func TestAWorktreeWithARenameOfAMissingFileIsChanged(t *testing.T) {
	clone, _ := blobless(t)
	a, err := Add(clone, "work", Options{Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, a.Path, "reset", "-q", "--soft", "origin/rebased")
	gitIn(t, a.Path, "mv", "a", "a2")
	if _, err := Add(clone, "work", Options{Offline: true, Base: "main", Onto: true, Rebase: true}); err == nil || !strings.Contains(err.Error(), "has uncommitted changes") {
		t.Errorf("add: %v", err)
	}
	gitIn(t, a.Path, "reset", "-q", "--soft", "origin/squashed")
	gitIn(t, a.Path, "mv", "b", "b2")
	if _, err := Remove(clone, a.Path, Options{Offline: true}); err == nil || !strings.Contains(err.Error(), "has uncommitted changes") {
		t.Errorf("remove: %v", err)
	}
}

// TestResolveBaseFetchesNoMissingObject: a base that is a commit the clone does not have is none
// for [ResolveBase], which fetches nothing, and one for [Add], whose git fetches the commit.
func TestResolveBaseFetchesNoMissingObject(t *testing.T) {
	clone, from := blobless(t)
	gitIn(t, from, "update-ref", "refs/hidden/one", gitOut(from, "commit-tree", "-p", "main", "-m", "one", "main^{tree}"))
	base := gitOut(from, "rev-parse", "refs/hidden/one")
	if _, err := ResolveBase(clone, base); err == nil || !strings.Contains(err.Error(), "is not a branch, tag or commit") {
		t.Errorf("ResolveBase: %v", err)
	}
	if gitOut(clone, "cat-file", "-t", base) != "" {
		t.Fatal("ResolveBase fetched the commit")
	}
	if _, err := Add(clone, "new", Options{Offline: true, Base: base}); err != nil {
		t.Error(err)
	}
}
