package compose

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/source"
	"github.com/qoryai/qory/internal/stack"
)

// LoadReference fetches the folder that ref, a stack reference [stack.ParseReference]
// read from -f, selects, the way a git module is fetched with opts, and reads the
// folder's [stack.FileName] as if -f selected that file on this machine. It returns the
// stack and what the reference resolved to.
//
// Every module the stack lists by a path, a name alone included, becomes a git source
// of the same URL and ref, with its path inside the repository, so it resolves within
// the fetched tree: on the commit the reference resolved to, through opts.Memo, and
// with opts.Git when it is set. A module path outside the repository is an error, and
// so is a link out of it, which the fetch of the module finds. The modules the stack
// lists with a git source are fetched as they are, with opts.Git when it is set.
//
// A fetch that fails is a [*source.FetchError], and a ref the remote does not have is
// an error that contains the fix; a folder that is not in the tree, or contains no
// stack file, is an error that starts with "check the folder".
func LoadReference(ref stack.Source, opts Options) (*stack.Stack, source.Resolved, error) {
	sel := ref.Shown()
	src, err := source.Resolve("", ref, source.Options{Update: opts.Update, Cache: opts.Cache, Timeout: opts.Timeout, Memo: opts.Memo, Git: opts.Git})
	var gone *source.GoneError
	var fetch *source.FetchError
	var pathErr *fs.PathError
	switch {
	case errors.As(err, &gone):
		return nil, source.Resolved{}, fmt.Errorf("use a branch, tag or commit that %s has in -f %s: it has no branch or tag %s", exports.URL(gone.URL), sel, exports.Value(gone.Ref, false))
	case errors.As(err, &fetch):
		return nil, source.Resolved{}, fmt.Errorf("check the URL and the ref in -f %s: %w", sel, err)
	case err != nil && (hostgit.Refused(err) || errors.As(err, &pathErr)):
		return nil, source.Resolved{}, err
	case err != nil:
		return nil, source.Resolved{}, fmt.Errorf("check the folder in -f %s: %w", sel, exports.HideError(err))
	}
	// The clone is the folder's directory with the folder's segments taken off, as
	// [source.Resolve] joined them on.
	clone := src.Dir
	if ref.Path != "" {
		for range strings.Split(ref.Path, "/") {
			clone = filepath.Dir(clone)
		}
	}
	realClone, err := filepath.EvalSymlinks(clone)
	if err != nil {
		return nil, source.Resolved{}, unreadable(clone, sel, err)
	}
	file := filepath.Join(src.Dir, stack.FileName)
	noStack := fmt.Errorf("check the folder in -f %s: %s has no %s in %s at %s", sel, exports.URL(ref.Git), stack.FileName, folderShown(ref.Path), exports.Value(ref.Ref, false))
	// The stack file may be a link the repository contains; the file it leads to must
	// still be in the fetched tree.
	realFile, err := filepath.EvalSymlinks(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, source.Resolved{}, noStack
	}
	if err != nil {
		return nil, source.Resolved{}, unreadable(file, sel, err)
	}
	if outside(relTo(realClone, realFile)) {
		return nil, source.Resolved{}, fmt.Errorf("check the folder in -f %s: %s in %s at %s links outside the repository", sel, path.Join(folderShown(ref.Path), stack.FileName), exports.URL(ref.Git), exports.Value(ref.Ref, false))
	}
	// The repository's qory.yaml, which the stack reader reads at the root of the
	// checkout that contains the stack file, must be in the fetched tree too.
	root, err := checkout.Root(filepath.Dir(file))
	if err != nil {
		return nil, source.Resolved{}, err
	}
	if linked, err := configLinksOut(realClone, root, sel); err != nil || linked != "" {
		if err == nil {
			err = fmt.Errorf("check the %s in -f %s: %s in %s at %s links outside the repository", path.Base(linked), sel, linked, exports.URL(ref.Git), exports.Value(ref.Ref, false))
		}
		return nil, source.Resolved{}, err
	}
	p, err := stack.Load(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, source.Resolved{}, noStack
	}
	if err != nil {
		return nil, source.Resolved{}, err
	}
	for i, l := range p.Modules {
		if l.Source.Git != "" {
			continue
		}
		name := l.Name
		if name == "" {
			name = fmt.Sprintf("modules[%d]", i)
		}
		rel, err := inTree(p, l, realClone, sel)
		if err != nil {
			return nil, source.Resolved{}, err
		}
		if !outside(rel) && l.Source.Module != "" {
			// A path to a repository in the tree with an export selected: the export's
			// directory, which that repository's qory.yaml sets, has to be in the tree too,
			// and so does the qory.yaml.
			linked, err := configLinksOut(realClone, filepath.Join(realClone, rel), sel)
			if err != nil {
				return nil, source.Resolved{}, err
			}
			if linked != "" {
				return nil, source.Resolved{}, fmt.Errorf("check the export of module %s in the stack of -f %s: %s in %s at %s links outside the repository", shown(name), sel, linked, exports.URL(ref.Git), exports.Value(ref.Ref, false))
			}
			r, err := source.Resolve(realClone, stack.Source{Path: rel, Module: l.Source.Module}, source.Options{})
			if err != nil {
				return nil, source.Resolved{}, fmt.Errorf("check the export of module %s in the stack of -f %s: %w", shown(name), sel, exports.HideError(err))
			}
			rel = relTo(realClone, r.Dir)
		}
		if outside(rel) {
			return nil, source.Resolved{}, fmt.Errorf("give module %s a path inside the repository, or a git source, in the stack of -f %s: qory reads the modules of a stack fetched by reference from the fetched tree only, and %s is outside it", shown(name), sel, exports.Value(p.SourceOf(l).Path, false))
		}
		sub := filepath.ToSlash(rel)
		if sub == "." {
			sub = ""
		}
		p.Modules[i].Source = stack.Source{Git: ref.Git, Ref: ref.Ref, Path: sub}
	}
	return p, src, nil
}

// inTree is the directory module l of p reads by a path, relative to realClone, the
// fetched tree with its links resolved; [outside] reports whether it is outside it. sel
// is the reference as a message prints it.
func inTree(p *stack.Stack, l stack.Module, realClone, sel string) (string, error) {
	s := p.SourceOf(l)
	dir := filepath.Clean(s.Path)
	if !filepath.IsAbs(dir) {
		base, err := filepath.EvalSymlinks(p.DirOf(l))
		if err != nil {
			return "", unreadable(p.DirOf(l), sel, err)
		}
		dir = filepath.Join(base, dir)
	}
	return relTo(realClone, dir), nil
}

// configLinksOut is the qory.yaml in dir, a directory of the fetched tree of the
// reference sel, as a path from the repository's root, when the file is a link that
// leads out of realClone, the fetched tree with its links resolved. It is "" when dir
// contains no qory.yaml, under either of its names, and when the file is in the tree.
func configLinksOut(realClone, dir, sel string) (string, error) {
	file, err := exports.File(dir)
	if err != nil || file == "" {
		// A directory with both names is an error the reader of the file reports.
		return "", nil
	}
	real, err := filepath.EvalSymlinks(file)
	if err != nil {
		return "", unreadable(file, sel, err)
	}
	if !outside(relTo(realClone, real)) {
		return "", nil
	}
	shownDir := relTo(realClone, dir)
	if outside(shownDir) {
		shownDir = "."
	}
	return exports.Value(path.Join(filepath.ToSlash(shownDir), filepath.Base(file)), false), nil
}

// outside reports whether rel, a path relative to a directory, leads out of it.
func outside(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// unreadable is the error for dir, a directory of the fetched tree of the reference sel
// whose links could not be resolved, with err, the error the resolution returned.
func unreadable(dir, sel string, err error) error {
	return fmt.Errorf("check that you can read %s, in the fetched tree of -f %s: %w", exports.Value(dir, false), sel, exports.HideError(err))
}

// relTo is dir relative to root, ".." when no relative path leads there.
func relTo(root, dir string) string {
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return ".."
	}
	return rel
}

// folderShown is a reference's folder as a message prints it, "." for the root.
func folderShown(folder string) string {
	if folder == "" {
		return "."
	}
	return exports.Value(folder, false)
}
