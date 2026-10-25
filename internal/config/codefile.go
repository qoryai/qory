package config

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/qoryai/qory/internal/exports"
)

// CheckWriters returns an error for path, the file that setting of forager.yaml is set
// to and that another program writes a secret into, unless only root and the user
// running qory may write it and every folder above it, each read without following a
// link. Each is owned by root or that user, and neither its group nor other users may
// write it, unless it is a folder that root owns with the sticky bit set, such as /tmp,
// where no other user may replace the file. The rule is stricter than the one an
// integration's program keeps ([trusted]), whose groups of root, wheel, admin and the
// owner's private group may write. The caller resolves the links of the folders
// first. Each error starts with its fix and prints the path as [exports.Value] prints
// it; a path that cannot be read is the error of its lstat. On a system with no file
// owners, every path passes.
func CheckWriters(setting, path string) error {
	err := ownersOnly([]string{path}, nil)
	if err == nil {
		err = noGroupWrite(path)
	}
	return writersRefusal(setting, err)
}

// writersRefusal is the error of [CheckWriters] for err, the error of [ownersOnly] or
// [noGroupWrite]: an [untrusted] in the words for setting, any other error as it is.
func writersRefusal(setting string, err error) error {
	var u *untrusted
	if !errors.As(err, &u) {
		return err
	}
	shown := exports.Value(u.path, false)
	rule := "only root and the user running qory may write " + setting + " and its folders, except a folder that root owns with the sticky bit set"
	switch {
	case u.owner != "":
		return fmt.Errorf("make root or the user running qory the owner of %s: it is owned by %s, and %s", shown, u.owner, rule)
	case u.group != "":
		return fmt.Errorf("remove the group's write permission from %s: the group %s may write it, and %s", shown, u.group, rule)
	}
	return fmt.Errorf("remove the write permission of other users from %s: every user may write it, and %s", shown, rule)
}

// groupWriteChain is [noGroupWrite] with each path read by stat and each group named by
// n: the error for the first of path and the folders above it, up to /, that its group
// may write, unless it is a folder root owns with the sticky bit set.
func groupWriteChain(path string, stat func(string) (fileOwner, error), n names) error {
	for p := path; ; p = filepath.Dir(p) {
		o, err := stat(p)
		if err != nil {
			return err
		}
		if o.Mode.Perm()&0o020 != 0 && !stickyByRoot(o) {
			return &untrusted{path: p, group: named(n.Group(o.GID), o.GID)}
		}
		if p == filepath.Dir(p) {
			return nil
		}
	}
}
