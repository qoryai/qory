package hostgit

import (
	"context"
	"path/filepath"
	"time"

	"github.com/qoryai/qory/internal/places"
)

// Record records path as a place a walled run may write, of kind, for the run run. A
// place recorded before keeps its record as it is. A place recorded now gets the
// fingerprint of its repository's git settings when path is that repository's work
// tree, git directory or common directory, and no fingerprint otherwise: a repository
// deeper inside it is then held to the keys qory and git write. holding are the places
// the run holds already, see [Starting].
//
// A repository whose git directory, common directory or configuration lies in a place
// recorded before is checked first, as [Command] checks it, against that record: an
// earlier walled run could have written its configuration, and the fingerprint would
// take that run's settings for the person's. A refusal of the check is the error, and
// nothing is recorded.
func Record(ctx context.Context, path, kind, run string, holding ...string) error {
	if err := Ready(ctx); err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Second)
	return places.Update(path, kind, func(r *places.Record) error {
		if r.Run != "" {
			return nil
		}
		r.Recorded, r.Run = now, run
		fp, ok, err := repositoryAt(ctx, path, holding)
		if err != nil {
			return err
		}
		if ok {
			r.Git = []places.Repository{fp}
		}
		return nil
	})
}

// repositoryAt is the fingerprint of the repository whose work tree, git directory or
// common directory is path, ok false when path is none of them. The repository is
// checked first against the places recorded before, see [Record]. The recorded places
// it lies in are held for the check, but for those of holding, which the caller holds.
func repositoryAt(ctx context.Context, path string, holding []string) (places.Repository, bool, error) {
	real := places.Real(path)
	root := func(repo Repo) bool {
		return repo.Worktree == real || repo.GitDir == real || repo.CommonDir == real
	}
	repo, ok := Resolve(ctx, path)
	if !ok || !root(repo) {
		return places.Repository{}, false, nil
	}
	recs, err := places.Cached()
	if err != nil {
		return places.Repository{}, false, err
	}
	if len(recs) > 0 {
		u := &using{ctx: ctx, h: &places.Hold{}, recs: recs, dir: real, held: map[string]bool{}}
		defer u.h.Release()
		for _, o := range holding {
			for _, r := range places.Containing(recs, o) {
				if r.Path == places.Real(o) {
					u.held[r.Path] = true
				}
			}
		}
		if _, _, err := fullCheck(u); err != nil {
			return places.Repository{}, false, err
		}
		// The repository as git resolves it under the hold.
		if repo, ok = Resolve(ctx, path); !ok || !root(repo) {
			return places.Repository{}, false, nil
		}
	}
	fp, err := Fingerprint(ctx, repo)
	return fp, err == nil, err
}

// Starting holds the places at paths for a walled run that starts there, see
// [places.Starting], and then refuses a place a walled run still going binds writable,
// see [places.Going], with the refusal [Command] returns for a place in use. Nothing is
// recorded or read there before the hold. On a refusal or an error nothing stays held.
func Starting(ctx context.Context, paths []string) (*places.Hold, error) {
	h, err := places.Starting(paths)
	if err != nil {
		return nil, err
	}
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			h.Release()
			return nil, err
		}
		going, err := places.Going(ctx, abs)
		if err := usedRefusal(p, going, err); err != nil {
			h.Release()
			return nil, err
		}
	}
	return h, nil
}

// Hold checks dir as [Command] does, and holds the places the check holds, and dir itself
// as a place whether or not it is recorded, until the returned hold is released: a
// walled run that starts at dir waits for the release. It is for an action of more than
// one git command on dir, such as qory worktree remove, which checks a worktree, asks
// the person, and then removes the worktree. A walled run that uses dir is the refusal
// [Command] returns. On a refusal or an error nothing stays held.
func Hold(ctx context.Context, dir string) (*places.Hold, error) {
	if err := Ready(ctx); err != nil {
		return nil, err
	}
	h, err := check(ctx, dir)
	if err != nil {
		return nil, err
	}
	if h == nil {
		h = &places.Hold{}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		h.Release()
		return nil, err
	}
	busy, err := places.InUse(ctx, h, abs)
	if err := usedRefusal(abs, busy, err); err != nil {
		h.Release()
		return nil, err
	}
	return h, nil
}
