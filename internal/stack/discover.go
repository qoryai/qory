package stack

import (
	"fmt"
	"os"
	"path/filepath"
)

// Discover finds the stack file or the compose file for a checkout and returns its path.
//
// It looks for [FileName] and [ComposeFileName] in root first, and takes the one that is
// there. Otherwise it walks up from root and takes the nearest ancestor directory's file
// that the current user owns, so a document another user could write is not read. The
// ownership check applies to the ancestors only: a file in root belongs to the checkout
// the caller named. On a system without file ownership every ancestor file counts as
// owned. A directory holding both files is an error naming them, wherever it is found.
//
// The error names root and does not wrap an error a caller can match.
func Discover(root string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if found, err := in(root, false); found != "" || err != nil {
		return found, err
	}
	for dir := filepath.Dir(root); ; dir = filepath.Dir(dir) {
		if found, err := in(dir, true); found != "" || err != nil {
			return found, err
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	return "", fmt.Errorf("no %s or %s in %s or in an ancestor directory you own", FileName, ComposeFileName, root)
}

// in returns the one of the two files dir holds, "" for none, and an error for both. With
// owned, a file the current user does not own does not count.
func in(dir string, owned bool) (string, error) {
	var found []string
	for _, name := range []string{FileName, ComposeFileName} {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && (!owned || OwnedByCurrentUser(info)) {
			found = append(found, candidate)
		}
	}
	switch len(found) {
	case 0:
		return "", nil
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("%s holds both %s and %s; a directory holds one of the two", dir, FileName, ComposeFileName)
}
