package source_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/places"
	"github.com/qoryai/qory/internal/source"
	"github.com/qoryai/qory/internal/stack"
)

// recordedWithAKey records root as a walled run's checkout, then adds a key the
// settings check refuses.
func recordedWithAKey(t *testing.T, root string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	hostgit.Forget()
	t.Cleanup(hostgit.Forget)
	if err := hostgit.Record(context.Background(), root, places.KindCheckout, "run-1"); err != nil {
		t.Fatal(err)
	}
	run(t, root, "config", "alias.x", "log")
}

// refused fails the test unless err is the settings check refusing alias.x.
func refused(t *testing.T, err error) {
	t.Helper()
	var r *hostgit.RefusedError
	if !errors.As(err, &r) || r.Key != "alias.x" {
		t.Fatalf("got %v, want the refusal of alias.x", err)
	}
}

// TestAPathSourceInARefusedRepositoryIsRefused returns the refusal from the dirty mark of
// a path source, in place of reading it as clean.
func TestAPathSourceInARefusedRepositoryIsRefused(t *testing.T) {
	hermetic(t)
	root, err := filepath.EvalSymlinks(committedRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	recordedWithAKey(t, root)
	_, err = source.Resolve(root, stack.Source{Path: "modules/core"}, source.Options{})
	refused(t, err)
}

// TestAGitSourceWhoseCacheIsInARefusedRepositoryIsRefused returns the refusal from the
// lookup on the remote, in place of a FetchError, when the cache lies in a recorded
// repository.
func TestAGitSourceWhoseCacheIsInARefusedRepositoryIsRefused(t *testing.T) {
	url, _, _ := remote(t)
	root, err := filepath.EvalSymlinks(committedRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	recordedWithAKey(t, root)
	_, err = source.Resolve(root, stack.Source{Git: url, Ref: "main", Path: "modules/core"}, source.Options{Cache: filepath.Join(root, "cache")})
	refused(t, err)
	var fetch *source.FetchError
	if errors.As(err, &fetch) {
		t.Errorf("the refusal is a FetchError: %v", err)
	}
}
