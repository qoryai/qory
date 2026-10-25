package preparation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// movedRemote makes the repository base/path.git, bare, on branch main, its default:
// a first commit whose qory.yaml contains "first", then a second commit, the branch's
// head, whose qory.yaml contains "head". It points the fetches at base and returns the
// two commits.
func movedRemote(t *testing.T, base, path string) (first, head string) {
	t.Helper()
	work := t.TempDir()
	run(t, work, "init", "--quiet", "-b", "main")
	write(t, filepath.Join(work, "qory.yaml"), "first\n")
	run(t, work, "add", "-A")
	run(t, work, "commit", "--quiet", "-m", "first")
	first = run(t, work, "rev-parse", "HEAD")
	write(t, filepath.Join(work, "qory.yaml"), "head\n")
	run(t, work, "commit", "--quiet", "-am", "head")
	head = run(t, work, "rev-parse", "HEAD")
	bareFrom(t, base, path, work)
	return first, head
}

// fetchAt is [RunAt] of acme/shop on git.example.com, through [testProxy], with the
// target acme/shop there at commit, which returns the line it printed, its time as Xs.
func fetchAt(dir, commit string) (string, error) {
	return fetchListAt(dir, "acme/shop", Target{System: "git.example.com", Path: "acme/shop", Version: commit})
}

// fetchListAt is [fetchAt] of the repositories list, with the target at.
func fetchListAt(dir, list string, at Target) (string, error) {
	var report strings.Builder
	_, err := RunAt(context.Background(), dir, os.Environ(), env("git.example.com", list), at, testProxy, &report)
	return times.ReplaceAllString(strings.TrimSuffix(report.String(), "\n"), " in Xs"), err
}

// TestFetchesTheRunRepositoryAtItsCommit is a fetch at a commit that is not the default
// branch's head, written in upper case as well: the working folder is at that commit, on
// the local branch main with no upstream and no remote-tracking ref, shallow, with
// qory.yaml as the commit has it and not as the head has it, and the line shows the
// commit.
func TestFetchesTheRunRepositoryAtItsCommit(t *testing.T) {
	needGit(t)
	first, _ := movedRemote(t, t.TempDir(), "acme/shop")
	for _, commit := range []string{first, strings.ToUpper(first)} {
		dir := emptyFolder(t)
		line, err := fetchAt(dir, commit)
		if err != nil {
			t.Fatal(err)
		}
		if want := "qory run: fetched acme/shop at " + first[:12] + " in Xs"; line != want {
			t.Errorf("the line is %q, want %q", line, want)
		}
		if got := run(t, dir, "rev-parse", "HEAD"); got != first {
			t.Errorf("the working folder is at %s, want %s", got, first)
		}
		if got := run(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
			t.Errorf("the branch is %s, want main", got)
		}
		if out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "@{u}").CombinedOutput(); err == nil {
			t.Errorf("main has the upstream %s, want none", strings.TrimSpace(string(out)))
		}
		if got := run(t, dir, "for-each-ref", "--format=%(refname)", "refs/remotes"); got != "" {
			t.Errorf("the working folder has the remote-tracking refs %s, want none", got)
		}
		if got := run(t, dir, "rev-list", "--count", "HEAD"); got != "1" {
			t.Errorf("%s commits fetched, want 1", got)
		}
		if got := run(t, dir, "status", "--porcelain"); got != "" {
			t.Errorf("git status: %s", got)
		}
		if b, err := os.ReadFile(filepath.Join(dir, "qory.yaml")); err != nil || string(b) != "first\n" {
			t.Errorf("qory.yaml is %q, %v, want the commit's", b, err)
		}
	}
}

// TestFetchesTheHeadWithNoCommit is a fetch with no commit: the working folder is at the
// default branch's head, as [Run] fetches it.
func TestFetchesTheHeadWithNoCommit(t *testing.T) {
	needGit(t)
	_, head := movedRemote(t, t.TempDir(), "acme/shop")
	dir := emptyFolder(t)
	line, err := fetchAt(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := "qory run: fetched acme/shop at " + head[:12] + " in Xs"; line != want {
		t.Errorf("the line is %q, want %q", line, want)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "qory.yaml")); string(b) != "head\n" {
		t.Errorf("qory.yaml is %q, want the head's", b)
	}
}

// TestStopsOnACommitTheRemoteLacks is a fetch at a commit the remote does not have: it
// stops with the line that starts with taking the work item in again and shows the
// commit's first twelve characters, the repository and the host, and fetches nothing.
func TestStopsOnACommitTheRemoteLacks(t *testing.T) {
	needGit(t)
	movedRemote(t, t.TempDir(), "acme/shop")
	dir := emptyFolder(t)
	_, err := fetchAt(dir, "0123456789abcdef0123456789abcdef01234567")
	prefix := "qory run: take the work item in again in Qory Apiary: the commit 0123456789ab that the run's documents set is not on acme/shop on git.example.com. The fetch through the run's gateway failed: "
	if err == nil || !strings.HasPrefix(err.Error(), prefix) || !strings.Contains(err.Error(), "not our ref") || !strings.HasSuffix(err.Error(), "; the code is used") || strings.Contains(err.Error(), "\n") {
		t.Fatalf("%v\nwant one line starting %q", err, prefix)
	}
	if _, err := os.Stat(filepath.Join(dir, "qory.yaml")); !os.IsNotExist(err) {
		t.Errorf("the working folder has qory.yaml: %v", err)
	}
}

// TestStopsWhereTheRemoteServesNoFetchByID is a fetch at a commit the remote has, over
// git's protocol version 0, from a remote without uploadpack.allowReachableSHA1InWant:
// such a remote serves no fetch of a commit it did not list, so the fetch stops with the
// line that starts with that setting. With the setting, the same fetch succeeds.
func TestStopsWhereTheRemoteServesNoFetchByID(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	first, _ := movedRemote(t, base, "acme/shop")
	protocols = append(slices.Clone(protocols), "-c", "protocol.version=0")
	_, err := fetchAt(emptyFolder(t), first)
	prefix := "qory run: let git.example.com serve a fetch of a commit by its id, such as with git's uploadpack.allowReachableSHA1InWant, then take the work item in again in Qory Apiary: git.example.com does not serve the commit " + first[:12] + " of acme/shop by its id, or does not have it. The fetch through the run's gateway failed: "
	if err == nil || !strings.HasPrefix(err.Error(), prefix) || !strings.Contains(err.Error(), "unadvertised object") || !strings.HasSuffix(err.Error(), "; the code is used") || strings.Contains(err.Error(), "\n") {
		t.Fatalf("%v\nwant one line starting %q", err, prefix)
	}
	run(t, filepath.Join(base, "acme", "shop.git"), "config", "uploadpack.allowReachableSHA1InWant", "true")
	dir := emptyFolder(t)
	if _, err := fetchAt(dir, first); err != nil {
		t.Fatal(err)
	}
	if got := run(t, dir, "rev-parse", "HEAD"); got != first {
		t.Errorf("the working folder is at %s, want %s", got, first)
	}
}

// TestRefusesAVersionThatIsNoCommitID is a fetch of the target at a version that is not
// a full commit id, such as a branch name or an abbreviated id: it stops before git
// runs.
func TestRefusesAVersionThatIsNoCommitID(t *testing.T) {
	needGit(t)
	movedRemote(t, t.TempDir(), "acme/shop")
	for _, v := range []string{"main", "0123456789ab", "release/2"} {
		dir := emptyFolder(t)
		_, err := fetchAt(dir, v)
		if want := "qory run: ask Qory Apiary's instance admin to check the adapter of the target: the run's documents set the target version " + v + ", and qory fetches a target at a version only when the version is a commit id of 40 or 64 hex digits; the code is used"; err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", v, err, want)
		}
		if got, _ := os.ReadDir(filepath.Join(dir, ".git", "refs", "remotes")); len(got) != 0 {
			t.Errorf("%s: fetched %v", v, got)
		}
	}
}

// TestFetchesAtACommitThroughTheHostProxy fetches the run's repository at a commit over
// https through the host proxy, whose server has no uploadpack.allowReachableSHA1InWant:
// git fetches the commit by its id over git's protocol version 2. A commit the server
// does not have stops with the line of [textNoCommit], without the proxy's password.
func TestFetchesAtACommitThroughTheHostProxy(t *testing.T) {
	needGit(t)
	s := proxygittest.Start(t)
	first := s.Repository(t, "acme/shop", "main", map[string]string{"qory.yaml": "first\n"})
	work := t.TempDir()
	proxygittest.Git(t, work, "clone", "--quiet", filepath.Join(s.Root, "acme", "shop.git"), ".")
	write(t, filepath.Join(work, "qory.yaml"), "head\n")
	proxygittest.Git(t, work, "-c", "user.name=Example", "-c", "user.email=example@example.com", "commit", "--quiet", "-am", "head")
	proxygittest.Git(t, work, "push", "--quiet", "origin", "main")
	dir := emptyFolder(t)
	var report strings.Builder
	if _, err := RunAt(context.Background(), dir, os.Environ(), env(proxygittest.Host, "acme/shop"), Target{System: proxygittest.Host, Path: "acme/shop", Version: first}, s.Proxy, &report); err != nil {
		t.Fatal(err)
	}
	if want := "qory run: fetched acme/shop at " + first[:12] + " in "; !strings.HasPrefix(report.String(), want) {
		t.Errorf("the line is %q, want %q…", report.String(), want)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "qory.yaml")); string(b) != "first\n" {
		t.Errorf("qory.yaml is %q, want the commit's", b)
	}
	if s.Tunnels() == 0 {
		t.Error("no request went through the host proxy")
	}
	_, err := RunAt(context.Background(), emptyFolder(t), os.Environ(), env(proxygittest.Host, "acme/shop"), Target{System: proxygittest.Host, Path: "acme/shop", Version: "0123456789abcdef0123456789abcdef01234567"}, s.Proxy, &report)
	prefix := "qory run: take the work item in again in Qory Apiary: the commit 0123456789ab that the run's documents set is not on acme/shop on git.test. The fetch through the run's gateway failed: "
	if err == nil || !strings.HasPrefix(err.Error(), prefix) || strings.Contains(err.Error(), proxygittest.Password) {
		t.Errorf("a commit the server lacks: %v, want %q…", err, prefix)
	}
	t.Logf("the line of a commit the server lacks: %v", err)
}

// TestStopsOnATargetThatIsNotTheFirstEntry is a target on the list's system whose path
// is a later entry, one whose path the list does not contain, and one with an empty
// list: each stops with its line before any fetch, whatever the version, so the working
// folder has no remote and no commit.
func TestStopsOnATargetThatIsNotTheFirstEntry(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	first, _ := movedRemote(t, base, "acme/shop")
	remote(t, base, "acme/lib", "main", nil)
	later := "qory run: report this run to the instance admin of Qory Apiary: the run's documents set the target acme/shop, but the run configuration lists it after another repository; the target must be the first repository of the run configuration; the code is used"
	missing := "qory run: report this run to the instance admin of Qory Apiary: the run's documents set the target acme/shop, but the run configuration does not list it; the target must be the first repository of the run configuration; the code is used"
	for _, c := range []struct{ list, version, want string }{
		{"acme/lib,acme/shop", first, later},
		{"acme/lib,acme/shop", "main", later},
		{"acme/lib", first, missing},
		{"acme/lib", "main", missing},
		{"", first, missing},
	} {
		dir := emptyFolder(t)
		_, err := fetchListAt(dir, c.list, Target{System: "git.example.com", Path: "acme/shop", Version: c.version})
		if err == nil || err.Error() != c.want {
			t.Errorf("%s at %s: %v\nwant %s", c.list, c.version, err, c.want)
		}
		if got := run(t, dir, "remote"); got != "" {
			t.Errorf("%s at %s: the working folder has the remote %s", c.list, c.version, got)
		}
		if got := run(t, dir, "rev-list", "-n", "1", "--all"); got != "" {
			t.Errorf("%s at %s: the working folder has the commit %s", c.list, c.version, got)
		}
	}
}

// TestUsesNoVersionOfATargetOnAnotherSystem is a target on another system than the
// list's, or with no version: the version is not used, a version that is no commit id
// stops nothing, and the run's repository is at its head.
func TestUsesNoVersionOfATargetOnAnotherSystem(t *testing.T) {
	needGit(t)
	_, head := movedRemote(t, t.TempDir(), "acme/shop")
	for _, at := range []Target{
		{System: "ads.example.com", Path: "acme/shop", Version: "0123456789abcdef0123456789abcdef01234567"},
		{System: "ads.example.com", Path: "accounts/1234", Version: "2026-10-24.3"},
		{System: "git.example.com", Path: "acme/other"},
	} {
		dir := emptyFolder(t)
		if _, err := fetchListAt(dir, "acme/shop", at); err != nil {
			t.Fatalf("%+v: %v", at, err)
		}
		if got := run(t, dir, "rev-parse", "HEAD"); got != head {
			t.Errorf("%+v: the working folder is at %s, want the head %s", at, got, head)
		}
	}
}

// TestUnlistedStopsOnlyATargetOnTheSystem is [Unlisted]: an empty list stops a target
// on the list's system that sets a version, and nothing else.
func TestUnlistedStopsOnlyATargetOnTheSystem(t *testing.T) {
	at := Target{System: "git.example.com", Path: "acme/shop", Version: "0123456789abcdef0123456789abcdef01234567"}
	missing := "qory run: report this run to the instance admin of Qory Apiary: the run's documents set the target acme/shop, but the run configuration does not list it; the target must be the first repository of the run configuration; the code is used"
	if err := Unlisted(env("git.example.com", ""), at); err == nil || err.Error() != missing {
		t.Errorf("an empty list: %v, want %s", err, missing)
	}
	for _, c := range []struct {
		system, list string
		at           Target
	}{
		{"", "", at},
		{"ads.example.com", "", at},
		{"git.example.com", "", Target{System: "git.example.com", Path: "acme/shop"}},
		{"git.example.com", "acme/shop", at},
	} {
		if err := Unlisted(env(c.system, c.list), c.at); err != nil {
			t.Errorf("%q %q %+v: %v, want nil", c.system, c.list, c.at, err)
		}
	}
}

// TestTheTargetIsTheEntryOfItsPath is [targetIn]: the entry that is the target's path,
// in any case on github.com alone, and -1 for a path the list lacks.
func TestTheTargetIsTheEntryOfItsPath(t *testing.T) {
	for _, c := range []struct {
		system string
		paths  []string
		path   string
		want   int
	}{
		{"git.example.com", []string{"acme/shop", "acme/lib"}, "acme/shop", 0},
		{"git.example.com", []string{"acme/lib", "acme/shop"}, "acme/shop", 1},
		{"git.example.com", []string{"acme/shop"}, "Acme/Shop", -1},
		{"github.com", []string{"acme/shop"}, "Acme/Shop", 0},
		{"git.example.com", []string{"acme/lib"}, "acme/shop", -1},
	} {
		if got := targetIn(c.system, c.paths, c.path); got != c.want {
			t.Errorf("%s %v %s: %d, want %d", c.system, c.paths, c.path, got, c.want)
		}
	}
}
