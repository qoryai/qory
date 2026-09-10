package profile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Discover finds the profile file for a checkout and returns its path.
//
// It looks for [FileName] in root first, and takes it when it is there. Otherwise it walks
// up from root and takes the nearest ancestor directory's file that the current user owns,
// so a profile another user could write is not read. The ownership check applies to the
// ancestors only: a file in root belongs to the checkout the caller named. On a system
// without file ownership every ancestor file counts as owned.
//
// The error names root and does not wrap an error a caller can match.
func Discover(root string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(root, FileName)
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}
	for dir := filepath.Dir(root); ; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, FileName)
		if info, err := os.Stat(candidate); err == nil && OwnedByCurrentUser(info) {
			return candidate, nil
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	return "", fmt.Errorf("no %s in %s or in an ancestor directory you own", FileName, root)
}
