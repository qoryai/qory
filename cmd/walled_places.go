package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/acting"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/hostgit"
	walled "github.com/qoryai/qory/internal/places"
	"github.com/qoryai/qory/internal/stack"
)

// stateDir is qory's state directory, absolute: $XDG_STATE_HOME/qory, else
// ~/.local/state/qory when XDG_STATE_HOME is unset; see [walled.StateDir].
func stateDir() (string, error) { return walled.StateDir() }

// writablePlaces are the places a walled run's container may write: the checkout root,
// then each read-write mount, each once.
func writablePlaces(root string, mounts []wall.Mount) []struct{ path, kind string } {
	out := []struct{ path, kind string }{{root, walled.KindCheckout}}
	for _, m := range mounts {
		if m.ReadOnly || m.Path == root || slices.ContainsFunc(out, func(p struct{ path, kind string }) bool { return p.path == m.Path }) {
			continue
		}
		out = append(out, struct{ path, kind string }{m.Path, walled.KindMount})
	}
	return out
}

// recordPlaces holds every place the walled run of spec may write for the run that
// starts, see [hostgit.Starting], until the returned hold is released, once the session
// has registered the run. It refuses a place a walled run still going binds writable.
// Under the hold it records each place: with [hostgit.Record], and with the
// fingerprints of the qory files of the checkout at root and of its composed harness,
// at, that lie in the place. A place recorded before keeps its fingerprints. On an error
// nothing stays held.
func recordPlaces(ctx context.Context, spec *session.Spec, root string, at places) (*walled.Hold, error) {
	files, err := qoryFiles(root)
	if err != nil {
		return nil, err
	}
	writable := writablePlaces(root, spec.Mounts)
	var paths []string
	for _, p := range writable {
		paths = append(paths, p.path)
	}
	// A run still going could write a place's git configuration and qory files while
	// they are read, and they would be recorded as the person's.
	hold, err := hostgit.Starting(ctx, paths)
	if err != nil {
		return nil, err
	}
	for _, p := range writable {
		if err := recordPlace(ctx, p.path, p.kind, spec.RunID, files, at, paths); err != nil {
			hold.Release()
			return nil, err
		}
	}
	return hold, nil
}

// recordPlace records the place at path, of kind, for the walled run runID, which holds
// the places of holding: its git fingerprint with [hostgit.Record], then the
// fingerprints of the qory files of files and of the harness at that lie in it, when the
// record has none yet.
func recordPlace(ctx context.Context, path, kind, runID string, files []string, at places, holding []string) error {
	if err := hostgit.Record(ctx, path, kind, runID, holding...); err != nil {
		return err
	}
	return walled.Update(path, kind, func(r *walled.Record) error {
		if r.Qory == nil {
			r.Qory = acting.Fingerprint(r.Path, files)
			takeHarness(r, at)
		}
		return nil
	})
}

// unwalledHere refuses an agent started without a wall in the checkout at root, or from
// the working folder cwd, when either lies in a place a walled run has used: the run
// could have left settings, hooks or scripts there that the agent would run on this
// machine, and qory fingerprints none of them.
func unwalledHere(root, cwd string) error {
	recs, err := walled.Cached()
	if err != nil {
		return err
	}
	for _, dir := range []string{root, cwd} {
		if len(walled.Containing(recs, dir)) > 0 {
			return fmt.Errorf("a walled run has used %s, so qory runs no agent there without a wall, since the run could have left settings, hooks or scripts the agent would run; run it behind a wall, or in another clone or worktree", hostgit.Shown(dir))
		}
	}
	return nil
}

// qoryFiles are the qory files host qory reads for the checkout at root: the
// configuration files [config.Discover] lists but the user's qory.yaml, the checkout's
// stack file, and the stack file each of them extends by a path. It refuses as
// [config.Discover] refuses.
func qoryFiles(root string) ([]string, error) {
	files, err := config.Discover(root)
	if err != nil {
		return nil, err
	}
	dir, err := config.UserDir()
	if err != nil {
		return nil, err
	}
	if dir != "" {
		files = slices.DeleteFunc(files, func(f string) bool { return filepath.Dir(f) == filepath.Clean(dir) })
	}
	files = append(files, filepath.Join(root, stack.FileName))
	for i := 0; i < len(files); i++ {
		data, err := os.ReadFile(files[i])
		if err != nil {
			continue
		}
		if base := acting.Base(files[i], data); base != "" && !slices.Contains(files, base) {
			files = append(files, base)
		}
	}
	return files, nil
}

// recordedPlaces are the paths of every place a walled run has used, as qory's record
// holds them.
func recordedPlaces() ([]string, error) {
	recs, err := walled.Cached()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.Path)
	}
	return out, nil
}

// writtenPlaces are the places a walled run could have written, for the session's
// Written: the checkout at root and the writable mounts of this run, then every place
// qory's record holds, each once. A place Forager cannot resolve, such as one below a
// directory this user cannot search, or below a path that is now a file, is left out:
// passed, it would stop every walled run on this machine, and it guards nothing, since
// Forager refuses a mount, a workspace and a file it cannot resolve, so nothing the run
// binds or reads can lie in or go through it.
func writtenPlaces(root string, mounts []wall.Mount) ([]string, error) {
	recorded, err := recordedPlaces()
	if err != nil {
		return nil, err
	}
	var out []string
	add := func(p string) {
		if !slices.Contains(out, p) && session.Overlap(p, p) != "" {
			out = append(out, p)
		}
	}
	for _, p := range writablePlaces(root, mounts) {
		add(p.path)
	}
	for _, p := range recorded {
		add(p)
	}
	return out, nil
}
