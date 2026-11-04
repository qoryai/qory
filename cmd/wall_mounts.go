package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/realpath"
)

// errRelativeStateHome is the refusal of an XDG_STATE_HOME that is no absolute path,
// which [stateDir] returns before anything is read from the directory it would name.
var errRelativeStateHome = errors.New("qory: XDG_STATE_HOME is not an absolute path; qory keeps its state only in an absolute one, so that it does not change with the working folder")

// errNoStateDir is a state directory that cannot be found: HOME and XDG_STATE_HOME are
// both unset.
var errNoStateDir = errors.New("no state directory: set HOME or XDG_STATE_HOME")

// stateDir is qory's state directory, absolute: $XDG_STATE_HOME/qory, else
// ~/.local/state/qory when XDG_STATE_HOME is unset. An XDG_STATE_HOME that is set but is
// no absolute path is [errRelativeStateHome], so that the state never moves with the
// working folder, and no HOME with no XDG_STATE_HOME is [errNoStateDir].
func stateDir() (string, error) {
	if base := os.Getenv("XDG_STATE_HOME"); base != "" {
		if !filepath.IsAbs(base) {
			return "", errRelativeStateHome
		}
		return filepath.Join(base, "qory"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errNoStateDir
	}
	return filepath.Abs(filepath.Join(home, ".local", "state", "qory"))
}

// writtenPlaces are the places a walled run's container may write, for the session's
// Written: the checkout root, then each read-write mount, each once. A place Forager
// cannot resolve, such as one below a directory this user cannot search, or below a
// path that is now a file, is left out: passed, it would stop the run, and it guards
// nothing, since Forager stops at a mount, a workspace and a file it cannot resolve, so
// nothing the run binds or reads can lie in or go through it.
func writtenPlaces(root string, mounts []wall.Mount) []string {
	var out []string
	add := func(p string) {
		if !slices.Contains(out, p) && session.Overlap(p, p) != "" {
			out = append(out, p)
		}
	}
	add(root)
	for _, m := range mounts {
		if !m.ReadOnly {
			add(m.Path)
		}
	}
	return out
}

// reservedMounts stops a walled run with an input error when one of the read-write
// mounts of mounts, other than its checkout at root, is the person's home folder or
// qory's state directory, or an ancestor of either: a walled run may change any file in
// a read-write mount. A read-only mount and the checkout's mount are not compared. The paths are compared as written, made absolute,
// and again with the links of their nearest existing ancestor resolved, see
// [resolvedUp]: a home folder or a state directory reached through a link inside the
// mount lies in it as written, and one that does not exist yet, below a link, lies in it
// where the link leads. A home folder that cannot be found, or that is a relative path,
// and a state directory that cannot be found, are not compared.
func reservedMounts(root string, mounts []wall.Mount) error {
	for _, m := range mounts {
		if m.ReadOnly || m.Path == root {
			continue
		}
		if relation := reserved(m.Path); relation != "" {
			return input(fmt.Errorf("mount a narrower folder: the mount %s %s, and a walled run may change any file in a read-write mount", hostgit.Shown(m.Path), relation))
		}
	}
	return nil
}

// reserved is how the folder at path relates to the home folder, or else to qory's state
// directory, when path is that folder or an ancestor of it: "is" and the folder's
// description, or "contains", the description and the folder's path. It is "" for a
// path that is neither. A relative HOME is not compared: resolved against the working
// folder, it would change with it.
func reserved(path string) string {
	if dir, err := os.UserHomeDir(); err == nil && filepath.IsAbs(dir) && liesIn(dir, path) {
		return contains(path, dir, "your home folder")
	}
	if dir, err := stateDir(); err == nil && liesIn(dir, path) {
		return contains(path, dir, "qory's state directory")
	}
	return ""
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
// as [realpath.Real] returns it, and the names below that ancestor as given:
// [realpath.Real] resolves no link in a path that does not exist.
func resolvedUp(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	var rest []string
	for dir := abs; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(dir); err == nil {
			slices.Reverse(rest)
			return filepath.Join(append([]string{realpath.Real(dir)}, rest...)...)
		}
		if filepath.Dir(dir) == dir {
			return abs
		}
		rest = append(rest, filepath.Base(dir))
	}
}
