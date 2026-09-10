package source_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/profile"
	"github.com/qoryai/qory/internal/source"
)

// remote makes a repository with a layer under layers/core, a tag v1 on its first commit,
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
	write(t, filepath.Join(dir, "layers", "core", "AGENTS.md"), "# Core v1\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "first")
	run(t, dir, "tag", "v1")
	first = head(t, dir)
	write(t, filepath.Join(dir, "layers", "core", "AGENTS.md"), "# Core v2\n")
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

// TestResolveGitPinsTheRefToItsCommit fetches a tag and reports the commit as the pin, with
// the layer's directory inside the clone.
func TestResolveGitPinsTheRefToItsCommit(t *testing.T) {
	url, first, _ := remote(t)
	src := profile.Source{Git: url, Ref: "v1", Path: "layers/core"}
	got, err := source.Resolve("/nowhere", src, source.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Pin != short(first) || got.Dirty {
		t.Errorf("pin %q dirty %v, want %s clean", got.Pin, got.Dirty, short(first))
	}
	data, err := os.ReadFile(filepath.Join(got.Dir, "AGENTS.md"))
	if err != nil || string(data) != "# Core v1\n" {
		t.Errorf("layer at %s reads %q (%v)", got.Dir, data, err)
	}
	cache, _ := source.CacheDir()
	if !strings.HasPrefix(got.Dir, cache) {
		t.Errorf("clone %s is not under the cache %s", got.Dir, cache)
	}
	rel, _ := filepath.Rel(cache, got.Dir)
	if parts := strings.Split(rel, string(filepath.Separator)); len(parts) != 4 || parts[1] != first || parts[2] != "layers" || parts[3] != "core" {
		t.Errorf("clone %s is not <cache>/<url>/<commit>/<path>", got.Dir)
	}
}

// TestResolveGitReadsTheCacheUntilUpdate is a branch ref: the cached clone serves the
// second compose without the network, and --update moves it.
func TestResolveGitReadsTheCacheUntilUpdate(t *testing.T) {
	url, first, second := remote(t)
	dir := strings.TrimPrefix(url, "file://")
	src := profile.Source{Git: url, Ref: "main", Path: "layers/core"}
	got, err := source.Resolve("/nowhere", src, source.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Pin != short(second) {
		t.Fatalf("pin %q, want %s", got.Pin, short(second))
	}
	// The branch moves back to the first commit; the cache still holds the second.
	run(t, dir, "reset", "-q", "--hard", "v1")
	got, err = source.Resolve("/nowhere", src, source.Options{})
	if err != nil || got.Pin != short(second) {
		t.Fatalf("cached pin %q (%v), want %s", got.Pin, err, short(second))
	}
	before := got.Dir
	got, err = source.Resolve("/nowhere", src, source.Options{Update: true})
	if err != nil || got.Pin != short(first) {
		t.Fatalf("updated pin %q (%v), want %s", got.Pin, err, short(first))
	}
	// The clone the earlier compose linked into is still there, unchanged: another
	// checkout composed from it keeps reading the commit it was composed from.
	if data, err := os.ReadFile(filepath.Join(before, "AGENTS.md")); err != nil || string(data) != "# Core v2\n" {
		t.Errorf("the earlier clone reads %q (%v) after an update", data, err)
	}
	// Without the remote, the cache alone serves the compose, and an update that cannot
	// fetch fails without touching what is there.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if got, err := source.Resolve("/nowhere", src, source.Options{}); err != nil || got.Pin != short(first) {
		t.Fatalf("offline pin %q (%v), want %s", got.Pin, err, short(first))
	}
	if _, err := source.Resolve("/nowhere", src, source.Options{Update: true}); err == nil {
		t.Fatal("an update without the remote succeeded")
	}
	if got, err := source.Resolve("/nowhere", src, source.Options{}); err != nil || got.Pin != short(first) {
		t.Fatalf("pin after a failed update %q (%v), want %s", got.Pin, err, short(first))
	}
	if entries, _ := os.ReadDir(filepath.Dir(filepath.Dir(filepath.Dir(got.Dir)))); len(entries) != 3 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the source's cache holds %v, want the two clones and refs", names)
	}
}

// TestResolveGitFetchesACommit pins a source by the commit itself.
func TestResolveGitFetchesACommit(t *testing.T) {
	url, first, _ := remote(t)
	got, err := source.Resolve("/nowhere", profile.Source{Git: url, Ref: first}, source.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Pin != short(first) {
		t.Errorf("pin %q, want %s", got.Pin, short(first))
	}
	if _, err := os.Stat(filepath.Join(got.Dir, "layers", "core", "AGENTS.md")); err != nil {
		t.Errorf("clone root is not the layer: %v", err)
	}
}

// TestResolveGitRefuses covers a ref the remote does not have, a path the repository does
// not hold, and a remote that is not there, and checks that a failed fetch leaves no clone
// behind to be read as an empty layer next time.
func TestResolveGitRefuses(t *testing.T) {
	url, _, _ := remote(t)
	var fetch *source.FetchError
	_, err := source.Resolve("/nowhere", profile.Source{Git: url, Ref: "v9"}, source.Options{})
	if !errors.As(err, &fetch) || !strings.HasPrefix(err.Error(), "fetching "+url+"#v9: ") {
		t.Errorf("missing ref: %v", err)
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
	_, err = source.Resolve("/nowhere", profile.Source{Git: url, Ref: "v1", Path: "layers/nope"}, source.Options{})
	if err == nil || errors.As(err, &fetch) || !strings.Contains(err.Error(), "has no layers/nope at v1") {
		t.Errorf("missing path: %v", err)
	}
	_, err = source.Resolve("/nowhere", profile.Source{Git: "file:///nowhere/at/all", Ref: "v1"}, source.Options{})
	if !errors.As(err, &fetch) {
		t.Errorf("missing remote: %v", err)
	}
}

// TestResolveGitTakesTheRefAsARef is a profile a repository carries: a ref written as a git
// option is a ref git cannot find, never an option git acts on, and -q is not --quiet.
func TestResolveGitTakesTheRefAsARef(t *testing.T) {
	url, _, _ := remote(t)
	marker := filepath.Join(t.TempDir(), "ran")
	var fetch *source.FetchError
	for _, ref := range []string{"--upload-pack=touch " + marker + " #", "-q"} {
		_, err := source.Resolve("/nowhere", profile.Source{Git: url, Ref: ref}, source.Options{})
		if !errors.As(err, &fetch) {
			t.Errorf("ref %q: err = %v, want a FetchError", ref, err)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a ref written as --upload-pack ran a command")
	}
}

// TestResolveGitKeepsTheCheckoutsPin is two checkouts on one branch: the second's update
// moves the ref, and the first, composing again with its own pin, stays where it was.
func TestResolveGitKeepsTheCheckoutsPin(t *testing.T) {
	url, first, second := remote(t)
	dir := strings.TrimPrefix(url, "file://")
	src := profile.Source{Git: url, Ref: "main"}
	run(t, dir, "reset", "-q", "--hard", "v1")
	a, err := source.Resolve("/nowhere", src, source.Options{})
	if err != nil || a.Pin != short(first) {
		t.Fatalf("a: pin %q (%v), want %s", a.Pin, err, short(first))
	}
	run(t, dir, "reset", "-q", "--hard", second)
	b, err := source.Resolve("/nowhere", src, source.Options{Update: true})
	if err != nil || b.Pin != short(second) {
		t.Fatalf("b: pin %q (%v), want %s", b.Pin, err, short(second))
	}
	again, err := source.Resolve("/nowhere", src, source.Options{Pin: a.Pin})
	if err != nil || again.Pin != a.Pin || again.Dir != a.Dir {
		t.Fatalf("a again: %+v (%v), want its own pin %s", again, err, a.Pin)
	}
	// A pin whose clone is gone falls back to the ref's last resolution.
	if err := os.RemoveAll(a.Dir); err != nil {
		t.Fatal(err)
	}
	fresh, err := source.Resolve("/nowhere", src, source.Options{Pin: a.Pin})
	if err != nil || fresh.Pin != b.Pin {
		t.Fatalf("a without its clone: %+v (%v), want %s", fresh, err, b.Pin)
	}
}

// TestResolveGitFetchesOnceForManyComposes is six composes fetching one source at the same
// moment: every one gets the commit, and the cache holds one clone.
func TestResolveGitFetchesOnceForManyComposes(t *testing.T) {
	url, _, second := remote(t)
	src := profile.Source{Git: url, Ref: "main", Path: "layers/core"}
	results := make(chan error, 6)
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

// TestResolveGitRefusesAPathThatLeavesTheClone is a repository whose layers/link is a
// symlink out of the repository: the layer is refused.
func TestResolveGitRefusesAPathThatLeavesTheClone(t *testing.T) {
	url, _, _ := remote(t)
	dir := strings.TrimPrefix(url, "file://")
	if err := os.Symlink("/", filepath.Join(dir, "layers", "link")); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "link")
	_, err := source.Resolve("/nowhere", profile.Source{Git: url, Ref: "main", Path: "layers/link"}, source.Options{})
	if err == nil || !strings.Contains(err.Error(), "links outside the repository") {
		t.Errorf("err = %v", err)
	}
}

// TestResolveStopsAGitCommandAtTheTimeout is a fetch with a timeout already over: git is
// stopped, and the error is a FetchError that names the time given.
func TestResolveStopsAGitCommandAtTheTimeout(t *testing.T) {
	url, _, _ := remote(t)
	_, err := source.Resolve("/nowhere", profile.Source{Git: url, Ref: "v1"}, source.Options{Timeout: time.Nanosecond})
	var fetch *source.FetchError
	if !errors.As(err, &fetch) || !strings.Contains(err.Error(), "ran past 1ns and was stopped") {
		t.Fatalf("err = %v, want a FetchError naming the timeout", err)
	}
}
