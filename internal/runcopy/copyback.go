package runcopy

import (
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"github.com/qoryai/qory/internal/checkout"
)

// folded is name as a file system that ignores letter case and normalization, and the code
// points macOS ignores in a name, compares it: without those code points, case-folded by
// Unicode, so U+017F is s and the Kelvin sign is k, and in NFD. A [cases.Caser] may keep
// state, so each call makes one.
func folded(name string) string {
	stripped := strings.Map(func(r rune) rune {
		if ignorable(r) {
			return -1
		}
		return r
	}, name)
	return norm.NFD.String(cases.Fold().String(stripped))
}

// folds reports whether name and want are one name as [folded] compares them.
func folds(name, want string) bool { return folded(name) == folded(want) }

// ignorable reports whether r is a code point HFS+ leaves out when it compares names,
// the list git checks for .git: zero-width joiners and marks of direction.
func ignorable(r rune) bool {
	switch {
	case r >= 0x200c && r <= 0x200f, r >= 0x202a && r <= 0x202e, r >= 0x206a && r <= 0x206f, r == 0xfeff:
		return true
	}
	return false
}

// leftOutName reports whether a file or folder named name stays out of a copy-back
// anywhere in the tree, as [folds] compares names: .git, where git on this machine
// would find a repository, and .qory, where qory would find a checkout's files.
func leftOutName(name string) bool { return folds(name, ".git") || folds(name, checkout.Dir) }

// gitDirLike reports whether a folder with the entries names is one git takes as a git
// folder, as [folds] compares names: one with HEAD and objects or refs, or one with
// commondir.
func gitDirLike(names []string) bool {
	has := map[string]bool{}
	for _, n := range names {
		has[folded(n)] = true
	}
	return has["head"] && (has["objects"] || has["refs"]) || has["commondir"]
}
