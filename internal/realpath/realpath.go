// Package realpath resolves a path as qory compares paths on this machine: made
// absolute, with its symbolic links resolved, and with each name in the case the folder
// that contains it lists it, so that two paths to one folder on a file system that
// ignores case, such as macOS's, compare equal.
package realpath

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode"
)

// Real is path made absolute with its symlinks resolved, or made absolute and clean
// when it cannot be resolved, such as a path that is not there; either way with each
// name that exists as the folder that contains it lists it, see [cased].
func Real(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return cased(real)
	}
	return cased(abs)
}

// Inside reports whether path lies in place both as written and with its symlinks
// resolved: path made absolute and clean is place or lies inside place as given or as
// resolved, and path resolved lies inside place resolved. A path that is inside as
// written but leads out through a link, or that leads in through a link from outside,
// is not.
func Inside(place, path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	// As written, but each name in the case the folder that contains it lists it.
	written := cased(abs)
	return (inside(cased(place), written) || inside(Real(place), written)) && inside(Real(place), Real(abs))
}

func inside(place, path string) bool {
	rel, err := filepath.Rel(place, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// cased is the absolute, clean path with each of its names that exists as the folder
// that contains it lists it, from the root down to the first name that does not exist,
// and the names from there on as given. No link in it is resolved. On a file system
// that ignores case, such as macOS's, /srv/app/Code and /srv/app/code are one folder,
// and cased returns the case that folder's parent lists for both. A name the folder
// lists as given stays; another case of it is taken only when it is the same file. On a
// file system that tells names apart by case, such as Linux's, path is returned as it
// is.
func cased(path string) string {
	if !filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return path
	}
	names := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	out := string(filepath.Separator)
	for i, name := range names {
		if name == "" {
			continue
		}
		info, err := os.Lstat(filepath.Join(out, name))
		if err != nil {
			return filepath.Join(append([]string{out}, names[i:]...)...)
		}
		out = filepath.Join(out, storedName(out, name, info))
	}
	return out
}

// storedName is the name in the folder dir that is the file info, which dir lists as
// name in some case: name when dir lists it as given, or when no other case of it is the
// same file, and otherwise the case dir lists.
func storedName(dir, name string, info fs.FileInfo) string {
	swapped := strings.Map(func(r rune) rune {
		if unicode.IsUpper(r) {
			return unicode.ToLower(r)
		}
		return unicode.ToUpper(r)
	}, name)
	if swapped == name {
		return name
	}
	// A file system that tells names apart by case has no file by the swapped name, or
	// another file: name is the one it lists.
	if other, err := os.Lstat(filepath.Join(dir, swapped)); err != nil || !os.SameFile(info, other) {
		return name
	}
	names := listing(dir)
	folded := names[strings.ToLower(name)]
	if slices.Contains(folded, name) {
		return name
	}
	for _, n := range folded {
		if other, err := os.Lstat(filepath.Join(dir, n)); err == nil && os.SameFile(info, other) {
			return n
		}
	}
	return name
}

// listings is what [listing] keeps for the life of the process: the names of each folder
// listed, with the folder's file info when it was listed.
var listings struct {
	sync.Mutex
	folders map[string]listed
}

// listed is one folder's names, by their lower case, and its file info then.
type listed struct {
	info  fs.FileInfo
	names map[string][]string
}

// listing returns the names in the folder dir, by their lower case, none when it cannot
// be listed. A folder listed before is listed again only when it is another file now, or
// its modification time changed, as it does when a name in it is added, removed or
// changed.
func listing(dir string) map[string][]string {
	info, err := os.Stat(dir)
	if err != nil {
		return nil
	}
	listings.Lock()
	defer listings.Unlock()
	if l, ok := listings.folders[dir]; ok && os.SameFile(l.info, info) && l.info.ModTime().Equal(info.ModTime()) {
		return l.names
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := map[string][]string{}
	for _, e := range entries {
		k := strings.ToLower(e.Name())
		names[k] = append(names[k], e.Name())
	}
	if listings.folders == nil || len(listings.folders) >= 4096 {
		listings.folders = map[string]listed{}
	}
	listings.folders[dir] = listed{info: info, names: names}
	return names
}
