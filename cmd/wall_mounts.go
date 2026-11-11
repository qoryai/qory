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
)

// errRelativeStateHome is the refusal of an XDG_STATE_HOME that is no absolute path,
// which [stateDir] returns before anything is read from the directory it would name.
var errRelativeStateHome = errors.New("set XDG_STATE_HOME to an absolute path, or unset it: qory keeps its state only in an absolute path, so that the state does not move with the working folder")

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
// Written: the checkout root, then each read-write mount, then the folder of the copies of
// walled runs, each once. A place Forager
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
	if runs := runCopiesDir(); runs != "" {
		add(runs)
	}
	return out
}

// reservedMounts stops a walled run with an input error when one of the read-write mounts
// of mounts, other than its checkout at root, is the person's home folder or qory's state
// directory, or an ancestor of either, see [homeOrState]: a walled run may change any file in a read-write
// mount. A read-only mount and the checkout's mount are not compared. The paths are
// compared as written, made absolute, and again as [session.Overlap] compares them: a home
// folder or a state directory reached through a link inside the mount lies in it as written, and
// one written another way, such as through a firmlink or in another Unicode form, or that does
// not exist yet, below a link, lies in it as the file system has it. A home folder that cannot be
// found, or that is a relative path, and a state directory that cannot be found, are not compared.
func reservedMounts(root string, mounts []wall.Mount) error {
	for _, m := range mounts {
		if m.ReadOnly || m.Path == root {
			continue
		}
		if relation := homeOrState(m.Path); relation != "" {
			return input(fmt.Errorf("make the mount %s read-only, with :ro on --mount or in wall.mounts: it %s, and a walled run may change any file in a read-write mount", hostgit.Shown(m.Path), relation))
		}
	}
	return nil
}

// The lines of [bindsReserved], one per kind of run that binds the checkout into the agent's
// container: the folder's path, then how it relates to the home folder or qory's state
// directory, see [reserved].
const (
	textReadBindsReserved      = "start the run from a folder that does not contain your home folder or qory's state directory: the folder %s %s, and a read run binds it into the agent's container" + codeUsed
	textCodeWriteBindsReserved = "start the run from a folder that does not contain your home folder, qory's state directory or the folder of the runs' copies: the folder %s %s, and a write run from a run code binds it into the agent's container" + codeUsed
)

// bindsReserved stops a walled run that binds the checkout at root and the working folder cwd
// themselves, with an input error of the line text, when relation, [reserved] or [homeOrState],
// relates one of them to a folder it is or contains: the agent could read every file of the
// person there, and a write run could change them. Two kinds of run bind them: a read run with no
// setup, with [textReadBindsReserved] and [homeOrState], as [codeRun.readExposed] checks qory's
// cache folder with a line that has the harness report, and a write run from a run code, which
// gets no copy, with [textCodeWriteBindsReserved] and [reserved].
func bindsReserved(text, root, cwd string, relation func(string) string) error {
	for _, p := range []string{root, cwd} {
		if relation := relation(p); relation != "" {
			return input(fmt.Errorf(text, hostgit.Shown(p), relation))
		}
	}
	return nil
}

// reserved is [homeOrState], or else how the folder at path relates to the folder of the runs'
// copies, [runCopiesDir], which a run could have written, when path is that folder or an ancestor
// of it. A relative cache folder, from a relative HOME, is not compared.
func reserved(path string) string {
	if relation := homeOrState(path); relation != "" {
		return relation
	}
	if dir := runCopiesDir(); filepath.IsAbs(dir) && liesIn(dir, path) {
		return contains(path, dir, "the folder of the runs' copies")
	}
	return ""
}

// homeOrState is how the folder at path relates to the home folder, or else to qory's state
// directory, when path is that folder or an ancestor of it: "is" and the folder's
// description, or "contains", the description and the folder's path. It is "" for a
// path that is neither. A relative HOME is not compared: resolved against the working
// folder, it would change with it. A read-write mount of qory's cache folder, which contains the
// folder of the runs' copies, stops in [codeRun.reportExposed].
func homeOrState(path string) string {
	if dir, err := os.UserHomeDir(); err == nil && filepath.IsAbs(dir) && liesIn(dir, path) {
		return contains(path, dir, "your home folder")
	}
	if dir, err := stateDir(); err == nil && liesIn(dir, path) {
		return contains(path, dir, "qory's state directory")
	}
	return ""
}

// liesIn reports whether dir is path or lies inside it, as written and made absolute, or as
// [session.Overlap] compares them.
func liesIn(dir, path string) bool {
	a, errA := filepath.Abs(path)
	b, errB := filepath.Abs(dir)
	if errA != nil || errB != nil {
		return false
	}
	if within(a, b) {
		return true
	}
	how := session.Overlap(a, b)
	return how == "is" || how == "contains"
}

// contains is how the folder at path relates to the folder dir, called what: "is" and
// what when path is dir, else "contains", what and dir.
func contains(path, dir, what string) string {
	if filepath.Clean(path) == filepath.Clean(dir) {
		return "is " + what
	}
	a, errA := filepath.Abs(path)
	b, errB := filepath.Abs(dir)
	if errA == nil && errB == nil && session.Overlap(a, b) == "is" {
		return "is " + what
	}
	return "contains " + what + " " + hostgit.Shown(dir)
}
