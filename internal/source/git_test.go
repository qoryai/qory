package source_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	got, err := source.Resolve("/nowhere", src, false)
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
	got, err := source.Resolve("/nowhere", src, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Pin != short(second) {
		t.Fatalf("pin %q, want %s", got.Pin, short(second))
	}
	// The branch moves back to the first commit; the cache still holds the second.
	run(t, dir, "reset", "-q", "--hard", "v1")
	got, err = source.Resolve("/nowhere", src, false)
	if err != nil || got.Pin != short(second) {
		t.Fatalf("cached pin %q (%v), want %s", got.Pin, err, short(second))
	}
	before := got.Dir
	got, err = source.Resolve("/nowhere", src, true)
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
	if got, err := source.Resolve("/nowhere", src, false); err != nil || got.Pin != short(first) {
		t.Fatalf("offline pin %q (%v), want %s", got.Pin, err, short(first))
	}
	if _, err := source.Resolve("/nowhere", src, true); err == nil {
		t.Fatal("an update without the remote succeeded")
	}
	if got, err := source.Resolve("/nowhere", src, false); err != nil || got.Pin != short(first) {
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
	got, err := source.Resolve("/nowhere", profile.Source{Git: url, Ref: first}, false)
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
	_, err := source.Resolve("/nowhere", profile.Source{Git: url, Ref: "v9"}, false)
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
	_, err = source.Resolve("/nowhere", profile.Source{Git: url, Ref: "v1", Path: "layers/nope"}, false)
	if err == nil || errors.As(err, &fetch) || !strings.Contains(err.Error(), "has no layers/nope at v1") {
		t.Errorf("missing path: %v", err)
	}
	_, err = source.Resolve("/nowhere", profile.Source{Git: "file:///nowhere/at/all", Ref: "v1"}, false)
	if !errors.As(err, &fetch) {
		t.Errorf("missing remote: %v", err)
	}
}
