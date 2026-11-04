// Package runcopy makes the copy that a walled run with write access works in, and
// brings the run's commits back from it, so that git on this machine never opens a
// folder the run could write.
//
// Before the run, [Dir.Clone] clones the person's repository, or [Dir.CopyFolder]
// copies a folder that is not a repository, into <user cache>/qory/runs/<run id>/work.
// The run binds that copy in place of the person's checkout. After the agent ends, a
// bare session in the wall runs the script of [Dir.GitSession]: it commits what the
// agent left in the copy and writes the run's branch to a git bundle, through a git
// folder that the script makes, so no setting in the copy's .git applies to the add,
// the commit or the bundle. Once the
// run's containers are gone, [Dir.Quarantine] checks the bundle file and fetches it into
// a new bare repository with git's object checks on; [Quarantine.FetchInto] fetches the
// run's branch from there into the person's repository, and [Quarantine.ActingChanges]
// lists the acting keys of qory's files that the returned commits change. For a plain
// folder, [Dir.CopyBack] copies the regular files back. [Dir.Remove] deletes the copy,
// the bundle and the quarantine.
//
// After the run, every git command on this machine runs in the quarantine, whose
// configuration this package writes, or in the person's repository, and never in the
// copy.
package runcopy

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
)

// Names of the folders and files under a run's folder.
const (
	workName       = "work"
	bundleName     = "bundle"
	quarantineName = "quarantine"
	// bundleFile is the bundle the git session writes in the bundle folder, and the
	// checked copy of it in the run's folder.
	bundleFile = "run.bundle"
)

// runID is the form of a run id that can be a folder's name: one path element, with no
// separator, no dot at the start and nothing a shell or a pattern reads specially.
var runID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// Dir is the folder of one run's copy, <user cache>/qory/runs/<run id>: the copy in
// work, the git session's folder in bundle, and the quarantine repository in
// quarantine.
type Dir struct{ root string }

// New makes the folder of the run with id runID under the user's cache folder,
// ~/Library/Caches on macOS and $XDG_CACHE_HOME or ~/.cache on Linux, and returns it.
// The folder must not overlap any of keepOut: a caller passes qory's state folder and
// Forager's runs folder, which a walled run must not write. The folder must not exist
// yet.
func New(runID string, keepOut ...string) (Dir, error) {
	if !validRunID(runID) {
		return Dir{}, fmt.Errorf("give a run id of letters, digits, dots, dashes and underscores: the run's copy goes to a folder named after it, and %s is not such a name", shown(runID))
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return Dir{}, fmt.Errorf("set HOME: the copy of a walled run's checkout goes under the user's cache folder, and the lookup of that folder failed: %w", err)
	}
	runs := filepath.Join(base, "qory", "runs")
	root := filepath.Join(runs, runID)
	for _, p := range keepOut {
		if p == "" {
			continue
		}
		if overlap(root, p) {
			return Dir{}, fmt.Errorf("set XDG_STATE_HOME to a folder outside %s: a walled run writes its copy in %s, and that folder overlaps %s, which no walled run may write", shown(base), shown(root), shown(p))
		}
	}
	if err := os.MkdirAll(runs, 0o700); err != nil {
		return Dir{}, err
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Dir{}, fmt.Errorf("remove %s, or start the run again with another id: a run's copy goes into a new folder, and that folder exists", shown(root))
		}
		return Dir{}, err
	}
	return Dir{root: root}, nil
}

// validRunID reports whether id can be one folder's name: [runID], and not . or ..
func validRunID(id string) bool { return runID.MatchString(id) && id != "." && id != ".." }

// Root is the run's folder.
func (d Dir) Root() string { return d.root }

// Work is the copy: the folder the run binds read-write as its checkout.
func (d Dir) Work() string { return filepath.Join(d.root, workName) }

// Bundle is the git session's folder: its git folder, its HOME and the bundle it
// writes.
func (d Dir) Bundle() string { return filepath.Join(d.root, bundleName) }

// QuarantinePath is the bare repository the bundle is fetched into.
func (d Dir) QuarantinePath() string { return filepath.Join(d.root, quarantineName) }

// Remove deletes the run's folder: the copy, the bundle and the quarantine. A run can
// leave folders without the write bit, so when the first pass fails, Remove sets each
// folder in it to mode 0700 and deletes again. It follows no link.
func (d Dir) Remove() error {
	if d.root == "" {
		return nil
	}
	if err := os.RemoveAll(d.root); err == nil {
		return nil
	}
	_ = filepath.WalkDir(d.root, func(path string, e fs.DirEntry, err error) error {
		if err == nil && e.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
	return os.RemoveAll(d.root)
}

// overlap reports whether a and b are the same folder or one contains the other, each
// with the links of the part of its path that exists resolved.
func overlap(a, b string) bool {
	ra, rb := resolved(a), resolved(b)
	return inside(ra, rb) || inside(rb, ra)
}

// inside reports whether path is dir or lies under it, both clean and absolute.
func inside(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolved is path made absolute, with the links of its longest part that exists
// resolved and the rest joined as written.
func resolved(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	rest := ""
	for p := abs; ; p = filepath.Dir(p) {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		if p == filepath.Dir(p) {
			return abs
		}
		rest = filepath.Join(filepath.Base(p), rest)
	}
}

// shown is a path or a value as a line prints it.
func shown(s string) string { return integration.Printable(exports.Value(s, false)) }
