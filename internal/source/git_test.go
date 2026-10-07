package source_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/source"
	"github.com/qoryai/qory/internal/stack"
)

// remote makes a repository with a module under modules/core, a tag v1 on its first commit,
// and returns its file:// URL and the two commits' full ids. HOME points at a temporary
// directory, so the cache lands under the test and nothing reaches the machine's own.
func remote(t *testing.T) (url, first, second string) {
	t.Helper()
	hermetic(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", filepath.Join(os.Getenv("HOME"), ".cache"))
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "config", "user.name", "Test User")
	run(t, dir, "config", "user.email", "test@example.com")
	write(t, filepath.Join(dir, "modules", "core", "AGENTS.md"), "# Core v1\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "first")
	run(t, dir, "tag", "v1")
	first = head(t, dir)
	write(t, filepath.Join(dir, "modules", "core", "AGENTS.md"), "# Core v2\n")
	run(t, dir, "commit", "-q", "-am", "second")
	second = head(t, dir)
	// A shallow fetch of a commit needs the server to allow it; a file remote does.
	run(t, dir, "config", "uploadpack.allowReachableSHA1InWant", "true")
	return "file://" + dir, first, second
}

func head(t *testing.T, dir string) string {
	t.Helper()
	out, err := gitOut(dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// short is the twelve characters of a commit id the pin carries.
func short(id string) string { return id[:12] }

// traced runs f with git tracing to a file and returns the git commands every git process
// f started ran, one line each, such as "git ls-remote -- <url> refs/heads/main".
func traced(t *testing.T, f func()) []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trace")
	t.Setenv("GIT_TRACE", path)
	f()
	os.Setenv("GIT_TRACE", "0")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var commands []string
	for _, line := range strings.Split(string(data), "\n") {
		if _, command, ok := strings.Cut(line, "trace: built-in: "); ok {
			commands = append(commands, command)
		}
	}
	return commands
}

// count is how many of the traced commands are the git subcommand named.
func count(commands []string, sub string) int {
	n := 0
	for _, c := range commands {
		if strings.HasPrefix(c, "git "+sub+" ") {
			n++
		}
	}
	return n
}

// refsFile is the file under cache that records what ref resolved to, for the one source
// the cache holds.
func refsFile(t *testing.T, cache, ref string) string {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(cache, "*", "refs", ref+"-*"))
	if len(matches) != 1 {
		t.Fatalf("refs files for %s: %v", ref, matches)
	}
	return matches[0]
}

// TestResolveGitPinsTheRefToItsCommit fetches a tag and reports the commit as the pin, with
// the module's directory inside the clone.
func TestResolveGitPinsTheRefToItsCommit(t *testing.T) {
	url, first, _ := remote(t)
	src := stack.Source{Git: url, Ref: "v1", Path: "modules/core"}
	got, err := source.Resolve("/nowhere", src, source.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Pin != short(first) || got.Dirty {
		t.Errorf("pin %q dirty %v, want %s clean", got.Pin, got.Dirty, short(first))
	}
	data, err := os.ReadFile(filepath.Join(got.Dir, "AGENTS.md"))
	if err != nil || string(data) != "# Core v1\n" {
		t.Errorf("module at %s reads %q (%v)", got.Dir, data, err)
	}
	cache, _ := source.CacheDir()
	if !strings.HasPrefix(got.Dir, cache) {
		t.Errorf("clone %s is not under the cache %s", got.Dir, cache)
	}
	rel, _ := filepath.Rel(cache, got.Dir)
	if parts := strings.Split(rel, string(filepath.Separator)); len(parts) != 4 || parts[1] != first || parts[2] != "modules" || parts[3] != "core" {
		t.Errorf("clone %s is not <cache>/<url>/<commit>/<path>", got.Dir)
	}
}

// TestResolveGitFollowsABranch is a branch ref that moves on the remote: the next compose,
// without an update, lands on the new commit, and the clone of the old one stays as it
// was for a checkout composed from it.
func TestResolveGitFollowsABranch(t *testing.T) {
	url, first, second := remote(t)
	dir := strings.TrimPrefix(url, "file://")
	opts := source.Options{Cache: t.TempDir()}
	run(t, dir, "reset", "-q", "--hard", "v1")
	src := stack.Source{Git: url, Ref: "main", Path: "modules/core"}
	got, err := source.Resolve("/nowhere", src, opts)
	if err != nil || got.Pin != short(first) {
		t.Fatalf("pin %q (%v), want %s", got.Pin, err, short(first))
	}
	before := got.Dir
	run(t, dir, "reset", "-q", "--hard", second)
	got, err = source.Resolve("/nowhere", src, opts)
	if err != nil || got.Pin != short(second) || got.Warning != "" {
		t.Fatalf("after the push: %+v (%v), want %s", got, err, short(second))
	}
	if data, err := os.ReadFile(filepath.Join(got.Dir, "AGENTS.md")); err != nil || string(data) != "# Core v2\n" {
		t.Errorf("the new clone reads %q (%v)", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(before, "AGENTS.md")); err != nil || string(data) != "# Core v1\n" {
		t.Errorf("the earlier clone reads %q (%v) after the branch moved", data, err)
	}
}

// TestResolveGitMovesACheckoutPinnedToTheOldCommit is a second checkout whose report pins
// the branch's old commit: its next compose takes the new one and says what it had.
func TestResolveGitMovesACheckoutPinnedToTheOldCommit(t *testing.T) {
	url, first, second := remote(t)
	dir := strings.TrimPrefix(url, "file://")
	cache := t.TempDir()
	src := stack.Source{Git: url, Ref: "main"}
	run(t, dir, "reset", "-q", "--hard", "v1")
	a, err := source.Resolve("/nowhere", src, source.Options{Cache: cache})
	if err != nil || a.Pin != short(first) {
		t.Fatalf("a: pin %q (%v), want %s", a.Pin, err, short(first))
	}
	run(t, dir, "reset", "-q", "--hard", second)
	if _, err := source.Resolve("/nowhere", src, source.Options{Cache: cache}); err != nil {
		t.Fatal(err)
	}
	again, err := source.Resolve("/nowhere", src, source.Options{Cache: cache, Pin: a.Pin})
	if err != nil || again.Pin != short(second) || again.Previous != a.Pin {
		t.Fatalf("a again: %+v (%v), want %s moved from %s", again, err, short(second), a.Pin)
	}
	same, err := source.Resolve("/nowhere", src, source.Options{Cache: cache, Pin: again.Pin})
	if err != nil || same.Previous != "" {
		t.Errorf("a pin that did not move: %+v (%v), want no previous pin", same, err)
	}
}

// TestResolveGitKeepsATag is a tag after a new commit on main: it stays on its commit, and
// once its clone is cached it runs no git command, so it resolves with the remote gone
// and no warning.
func TestResolveGitKeepsATag(t *testing.T) {
	url, first, _ := remote(t)
	dir := strings.TrimPrefix(url, "file://")
	opts := source.Options{Cache: t.TempDir()}
	src := stack.Source{Git: url, Ref: "v1", Path: "modules/core"}
	if got, err := source.Resolve("/nowhere", src, opts); err != nil || got.Pin != short(first) {
		t.Fatalf("pin %q (%v), want %s", got.Pin, err, short(first))
	}
	write(t, filepath.Join(dir, "modules", "core", "AGENTS.md"), "# Core v3\n")
	run(t, dir, "commit", "-q", "-am", "third")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	var got source.Resolved
	var err error
	commands := traced(t, func() { got, err = source.Resolve("/nowhere", src, opts) })
	if err != nil || got.Pin != short(first) || got.Warning != "" {
		t.Fatalf("offline: %+v (%v), want %s and no warning", got, err, short(first))
	}
	if len(commands) != 0 {
		t.Errorf("a cached tag ran git: %v", commands)
	}
}

// TestResolveGitReadsACommitWithoutTheRemote pins a source by a full commit id: once its
// clone is cached, it resolves with the remote gone and runs no git command.
func TestResolveGitReadsACommitWithoutTheRemote(t *testing.T) {
	url, first, _ := remote(t)
	opts := source.Options{Cache: t.TempDir()}
	src := stack.Source{Git: url, Ref: first}
	if _, err := source.Resolve("/nowhere", src, opts); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(strings.TrimPrefix(url, "file://")); err != nil {
		t.Fatal(err)
	}
	var got source.Resolved
	var err error
	commands := traced(t, func() { got, err = source.Resolve("/nowhere", src, opts) })
	if err != nil || got.Pin != short(first) || got.Warning != "" {
		t.Fatalf("offline: %+v (%v), want %s", got, err, short(first))
	}
	if len(commands) != 0 {
		t.Errorf("a cached commit ran git: %v", commands)
	}
}

// TestResolveGitKeepsTheCacheOffline is a branch whose remote cannot be reached, gone or
// slower than the timeout: with a clone cached, the compose keeps it and warns, the
// checkout's pin first; with nothing cached, it fails with a FetchError as before.
func TestResolveGitKeepsTheCacheOffline(t *testing.T) {
	url, first, second := remote(t)
	dir := strings.TrimPrefix(url, "file://")
	cache := t.TempDir()
	src := stack.Source{Git: url, Ref: "main"}
	run(t, dir, "reset", "-q", "--hard", "v1")
	a, err := source.Resolve("/nowhere", src, source.Options{Cache: cache})
	if err != nil {
		t.Fatal(err)
	}
	run(t, dir, "reset", "-q", "--hard", second)
	if _, err := source.Resolve("/nowhere", src, source.Options{Cache: cache}); err != nil {
		t.Fatal(err)
	}
	got, err := source.Resolve("/nowhere", src, source.Options{Cache: cache, Timeout: time.Nanosecond})
	if want := url + "#main: could not reach the remote; kept " + short(second); err != nil || got.Pin != short(second) || got.Warning != want {
		t.Errorf("past the timeout: %+v (%v), want the warning %q", got, err, want)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	got, err = source.Resolve("/nowhere", src, source.Options{Cache: cache, Pin: a.Pin})
	if want := url + "#main: could not reach the remote; kept " + short(first); err != nil || got.Pin != short(first) || got.Warning != want || got.Previous != "" {
		t.Errorf("offline with a pin: %+v (%v), want the warning %q", got, err, want)
	}
	_, err = source.Resolve("/nowhere", src, source.Options{Cache: t.TempDir()})
	var fetch *source.FetchError
	if !errors.As(err, &fetch) || !strings.HasPrefix(err.Error(), "fetching "+url+"#main: ") {
		t.Errorf("offline with nothing cached: %v, want a FetchError", err)
	}
	if _, err := source.Resolve("/nowhere", src, source.Options{Cache: cache, Update: true}); !errors.As(err, &fetch) {
		t.Errorf("an update offline: %v, want a FetchError", err)
	}
}

// TestResolveGitMatchesABranchByItsFullName is a remote with refs/heads/feature/main: it
// never answers for main, before or after main is deleted.
func TestResolveGitMatchesABranchByItsFullName(t *testing.T) {
	url, first, second := remote(t)
	dir := strings.TrimPrefix(url, "file://")
	opts := source.Options{Cache: t.TempDir()}
	run(t, dir, "branch", "feature/main", first)
	src := stack.Source{Git: url, Ref: "main"}
	if got, err := source.Resolve("/nowhere", src, opts); err != nil || got.Pin != short(second) {
		t.Fatalf("pin %q (%v), want %s", got.Pin, err, short(second))
	}
	run(t, dir, "checkout", "-q", "feature/main")
	run(t, dir, "branch", "-q", "-D", "main")
	var gone *source.GoneError
	if _, err := source.Resolve("/nowhere", src, opts); !errors.As(err, &gone) {
		t.Errorf("main without main: %v, want a GoneError", err)
	}
}

// TestResolveGitTakesTheTagOfANameThatIsBoth is a name that is a tag and a branch: it
// resolves to the tag, as git fetch does.
func TestResolveGitTakesTheTagOfANameThatIsBoth(t *testing.T) {
	url, first, _ := remote(t)
	dir := strings.TrimPrefix(url, "file://")
	run(t, dir, "tag", "-a", "-m", "release", "main", first)
	got, err := source.Resolve("/nowhere", stack.Source{Git: url, Ref: "main"}, source.Options{Cache: t.TempDir()})
	if err != nil || got.Pin != short(first) {
		t.Errorf("pin %q (%v), want the tag's %s", got.Pin, err, short(first))
	}
}

// TestResolveGitLooksUpOnceForManyModules is two modules from one repository at main in
// one compose, sharing a memo: the remote is asked once, and a push between the two
// leaves both on one commit.
func TestResolveGitLooksUpOnceForManyModules(t *testing.T) {
	url, _, second := remote(t)
	dir := strings.TrimPrefix(url, "file://")
	opts := source.Options{Cache: t.TempDir(), Memo: source.NewMemo()}
	var a, b source.Resolved
	var errA, errB error
	commands := traced(t, func() {
		a, errA = source.Resolve("/nowhere", stack.Source{Git: url, Ref: "main", Path: "modules/core"}, opts)
	})
	write(t, filepath.Join(dir, "modules", "core", "AGENTS.md"), "# Core v3\n")
	run(t, dir, "commit", "-q", "-am", "third")
	commands = append(commands, traced(t, func() {
		b, errB = source.Resolve("/nowhere", stack.Source{Git: url, Ref: "main"}, opts)
	})...)
	if errA != nil || errB != nil {
		t.Fatal(errA, errB)
	}
	if a.Pin != short(second) || b.Pin != a.Pin {
		t.Errorf("pins %s and %s, want both %s", a.Pin, b.Pin, short(second))
	}
	if n := count(commands, "ls-remote"); n != 1 {
		t.Errorf("ls-remote ran %d times, want once: %v", n, commands)
	}
}

// TestResolveGitFailsForADeletedBranch is a branch deleted on the remote: the compose
// fails with the gone error, although the cache holds its clone.
func TestResolveGitFailsForADeletedBranch(t *testing.T) {
	url, _, _ := remote(t)
	dir := strings.TrimPrefix(url, "file://")
	opts := source.Options{Cache: t.TempDir()}
	run(t, dir, "branch", "topic")
	src := stack.Source{Git: url, Ref: "topic"}
	first, err := source.Resolve("/nowhere", src, opts)
	if err != nil {
		t.Fatal(err)
	}
	run(t, dir, "branch", "-q", "-D", "topic")
	_, err = source.Resolve("/nowhere", src, source.Options{Cache: opts.Cache, Pin: first.Pin})
	var gone *source.GoneError
	want := url + " has no branch or tag topic; to keep the old harness, set ref to a commit id"
	if !errors.As(err, &gone) || err.Error() != want {
		t.Errorf("err = %v\nwant %s", err, want)
	}
}

// TestResolveGitReadsTheOldRefsFile is a refs file written before it recorded the kind,
// the commit alone: a branch offline keeps the commit it names, and a tag rewrites it
// with its kind.
func TestResolveGitReadsTheOldRefsFile(t *testing.T) {
	url, first, second := remote(t)
	cache := t.TempDir()
	opts := source.Options{Cache: cache}
	if _, err := source.Resolve("/nowhere", stack.Source{Git: url, Ref: "v1"}, opts); err != nil {
		t.Fatal(err)
	}
	if got, err := source.Resolve("/nowhere", stack.Source{Git: url, Ref: "main"}, opts); err != nil || got.Pin != short(second) {
		t.Fatalf("main: pin %q (%v), want %s", got.Pin, err, short(second))
	}
	tag, branch := refsFile(t, cache, "v1"), refsFile(t, cache, "main")
	for path, want := range map[string]string{tag: "tag " + first + "\n", branch: "branch " + second + "\n"} {
		if data, err := os.ReadFile(path); err != nil || string(data) != want {
			t.Errorf("%s reads %q (%v), want %q", filepath.Base(path), data, err, want)
		}
	}
	write(t, tag, first+"\n")
	if got, err := source.Resolve("/nowhere", stack.Source{Git: url, Ref: "v1"}, opts); err != nil || got.Pin != short(first) {
		t.Fatalf("v1: pin %q (%v), want %s", got.Pin, err, short(first))
	}
	if data, _ := os.ReadFile(tag); string(data) != "tag "+first+"\n" {
		t.Errorf("the old refs file of a tag reads %q after a compose", data)
	}
	write(t, branch, first+"\n")
	if err := os.RemoveAll(strings.TrimPrefix(url, "file://")); err != nil {
		t.Fatal(err)
	}
	got, err := source.Resolve("/nowhere", stack.Source{Git: url, Ref: "main"}, opts)
	if err != nil || got.Pin != short(first) || got.Warning == "" {
		t.Errorf("main offline: %+v (%v), want the old file's %s and a warning", got, err, short(first))
	}
}

// TestResolveGitFetchesACommit pins a source by the commit itself.
func TestResolveGitFetchesACommit(t *testing.T) {
	url, first, _ := remote(t)
	got, err := source.Resolve("/nowhere", stack.Source{Git: url, Ref: first}, source.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Pin != short(first) {
		t.Errorf("pin %q, want %s", got.Pin, short(first))
	}
	if _, err := os.Stat(filepath.Join(got.Dir, "modules", "core", "AGENTS.md")); err != nil {
		t.Errorf("clone root is not the module: %v", err)
	}
}

// TestResolveGitRefuses covers a ref the remote does not have, an abbreviated commit it
// cannot fetch, a path the repository does not hold, and a remote that is not there, and
// checks that a failed resolve leaves no clone behind to be read as an empty module next
// time.
func TestResolveGitRefuses(t *testing.T) {
	url, _, _ := remote(t)
	var fetch *source.FetchError
	var gone *source.GoneError
	_, err := source.Resolve("/nowhere", stack.Source{Git: url, Ref: "v9"}, source.Options{})
	if !errors.As(err, &gone) || err.Error() != url+" has no branch or tag v9; to keep the old harness, set ref to a commit id" {
		t.Errorf("missing ref: %v", err)
	}
	_, err = source.Resolve("/nowhere", stack.Source{Git: url, Ref: "abc1234"}, source.Options{})
	if !errors.As(err, &fetch) || !strings.HasPrefix(err.Error(), "fetching "+url+"#abc1234: ") {
		t.Errorf("missing abbreviated commit: %v", err)
	}
	cache, _ := source.CacheDir()
	if entries, _ := os.ReadDir(cache); len(entries) != 0 {
		var left []string
		for _, e := range entries {
			sub, _ := os.ReadDir(filepath.Join(cache, e.Name()))
			for _, s := range sub {
				left = append(left, e.Name()+"/"+s.Name())
			}
		}
		if len(left) != 0 {
			t.Errorf("a failed fetch left %v in the cache", left)
		}
	}
	_, err = source.Resolve("/nowhere", stack.Source{Git: url, Ref: "v1", Path: "modules/nope"}, source.Options{})
	if err == nil || errors.As(err, &fetch) || !strings.Contains(err.Error(), "has no modules/nope at v1") {
		t.Errorf("missing path: %v", err)
	}
	_, err = source.Resolve("/nowhere", stack.Source{Git: "file:///nowhere/at/all", Ref: "v1"}, source.Options{})
	if !errors.As(err, &fetch) {
		t.Errorf("missing remote: %v", err)
	}
}

// TestResolveGitTakesTheRefAsARef is a stack a repository carries: a ref written as a git
// option is a ref the remote does not have, never an option git acts on, and -q is not
// --quiet.
func TestResolveGitTakesTheRefAsARef(t *testing.T) {
	url, _, _ := remote(t)
	marker := filepath.Join(t.TempDir(), "ran")
	var gone *source.GoneError
	for _, ref := range []string{"--upload-pack=touch " + marker + " #", "-q"} {
		_, err := source.Resolve("/nowhere", stack.Source{Git: url, Ref: ref}, source.Options{})
		if !errors.As(err, &gone) {
			t.Errorf("ref %q: err = %v, want a GoneError", ref, err)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a ref written as --upload-pack ran a command")
	}
}

// TestResolveGitFetchesOnceForManyComposes is six composes fetching one source at the same
// moment: every one gets the commit, each asks the remote once, and the cache holds one
// clone.
func TestResolveGitFetchesOnceForManyComposes(t *testing.T) {
	url, _, second := remote(t)
	src := stack.Source{Git: url, Ref: "main", Path: "modules/core"}
	results := make(chan error, 6)
	commands := traced(t, func() {
		for i := 0; i < 6; i++ {
			go func() {
				got, err := source.Resolve("/nowhere", src, source.Options{})
				if err == nil && got.Pin != short(second) {
					err = errors.New("pin " + got.Pin)
				}
				results <- err
			}()
		}
		for i := 0; i < 6; i++ {
			if err := <-results; err != nil {
				t.Errorf("compose %d: %v", i, err)
			}
		}
	})
	if n := count(commands, "ls-remote"); n != 6 {
		t.Errorf("ls-remote ran %d times for six composes, want six", n)
	}
	cache, _ := source.CacheDir()
	sources, _ := os.ReadDir(cache)
	if len(sources) != 1 {
		t.Fatalf("cache holds %d sources", len(sources))
	}
	entries, _ := os.ReadDir(filepath.Join(cache, sources[0].Name()))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 2 {
		t.Errorf("the source holds %v, want one clone and refs", names)
	}
}

// TestResolveGitRefusesAPathThatLeavesTheClone is a repository whose modules/link is a
// symlink out of the repository: the module is refused.
func TestResolveGitRefusesAPathThatLeavesTheClone(t *testing.T) {
	url, _, _ := remote(t)
	dir := strings.TrimPrefix(url, "file://")
	if err := os.Symlink("/", filepath.Join(dir, "modules", "link")); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "link")
	_, err := source.Resolve("/nowhere", stack.Source{Git: url, Ref: "main", Path: "modules/link"}, source.Options{})
	if err == nil || !strings.Contains(err.Error(), "links outside the repository") {
		t.Errorf("err = %v", err)
	}
}

// TestResolveStopsAGitCommandAtTheTimeout is a fetch with a timeout already over: git is
// stopped, and the error is a FetchError that names the time given.
func TestResolveStopsAGitCommandAtTheTimeout(t *testing.T) {
	url, _, _ := remote(t)
	_, err := source.Resolve("/nowhere", stack.Source{Git: url, Ref: "v1"}, source.Options{Timeout: time.Nanosecond})
	var fetch *source.FetchError
	if !errors.As(err, &fetch) || !strings.Contains(err.Error(), "ran past 1ns and was stopped") {
		t.Fatalf("err = %v, want a FetchError naming the timeout", err)
	}
}
