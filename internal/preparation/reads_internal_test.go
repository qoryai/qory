package preparation

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/integrations/contracts"
)

// readOn is a read target on git.example.com at path.
func readOn(path string) Read { return Read{System: "git.example.com", Path: path} }

// TestTheDocumentsListTheTargetThenTheReadTargets is [Target.Repositories]: the target's
// repository first when it is on the run's system, then each read target once, and each
// stop of a read target on another system, of another owner or inside another.
func TestTheDocumentsListTheTargetThenTheReadTargets(t *testing.T) {
	shop := Target{System: "git.example.com", Path: "acme/shop"}
	withReads := func(at Target, reads ...Read) Target {
		at.Reads = reads
		return at
	}
	for _, c := range []struct {
		name   string
		system string
		at     Target
		want   []string
		target bool
	}{
		{"target alone", "git.example.com", shop, []string{"acme/shop"}, true},
		{"target and two reads", "git.example.com", withReads(shop, readOn("acme/api"), readOn("acme/docs")), []string{"acme/shop", "acme/api", "acme/docs"}, true},
		{"reads and no target", "git.example.com", withReads(Target{}, readOn("acme/api"), readOn("acme/docs")), []string{"acme/api", "acme/docs"}, false},
		{"a target on another system", "git.example.com", withReads(Target{System: "ads.example.com", Path: "accounts/1"}, readOn("acme/api")), []string{"acme/api"}, false},
		{"one address twice", "git.example.com", withReads(shop, readOn("acme/api"), readOn("acme/shop"), readOn("acme/api")), []string{"acme/shop", "acme/api"}, true},
		{"one repository in two cases on github.com", ForgeGitHub, withReads(Target{System: ForgeGitHub, Path: "acme/shop"}, Read{System: ForgeGitHub, Path: "Acme/Shop"}, Read{System: ForgeGitHub, Path: "acme/api"}), []string{"acme/shop", "acme/api"}, true},
		{"neither", "git.example.com", Target{}, nil, false},
		// With no host for the run's repositories, the read targets are places in another
		// system: none is listed, and the list does not stop.
		{"reads and no system", "", withReads(Target{}, readOn("acme/api")), nil, false},
		{"a target, reads and no system", "", withReads(shop, readOn("acme/api")), nil, false},
	} {
		paths, target, err := c.at.Repositories(c.system)
		if err != nil || !slices.Equal(paths, c.want) || target != c.target {
			t.Errorf("%s: %v %v %v, want %v %v", c.name, paths, target, err, c.want, c.target)
		}
	}
	// A list of the most read targets is past the bytes of QORY_RUN_REPOSITORIES and passes.
	var reads []Read
	for i := range 20 {
		reads = append(reads, readOn("acme/"+strings.Repeat("r", 30)+string(rune('a'+i))))
	}
	if paths, _, err := withReads(shop, reads...).Repositories("git.example.com"); err != nil || len(paths) != 21 {
		t.Errorf("twenty read targets: %d, %v", len(paths), err)
	}
	for _, c := range []struct {
		name, system string
		at           Target
		want         string
	}{
		{"another system", "git.example.com", withReads(shop, Read{System: "other.example.com", Path: "acme/api"}),
			"qory run: A read target is on another host, so the run did not start; the code is used\n  The read target acme/api is on other.example.com, and the run's repositories are on git.example.com."},
		{"another owner", ForgeGitHub, withReads(Target{System: ForgeGitHub, Path: "acme/shop"}, Read{System: ForgeGitHub, Path: "other/api"}),
			"qory run: A read target is of another owner, so the run did not start; the code is used\n  The read target other/api is of owner other, and on github.com the repositories of a run are of one owner, acme."},
		{"inside another", "git.example.com", withReads(shop, readOn("acme/api"), readOn("acme/api/docs")),
			"qory run: A read target is inside another, so the run did not start; the code is used\n  The read target acme/api/docs is inside the read target acme/api, and qory clones no repository inside another."},
		{"not a path", "git.example.com", withReads(shop, readOn("acme/../api")),
			"qory run: A read target is not a repository path, so the run did not start; the code is used\n  The path is acme/../api. A repository path is segments of A-Z, a-z, 0-9, dot, underscore and dash, 1 to 200 characters, with no ., .. or .git segment."},
		{"a target that is not owner/name", ForgeGitHub, Target{System: ForgeGitHub, Path: "acme"},
			"qory run: A target is not owner/name, so the run did not start; the code is used\n  The path is acme, and a repository on github.com has a path of the form owner/name."},
	} {
		if _, _, err := c.at.Repositories(c.system); err == nil || err.Error() != c.want {
			t.Errorf("%s: %v\nwant %s", c.name, err, c.want)
		}
	}
}

// fetchReads fetches at, a run from a run code, into a new working folder, with the
// variable listing acme/extra, which no document lists, and returns the folder and the line.
func fetchReads(t *testing.T, at Target) (string, string) {
	t.Helper()
	at.FromCode = true
	dir := emptyFolder(t)
	line, err := fetchListAt(dir, "acme/extra,acme/shop", at)
	if err != nil {
		t.Fatal(err)
	}
	return dir, line
}

// exists reports whether the folder of a read target is in the working folder dir.
func readFetched(t *testing.T, dir, path string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, ".qory", "repositories", filepath.FromSlash(path), ".git"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

// TestReadTargetsWithNoTargetAreFetchedReadOnly is documents with read targets and no
// target: each read target is under .qory/repositories, the working folder stays
// an empty repository with no remote, and the variable's list is not fetched.
func TestReadTargetsWithNoTargetAreFetchedReadOnly(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	for _, path := range []string{"acme/api", "acme/docs", "acme/extra"} {
		remote(t, base, path, "main", nil)
	}
	dir, line := fetchReads(t, Target{Reads: []Read{readOn("acme/api"), readOn("acme/docs")}})
	if want := "qory run: fetched 2 read-only repositories in Xs"; line != want {
		t.Errorf("the line is %q, want %q", line, want)
	}
	if !readFetched(t, dir, "acme/api") || !readFetched(t, dir, "acme/docs") || readFetched(t, dir, "acme/extra") {
		t.Error("the read targets are not the ones fetched")
	}
	if got := run(t, dir, "remote"); got != "" {
		t.Errorf("the working folder has the remote %s", got)
	}
	if got := run(t, dir, "rev-list", "-n", "1", "--all"); got != "" {
		t.Errorf("the working folder has the commit %s", got)
	}
	if got := run(t, dir, "status", "--porcelain", "--untracked-files=all"); got != "" {
		t.Errorf("git status of the working folder lists %s", got)
	}
}

// TestDocumentsWithNeitherFetchNothing is documents with no target and no read target:
// nothing is fetched, whatever the variable lists.
func TestDocumentsWithNeitherFetchNothing(t *testing.T) {
	needGit(t)
	remote(t, t.TempDir(), "acme/extra", "main", nil)
	dir := emptyFolder(t)
	fetched, err := RunAt(t.Context(), dir, os.Environ(), env("git.example.com", "acme/extra"), Target{FromCode: true}, testProxy, &strings.Builder{})
	if err != nil || fetched {
		t.Errorf("fetched %v, %v", fetched, err)
	}
}

// TestOneAddressInTwoCollectionsIsFetchedOnce is a read target listed twice and one that is
// the target: the target goes into the working folder and the other read target
// once under .qory/repositories.
func TestOneAddressInTwoCollectionsIsFetchedOnce(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	for _, path := range []string{"acme/shop", "acme/api"} {
		remote(t, base, path, "main", nil)
	}
	dir, line := fetchReads(t, Target{System: "git.example.com", Path: "acme/shop", Reads: []Read{readOn("acme/api"), readOn("acme/shop"), readOn("acme/api")}})
	if !strings.HasSuffix(line, " and 1 read-only repository in Xs") {
		t.Errorf("the line is %q", line)
	}
	if !readFetched(t, dir, "acme/api") || readFetched(t, dir, "acme/shop") {
		t.Error("the read targets are not the ones fetched")
	}
}

// TestAReadTargetOnAnotherSystemStopsBeforeAnyFetch is a read target on another host: the
// run stops with the cause, and the working folder has no remote.
func TestAReadTargetOnAnotherSystemStopsBeforeAnyFetch(t *testing.T) {
	needGit(t)
	remote(t, t.TempDir(), "acme/shop", "main", nil)
	dir := emptyFolder(t)
	at := Target{System: "git.example.com", Path: "acme/shop", Reads: []Read{{System: "other.example.com", Path: "acme/api"}}, FromCode: true}
	_, err := fetchListAt(dir, "acme/shop", at)
	if err == nil || !strings.HasPrefix(err.Error(), "qory run: A read target is on another host, so the run did not start; the code is used\n") {
		t.Errorf("%v", err)
	}
	if got := run(t, dir, "remote"); got != "" {
		t.Errorf("the working folder has the remote %s", got)
	}
}

// TestTheTargetAloneIsPrebuilt is [RunPrebuild] of a target with read targets, and a
// variable that lists another repository: the target's repository alone is fetched.
func TestTheTargetAloneIsPrebuilt(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	for _, path := range []string{"acme/shop", "acme/api"} {
		remote(t, base, path, "main", nil)
	}
	dir := emptyFolder(t)
	var report strings.Builder
	at := Target{System: "git.example.com", Path: "acme/shop", Reads: []Read{readOn("acme/api")}}
	fetched, err := RunPrebuild(t.Context(), dir, os.Environ(), env("git.example.com", "acme/api,acme/shop"), at, testProxy, &report)
	if err != nil || !fetched {
		t.Fatalf("fetched %v, %v", fetched, err)
	}
	if line := times.ReplaceAllString(strings.TrimSpace(report.String()), " in Xs"); !commits.MatchString(line) || !strings.HasPrefix(line, "qory run: fetched acme/shop at ") || strings.Contains(line, "further") {
		t.Errorf("the line is %q", line)
	}
	if readFetched(t, dir, "acme/api") {
		t.Error("the prebuild fetched a read target")
	}
	if got := run(t, dir, "remote", "get-url", "origin"); !strings.HasSuffix(got, "/acme/shop.git") {
		t.Errorf("the working folder is of %s", got)
	}
}

// readsOnBranch fetches acme/shop as the target and acme/api and acme/docs as read targets
// of a run from a run code, on the work branch, and checks the branch out. Each remote
// takes a push. It returns the working folder, the branch and the folder of the remotes.
func readsOnBranch(t *testing.T, entries func(base string) []Repository) (string, *WorkBranch, string) {
	t.Helper()
	needGit(t)
	base := t.TempDir()
	version, _ := movedRemote(t, base, "acme/shop")
	for _, path := range []string{"acme/api", "acme/docs"} {
		movedRemote(t, base, path)
	}
	b := &WorkBranch{Ref: testRef}
	if entries != nil {
		b.Version = entries(base)
	}
	dir := emptyFolder(t)
	at := Target{System: "git.example.com", Path: "acme/shop", Version: version, Branch: b, Reads: []Read{readOn("acme/api"), readOn("acme/docs")}, FromCode: true}
	if _, err := fetchListAt(dir, "acme/extra", at); err != nil {
		t.Fatal(err)
	}
	if _, err := switchTo(dir, b); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"acme/shop", "acme/api", "acme/docs"} {
		run(t, filepath.Join(base, filepath.FromSlash(path)+".git"), "config", "http.receivepack", "true")
	}
	return dir, b, base
}

// TestAReadTargetGetsTheWorkBranchCommit is a read target whose commit the work branch's
// version lists: it is at that commit after the switch, and it is read-only.
func TestAReadTargetGetsTheWorkBranchCommit(t *testing.T) {
	var apiCommit string
	dir, b, _ := readsOnBranch(t, func(base string) []Repository {
		first := run(t, filepath.Join(base, "acme", "api.git"), "rev-list", "--max-parents=0", "main")
		apiCommit = branchCommit(t, base, "acme/api", first, func(work string) {
			write(t, filepath.Join(work, "qory.yaml"), "branch\n")
		})
		return versionOf("acme/api", apiCommit)
	})
	api := filepath.Join(dir, ".qory", "repositories", "acme", "api")
	if got := run(t, api, "rev-parse", "HEAD"); got != apiCommit {
		t.Errorf("api HEAD = %s, want the branch's commit %s", got, apiCommit)
	}
	if got := run(t, api, "symbolic-ref", "--short", "HEAD"); got != testRef {
		t.Errorf("api is on %s", got)
	}
	for i, r := range b.Repositories {
		if r.ReadOnly != (i > 0) {
			t.Errorf("repository %s: ReadOnly %v", r.Path, r.ReadOnly)
		}
	}
}

// TestOnlyTheTargetIsPushed is a run whose read-only checkouts have a new commit, with the
// target changed too: the push moves the target alone, the read targets' remotes have no
// work branch, and the version that the check gets and that Push returns has the target
// alone. Without the skip of read targets in [Push], the read targets would land, since
// their remotes take the push.
func TestOnlyTheTargetIsPushed(t *testing.T) {
	dir, b, base := readsOnBranch(t, nil)
	write(t, filepath.Join(dir, "change.txt"), "target\n")
	for _, path := range []string{"acme/api", "acme/docs"} {
		folder := filepath.Join(dir, ".qory", "repositories", filepath.FromSlash(path))
		write(t, filepath.Join(folder, "change.txt"), "read\n")
		run(t, folder, "add", "-A")
		run(t, folder, "commit", "--quiet", "-m", "agent")
	}
	var checked []Repository
	got, lines, err := pushChecked(dir, b, true, func(version []Repository) error {
		checked = version
		return nil
	})
	if err != nil {
		t.Fatalf("%v\n%s", err, lines)
	}
	if strings.Count(lines, "qory run: pushed ") != 1 || !strings.Contains(lines, "qory run: pushed "+testRef+" to acme/shop at ") {
		t.Errorf("the lines are %q", lines)
	}
	for _, path := range []string{"acme/api", "acme/docs"} {
		if refs := run(t, filepath.Join(base, filepath.FromSlash(path)+".git"), "for-each-ref", "refs/heads/"+testRef); refs != "" {
			t.Errorf("%s has the work branch: %s", path, refs)
		}
	}
	if head := run(t, filepath.Join(base, "acme", "shop.git"), "rev-parse", testRef); head != run(t, dir, "rev-parse", "HEAD") {
		t.Errorf("the target's branch is at %s", head)
	}
	for name, version := range map[string][]Repository{"checked": checked, "returned": got.Repositories} {
		if len(version) != 1 || version[0].Path != "acme/shop" {
			t.Errorf("the %s version is %+v, want acme/shop alone", name, version)
		}
	}
	if landed := b.Landed(); len(landed) != 1 || landed[0].Path != "acme/shop" {
		t.Errorf("landed %+v", landed)
	}
}

// TestAReadCheckoutStopsNoPush is a read-only checkout with an index lock, with the stop of
// the agent not proven: no check of the read target stops the push of the target.
func TestAReadCheckoutStopsNoPush(t *testing.T) {
	dir, b, _ := readsOnBranch(t, nil)
	write(t, filepath.Join(dir, "change.txt"), "target\n")
	write(t, filepath.Join(dir, ".qory", "repositories", "acme", "docs", ".git", "index.lock"), "")
	if _, lines, err := pushWith(dir, b, false); err != nil || !strings.Contains(lines, "to acme/shop at ") {
		t.Errorf("%v\n%s", err, lines)
	}
}

// TestAReadTargetsStartStaysOutOfTheVersion is a read target with a commit that a run of
// the work item pushed after its version: the fetch starts there and adds it to the
// branch's version, and the version of the push has the target alone.
func TestAReadTargetsStartStaysOutOfTheVersion(t *testing.T) {
	dir, b, _ := readsOnBranch(t, nil)
	b.Version = append(b.Version, Repository{System: "git.example.com", Path: "acme/api", Commit: strings.Repeat("a", 40)})
	write(t, filepath.Join(dir, "change.txt"), "target\n")
	got, lines, err := pushOf(dir, b)
	if err != nil {
		t.Fatalf("%v\n%s", err, lines)
	}
	if len(got.Repositories) != 1 || got.Repositories[0].Path != "acme/shop" {
		t.Errorf("the version is %+v, want acme/shop alone", got.Repositories)
	}
}

// TestAFolderWithFilesStopsReadTargetsAlone is documents with read targets and no target,
// in a working folder that is not empty: with a .git and a file, and with a file alone. The
// stop is about the read-only repositories, and nothing is fetched.
func TestAFolderWithFilesStopsReadTargetsAlone(t *testing.T) {
	needGit(t)
	remote(t, t.TempDir(), "acme/api", "main", nil)
	at := Target{FromCode: true, Reads: []Read{readOn("acme/api")}}
	for name, c := range map[string]struct {
		dir  func() string
		want string
	}{
		"a git working tree with a file": {func() string { return emptyFolder(t) }, textNotEmptyReads},
		"a folder with a file":           {t.TempDir, textNotTreeReads},
	} {
		t.Run(name, func(t *testing.T) {
			dir := c.dir()
			write(t, filepath.Join(dir, "notes.txt"), "notes\n")
			_, err := fetchListAt(dir, "", at)
			if err == nil || err.Error() != c.want {
				t.Fatalf("the error is %v, want %q", err, c.want)
			}
			if readFetched(t, dir, "acme/api") {
				t.Error("acme/api is fetched")
			}
		})
	}
}

// TestTheSetupTakesTheLongestListOfARunCode is [Prepare] with the list qory gives the
// setup container of a run code with a target and 20 read targets, each path of 200
// characters: the check passes. A list one byte longer stops, with the bound in the line.
func TestTheSetupTakesTheLongestListOfARunCode(t *testing.T) {
	at := Target{FromCode: true, System: "git.example.com", Path: longPath('z')}
	for i := range contracts.MaxReadTargets {
		at.Reads = append(at.Reads, readOn(longPath(rune('a'+i))))
	}
	paths, target, err := at.Repositories("git.example.com")
	if err != nil || !target || len(paths) != contracts.MaxReadTargets+1 {
		t.Fatalf("the list is %d paths, target %v: %v", len(paths), target, err)
	}
	list := strings.Join(paths, ",")
	if len(list) != maxSetupList {
		t.Fatalf("the list has %d bytes, want %d", len(list), maxSetupList)
	}
	listed, err := Prepare(t.Context(), t.TempDir(), "", env("git.example.com", list), io.Discard)
	if err != nil || !listed {
		t.Errorf("Prepare returned %v, %v; want the list taken", listed, err)
	}
	_, err = Prepare(t.Context(), t.TempDir(), "", env("git.example.com", list+"x"), io.Discard)
	if want := "qory run: The run's repository list is longer than 4220 bytes, so the setup did not start"; err == nil || err.Error() != want {
		t.Errorf("one byte more gave %v, want %q", err, want)
	}
	// A run configuration's list keeps the bound of 256 bytes at the fetch.
	if _, err := Check("git.example.com", list); err == nil || err.Error() != "qory run: shorten QORY_RUN_REPOSITORIES to 256 bytes or fewer" {
		t.Errorf("Check of %d bytes gave %v", len(list), err)
	}
}

// longPath is acme/ and a name of c, a repository path of [maxPath] characters.
func longPath(c rune) string {
	return "acme/" + strings.Repeat(string(c), maxPath-len("acme/"))
}

// TestARunCodesKeptRepositoriesStopsNameTheReadOnlyRepositories is [keepRepositories] on a
// working folder with a .qory file and on one with .qory/repositories: a run from a run
// code, with a target or none, stops with the cause alone about the read-only repositories,
// and any other run with the fix for its repository.
func TestARunCodesKeptRepositoriesStopsNameTheReadOnlyRepositories(t *testing.T) {
	qoryFile := func(dir string) { write(t, filepath.Join(dir, ".qory"), "a file\n") }
	kept := func(dir string) {
		if err := os.MkdirAll(filepath.Join(dir, ".qory", "repositories"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name  string
		setup func(string)
		path  string
		code  bool
		want  string
	}{
		{".qory a file, a run code with a target", qoryFile, "acme/shop", true, textQoryReads},
		{".qory a file, a run code with no target", qoryFile, "", true, textQoryReads},
		{".qory a file, another run", qoryFile, "acme/shop", false, fmt.Sprintf(textQoryNotDir, "acme/shop")},
		{"repositories kept, a run code with a target", kept, "acme/shop", true, textKeptReads},
		{"repositories kept, another run", kept, "acme/shop", false, fmt.Sprintf(textKept, "acme/shop")},
	} {
		dir := t.TempDir()
		c.setup(dir)
		if err := keepRepositories(dir, c.path, c.code); err == nil || err.Error() != c.want {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}
