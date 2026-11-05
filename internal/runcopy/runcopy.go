// Package runcopy makes the copy that a walled run with write access works in, and
// brings the run's commits back from it, so that git on this machine never opens a
// folder the run could write.
//
// Before the run, [Dir.Clone] clones the person's repository into a bare repository
// that no run binds, and from there into <user cache>/qory/runs/<run id>/work, or
// [Dir.CopyFolder] copies a folder that is not a repository there. The run binds that
// copy in place of the person's checkout. After the agent ends, a bare session in the
// wall runs the script of [Dir.GitSession]: it commits what the agent left in each
// repository of the copy and writes the run's branch to a git bundle per repository,
// through a git folder that the script makes, so no setting in the copy's .git applies
// to the add, the commit or the bundle. One git of the script reads the branch's commit
// through the copy's .git and its settings, inside the wall, with no transport and no lazy
// fetch. Once the run's containers are gone,
// [Dir.Quarantine] checks each bundle file and fetches it into a new bare repository
// with git's object checks on; [Quarantine.FetchInto] fetches the run's branch from
// there into the person's repository, and [Quarantine.ActingChanges] lists the acting
// keys of qory's files that the returned commits change. For a plain folder,
// [Dir.CopyBack] brings the files back. [Dir.Remove] deletes the run's folder.
//
// After the run, every git command on this machine runs in a quarantine or a bare
// source repository, whose configuration this package writes, or in the person's
// repository, and never in the copy.
package runcopy

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/preparation"
)

// Names of the folders and files under a run's folder.
const (
	workName       = "work"
	bundleName     = "bundle"
	sourcesName    = "sources"
	quarantineName = "quarantine"
	// recordName is the record of the files [Dir.CopyFolder] copied.
	recordName = "folder.json"
)

// runIDPattern is the form of a run id that can be a folder's name: one path element that
// starts with a letter or a digit, with nothing a shell or a pattern reads specially.
var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// Dir is the folder of one run's copy, <user cache>/qory/runs/<run id>: the copy in
// work, the git session's folder in bundle, the bare clones of the person's
// repositories in sources, and the quarantine repositories in quarantine.
type Dir struct{ root string }

// New makes the folder of the run with id runID under the user's cache folder,
// ~/Library/Caches on macOS and $XDG_CACHE_HOME or ~/.cache on Linux, and returns it.
// The folder must not overlap any of keepOut: a caller passes qory's state folder and
// Forager's runs folder, which a walled run must not write. The folder must not exist
// yet.
func New(runID string, keepOut ...string) (Dir, error) {
	if !runIDPattern.MatchString(runID) {
		return Dir{}, fmt.Errorf(textRunID, shown(runID))
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return Dir{}, fmt.Errorf(textNoCache, err)
	}
	runs := filepath.Join(base, "qory", "runs")
	root := filepath.Join(runs, runID)
	for _, p := range keepOut {
		if p != "" && session.Overlap(root, p) != "" {
			return Dir{}, fmt.Errorf(textOverlap, shown(base), shown(root), shown(p))
		}
	}
	if err := os.MkdirAll(runs, 0o700); err != nil {
		return Dir{}, fileErr(runs, err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Dir{}, fmt.Errorf(textRunExists, shown(root))
		}
		return Dir{}, fileErr(root, err)
	}
	return Dir{root: root}, nil
}

// Root is the run's folder.
func (d Dir) Root() string { return d.root }

// Work is the copy: the folder the run binds read-write as its checkout.
func (d Dir) Work() string { return filepath.Join(d.root, workName) }

// Bundle is the git session's folder: its git folders, its HOME and the bundles it
// writes.
func (d Dir) Bundle() string { return filepath.Join(d.root, bundleName) }

// source is the bare clone of the person's repository number i, which no run binds.
func (d Dir) source(i int) string {
	return filepath.Join(d.root, sourcesName, strconv.Itoa(i)+".git")
}

// quarantine is the quarantine of the repository number i.
func (d Dir) quarantine(i int) string {
	return filepath.Join(d.root, quarantineName, strconv.Itoa(i)+".git")
}

// shown is a path or a value as a line prints it.
func shown(s string) string { return preparation.ShownPath(s) }

// fileErr is err from a file operation on path, as a line that starts with its fix.
func fileErr(path string, err error) error {
	return fmt.Errorf(textFile, shown(path), exports.HideError(err))
}

// InRunCopies returns an error when the folder at path is a run's copy or lies inside one: the
// folder of the copies, under the user's cache folder. A run could have written such a folder,
// so no run and no compose starts there. It returns nil when the lookup of the cache folder
// fails, since qory then makes no copy.
func InRunCopies(path string) error {
	base, err := os.UserCacheDir()
	if err != nil {
		return nil
	}
	runs := filepath.Join(base, "qory", "runs")
	if how := session.Overlap(path, runs); how == "is" || how == "lies inside" {
		return fmt.Errorf(textInRunCopies, shown(path), shown(runs))
	}
	return nil
}
