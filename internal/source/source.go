// Package source resolves a layer's source to a directory on disk and a pin.
//
// A source is a directory today and its pin is [WorkingTree], so nothing pins the material
// a compose reads. [Resolved.Dirty] says whether git sees uncommitted changes under the
// directory, and the report carries pin and dirty mark, so a reader of one compose knows
// what it ran on.
//
// [Resolve] joins a relative path onto the profile's directory and checks that the result
// is a directory. Whether that directory holds a layer is a question for
// [github.com/qoryai/qory/internal/layer].
package source

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/qoryai/qory/internal/profile"
)

// WorkingTree is the pin of a path source: the directory as it stands, pinned by nothing.
const WorkingTree = "working-tree"

// Resolved is a layer's directory and how it is pinned.
type Resolved struct {
	// Dir is the layer directory: the source path when it is absolute, else baseDir joined
	// with it.
	Dir string
	// Pin is what the source resolved to, [WorkingTree] for a path source.
	Pin string
	// Dirty is set for a working tree with uncommitted changes under Dir.
	Dirty bool
}

// Resolve turns a source into a directory and its pin. A relative path resolves against
// baseDir, which a caller passes absolute, such as [profile.Profile.Dir].
//
// The error for a path that is not there comes from the operating system unchanged, and a
// caller can match it with errors.Is and os.ErrNotExist. A path that is there but is not a
// directory gets an error naming the path.
func Resolve(baseDir string, s profile.Source) (Resolved, error) {
	dir := s.Path
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(baseDir, dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return Resolved{}, err
	}
	if !info.IsDir() {
		return Resolved{}, fmt.Errorf("%s is not a directory", dir)
	}
	return Resolved{Dir: dir, Pin: WorkingTree, Dirty: dirty(dir)}, nil
}

// dirty reports whether git sees uncommitted changes under dir, untracked files included.
// It asks git about dir alone, so changes elsewhere in the same repository do not count. A
// directory outside a git working tree, and a host without git, read as clean, because the
// dirty mark is a note in the report and not a reason to refuse a compose.
func dirty(dir string) bool {
	cmd := exec.Command("git", "status", "--porcelain", "--", ".")
	cmd.Dir = dir
	out, err := cmd.Output()
	return err == nil && len(out) > 0
}
