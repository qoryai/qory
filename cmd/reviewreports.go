package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"
)

// newReviewReports makes the reports folder of a review session in round round of the
// Build loop: <under>/review-reports-<round>, new and empty, mode 0700. It returns the
// folder and its mount, writable, which only that session gets. under is a folder of the
// run that no session can write. os.Mkdir fails on a folder that exists, so a round
// never gets the reports of an earlier round or of another run.
//
// shortcut: not called yet; wired in when cmd/run.go, cmd/rundocs.go and
// cmd/pushstep.go can change (after Q-01, Q-36 and Q-73).
func newReviewReports(under string, round int) (string, wall.Mount, error) {
	if round < 1 {
		return "", wall.Mount{}, fmt.Errorf("Number the review round from 1.\nThe round is %d.", round)
	}
	dir := filepath.Join(under, "review-reports-"+strconv.Itoa(round))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", wall.Mount{}, fmt.Errorf("Remove %s, or give the round a new folder.\nMaking the reports folder failed: %w", dir, err)
	}
	return dir, wall.Mount{Path: dir}, nil
}

// checkReviewReports checks reports, the reports folder of a review session, against
// the rules that the harness's tool guard relies on (H-16b):
//  1. it lies outside checkout and outside artifacts, the session's artifact folder;
//  2. it lies outside home, the session's $QORY_HARNESS_HOME;
//  3. no folder above it is a place the session can write: checkout, a read-write mount
//     of mounts other than the folder's own, or the folder of run copies, see
//     [writtenPlaces]. The guard checks only the target of tar -C, unzip -d and
//     git -C … checkout, not the files they write below it.
//
// "Outside" means neither path lies in the other, as [session.Overlap] resolves them. It
// returns an error that starts with the fix for a folder that breaks a rule, and for one
// that is not a folder or is a link.
//
// shortcut: not called yet; wired in when cmd/run.go, cmd/rundocs.go and
// cmd/pushstep.go can change (after Q-01, Q-36 and Q-73).
func checkReviewReports(reports, checkout, artifacts, home string, mounts []wall.Mount) error {
	info, err := os.Lstat(reports)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("Give the review session a reports folder that exists and is no link.\nThe path is %s.", strconv.Quote(reports))
	}
	for _, p := range []struct{ what, path string }{
		{"the checkout", checkout}, {"the artifact folder", artifacts}, {"the harness home", home},
	} {
		if session.Overlap(p.path, reports) != "" {
			return fmt.Errorf("Place the review reports folder outside the checkout, the artifact folder and the harness home.\n  The reports folder %s overlaps %s, %s.",
				strconv.Quote(reports), p.what, strconv.Quote(p.path))
		}
	}
	for _, p := range writtenPlaces(checkout, mounts) {
		if session.Overlap(p, reports) == "contains" {
			return fmt.Errorf("Place the review reports folder below no folder that the session can write.\n  The session can write %s, which contains %s.",
				strconv.Quote(p), strconv.Quote(reports))
		}
	}
	return nil
}
