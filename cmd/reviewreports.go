package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"
)

// newReviewReports makes the reports folder of the review session with the number number
// in the run: <under>/review-reports-<number>, new and empty, mode 0700. It returns the
// folder and its mount, writable, which only that session gets. under is a folder of the
// run that no session can write, and that no mount reaches. os.Mkdir fails on a folder
// that exists, so a session never gets the reports of an earlier session. The number is
// the session's, not the round's: a round that repeats, or a stage that starts again at
// round 1 in the same run, makes a new folder.
//
// The folder belongs to qory's user, whose uid is the wall user's, see [oneUser], and its
// mode stays 0700.
func newReviewReports(under string, number int) (string, wall.Mount, error) {
	if number < 1 {
		return "", wall.Mount{}, reviewStop("The review's session number is below 1, so the review did not start", fmt.Sprintf("The number is %d", number))
	}
	if !cleanAbs(under) {
		return "", wall.Mount{}, reviewStop("The review's reports folder is not an absolute, clean path, so the review did not start",
			fmt.Sprintf("The folder is %s", strconv.Quote(under)))
	}
	dir := filepath.Join(under, "review-reports-"+strconv.Itoa(number))
	if err := os.Mkdir(dir, 0o700); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", wall.Mount{}, reviewStop("The review's reports folder exists already, so the review did not start",
				fmt.Sprintf("%s exists", strconv.Quote(dir)))
		}
		return "", wall.Mount{}, reviewStop("Making the review's reports folder failed, so the review did not start", shownError(err))
	}
	return dir, wall.Mount{Path: dir}, nil
}

// checkReviewReports checks reports, the reports folder of a review session, against
// the rules that the harness's tool guard relies on:
//  1. it lies outside checkout and outside artifacts, the session's artifact folder;
//  2. it lies outside home, the session's $QORY_HARNESS_HOME;
//  3. no folder above it is a place the session can write: checkout, a read-write mount
//     of mounts, or the folder of run copies, see [writtenPlaces]. The mount of reports
//     itself is the same folder, not one above it. The guard checks only the target of
//     tar -C, unzip -d and git -C … checkout, not the files they write below it.
//
// "Outside" means neither path lies in the other, as [session.Overlap] resolves them. A
// pass means the rules hold, so a path that Overlap cannot resolve, and an empty,
// relative or unclean one, is an error, as is a reports path that is not a folder or is
// a link. Run before the session starts, it also checks that reports is empty, belongs
// to owner, the uid of qory's user, which is the wall's user, has no group or other
// write bit, and that mounts reach it only through one writable mount at its path. Each
// error is a [reviewStop].
func checkReviewReports(reports, checkout, artifacts, home string, mounts []wall.Mount, owner int) error {
	if !cleanAbs(reports) {
		return reviewStop("The review's reports folder is not an absolute, clean path, so the review did not start",
			fmt.Sprintf("The folder is %s", strconv.Quote(reports)))
	}
	info, err := os.Lstat(reports)
	if err != nil {
		return reviewStop("Reading the review's reports folder failed, so the review did not start", shownError(err))
	}
	if !info.IsDir() {
		return reviewStop("The review's reports folder is a link or a file, so the review did not start", strconv.Quote(reports))
	}
	places := []struct{ what, path string }{
		{"the reports folder", reports}, {"the checkout", checkout},
		{"the artifact folder", artifacts}, {"the harness home", home},
	}
	for _, p := range places {
		if !cleanAbs(p.path) || session.Overlap(p.path, p.path) != "is" {
			return reviewStop("A path of the review could not be resolved, so the review did not start",
				fmt.Sprintf("The path of %s is %s", p.what, strconv.Quote(p.path)))
		}
	}
	for _, p := range places[1:] {
		if session.Overlap(p.path, reports) != "" {
			return reviewStop("The review's reports folder overlaps a folder that the session reads or writes, so the review did not start",
				fmt.Sprintf("The reports folder %s overlaps %s, %s", strconv.Quote(reports), p.what, strconv.Quote(p.path)))
		}
	}
	for _, p := range writtenPlaces(checkout, mounts) {
		if session.Overlap(p, reports) == "contains" {
			return reviewStop("The review's reports folder lies in a folder that the session can write, so the review did not start",
				fmt.Sprintf("The session can write %s, which contains %s", strconv.Quote(p), strconv.Quote(reports)))
		}
	}
	entries, err := os.ReadDir(reports)
	if err != nil {
		return reviewStop("Reading the review's reports folder failed, so the review did not start", shownError(err))
	}
	if len(entries) != 0 {
		return reviewStop("The review's reports folder is not empty, so the review did not start",
			fmt.Sprintf("%s has %d entries", strconv.Quote(reports), len(entries)))
	}
	uid := -1
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		uid = int(st.Uid)
	}
	if uid != owner || info.Mode().Perm()&0o022 != 0 {
		return reviewStop("The review's reports folder has another owner or a group or other write bit, so the review did not start",
			fmt.Sprintf("%s has the owner %d and mode %04o, and the wall's user is %d", strconv.Quote(reports), uid, info.Mode().Perm(), owner))
	}
	writable := 0
	for _, m := range mounts {
		how := session.Overlap(m.Path, reports)
		if how == "is" && !m.ReadOnly && m.Target == "" {
			writable++
			continue
		}
		if how == "is" || how == "lies inside" || (m.Target != "" && within(reports, m.Target)) {
			return reviewStop(fmt.Sprintf("A mount of %s at %s also reaches the reports folder %s, so the review did not start",
				strconv.Quote(m.Path), strconv.Quote(m.Target), strconv.Quote(reports)), "")
		}
	}
	if writable != 1 {
		return reviewStop(fmt.Sprintf("The review's reports folder has %d writable mounts, not one, so the review did not start", writable), "")
	}
	return nil
}
