package runcopy

import (
	"strings"

	"github.com/qoryai/qory/internal/checkout"
)

// CopyBackResult is what [Dir.CopyBack] did, each path relative to the folder with
// forward slashes, in order.
type CopyBackResult struct {
	// Copied are the new and changed regular files it wrote.
	Copied []string
	// Skipped are the entries of the copy it left out: a link, a .git, a .qory, a file
	// that is not regular, and a path whose folder in the destination is a link or no
	// folder.
	Skipped []string
}

// leftOutName reports whether a file or folder named name stays out of a copy-back
// anywhere in the tree: .git, where git on this machine would find a repository, and
// .qory, where qory would find a checkout's files. The names match as a file system that
// ignores letter case, and the code points macOS ignores in a name, would match them.
func leftOutName(name string) bool {
	plain := strings.Map(func(r rune) rune {
		if ignorable(r) {
			return -1
		}
		return r
	}, name)
	return strings.EqualFold(plain, ".git") || strings.EqualFold(plain, checkout.Dir)
}

// ignorable reports whether r is a code point HFS+ leaves out when it compares names,
// the list git checks for .git: zero-width joiners and marks of direction.
func ignorable(r rune) bool {
	switch {
	case r >= 0x200c && r <= 0x200f, r >= 0x202a && r <= 0x202e, r >= 0x206a && r <= 0x206f, r == 0xfeff:
		return true
	}
	return false
}
