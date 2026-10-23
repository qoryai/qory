package render

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/module"
)

// maxEntries is the most files, directories and links qory copies for one module, and
// the most a home's claude/plugin holds when the home holds a copy: Forager's check of a
// walled Claude Code launch reads no more under one --plugin-dir.
const maxEntries = 10_000

// maxFileBytes is the largest file qory copies or reads from a module that lies in the
// checkout. A copy streams each file; only a Markdown file the copy edits, and a file
// [readEntry] reads, are held in memory, one at a time, so memory is bounded. The cap is
// per file: the total a copy writes to disk has no cap.
const maxFileBytes = 64 << 20

// maxModuleBytes is the most bytes one copy of a module, or of one of its entries,
// writes, counted as they are written. A variable so a test can lower it.
var maxModuleBytes int64 = 1 << 30

// copier copies the files of one module that lies in the checkout into a home, see
// [compose.Module.Copy]. It reads through the [os.Root] the compose opened at the
// module's directory, so a link inside the module is followed only within the module.
type copier struct {
	name     string   // the module's name as a refusal shows it
	dir      string   // the module's directory with every link resolved
	written  string   // the module's directory as the compose read it, which entry paths start with
	root     *os.Root // [compose.Module.Root], which [compose.Result.Close] closes
	real     string   // [compose.Module.RealDir], where an absolute link may lead
	anywhere bool     // [compose.Module.Walled] or [compose.Module.Hidden]: moving the module out of the checkout copies it too
}

// openCopier is the copier of m, which reads through [compose.Module.Root], the module
// directory the compose opened before it read the manifest. It refuses the module when
// the compose opened no root, and when m.Dir, with every link resolved, is no longer that
// directory, since a link inside the module is followed by that path.
func openCopier(m compose.Module) (*copier, error) {
	name := accesskey.ShownName(m.Name, false)
	moved := fmt.Errorf("module %s: Compose again. %s leads to another directory than when qory read the module, so qory did not copy it into the home.", name, exports.HidePath(m.Source))
	if m.Root == nil || m.DirInfo == nil {
		return nil, moved
	}
	real, err := filepath.EvalSymlinks(m.Dir)
	if err != nil {
		return nil, moved
	}
	info, err := os.Stat(real)
	if err != nil || !os.SameFile(info, m.DirInfo) {
		return nil, moved
	}
	return &copier{name: name, dir: real, written: m.Dir, root: m.Root, real: m.RealDir, anywhere: m.Walled || m.Hidden}, nil
}

// counter counts the entries a copy writes and refuses past [maxEntries].
type counter struct {
	n      int
	refuse error // what the copy returns past the limit
}

// add counts one entry more.
func (n *counter) add() error {
	n.n++
	if n.n > maxEntries {
		return n.refuse
	}
	return nil
}

// tooMany is the refusal of a module whose copy passes [maxEntries].
func (c *copier) tooMany() error {
	if c.anywhere {
		return fmt.Errorf("module %s: Ship fewer files. It contains more than 10,000 files, directories and links, the most qory copies into the home.", c.name)
	}
	return fmt.Errorf("module %s: Move the module out of the checkout, or ship fewer files. It contains more than 10,000 files, directories and links, the most qory copies into the home.", c.name)
}

// copyRules are what one copy leaves out and changes.
type copyRules struct {
	edit     func([]byte) []byte // what each Markdown file's bytes go through, nil for nothing
	skip     string              // a name left out at any depth
	top      string              // a name left out at the top of the copy alone
	leave    map[string]bool     // the paths, relative to the module's directory, left out
	left     []leftEntry         // what each path in leave is, see [copier.leaveOut]
	dangling bool                // whether a link that leads nowhere inside the module is left out
	n        *counter
	bytes    *budget
}

// leftEntry is one path a copy leaves out: the file it opens, and its path relative to the
// module's directory with every link followed, "" when that path could not be resolved.
type leftEntry struct {
	info fs.FileInfo
	at   string
}

// leaveOut adds each path in leave, an absolute path inside the module that the stack's
// only or exclude left out, to what r leaves out: by its path, by the file it opens, and,
// for a directory, by every path inside it, so a link elsewhere in the module that leads
// to a left-out entry, or into one, is left out too.
func (c *copier) leaveOut(r *copyRules, leave []string) {
	if r.leave == nil {
		r.leave = map[string]bool{}
	}
	for _, path := range leave {
		rel, err := c.rel(path)
		if err != nil || rel == "." {
			continue
		}
		r.leave[rel] = true
		info, err := c.stat(rel)
		if err != nil {
			continue
		}
		at, err := module.ResolveIn(c.root, c.real, rel)
		if err != nil || at == "." {
			at = ""
		}
		r.left = append(r.left, leftEntry{info: info, at: at})
	}
}

// isLeft reports whether rel, opened as the file opened, is a path r leaves out: the same
// file as a left-out entry, or a path that, with every link followed, lies inside one.
func (c *copier) isLeft(rel string, opened fs.FileInfo, r *copyRules) bool {
	if len(r.left) == 0 || rel == "." {
		return false
	}
	at, err := module.ResolveIn(c.root, c.real, rel)
	for _, l := range r.left {
		if os.SameFile(l.info, opened) {
			return true
		}
		if err == nil && l.at != "" && (at == l.at || strings.HasPrefix(at, l.at+string(filepath.Separator))) {
			return true
		}
	}
	return false
}

// budget is the bytes one copy may still write, see [maxModuleBytes].
type budget struct {
	left   int64
	refuse error // what the copy returns past the budget
}

// budget is a full budget for one copy of the module.
func (c *copier) budget() *budget {
	if c.anywhere {
		return &budget{left: maxModuleBytes, refuse: fmt.Errorf("module %s: Ship fewer files. Its files add up to more than 1 GiB, the most qory copies into the home.", c.name)}
	}
	return &budget{left: maxModuleBytes, refuse: fmt.Errorf("module %s: Move the module out of the checkout, or ship fewer files. Its files add up to more than 1 GiB, the most qory copies into the home.", c.name)}
}

// budgetWriter writes to w and counts each write against b. A write that would pass
// the budget writes nothing and returns the budget's refusal.
type budgetWriter struct {
	w io.Writer
	b *budget
}

// Write writes p when the budget has room for it.
func (bw budgetWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > bw.b.left {
		return 0, bw.b.refuse
	}
	n, err := bw.w.Write(p)
	bw.b.left -= int64(n)
	return n, err
}

// copyModule copies the directory of m to the path to in out, which does not exist yet,
// as the home's modules/<name>, leaving out every entry named .git, the entry named .qory
// at the module's top, and every link that leads nowhere inside the module, so the home
// has no file at its name. A link that leads out of the module is refused whether or not
// it leads anywhere. It refuses a module of more than [maxEntries] entries below its
// directory. out is a directory of the staging tree, and every write goes through it, so
// none follows a link out of it. .qory is left out at the top alone: a module that is a
// checkout holds the checkout's qory directory there, with a home inside the checkout
// whose links lead back into the module, and a .qory deeper down is the module's content.
// Each path in leave, an absolute path inside the module that the stack's only or exclude
// left out, is left out too.
func copyModule(m compose.Module, out *os.Root, to string, leave []string) error {
	c, err := openCopier(m)
	if err != nil {
		return err
	}
	r := &copyRules{skip: ".git", top: checkout.Dir, dangling: true, n: &counter{refuse: c.tooMany()}, bytes: c.budget()}
	c.leaveOut(r, leave)
	// An error of the file system, such as a write into the staging tree, contains a path
	// that may contain an access key secret.
	return exports.HideError(c.copy(".", out, to, r))
}

// copyEntry copies the file or directory at path, an absolute path inside the directory
// of m as the compose read it, to the path to in out, which does not exist yet, the way
// [copyModule] writes, leaving out every entry named .git as that copy does, so the copy
// of an entry is part of the copy of its module. edit, when not nil, gets the bytes of
// each Markdown file and returns what to write. A copy of more than [maxEntries] entries
// is refused as a module of that many is. Each path in leave is left out as [copyModule]
// leaves it out.
func copyEntry(m compose.Module, path string, out *os.Root, to string, edit func([]byte) []byte, leave []string) error {
	c, err := openCopier(m)
	if err != nil {
		return err
	}
	rel, err := c.rel(path)
	if err != nil {
		return err
	}
	r := &copyRules{edit: edit, skip: ".git", n: &counter{refuse: c.tooMany()}, bytes: c.budget()}
	c.leaveOut(r, leave)
	return exports.HideError(c.copy(rel, out, to, r))
}

// errLeftOut is what [readEntry] returns for a file that a copy leaves out, which the
// caller then writes nowhere in the home.
var errLeftOut = errors.New("left out of the copy")

// readEntry reads the file at path, an absolute path inside the directory of m as the
// compose read it, the way [copyEntry] reads one. It returns [errLeftOut] for a file that
// a copy with leave leaves out, see [copier.leaveOut].
func readEntry(m compose.Module, path string, leave []string) ([]byte, error) {
	c, err := openCopier(m)
	if err != nil {
		return nil, err
	}
	rel, err := c.rel(path)
	if err != nil {
		return nil, err
	}
	r := &copyRules{}
	c.leaveOut(r, leave)
	info, err := c.stat(rel)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, c.notCopyable(rel)
	}
	f, info, err := c.open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if r.leave[rel] || c.isLeft(rel, info, r) {
		return nil, errLeftOut
	}
	if !info.Mode().IsRegular() {
		return nil, c.notCopyable(rel)
	}
	return c.readAll(rel, f, info)
}

// readAll reads f, the file at rel, and refuses it past [maxFileBytes], by its size or
// by what the read returns, for a file that grows while it is read.
func (c *copier) readAll(rel string, f *os.File, info fs.FileInfo) ([]byte, error) {
	if info.Size() > maxFileBytes {
		return nil, c.tooLarge(rel)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, c.failed(rel, err)
	}
	if len(data) > maxFileBytes {
		return nil, c.tooLarge(rel)
	}
	return data, nil
}

// tooLarge is the refusal of a file at rel larger than [maxFileBytes].
func (c *copier) tooLarge(rel string) error {
	if c.anywhere {
		return fmt.Errorf("module %s: Ship %s under %d MiB. It is larger than %d MiB, the most qory copies into the home.", c.name, exports.HidePath(filepath.ToSlash(rel)), maxFileBytes>>20, maxFileBytes>>20)
	}
	return fmt.Errorf("module %s: Move the module out of the checkout, or ship %s under %d MiB. It is larger than %d MiB, the most qory copies into the home.", c.name, exports.HidePath(filepath.ToSlash(rel)), maxFileBytes>>20, maxFileBytes>>20)
}

// rel is path, inside the module's directory as the compose read it, relative to that
// directory.
func (c *copier) rel(path string) (string, error) {
	rel, err := filepath.Rel(c.written, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("module %s: Move %s inside the module. It lies outside the module, and qory copies only the module's files into the home.", c.name, exports.HidePath(path))
	}
	return rel, nil
}

// copy copies rel, a path relative to the module's directory, to the path to in out: a
// file with its bytes, a directory with everything in it, and a link as what it leads to.
// Every directory above rel in the module counts as above it for the loop check.
func (c *copier) copy(rel string, out *os.Root, to string, r *copyRules) error {
	var above []fs.FileInfo
	if rel != "." {
		dirs := []string{"."}
		if parent := filepath.Dir(rel); parent != "." {
			parts := strings.Split(parent, string(filepath.Separator))
			for i := range parts {
				dirs = append(dirs, filepath.Join(parts[:i+1]...))
			}
		}
		for _, d := range dirs {
			info, err := c.stat(d)
			if err != nil {
				return err
			}
			above = append(above, info)
		}
	}
	return c.copyAt(rel, out, to, above, r)
}

// copyAt is [copier.copy] for one path, with above the directories the copy is inside.
func (c *copier) copyAt(rel string, out *os.Root, to string, above []fs.FileInfo, r *copyRules) error {
	if r.dangling && c.leadsNowhere(rel) {
		return nil
	}
	info, err := c.stat(rel)
	if err != nil {
		return err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return c.notCopyable(rel)
	}
	f, opened, err := c.open(rel)
	if err != nil {
		return err
	}
	if c.isLeft(rel, opened, r) {
		f.Close()
		return nil
	}
	if opened.IsDir() != info.IsDir() || !opened.IsDir() && !opened.Mode().IsRegular() {
		f.Close()
		return c.notCopyable(rel)
	}
	if !opened.IsDir() {
		defer f.Close()
		return c.copyFile(rel, f, opened, out, to, r)
	}
	for _, a := range above {
		if os.SameFile(a, opened) {
			f.Close()
			return fmt.Errorf("module %s: Remove %s, or point it at a directory that is not above it. It leads to a directory above it, which qory cannot copy into the home.", c.name, exports.HidePath(filepath.ToSlash(rel)))
		}
	}
	entries, err := readCounted(f, func(e fs.DirEntry) bool {
		return r.skip != "" && e.Name() == r.skip || r.top != "" && rel == "." && e.Name() == r.top || r.leave[filepath.Join(rel, e.Name())]
	}, r.n.add)
	f.Close()
	if err != nil {
		if err == r.n.refuse {
			return err
		}
		return c.failed(rel, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	mode := dirMode(opened.Mode())
	if err := out.Mkdir(to, mode); err != nil {
		return err
	}
	if err := out.Chmod(to, mode); err != nil {
		return err
	}
	inside := append(above[:len(above):len(above)], opened)
	for _, e := range entries {
		if err := c.copyAt(filepath.Join(rel, e.Name()), out, filepath.Join(to, e.Name()), inside, r); err != nil {
			return err
		}
	}
	return nil
}

// readBatch is how many entries [readCounted] reads from a directory at a time.
const readBatch = 256

// readCounted reads the entries of the directory f, [readBatch] at a time, leaves out each
// one skip returns true for, and calls count for each other one as it is read, so a count
// that refuses stops the read of a large directory early. It returns count's error, or
// the read's.
func readCounted(f *os.File, skip func(fs.DirEntry) bool, count func() error) ([]fs.DirEntry, error) {
	var entries []fs.DirEntry
	for {
		batch, err := f.ReadDir(readBatch)
		for _, e := range batch {
			if skip != nil && skip(e) {
				continue
			}
			if cerr := count(); cerr != nil {
				return nil, cerr
			}
			entries = append(entries, e)
		}
		if err == io.EOF {
			return entries, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// leadsNowhere reports whether rel is a link that, followed through the root, leads to
// nothing inside the module. A link that leads out of the module fails in the root with
// another error than one for a missing file, so it is not one of them, and the copy
// refuses it. The answer comes from the root alone, and a link that leads nowhere is then
// never opened.
func (c *copier) leadsNowhere(rel string) bool {
	info, err := c.root.Lstat(rel)
	if err != nil || info.Mode()&fs.ModeSymlink == 0 {
		return false
	}
	_, err = c.root.Stat(rel)
	return errors.Is(err, fs.ErrNotExist)
}

// stat is what rel leads to: the entry itself, or what a link there leads to inside the
// module.
func (c *copier) stat(rel string) (fs.FileInfo, error) {
	info, err := c.root.Lstat(rel)
	if err == nil && info.Mode()&fs.ModeSymlink != 0 {
		info, err = c.root.Stat(rel)
	}
	if err != nil {
		inner, ok := c.inner(rel)
		if !ok {
			return nil, c.failed(rel, err)
		}
		if info, err = c.root.Stat(inner); err != nil {
			return nil, c.failed(rel, err)
		}
	}
	return info, nil
}

// open opens rel for reading, without waiting on a pipe or becoming the terminal of the
// process, and returns what the opened file is.
func (c *copier) open(rel string) (*os.File, fs.FileInfo, error) {
	f, err := c.root.OpenFile(rel, os.O_RDONLY|copyFlags, 0)
	if err != nil {
		inner, ok := c.inner(rel)
		if !ok {
			return nil, nil, c.failed(rel, err)
		}
		if f, err = c.root.OpenFile(inner, os.O_RDONLY|copyFlags, 0); err != nil {
			return nil, nil, c.failed(rel, err)
		}
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, c.failed(rel, err)
	}
	return f, info, nil
}

// inner is the path rel leads to with every link followed through the root, as
// [module.ResolveIn] follows it, when that path is not rel. The root follows no absolute
// link, while a module read takes one that leads into the module as inside it; the root
// then opens the path the link leads to, which holds no link. Nothing is resolved
// outside the root.
func (c *copier) inner(rel string) (string, bool) {
	inner, err := module.ResolveIn(c.root, c.real, rel)
	if err != nil || inner == filepath.Clean(rel) {
		return "", false
	}
	return inner, true
}

// fileMode is the mode of the copy of a file of mode m: m's permission bits without write
// for group and others. The setuid, setgid and sticky bits are not permission bits, so
// the copy has none of them.
func fileMode(m fs.FileMode) fs.FileMode { return m.Perm() &^ 0o022 }

// dirMode is the mode of the copy of a directory of mode m: [fileMode] with read, write
// and search for the owner, so qory can write the copy into it and remove the home on
// the next compose.
func dirMode(m fs.FileMode) fs.FileMode { return fileMode(m) | 0o700 }

// copyFile writes the bytes of f, the file at rel, to the path to in out, a new file with
// one name and the mode [fileMode] gives. A Markdown file's bytes go through the rules'
// edit first, when it is not nil; any other file is copied as it is read. A file larger
// than [maxFileBytes] is refused, and so is a write past the copy's budget.
func (c *copier) copyFile(rel string, f *os.File, info fs.FileInfo, out *os.Root, to string, r *copyRules) error {
	var data []byte
	edited := r.edit != nil && strings.HasSuffix(rel, ".md")
	if edited {
		read, err := c.readAll(rel, f, info)
		if err != nil {
			return err
		}
		data = r.edit(read)
	} else if info.Size() > maxFileBytes {
		return c.tooLarge(rel)
	} else if info.Size() > r.bytes.left {
		return r.bytes.refuse
	}
	mode := fileMode(info.Mode())
	w, err := out.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	counted := budgetWriter{w, r.bytes}
	if edited {
		_, err = counted.Write(data)
	} else {
		var n int64
		n, err = io.Copy(counted, io.LimitReader(f, maxFileBytes+1))
		if err == nil && n > maxFileBytes {
			err = c.tooLarge(rel)
		}
	}
	if err == nil {
		err = w.Chmod(mode)
	}
	if closeErr := w.Close(); err == nil {
		err = closeErr
	}
	return err
}

// notCopyable is the refusal of an entry that is not a file, a directory or a link to one
// of them, such as a named pipe.
func (c *copier) notCopyable(rel string) error {
	return fmt.Errorf("module %s: Remove %s from the module. It is not a file, a directory or a link, so qory cannot copy it into the home.", c.name, exports.HidePath(filepath.ToSlash(rel)))
}

// failed is the error of a read of rel that failed: the refusal of a link out of the
// module when rel leads out of it, else err with the module and the path.
func (c *copier) failed(rel string, err error) error {
	shown := exports.HidePath(filepath.ToSlash(rel))
	if real, rerr := filepath.EvalSymlinks(filepath.Join(c.dir, rel)); rerr == nil && real != c.dir && !below(real, c.dir) {
		return fmt.Errorf("module %s: Remove the link %s or point it inside the module. It links to %s, outside the module, and qory copies only the module's files into the home.", c.name, shown, exports.HidePath(real))
	}
	if c.escapes(err) {
		// The root fails the same way for an absolute link to a missing file inside
		// the module, which it does not follow; ResolveIn tells that one apart.
		if _, rerr := module.ResolveIn(c.root, c.real, rel); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			return fmt.Errorf("module %s: Remove the link %s or point it inside the module. It points out of the module, and qory copies only the module's files into the home.", c.name, shown)
		}
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return fmt.Errorf("module %s: %s: %w", c.name, shown, exports.HideError(err))
}

// escapes reports whether err is the error an [os.Root] returns for a path that leads
// out of the root, such as a link to nothing outside the module. Package os does not
// export that error, so escapes takes it from a lookup of ".." in the root, which fails
// with it before any file is read. A closed root fails that lookup with
// [os.ErrClosed], which is not the error escapes looks for.
func (c *copier) escapes(err error) bool {
	_, probe := c.root.Lstat("..")
	if errors.Is(probe, os.ErrClosed) {
		return false
	}
	sentinel := errors.Unwrap(probe)
	return sentinel != nil && errors.Is(err, sentinel)
}

// CheckPluginSize refuses the plugin directory dir when it holds more than 10,000 files,
// directories and links, counted the way Forager's check of a walled Claude Code
// launch's --plugin-dir counts them: every entry below dir, and below each directory a
// link there leads to, each directory read once, by the file it is, so a directory
// reached again through a link or under another name counts as one entry. A link that
// leads nowhere counts as one entry. The claude runtime calls it for a home that holds a
// copy, with anywhere set for the compose of a walled run or of a run whose harness is
// hidden, which copies every module.
func CheckPluginSize(dir string, anywhere bool) error {
	s := &pluginScan{seen: map[fileKey]bool{}, anywhere: anywhere}
	if _, err := s.enter(dir); err != nil {
		return exports.HideError(err)
	}
	return exports.HideError(s.dir(dir))
}

// pluginScan is one count of [CheckPluginSize]: how many entries it read, and the
// directories it read.
type pluginScan struct {
	n        int
	seen     map[fileKey]bool
	anywhere bool // whether the compose copies every module, see [CheckPluginSize]
}

// enter reports whether the directory at path is one the count has not read yet, and
// records it as read.
func (s *pluginScan) enter(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	key, ok := keyOf(info)
	if !ok {
		return true, nil
	}
	if s.seen[key] {
		return false, nil
	}
	s.seen[key] = true
	return true, nil
}

// dir counts every entry below the directory dir, links not followed as it reads them,
// and what each link to a directory leads to. It reads each directory in batches and
// counts each entry as it is read, so the count stops early in a large directory.
func (s *pluginScan) dir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	entries, err := readCounted(f, nil, func() error {
		if s.n++; s.n > maxEntries {
			if s.anywhere {
				return errors.New("Ship fewer files in the modules. claude/plugin contains more than 10,000 files, directories and links, the most a Claude Code run behind a wall or with a hidden harness takes.")
			}
			return errors.New("Move a module out of the checkout, or ship fewer files. claude/plugin contains more than 10,000 files, directories and links, the most a walled Claude Code run takes.")
		}
		return nil
	})
	f.Close()
	if err != nil {
		return err
	}
	for _, d := range entries {
		path := filepath.Join(dir, d.Name())
		if d.IsDir() {
			fresh, err := s.enter(path)
			if err != nil {
				return err
			}
			if fresh {
				if err := s.dir(path); err != nil {
					return err
				}
			}
			continue
		}
		if d.Type()&fs.ModeSymlink == 0 {
			continue
		}
		target, err := filepath.EvalSymlinks(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		info, err := os.Stat(target)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			continue
		}
		fresh, err := s.enter(target)
		if err != nil {
			return err
		}
		if fresh {
			if err := s.dir(target); err != nil {
				return err
			}
		}
	}
	return nil
}

// stageRoot is a directory of the staging tree that copies are written into, opened as
// an [os.Root] the first time a copy needs it, so no write of a copy follows a link out
// of it, such as a skill the home links to another module. Close it when done.
type stageRoot struct {
	dir  string
	root *os.Root
}

// get is the directory as a root, opened on the first call.
func (s *stageRoot) get() (*os.Root, error) {
	if s.root == nil {
		root, err := os.OpenRoot(s.dir)
		if err != nil {
			return nil, err
		}
		s.root = root
	}
	return s.root, nil
}

// Close closes the root when it was opened.
func (s *stageRoot) Close() {
	if s.root != nil {
		s.root.Close()
	}
}

// Copies reports whether the home of res holds a copy of any module, see
// [compose.Module.Copy].
func Copies(res *compose.Result) bool {
	for _, m := range res.Modules {
		if m.Copy {
			return true
		}
	}
	return false
}
