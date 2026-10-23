package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/source"
	"github.com/qoryai/qory/internal/stack"
	"github.com/qoryai/qory/internal/ui"
)

// extendsInCheckout is the first half of the rule for the compose of a run given a run
// key: a file inside the checkout at root reads no folder outside it, and fetches git
// sources over https only. It reports whether doc, the document the compose reads, lies
// inside the checkout. When it does, it returns a [*source.SchemeError] for a git source
// of the stack doc extends that is not an https URL, and an error for a path that
// resolves outside the checkout, every link and the export the source selects resolved.
// A base that -f named, base not nil, takes the place of what doc extends, so that
// source is not read. A branch of the repository can change the checkout's files, so
// without the rule it could pull another run's folder or this machine's configuration
// into the harness.
//
// checkouts is set when doc is the checkout's document: the one the compose discovered,
// or the one a stack -f named composes on. A document of the checkout, in the checkout's
// root directory, whose path resolves outside the checkout through a link is an error. A
// document the compose discovered in a directory above the checkout, and a stack -f named
// with no document in the checkout, are no files of the checkout, wherever they lie.
func extendsInCheckout(doc, base *stack.Stack, root string, checkouts bool) (bool, error) {
	realRoot := realPath(root)
	docAt := realPath(doc.File)
	docIn := within(realRoot, docAt)
	if checkouts && !docIn && realPath(filepath.Dir(doc.File)) == realRoot {
		return false, documentOut(root, exports.Value(ui.Short(doc.File, root), false), docAt)
	}
	if !docIn || base != nil || (doc.Extends.Path == "" && doc.Extends.Git == "") {
		return docIn, nil
	}
	file := exports.Value(ui.Short(doc.File, root), false)
	if doc.Extends.Git != "" {
		if source.HTTPSFix(doc.Extends.Git, "") != "" {
			return docIn, &source.SchemeError{URL: doc.Extends.Git, File: file}
		}
		return docIn, nil
	}
	dirAt, linked, in := sourceDir(realRoot, doc.Dir(), doc.Extends)
	if in {
		linked = filepath.Join(dirAt, stack.FileName)
		dirAt, in = fileAt(realRoot, dirAt, stack.FileName)
	}
	if !in && linked != "" {
		return docIn, linkOut(root, realRoot, linked, dirAt)
	}
	if !in {
		return docIn, fmt.Errorf("move the stack that %s extends into the checkout %s, or extend a git source. A run given a run key reads a stack that a file of the checkout extends only from inside the checkout, and %s is outside it", file, exports.Value(root, false), exports.Value(dirAt, false))
	}
	return docIn, nil
}

// modulesInCheckout is the second half of the rule of [extendsInCheckout], for every
// module of p, the stack to compose with its base's modules in it. A module that a file
// inside the checkout lists by a path resolves inside the checkout, every link and the
// export the source selects resolved, and a module with a git source has an https URL.
// Every file of a module the rule checks resolves inside the checkout too, every link
// resolved, the module's manifest among them, since the compose reads the files and the
// home links to them. The modules of doc are checked when doc lies inside the checkout,
// docIn. The modules of the base are checked when the base's file does: the stack -f
// named, or a path doc extends, which [extendsInCheckout] kept inside. The git sources of
// the base are checked too when doc extends a git source, since a file of the checkout
// chose that stack.
func modulesInCheckout(p, doc, base *stack.Stack, root string, docIn bool) error {
	realRoot := realPath(root)
	baseIn, gitBase := false, false
	switch {
	case base != nil:
		baseIn = within(realRoot, realPath(base.File))
	case doc.Extends.Git != "":
		gitBase = docIn
	case doc.Extends.Path != "":
		baseIn = docIn
	}
	for _, l := range p.Modules {
		in, gitIn, file := docIn, docIn, ui.Short(doc.File, root)
		if l.Base {
			in, gitIn = baseIn, baseIn || gitBase
			if base != nil {
				file = ui.Short(base.File, root)
			} else {
				file = "the stack " + doc.Extends.Shown()
			}
		}
		src := p.SourceOf(l)
		if src.Git != "" {
			if gitIn && source.HTTPSFix(src.Git, "") != "" {
				return &source.SchemeError{URL: src.Git, File: exports.Value(file, false)}
			}
			continue
		}
		if !in {
			continue
		}
		dirAt, linked, ok := sourceDir(realRoot, p.DirOf(l), src)
		if !ok && linked != "" {
			return linkOut(root, realRoot, linked, dirAt)
		}
		if !ok {
			return fmt.Errorf("move module %s into the checkout %s, or give it a git source in %s. A run given a run key reads a module that a file of the checkout lists only from inside the checkout, and %s is outside it", accesskey.ShownName(l.Name, false), exports.Value(root, false), exports.Value(file, false), exports.Value(dirAt, false))
		}
		if err := linksInside(root, realRoot, dirAt); err != nil {
			return err
		}
	}
	return nil
}

// configInCheckout returns an error when the qory-stack.yaml, qory.yaml or harness.yaml
// at the root of the checkout at root is a link that resolves outside the checkout. A run
// given a run key calls it before any of these files is read: the compose discovers and
// loads the qory-stack.yaml, and the configuration reads keys of the qory.yaml, such as
// harness.tiers, whichever document the compose reads. A qory-stack.yaml is the checkout's
// document and gets the line of [extendsInCheckout]. Beside a qory-stack.yaml, the error
// for a qory.yaml is for a configuration file; otherwise the qory.yaml is taken for the
// document and gets that line too. A file that is not there, or two of them, are the
// configuration's to report.
func configInCheckout(root string) error {
	realRoot := realPath(root)
	stackFile := filepath.Join(root, stack.FileName)
	_, err := os.Lstat(stackFile)
	hasStack := err == nil
	if hasStack {
		if at := realPath(stackFile); !within(realRoot, at) {
			return documentOut(root, exports.Value(stack.FileName, false), at)
		}
	}
	file, err := config.FileIn(root)
	if err != nil || file == "" {
		return nil
	}
	at := realPath(file)
	if within(realRoot, at) {
		return nil
	}
	shown := exports.Value(filepath.Base(file), false)
	if hasStack {
		return fmt.Errorf("replace the link %s with a file inside the checkout %s. A run given a run key reads the configuration of the checkout only from inside the checkout, and %s links to %s, which is outside it", shown, exports.Value(root, false), shown, exports.Value(at, false))
	}
	return documentOut(root, shown, at)
}

// documentOut is the error for the checkout's document, shown, a link in the checkout at
// root that resolves to at, outside the checkout.
func documentOut(root, shown, at string) error {
	return fmt.Errorf("replace the link %s with a file inside the checkout %s. A run given a run key reads the document of the checkout only from inside the checkout, and %s links to %s, which is outside it", shown, exports.Value(root, false), shown, exports.Value(at, false))
}

// linkOut is the error for the link at path, a file in the checkout at root, which links
// to at, outside the checkout.
func linkOut(root, realRoot, path, at string) error {
	shown := path
	if rel, err := filepath.Rel(realRoot, path); err == nil && within(realRoot, path) {
		shown = rel
	} else {
		shown = ui.Short(path, root)
	}
	shown = exports.Value(shown, false)
	return fmt.Errorf("replace the link %s with a file inside the checkout %s. A run given a run key reads the files of a module or a stack that a file of the checkout lists only from inside the checkout, and %s links to %s, which is outside it", shown, exports.Value(root, false), shown, exports.Value(at, false))
}

// linksInside returns [linkOut] for the first link under dir, a directory inside
// realRoot, that resolves outside realRoot, every link resolved. A link to a directory
// inside realRoot is followed and its tree checked too, each directory once.
func linksInside(root, realRoot, dir string) error {
	seen := map[string]bool{}
	var walk func(dir string) error
	walk = func(dir string) error {
		if seen[dir] {
			return nil
		}
		seen[dir] = true
		return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type()&fs.ModeSymlink == 0 {
				return nil
			}
			at := realPath(path)
			if !within(realRoot, at) {
				return linkOut(root, realRoot, path, at)
			}
			if info, err := os.Stat(at); err == nil && info.IsDir() {
				return walk(at)
			}
			return nil
		})
	}
	err := walk(dir)
	if errors.Is(err, fs.ErrNotExist) {
		// A module that is not there is the compose's error to report.
		return nil
	}
	return err
}

// sourceDir is the directory the path source s resolves to against dir, every link
// resolved, and whether it lies inside realRoot. For a source that selects an export, it
// is the export's directory, which the repository's qory.yaml sets: the repository and
// that file are checked before the file is read. When that qory.yaml is a link that
// resolves outside realRoot, sourceDir returns where it resolves to, and the file as
// linked. For a repository whose exports cannot be read, or that does not export what s
// selects, it is the repository's directory, inside realRoot, and the compose then stops
// with the error of the export.
func sourceDir(realRoot, dir string, s stack.Source) (at, linked string, in bool) {
	if filepath.IsAbs(s.Path) {
		dir = s.Path
	} else {
		dir = filepath.Join(dir, s.Path)
	}
	repo := realPath(dir)
	if !within(realRoot, repo) || (s.Module == "" && s.Stack == "") {
		return repo, "", within(realRoot, repo)
	}
	file, err := exports.File(repo)
	if err != nil || file == "" {
		return repo, "", true
	}
	if at, in := fileAt(realRoot, repo, filepath.Base(file)); !in {
		return at, filepath.Join(repo, filepath.Base(file)), false
	}
	e, err := exports.Read(repo)
	if err != nil || e == nil {
		return repo, "", true
	}
	var rel string
	if s.Stack != "" {
		rel, err = e.Stack(s.Stack)
	} else {
		rel, err = e.Module(s.Module)
	}
	if err != nil {
		return repo, "", true
	}
	at = realPath(filepath.Join(repo, rel))
	return at, "", within(realRoot, at)
}

// fileAt is the file name in dir, every link resolved, and whether it lies inside
// realRoot.
func fileAt(realRoot, dir, name string) (string, bool) {
	at := realPath(filepath.Join(dir, name))
	return at, within(realRoot, at)
}

// maxLinkHops is how many links [realPath] follows by hand before it stops, as the
// operating system stops at a loop.
const maxLinkHops = 40

// realPath is path with every link resolved: the longest part of it that exists, with
// the rest joined on. A link whose target does not exist is followed as far as it goes,
// so a link that resolves outside a directory is outside it before its target exists.
func realPath(path string) string {
	path = filepath.Clean(path)
	var rest []string
	for hops := 0; ; {
		if real, err := filepath.EvalSymlinks(path); err == nil {
			return filepath.Join(append([]string{real}, rest...)...)
		}
		if target, err := os.Readlink(path); err == nil && hops < maxLinkHops {
			hops++
			if !filepath.IsAbs(target) {
				// The target is relative to the link's directory as the operating
				// system resolves it, its links followed first.
				target = filepath.Join(realPath(filepath.Dir(path)), target)
			}
			path = filepath.Clean(target)
			continue
		}
		parent := filepath.Dir(path)
		if parent == path {
			return filepath.Join(append([]string{path}, rest...)...)
		}
		rest = append([]string{filepath.Base(path)}, rest...)
		path = parent
	}
}
