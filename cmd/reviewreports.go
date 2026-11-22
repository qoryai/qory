package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"
)

// newReviewReports makes the reports folder of a review session in the given round of
// the Build loop: <under>/review-reports-<round>, new and empty, mode 0700. It returns the
// folder and its mount, writable, which only that session gets. under is a folder of the
// run that no session can write, outside [runCopiesDir]. os.Mkdir fails on a folder that
// exists, so a round never gets the reports of an earlier round or of another run.
//
// shortcut: not called yet; wired in when cmd/run.go, cmd/rundocs.go and
// cmd/pushstep.go can change (after Q-01, Q-36 and Q-73).
func newReviewReports(under string, round int) (string, wall.Mount, error) {
	if round < 1 {
		return "", wall.Mount{}, fmt.Errorf("Number the review round from 1.\nThe round is %d.", round)
	}
	dir := filepath.Join(under, "review-reports-"+strconv.Itoa(round))
	if err := os.Mkdir(dir, 0o700); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", wall.Mount{}, fmt.Errorf("Give the review round a reports folder that does not exist yet.\n  %s exists.", strconv.Quote(dir))
		}
		return "", wall.Mount{}, fmt.Errorf("Check that qory can make the reports folder in the run's folder.\n  %w", err)
	}
	return dir, wall.Mount{Path: dir}, nil
}

// checkReviewReports checks reports, the reports folder of a review session, against
// the rules that the harness's tool guard relies on (H-16b):
//  1. it lies outside checkout and outside artifacts, the session's artifact folder;
//  2. it lies outside home, the session's $QORY_HARNESS_HOME;
//  3. no folder above it is a place the session can write: checkout, a read-write mount
//     of mounts, or the folder of run copies, see [writtenPlaces]. The mount of reports
//     itself is the same folder, not one above it. The guard checks only the target of
//     tar -C, unzip -d and git -C … checkout, not the files they write below it.
//
// "Outside" means neither path lies in the other, as [session.Overlap] resolves them. A
// pass means the rules hold, so a path that Overlap cannot resolve, and an empty or
// relative one, is an error, as is a reports path that is not a folder or is a link. Each
// error starts with the fix.
//
// shortcut: not called yet; wired in when cmd/run.go, cmd/rundocs.go and
// cmd/pushstep.go can change (after Q-01, Q-36 and Q-73).
func checkReviewReports(reports, checkout, artifacts, home string, mounts []wall.Mount) error {
	info, err := os.Lstat(reports)
	if err != nil {
		return fmt.Errorf("Give the review session a reports folder that qory can read.\n  %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("Give the review session a reports folder, not a link or a file.\n  %s is a link or a file.", strconv.Quote(reports))
	}
	places := []struct{ what, path string }{
		{"the reports folder", reports}, {"the checkout", checkout},
		{"the artifact folder", artifacts}, {"the harness home", home},
	}
	for _, p := range places {
		if !filepath.IsAbs(p.path) || session.Overlap(p.path, p.path) != "is" {
			return fmt.Errorf("Give the review session an absolute path for %s that qory can resolve.\n  The path is %s.", p.what, strconv.Quote(p.path))
		}
	}
	for _, p := range places[1:] {
		if session.Overlap(p.path, reports) != "" {
			return fmt.Errorf("Place the review reports folder outside the checkout, the artifact folder and the harness home.\n  The reports folder %s overlaps %s, %s.",
				strconv.Quote(reports), p.what, strconv.Quote(p.path))
		}
	}
	for _, p := range writtenPlaces(checkout, mounts) {
		if session.Overlap(p, reports) == "contains" {
			return fmt.Errorf("Place the review reports folder in a folder that the session cannot write.\n  The session can write %s, which contains %s.",
				strconv.Quote(p), strconv.Quote(reports))
		}
	}
	return nil
}
