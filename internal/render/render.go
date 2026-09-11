package render

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/module"
)

// Link is one path in a checkout that points into the composed home.
//
// A link whose home path is a file is one symlink. A link whose home path is a directory,
// such as .claude, is written as a real directory in the checkout holding one symlink per
// entry of it, a file or a directory such as .claude/skills, so the checkout keeps its own
// files beside them: Claude Code writes .claude/settings.local.json when a person answers
// a permission prompt, and a repository may own .github/agents. [LinkInto] has the rules.
type Link struct {
	// Checkout is the path relative to the checkout root, such as ".claude".
	Checkout string
	// Home is the path inside the home the link points at, "" being the home itself.
	Home string
	// Soft marks a link that yields to whatever qory did not write at its path.
	Soft bool
}

// Linked is what [LinkInto] did besides writing links, for the caller to report.
type Linked struct {
	// Skipped are the checkout paths of soft links passed over because something qory did
	// not write stands there, such as "AGENTS.md" or ".github/agents/triage.agent.md".
	Skipped []string
	// Replaced are the checkout paths whose tracked, unmodified file or directory was
	// removed for a link under force. git checkout -- restores each of them.
	Replaced []string
}

// ForeignPathError is the refusal to remove or replace a path qory did not write. The
// command module matches it with errors.As and exits with its own status for it.
type ForeignPathError struct {
	// Path is the absolute checkout path that stands in the way.
	Path string
	// Target is the link target when Path is a symlink, "" when it is a file or directory.
	Target string
	// Reason is set when force was asked for and refused, and says why.
	Reason string
}

// Error names the path and what qory found there.
func (e *ForeignPathError) Error() string {
	switch {
	case e.Reason != "":
		return fmt.Sprintf("%s %s; qory does not replace it", e.Path, e.Reason)
	case e.Target != "":
		return fmt.Sprintf("%s links to %s, which qory did not write; qory does not replace it", e.Path, e.Target)
	default:
		return fmt.Sprintf("%s is not a link qory wrote; qory does not replace it", e.Path)
	}
}

// Reserved is one path under a runtime's directory that a files entry may not take,
// because the runtime writes or links something there or the program reads it as its
// own configuration. [CheckFiles] refuses a files entry at the path or, when the path is
// a directory, anywhere under it.
type Reserved struct {
	// Path is the path under the runtime's directory with forward slashes, a file such as
	// "settings.json" or a directory such as "skills". A directory reserves its whole
	// subtree, matched by path segment: "skills" covers "skills/x" and not
	// "skills-private/x".
	Path string
	// Why is the clause the refusal ends with, after "and": what stands at the path and
	// where the module ships the file instead, such as "qory writes it".
	Why string
}

// FilesKind is the entry kind of a file a module ships as it is, at
// files/<runtime>/<path> in the module, for the runtime's directory in the checkout.
const FilesKind = "files"

// Runtime renders for one program that runs the harness. A package under internal/render
// implements it for one program and registers the implementation from its init.
type Runtime interface {
	// Name is the target.runtime value.
	Name() string
	// Render writes the runtime's own files into dir, the runtime's directory inside a
	// staging copy of the home. Paths written into files name home, where the tree ends up.
	// The shared parts, AGENTS.md, skills/ and hooks/, are already at the home's root.
	// [Build] links the runtime's files entries into dir after Render returns.
	Render(res *compose.Result, dir, home string) error
	// Links are the paths a checkout needs so the program reads the home. A nil res asks for
	// the full set, which is what [Unlink] removes.
	Links(res *compose.Result) []Link
	// Skips are the entry kinds the runtime has no place for.
	Skips() []string
	// Reserved are the paths under the runtime's directory a files entry may not take.
	Reserved() []Reserved
}

// runtimes holds every registered runtime by its target.runtime value.
var runtimes = map[string]Runtime{}

// Register adds a runtime, and the runtime package's init is the only caller. A second
// runtime of the same name replaces the first.
func Register(p Runtime) { runtimes[p.Name()] = p }

// Lookup returns the runtime registered for a target.runtime value. The error for a name
// nothing registered lists the names that are.
func Lookup(name string) (Runtime, error) {
	p, ok := runtimes[name]
	if !ok {
		return nil, fmt.Errorf("runtime %q is not one this qory renders; runtimes: %s", name, strings.Join(Names(), ", "))
	}
	return p, nil
}

// Names lists the registered runtimes in alphabetical order.
func Names() []string {
	var names []string
	for n := range runtimes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Composed reports the runtimes the home already holds, by the directories in it that are
// named after a registered runtime. It returns nothing when the home does not exist, which
// is the first compose of a checkout. A directory of any other name is ignored, so a home
// written by a later qory that knows more runtimes loses only what this build cannot render.
func Composed(home string) []Runtime {
	entries, err := os.ReadDir(home)
	if err != nil {
		return nil
	}
	var found []Runtime
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if p, ok := runtimes[e.Name()]; ok {
			found = append(found, p)
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Name() < found[j].Name() })
	return found
}

// Build writes the composed home for one or more runtimes. It stages the tree with
// [BuildAt] in home with ".tmp" appended, discarding whatever that path held, then
// removes the old home and renames the staging directory over it. A failure before the
// rename leaves the previous home as it was.
//
// Every runtime the home is to hold must be passed in one call, because the rename replaces
// the whole tree. A caller composing for one runtime passes the runtimes already in the home
// alongside it, which [Composed] reports, so that the links of a checkout composed for
// several runtimes keep resolving and stay current. Build with no runtime is an error.
func Build(res *compose.Result, home string, runtimes ...Runtime) (err error) {
	if len(runtimes) == 0 {
		return errors.New("build needs at least one runtime")
	}
	// The qory directory and the home are qory's own, and a symlink at either, which a
	// repository can commit, would carry the tree and the remove wherever it points.
	for _, path := range []string{filepath.Dir(home), home} {
		if err := realDir(path); err != nil {
			return err
		}
	}
	tmp := home + ".tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(tmp)
		}
	}()
	if err := BuildAt(res, tmp, home, runtimes...); err != nil {
		return err
	}
	if err := os.RemoveAll(home); err != nil {
		return err
	}
	return os.Rename(tmp, home)
}

// BuildAt writes the composed tree into stage, addressed as home: the paths written into
// files, $QORY_HARNESS_HOME and a server's command say, name home, where the tree is read
// from, while the files themselves go under stage. [Build] stages beside the home and
// renames; a check stages elsewhere and compares. BuildAt links the shared skills and
// hooks, links every module's directory as modules/<name> and writes AGENTS.md at the
// staging root, then calls each runtime's Render for the subdirectory named after it and
// links the runtime's files entries into that subdirectory. The links point at the
// modules by absolute path, so a tree staged anywhere holds the same links.
//
// The files come after Render, so a files entry at a path Render wrote fails the build.
// [CheckFiles] refuses such an entry before a build, and the failure here is the check
// that a runtime's reserved table missed a path.
func BuildAt(res *compose.Result, stage, home string, runtimes ...Runtime) error {
	if len(runtimes) == 0 {
		return errors.New("build needs at least one runtime")
	}
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return err
	}
	if err := LinkEntries(res, stage, "skills", "hooks"); err != nil {
		return err
	}
	if err := linkModules(res, stage); err != nil {
		return err
	}
	if res.Instructions != "" {
		if err := WriteFile(stage, "AGENTS.md", []byte(res.Instructions)); err != nil {
			return err
		}
	}
	for _, p := range runtimes {
		dir := filepath.Join(stage, p.Name())
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := p.Render(res, dir, home); err != nil {
			return err
		}
		if err := linkFiles(res, p, dir); err != nil {
			return err
		}
	}
	return nil
}

// Difference is one path at which two trees differ, as [Diff] reports it.
type Difference struct {
	// Path is the path relative to the trees' roots, with forward slashes.
	Path string
	// What is how the trees differ there: "changed" for a file whose bytes differ,
	// "missing" for a path the first tree holds and the second does not, "extra" for one
	// only the second holds, "target" for a link pointing elsewhere, and "kind" for a file
	// where the other tree holds a directory or a link.
	What string
}

// Diff compares the tree at want with the tree at have and lists every path at which they
// differ, sorted by path. A link is compared by its target and not followed, a file by its
// bytes, and a directory in both trees is the same whatever it holds, since what it holds
// is compared on its own. A check renders the home again into a scratch directory and
// calls Diff with that as want and the home as have, so "missing" is a path a compose
// would write and "extra" one it would remove. A root that does not exist is an empty
// tree.
func Diff(want, have string) ([]Difference, error) {
	a, err := readTree(want)
	if err != nil {
		return nil, err
	}
	b, err := readTree(have)
	if err != nil {
		return nil, err
	}
	paths := map[string]bool{}
	for p := range a {
		paths[p] = true
	}
	for p := range b {
		paths[p] = true
	}
	var out []Difference
	for p := range paths {
		x, inA := a[p]
		y, inB := b[p]
		switch {
		case !inB:
			out = append(out, Difference{p, "missing"})
		case !inA:
			out = append(out, Difference{p, "extra"})
		case x.kind != y.kind:
			out = append(out, Difference{p, "kind"})
		case x.kind == "link" && x.body != y.body:
			out = append(out, Difference{p, "target"})
		case x.kind == "file" && x.body != y.body:
			out = append(out, Difference{p, "changed"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// node is one path of a tree as [readTree] records it: its kind, "dir", "link" or "file",
// and the link's target or the file's bytes.
type node struct {
	kind string
	body string
}

// readTree records every path under root, links not followed, keyed relative to root
// with forward slashes. A root that does not exist is an empty tree.
func readTree(root string) (map[string]node, error) {
	nodes := map[string]node{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root && errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			nodes[key] = node{"link", target}
		case d.IsDir():
			nodes[key] = node{"dir", ""}
		default:
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			nodes[key] = node{"file", string(data)}
		}
		return nil
	})
	return nodes, err
}

// realDir refuses a path that exists and is not a real directory: a symlink or a file
// where qory keeps its own tree is a [*ForeignPathError], because qory writes through
// nothing it did not make. A path that does not exist yet is fine.
func realDir(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(path)
		return &ForeignPathError{Path: path, Target: target}
	}
	if !info.IsDir() {
		return &ForeignPathError{Path: path}
	}
	return nil
}

// linkFiles writes one symlink per files entry of the runtime at dir/<path>, pointing at
// the file in its module, and creates the directories between. A runtime whose Skips
// names the files kind gets none. A path Render already wrote is an error naming the
// entry, because [CheckFiles] is meant to have refused the entry before the build.
func linkFiles(res *compose.Result, p Runtime, dir string) error {
	if contains(p.Skips(), FilesKind) {
		return nil
	}
	for _, e := range res.Entries {
		path, ok := FileFor(e, p.Name())
		if !ok {
			continue
		}
		link := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			return fmt.Errorf("files/%s from module %s: %w", e.Name, e.Module, err)
		}
		if err := os.Symlink(e.Path, link); err != nil {
			return fmt.Errorf("files/%s from module %s: %w", e.Name, e.Module, err)
		}
	}
	return nil
}

// FileFor returns the path under the runtime's directory a files entry lands at, such as
// "rules/nextjs-15.md" for the entry named "claude/rules/nextjs-15.md", and whether e is
// a files entry for the runtime. An entry of another kind or for another runtime is not.
func FileFor(e compose.Entry, runtime string) (string, bool) {
	if e.Kind != FilesKind {
		return "", false
	}
	rt, path, ok := strings.Cut(e.Name, "/")
	if !ok || rt != runtime {
		return "", false
	}
	return path, true
}

// CheckFiles refuses a files entry that names no registered runtime or a path the runtime
// reserves, and returns the first refusal in the result's order. Every entry is checked,
// for the target runtime or not, so a module shipping a wrong file is refused wherever it
// composes. The command module calls it right after the compose, before [Build].
//
// The refusal names the module and the entry as it sits in the module, files/<runtime>/<path>,
// and ends with the runtime's [Reserved] clause, or with the runtimes qory renders when the
// first segment is none of them.
func CheckFiles(res *compose.Result) error {
	for _, e := range res.Entries {
		if e.Kind != FilesKind {
			continue
		}
		rt, path, _ := strings.Cut(e.Name, "/")
		p, ok := runtimes[rt]
		if !ok {
			return fmt.Errorf("module %s ships files/%s, and %s is not a runtime qory renders; runtimes: %s", e.Module, e.Name, rt, strings.Join(Names(), ", "))
		}
		for _, r := range p.Reserved() {
			if path == r.Path || strings.HasPrefix(path, r.Path+"/") {
				return fmt.Errorf("module %s ships files/%s, and %s", e.Module, e.Name, r.Why)
			}
		}
	}
	return nil
}

// LinkInto makes the checkout at root read the home through the runtime's links, and
// returns what it passed over and what it replaced. Every link is relative, so a checkout
// that moves keeps them valid, and every link and the qory directory go into the
// clone-local exclude file, each link's line leaving the file with the link. home is the
// composed tree under that qory directory.
//
// A link to a file is one symlink. A link to a directory is a real directory in the checkout
// holding one symlink per entry of the home's directory, a file or a directory such as
// .claude/skills, so the checkout's own files there stay: .claude/settings.local.json
// beside qory's .claude/settings.json. A symlink qory wrote at the directory's path, the
// way a release before this one linked .claude whole, is removed and the directory made;
// a directory the home leaves empty gets no directory and no link.
//
// A path holding something qory did not write is a [*ForeignPathError] for a hard link and
// is passed over for a soft one, and so is a path behind a directory the checkout links
// elsewhere, such as a committed .github symlink, since nothing there is qory's. With
// force, a tracked and unmodified file or directory of the checkout is removed first and
// its path recorded, for either kind of link; anything untracked or modified is still
// refused, because git checkout -- could not bring it back.
//
// LinkInto also takes back what an earlier compose linked and this one does not: a link the
// runtime no longer asks for, such as AGENTS.override.md once the compose produces no
// instructions, a file inside a linked directory the home no longer has, and a link the
// runtime wrote for a files entry the compose no longer holds, such as
// .github/instructions/web.instructions.md, which Links with a nil result cannot name.
// For the last, LinkInto walks the checkout directories the runtime's links stand in,
// .github say, and removes every symlink into the runtime's directory in the home that
// the current links do not ask for, then each directory that is left empty. It removes
// only links that point into the qory directory. It stops at the first error, so the links
// before it are already written.
func LinkInto(p Runtime, res *compose.Result, root, home string, force bool) (Linked, error) {
	var out Linked
	if err := exclude(root, "/"+checkout.Dir); err != nil {
		return out, err
	}
	current := p.Links(res)
	for _, l := range current {
		src := filepath.Join(home, l.Home)
		info, err := os.Stat(src)
		if err != nil {
			return out, err
		}
		path := filepath.Join(root, l.Checkout)
		if err := inside(root, path); err != nil {
			if l.Soft {
				out.Skipped = append(out.Skipped, l.Checkout)
				continue
			}
			return out, err
		}
		if !info.IsDir() {
			if err := link(root, path, src, l, force, &out); err != nil {
				return out, err
			}
			continue
		}
		skip, err := clearForDir(root, path, l, force, &out)
		if err != nil {
			return out, err
		}
		if skip {
			continue
		}
		children, err := os.ReadDir(src)
		if err != nil {
			return out, err
		}
		keep := map[string]bool{}
		for _, c := range children {
			keep[c.Name()] = true
			child := Link{Checkout: l.Checkout + "/" + c.Name(), Soft: l.Soft}
			if err := link(root, filepath.Join(path, c.Name()), filepath.Join(src, c.Name()), child, force, &out); err != nil {
				return out, err
			}
		}
		if err := prune(root, path, keep); err != nil {
			return out, err
		}
	}
	for _, l := range p.Links(nil) {
		if asksFor(current, l.Checkout) {
			continue
		}
		path := filepath.Join(root, l.Checkout)
		if err := inside(root, path); err != nil {
			continue
		}
		if err := prune(root, path, nil); err != nil {
			return out, err
		}
	}
	if _, err := takeBack(p, root, current); err != nil {
		return out, err
	}
	return out, nil
}

// takeBack removes the runtime's links that current does not ask for from the checkout
// directories the runtime's declared links stand in, such as .github for copilot, and
// returns their checkout paths. A link is the runtime's when it is a relative symlink
// resolving into the runtime's directory in the home, which is where the links of files
// entries point and no other runtime's link does. A link at a current link's path, or
// inside a current directory link, stays. A directory a removal left empty is removed,
// up to and including the one walked. A symlink is never followed, so nothing behind a
// directory the checkout links elsewhere is touched. With no current links, every such
// link goes, which is what [Unlink] asks for.
func takeBack(p Runtime, root string, current []Link) ([]string, error) {
	qoryDir, err := realQoryDir(root)
	if err != nil {
		return nil, nil
	}
	prefix := filepath.Join(qoryDir, "harness", p.Name()) + string(filepath.Separator)
	var removed []string
	for _, dir := range nestedDirs(p.Links(nil)) {
		path := filepath.Join(root, dir)
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if err := takeBackIn(root, path, prefix, current, &removed); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

// takeBackIn is [takeBack] for one real directory, appending each removed link's checkout
// path to removed.
func takeBackIn(root, dir, prefix string, current []Link, removed *[]string) error {
	items, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	before := len(*removed)
	for _, it := range items {
		path := filepath.Join(dir, it.Name())
		if it.IsDir() {
			if err := takeBackIn(root, path, prefix, current, removed); err != nil {
				return err
			}
			continue
		}
		if it.Type()&os.ModeSymlink == 0 {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if asksForOrHolds(current, rel) {
			continue
		}
		target, err := linkTarget(path)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(target, prefix) {
			continue
		}
		own, err := unlinkOwn(root, path)
		if err != nil {
			return err
		}
		if own {
			*removed = append(*removed, rel)
		}
	}
	if len(*removed) > before {
		_ = os.Remove(dir)
	}
	return nil
}

// asksForOrHolds reports whether the links hold one at the checkout path or one whose
// directory the path is inside, such as .github/agents for .github/agents/reviewer.agent.md.
func asksForOrHolds(links []Link, path string) bool {
	for _, l := range links {
		if l.Checkout == path || strings.HasPrefix(path, l.Checkout+"/") {
			return true
		}
	}
	return false
}

// nestedDirs lists, sorted and once each, the first path segment of every link that stands
// inside a directory of the checkout, such as .github for .github/agents; a link at the
// checkout root, such as AGENTS.md or .claude, names none.
func nestedDirs(links []Link) []string {
	seen := map[string]bool{}
	var dirs []string
	for _, l := range links {
		first, _, ok := strings.Cut(l.Checkout, "/")
		if !ok || seen[first] {
			continue
		}
		seen[first] = true
		dirs = append(dirs, first)
	}
	sort.Strings(dirs)
	return dirs
}

// link writes one symlink at path pointing at src, relative, and excludes it. What stands
// at path decides: a link qory wrote is replaced; a foreign path is replaced under force
// when git can restore it, else passed over for a soft link and an error for a hard one.
func link(root, path, src string, l Link, force bool, out *Linked) error {
	err := removeOwnLink(root, path)
	var foreign *ForeignPathError
	if errors.As(err, &foreign) {
		switch {
		case force:
			if reason := checkout.Restorable(root, path); reason != "" {
				return &ForeignPathError{Path: path, Reason: reason}
			}
			if err := os.RemoveAll(path); err != nil {
				return err
			}
			out.Replaced = append(out.Replaced, l.Checkout)
		case l.Soft:
			out.Skipped = append(out.Skipped, l.Checkout)
			return nil
		default:
			return err
		}
	} else if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	target, err := filepath.Rel(filepath.Dir(path), src)
	if err != nil {
		return err
	}
	if err := os.Symlink(target, path); err != nil {
		return err
	}
	return exclude(root, "/"+l.Checkout)
}

// clearForDir makes room for a real directory at path. A directory already there, the
// checkout's own, is left for the links to go into. A symlink qory wrote there is removed.
// A symlink qory did not write, or a file, is foreign and follows the rules of [link]:
// replaced under force when git can restore it, passed over for a soft link, which
// clearForDir reports as skip, and a [*ForeignPathError] for a hard one.
func clearForDir(root, path string, l Link, force bool, out *Linked) (skip bool, err error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.IsDir() {
		return false, nil
	}
	err = removeOwnLink(root, path)
	if info.Mode()&os.ModeSymlink == 0 {
		err = &ForeignPathError{Path: path}
	}
	var foreign *ForeignPathError
	if !errors.As(err, &foreign) {
		return false, err
	}
	switch {
	case force:
		if reason := checkout.Restorable(root, path); reason != "" {
			return false, &ForeignPathError{Path: path, Reason: reason}
		}
		if err := os.RemoveAll(path); err != nil {
			return false, err
		}
		out.Replaced = append(out.Replaced, l.Checkout)
		return false, nil
	case l.Soft:
		out.Skipped = append(out.Skipped, l.Checkout)
		return true, nil
	default:
		return false, err
	}
}

// inside checks that no directory between root and path is a symlink, so a link is
// written, pruned or removed where the checkout is and not wherever a repository's own
// .github symlink points. The first symlink found is a [*ForeignPathError].
func inside(root, path string) error {
	rel, err := filepath.Rel(root, filepath.Dir(path))
	if err != nil {
		return err
	}
	dir := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		dir = filepath.Join(dir, part)
		info, err := os.Lstat(dir)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, _ := os.Readlink(dir)
			return &ForeignPathError{Path: dir, Target: target}
		}
	}
	return nil
}

// asksFor reports whether the links hold one at the checkout path.
func asksFor(links []Link, path string) bool {
	for _, l := range links {
		if l.Checkout == path {
			return true
		}
	}
	return false
}

// prune removes what qory wrote at path that the compose no longer asks for: a symlink
// into the qory directory, or, in a real directory, every such symlink whose name keep does
// not hold, and then the directory itself when nothing is left in it. Anything else at path
// or inside it is left alone, and a path that does not exist is nothing to prune.
func prune(root, path string, keep map[string]bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		_, err := unlinkOwn(root, path)
		return err
	}
	items, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	removed := 0
	for _, it := range items {
		if keep[it.Name()] {
			continue
		}
		own, err := unlinkOwn(root, filepath.Join(path, it.Name()))
		if err != nil {
			return err
		}
		if own {
			removed++
		}
	}
	if removed > 0 {
		_ = os.Remove(path)
	}
	return nil
}

// Unlink removes the runtime's links that qory wrote and returns their checkout paths as
// the runtime declares them: ".claude" once for the links inside it. A link that one of
// the others also declares, such as .agents/skills or AGENTS.md, is left for it, which is
// how one runtime leaves a checkout composed for several. A directory the links left
// empty, .claude itself or a parent such as .agents, is removed too, and a directory
// nothing was removed from is left as it was.
//
// Anything else at a link's path is not qory's to remove: a hard link's path fails, and a
// soft link's path is left alone, which is how a checkout's own AGENTS.md survives. Inside a
// linked directory only the symlinks into the qory directory go, so
// .claude/settings.local.json stays and so does the directory holding it. A link the
// runtime wrote for a files entry, which Links with a nil result cannot name, goes the way
// [LinkInto] takes one back and is returned by its own path, such as
// .github/instructions/web.instructions.md. Each removed link's line leaves the
// clone-local exclude file with it. Unlink leaves the home for the caller to remove, and
// the qory directory's own line with it, through [RemoveExclude].
func Unlink(p Runtime, root string, others ...Runtime) ([]string, error) {
	shared := map[string]bool{}
	for _, o := range others {
		for _, l := range o.Links(nil) {
			shared[l.Checkout] = true
		}
	}
	var removed []string
	for _, l := range p.Links(nil) {
		if shared[l.Checkout] {
			continue
		}
		path := filepath.Join(root, l.Checkout)
		if err := inside(root, path); err != nil {
			// A symlinked parent is the checkout's own; what lies behind it is not
			// qory's to remove.
			continue
		}
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return removed, err
		}
		if info.IsDir() {
			n, err := removeOwnLinks(root, path)
			if err != nil {
				return removed, err
			}
			if n == 0 {
				continue
			}
			removed = append(removed, l.Checkout)
			_ = os.Remove(path)
		} else {
			if err := removeOwnLink(root, path); err != nil {
				if l.Soft {
					continue
				}
				return removed, err
			}
			if err := unexclude(root, "/"+l.Checkout); err != nil {
				return removed, err
			}
			removed = append(removed, l.Checkout)
		}
		if parent := filepath.Dir(path); parent != root {
			_ = os.Remove(parent)
		}
	}
	files, err := takeBack(p, root, nil)
	removed = append(removed, files...)
	return removed, err
}

// LinkModules writes the links a stack asks for to its modules: for every module with a
// Link, a hard link at root/<Link> pointing, relative, at home/modules/<name>, under the
// rules of a hard file link of [LinkInto]: a path qory did not write is a
// [*ForeignPathError], and so is a symlinked parent, or replaced under force when git can
// restore it, with its path recorded; anything untracked or modified is refused with the
// reason git gives. The link is hard because permission rules and scripts name the path.
// Every link written is listed in the clone-local exclude file. LinkModules then takes back
// the links of previous, the names an earlier compose linked, that no module links now,
// when qory wrote them; a name that now holds something else is left alone.
//
// Before it writes anything, a Link at a path a registered runtime links, or at the qory
// directory, is refused with an error naming the runtime, because a module link there
// would stand where the runtime's link goes.
func LinkModules(res *compose.Result, root, home string, previous []string, force bool) (Linked, error) {
	var out Linked
	for _, l := range res.Modules {
		if l.Link == "" {
			continue
		}
		if err := moduleLinkFree(l.Link); err != nil {
			return out, err
		}
	}
	current := map[string]bool{}
	for _, l := range res.Modules {
		if l.Link == "" {
			continue
		}
		current[l.Link] = true
		src := filepath.Join(home, "modules", l.Name)
		if _, err := os.Stat(src); err != nil {
			return out, err
		}
		path := filepath.Join(root, l.Link)
		hard := Link{Checkout: l.Link, Home: "modules/" + l.Name}
		if err := inside(root, path); err != nil {
			return out, err
		}
		if err := link(root, path, src, hard, force, &out); err != nil {
			return out, err
		}
	}
	for _, name := range previous {
		if current[name] {
			continue
		}
		path := filepath.Join(root, name)
		if err := inside(root, path); err != nil {
			continue
		}
		if _, err := unlinkOwn(root, path); err != nil {
			return out, err
		}
	}
	return out, nil
}

// moduleLinkFree refuses a module link name that a registered runtime links or that is the
// qory directory.
func moduleLinkFree(name string) error {
	if name == checkout.Dir {
		return fmt.Errorf("link %s is qory's own directory", name)
	}
	for _, rt := range Names() {
		for _, l := range runtimes[rt].Links(nil) {
			if l.Checkout == name {
				return fmt.Errorf("link %s is where the %s runtime links %s", name, rt, l.Checkout)
			}
		}
	}
	return nil
}

// UnlinkModules removes the module links of the names in links that qory wrote, each with
// its exclude line, and returns the names it removed. A name holding anything else, or
// nothing, is passed over and no error.
func UnlinkModules(root string, links []string) ([]string, error) {
	var removed []string
	for _, name := range links {
		path := filepath.Join(root, name)
		if err := inside(root, path); err != nil {
			continue
		}
		own, err := unlinkOwn(root, path)
		if err != nil {
			return removed, err
		}
		if own {
			removed = append(removed, name)
		}
	}
	return removed, nil
}

// modulesPrefix is what the target of a module link starts with, from the checkout root:
// the qory directory, the home and its modules directory.
var modulesPrefix = filepath.Join(checkout.Dir, "harness", "modules") + string(filepath.Separator)

// UnlinkModuleLinks removes every module link in the checkout root without the report that
// names them, which is how a remove works after rm -rf .qory. It reads the root's own
// entries and takes each symlink whose relative target, cleaned, is under
// .qory/harness/modules, under the rules of [UnlinkModules], each with its exclude line, and
// returns the names removed in sorted order. A symlink to anywhere else, an absolute one,
// and a link inside a directory stay.
func UnlinkModuleLinks(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, e := range entries {
		if e.Type()&os.ModeSymlink == 0 {
			continue
		}
		path := filepath.Join(root, e.Name())
		target, err := os.Readlink(path)
		if err != nil {
			return removed, err
		}
		if filepath.IsAbs(target) || !strings.HasPrefix(filepath.Clean(target), modulesPrefix) {
			continue
		}
		own, err := unlinkOwn(root, path)
		if err != nil {
			return removed, err
		}
		if own {
			removed = append(removed, e.Name())
		}
	}
	sort.Strings(removed)
	return removed, nil
}

// removeOwnLinks removes every symlink into the checkout's qory directory inside dir, with
// its exclude line, and returns how many it removed. Files, directories and other links
// stay.
func removeOwnLinks(root, dir string) (int, error) {
	items, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, it := range items {
		own, err := unlinkOwn(root, filepath.Join(dir, it.Name()))
		if err != nil {
			return n, err
		}
		if own {
			n++
		}
	}
	return n, nil
}

// unlinkOwn removes path when it is a link qory wrote into this checkout, takes its line
// out of the exclude file, and reports whether it did. Anything else at path, or nothing,
// is left alone and reads as false.
func unlinkOwn(root, path string) (bool, error) {
	own, err := ownLink(root, path)
	if err != nil || !own {
		return false, err
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	line, err := excludeLine(root, path)
	if err != nil {
		return false, err
	}
	return true, unexclude(root, line)
}

// excludeLine is the exclude file's line for path: a slash and the path relative to
// root, with forward slashes, the way [exclude] writes a link's line.
func excludeLine(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	return "/" + filepath.ToSlash(rel), nil
}

// ownLink reports whether path is a link qory wrote into this checkout: a relative symlink
// that resolves, from the directory it really stands in, into the checkout's own qory
// directory. A link into another checkout's qory directory, reached through a symlinked
// parent say, is not qory's here. A path that does not exist reads as not qory's. A qory
// directory that is gone, after rm -rf .qory say, still has its place under the resolved
// root, so the links left dangling are still qory's and a remove takes them.
func ownLink(root, path string) (bool, error) {
	resolved, err := linkTarget(path)
	if err != nil || resolved == "" {
		return false, err
	}
	qoryDir, err := realQoryDir(root)
	if err != nil {
		return false, nil
	}
	return resolved == qoryDir || strings.HasPrefix(resolved, qoryDir+string(filepath.Separator)), nil
}

// linkTarget is where the relative symlink at path points: its target joined to the
// directory it really stands in, every symlink above resolved, and cleaned. It is "" for
// a path that does not exist, is not a symlink, holds an absolute target, or stands in a
// directory that cannot be resolved.
func linkTarget(path string) (string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return "", nil
	}
	target, err := os.Readlink(path)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(target) {
		return "", nil
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", nil
	}
	return filepath.Clean(filepath.Join(parent, target)), nil
}

// realQoryDir is the checkout's qory directory with every symlink above it resolved, the
// form a link's resolved target takes. When the directory does not exist, it is the
// resolved root joined with [checkout.Dir]. Any other failure to resolve it is an error.
func realQoryDir(root string) (string, error) {
	dir := checkout.QoryDir(root)
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			return "", err
		}
		return filepath.Clean(filepath.Join(resolved, checkout.Dir)), nil
	}
	return filepath.EvalSymlinks(dir)
}

// removeOwnLink removes path when it is a link qory wrote into this checkout. A path that
// does not exist is nothing to remove and no error. A regular file, a directory, or a
// link pointing elsewhere is a [*ForeignPathError] naming what stands in the way, and it
// is the check that keeps a render from eating a repository's own files.
func removeOwnLink(root, path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return &ForeignPathError{Path: path}
	}
	own, err := ownLink(root, path)
	if err != nil {
		return err
	}
	if !own {
		target, _ := os.Readlink(path)
		return &ForeignPathError{Path: path, Target: target}
	}
	return os.Remove(path)
}

// exclude appends one line to the checkout's clone-local exclude file, once. A checkout
// outside git has no such file, and then there is nothing to exclude and no error.
func exclude(root, line string) error {
	file := checkout.ExcludeFile(root)
	if file == "" {
		return nil
	}
	data, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == line {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	text := string(data)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return os.WriteFile(file, []byte(text+line+"\n"), 0o644)
}

// RemoveExclude takes one line out of the checkout's clone-local exclude file, the way
// the links take theirs when they go. The command module calls it for the qory directory's
// own line once it has removed the directory.
func RemoveExclude(root, line string) error { return unexclude(root, line) }

// unexclude removes every line of the checkout's clone-local exclude file that reads as
// line, and leaves the file's other bytes as they are. The file is the repository's, one
// for every worktree of it, so the line stays while another worktree still has something
// at the path it names: that worktree composed it, and its own remove takes the line in
// turn. A file without the line, no file, or a checkout outside git is nothing to change
// and no error.
func unexclude(root, line string) error {
	file := checkout.ExcludeFile(root)
	if file == "" {
		return nil
	}
	data, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var kept []string
	found := false
	for _, l := range strings.SplitAfter(string(data), "\n") {
		if strings.TrimSpace(l) == line {
			found = true
			continue
		}
		kept = append(kept, l)
	}
	if !found || heldElsewhere(root, line) {
		return nil
	}
	return os.WriteFile(file, []byte(strings.Join(kept, "")), 0o644)
}

// heldElsewhere reports whether a worktree of the repository other than root has
// something, a dangling link included, at the path an exclude line names. Git lists
// worktrees by their real paths, so root is told apart by its own.
func heldElsewhere(root, line string) bool {
	self := root
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		self = resolved
	}
	rel := filepath.FromSlash(strings.TrimPrefix(line, "/"))
	for _, wt := range checkout.Worktrees(root) {
		if wt == self {
			continue
		}
		if _, err := os.Lstat(filepath.Join(wt, rel)); err == nil {
			return true
		}
	}
	return false
}

// LinkEntries writes one symlink per composed entry of the kinds in keep, at
// dir/<kind>/<name>, pointing at the entry in its module. Skills and hooks keep their
// name, the Markdown kinds get ".md" appended. Every kind directory named is created,
// with no entry of that kind as well, so a checkout link to it resolves. Entries of any
// other kind are left out, and it is the caller's job to pass every kind the runtime
// reads from a link.
func LinkEntries(res *compose.Result, dir string, keep ...string) error {
	wanted := map[string]bool{}
	for _, k := range keep {
		wanted[k] = true
		if err := os.MkdirAll(filepath.Join(dir, k), 0o755); err != nil {
			return err
		}
	}
	for _, e := range res.Entries {
		if !wanted[e.Kind] {
			continue
		}
		link := filepath.Join(dir, e.Kind, e.Name)
		switch e.Kind {
		case "skills", "hooks":
		default:
			link += ".md"
		}
		if err := os.Symlink(e.Path, link); err != nil {
			return err
		}
	}
	return nil
}

// linkModules writes one symlink per module at dir/modules/<name>, pointing at the module's
// directory, so a settings fragment or a hook reaches a module's other files as
// $QORY_HARNESS_HOME/modules/<name>/<path>.
func linkModules(res *compose.Result, dir string) error {
	modules := filepath.Join(dir, "modules")
	if err := os.MkdirAll(modules, 0o755); err != nil {
		return err
	}
	for _, l := range res.Modules {
		if err := os.Symlink(l.Dir, filepath.Join(modules, l.Name)); err != nil {
			return err
		}
	}
	return nil
}

// Skipped lists the composed entries of the kinds a runtime has no place for, as
// "<kind>/<name>", in the order the compose produced them. The compose prints the list so
// an entry that went nowhere is announced rather than silently dropped. A files entry is
// listed only when it is for the runtime, as "files/goose/<path>": one for another runtime
// is neither rendered nor skipped, like a settings fragment for another runtime.
func Skipped(p Runtime, res *compose.Result) []string {
	skip := map[string]bool{}
	for _, k := range p.Skips() {
		skip[k] = true
	}
	var out []string
	for _, e := range res.Entries {
		if !skip[e.Kind] {
			continue
		}
		if e.Kind == FilesKind {
			if _, ok := FileFor(e, p.Name()); !ok {
				continue
			}
		}
		out = append(out, e.Kind+"/"+e.Name)
	}
	return out
}

// WriteSettings writes the runtime's merged settings files into dir, one per target file
// a module contributed a fragment to, with every $QORY_HARNESS_HOME already replaced by
// home. A ".toml" name is encoded as TOML, every other name as indented JSON.
//
// patch, when it is not nil, is called with each file's name and its merged map before
// the encoding, and changing the map there is how a runtime writes the target model.
// Names in ensure are written even when no module contributed to them, so a model reaches
// a file that would otherwise not exist.
func WriteSettings(res *compose.Result, runtime, dir, home string, ensure []string, patch func(file string, m map[string]any)) error {
	files := res.SettingsFiles(runtime)
	for _, e := range ensure {
		found := false
		for _, f := range files {
			if f == e {
				found = true
			}
		}
		if !found {
			files = append(files, e)
		}
	}
	for _, file := range files {
		m := res.SettingsFor(runtime, file, home)
		if patch != nil {
			patch(file, m)
		}
		if filepath.Ext(file) == ".toml" {
			data, err := EncodeTOML(m)
			if err != nil {
				return err
			}
			if err := WriteFile(dir, file, data); err != nil {
				return err
			}
			continue
		}
		if err := WriteJSON(dir, file, m); err != nil {
			return err
		}
	}
	return nil
}

// EncodeTOML encodes a settings map as TOML. A list whose every element is a map becomes
// an array of tables, which the encoder will not do for a list of the untyped maps a JSON
// or YAML decoder produces.
func EncodeTOML(m map[string]any) ([]byte, error) {
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(tables(m)); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// tables retypes a value for the TOML encoder: a []any of maps becomes a
// []map[string]any, the shape the encoder emits as an array of tables. A list with one
// non-map element is left as it is, because a mixed list is no array of tables.
func tables(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			out[k] = tables(e)
		}
		return out
	case []any:
		allMaps := len(x) > 0
		for _, e := range x {
			if _, ok := e.(map[string]any); !ok {
				allMaps = false
			}
		}
		if !allMaps {
			return x
		}
		out := make([]map[string]any, len(x))
		for i, e := range x {
			out[i] = tables(e).(map[string]any)
		}
		return out
	default:
		return v
	}
}

// WriteAgents writes one Markdown file per composed agent at dir/sub/<name><suffix>,
// where suffix carries the runtime's extension, such as ".agent.md". Each file is the
// agent's body under frontmatter holding only the keys in keep that the source agent has,
// so a key one runtime reads does not reach another. When keep names "name" and the
// source has none, the entry's name fills it. The keys come out in alphabetical order,
// not the order of keep.
func WriteAgents(res *compose.Result, dir, sub, suffix string, keep ...string) error {
	for _, e := range res.Entries {
		if e.Kind != "agents" {
			continue
		}
		doc, err := module.ReadDocument(e.Path)
		if err != nil {
			return err
		}
		front := map[string]any{}
		for _, k := range keep {
			if v, ok := doc.Front[k]; ok {
				front[k] = v
			}
		}
		if _, ok := front["name"]; !ok && contains(keep, "name") {
			front["name"] = e.Name
		}
		var b bytes.Buffer
		b.WriteString("---\n")
		data, err := yaml.Marshal(front)
		if err != nil {
			return err
		}
		b.Write(data)
		b.WriteString("---\n\n")
		b.WriteString(doc.Body)
		if err := WriteFile(dir, filepath.Join(sub, e.Name+suffix), b.Bytes()); err != nil {
			return err
		}
	}
	return nil
}

// contains reports whether list holds s.
func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// PutServers merges the MCP servers into m under key, on top of whatever a settings
// fragment put there, so a server declared as an entry wins over one written into a
// fragment by hand. It leaves m alone when servers is nil.
func PutServers(m map[string]any, key string, servers map[string]any) {
	if servers == nil {
		return
	}
	existing, _ := m[key].(map[string]any)
	if existing == nil {
		existing = map[string]any{}
	}
	for name, v := range servers {
		existing[name] = v
	}
	m[key] = existing
}

// WriteJSON writes v as indented JSON with a trailing newline to dir/name, through
// [WriteFile].
func WriteJSON(dir, name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteFile(dir, name, append(data, '\n'))
}

// WriteFile writes data to dir/name with mode 0644, creating the parent directories and
// overwriting a file already there. name may hold separators, such as
// "agents/reviewer.md".
func WriteFile(dir, name string, data []byte) error {
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Env is the variables a runtime writes where it keeps environment: the fragment's own,
// which existing is the map already there, then what the harness exports rendered for
// home, then QORY_HARNESS_HOME as the home, which nothing overrides. The result is a new
// map with string values, the shape a settings file encodes.
func Env(existing any, res *compose.Result, home string) map[string]any {
	env := map[string]any{}
	if m, ok := existing.(map[string]any); ok {
		for k, v := range m {
			env[k] = v
		}
	}
	for name, value := range res.EnvFor(home) {
		env[name] = value
	}
	env["QORY_HARNESS_HOME"] = home
	return env
}
