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

// walledPlaces stops a walled run with an input error when its checkout at root, or one
// of the read-write mounts of mounts, is the person's home folder or qory's state
// directory, or an ancestor of either. A walled run records each of those places as one
// it may write, and the record stays after the run: qory would then run no git unchecked
// in any repository under the home folder, and the run could write the records under the
// state directory. A read-only mount is not recorded, and is not compared. The paths are
// compared as written, made absolute, and again with the links of their nearest existing
// ancestor resolved, see [resolvedUp]: a home folder or a state directory reached through
// a link inside the place lies in it as written, and one that does not exist yet, below
// a link, lies in it where the link leads. A home folder that cannot be found, or that
// is a relative path, and a state directory that cannot be found, are not compared.
func walledPlaces(root string, mounts []wall.Mount) error {
	for i, p := range writablePlaces(root, mounts) {
		relation, home := reserved(p.path)
		switch {
		case relation == "":
		case i == 0 && home:
			return input(fmt.Errorf("start the walled run from a repository folder: its checkout %s %s, and a walled run may change any file in its checkout, so qory would stop running git in every repository under the checkout", hostgit.Shown(p.path), relation))
		case i == 0:
			return input(fmt.Errorf("start the walled run from a repository folder: its checkout %s %s, and a walled run may change any file in its checkout, the record of the places walled runs used among them", hostgit.Shown(p.path), relation))
		case home:
			return input(fmt.Errorf("mount a narrower folder: the mount %s %s, and a walled run may change any file in a read-write mount, so qory would stop running git in every repository under the mount", hostgit.Shown(p.path), relation))
		default:
			return input(fmt.Errorf("mount a narrower folder: the mount %s %s, and a walled run may change any file in a read-write mount, the record of the places walled runs used among them", hostgit.Shown(p.path), relation))
		}
	}
	return nil
}

// reserved is how the folder at path relates to the home folder, home true, or else to
// qory's state directory, when path is that folder or an ancestor of it: "is" and the
// folder's description, or "contains", the description and the folder's path. It is ""
// for a path that is neither. A relative HOME is not compared: resolved against the
// working folder, it would change with it.
func reserved(path string) (relation string, home bool) {
	if dir, err := os.UserHomeDir(); err == nil && filepath.IsAbs(dir) && liesIn(dir, path) {
		return contains(path, dir, "your home folder"), true
	}
	if dir, err := stateDir(); err == nil && liesIn(dir, path) {
		return contains(path, dir, "qory's state directory"), false
	}
	return "", false
}

// liesIn reports whether dir is path or lies inside it, as written and made absolute, or
// with the links of the nearest existing ancestor of each resolved, see [resolvedUp].
func liesIn(dir, path string) bool {
	a, errA := filepath.Abs(path)
	b, errB := filepath.Abs(dir)
	return errA == nil && errB == nil && within(a, b) || within(resolvedUp(path), resolvedUp(dir))
}

// contains is how the folder at path relates to the folder dir, called what: "is" and
// what when path is dir, else "contains", what and dir.
func contains(path, dir, what string) string {
	if filepath.Clean(path) == filepath.Clean(dir) || resolvedUp(path) == resolvedUp(dir) {
		return "is " + what
	}
	return "contains " + what + " " + hostgit.Shown(dir)
}

// resolvedUp is path made absolute, with its nearest existing ancestor, or path itself,
// as [walled.Real] returns it, and the names below that ancestor as given: [walled.Real]
// resolves no link in a path that does not exist.
func resolvedUp(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	var rest []string
	for dir := abs; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(dir); err == nil {
			slices.Reverse(rest)
			return filepath.Join(append([]string{walled.Real(dir)}, rest...)...)
		}
		if filepath.Dir(dir) == dir {
			return abs
		}
		rest = append(rest, filepath.Base(dir))
	}
}

// recordPlaces holds every place the walled run of spec may write for the run that
// starts, see [hostgit.Starting], until the returned hold is released, once the session
// has registered the run. It refuses a place a walled run still going binds writable.
// Under the hold it records each place: with [hostgit.Record], and with the
// fingerprints of the qory files of the checkout at root and of its composed harness,
// at, that lie in the place. A place recorded before keeps its fingerprints. A checkout
// or a read-write mount that is the home folder or the state directory, or an ancestor
// of either, is the error of [walledPlaces], and nothing is held or recorded. On an
// error nothing stays held.
func recordPlaces(ctx context.Context, spec *runSpec, root string, at places) (*walled.Hold, error) {
	if err := walledPlaces(root, spec.Mounts); err != nil {
		return nil, err
	}
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
	hold, err := hostgit.Starting(ctx, spec.RunID, paths)
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

// openingRun makes qory's checks of the places walled runs use leave out the walled run
// runID, which this process opened: its open listed it among the walled runs going,
// before its compose runs git. It returns what sets back the run before.
func openingRun(runID string) (restore func()) { return walled.SetThisRun(runID) }

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
