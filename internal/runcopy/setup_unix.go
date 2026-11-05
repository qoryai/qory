//go:build unix

package runcopy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"github.com/qoryai/qory/internal/checkout"
)

// SetupStates reads what the copy of the repository r, as [Dir.Clone] returned it, has
// at each of paths, the paths [Dir.SetupChanges] lists, for
// [LeftOut.Setup]. Call it after the setup and before the agent's session. A trailing
// / of a path is dropped. It opens each folder on the way from the one above it with
// O_NOFOLLOW, so it follows no link, and hashes a file's content, or a link's target,
// with git hash-object --stdin --no-filters in the bare source of r, which sets the
// object format. No git opens the copy.
func (d Dir) SetupStates(ctx context.Context, r Repository, paths []string) ([]SetupPath, error) {
	rel, err := repositoryRel(r)
	if err != nil {
		return nil, err
	}
	return d.setupStatesAt(ctx, filepath.Join(d.Work(), filepath.FromSlash(rel)), r.index, paths)
}

// SetupChanges lists what the setup changed in the copy of the repository r, as [Dir.Clone]
// returned it, for [Dir.SetupStates]: each regular file and link that differs from the commit
// the copy starts at, each one gone from it, and each new one that the .gitignore files do
// not ignore, sorted. A repository in the copy is one path with a trailing /. .qory, a
// submodule and every .git stay out. Call it after the setup and before the agent's session.
//
// No git opens the copy, since the setup could write it. The tree of the commit comes from
// the bare source of r. A walk opens each folder from the one above it with O_NOFOLLOW, so it
// follows no link, and pipes a tracked file's content to git hash-object in the bare source.
// git check-ignore reads the regular .gitignore files from a mirror in the run's folder, which
// no run binds, with an empty configuration: a program named in the copy has no way to run.
func (d Dir) SetupChanges(ctx context.Context, r Repository) ([]string, error) {
	rel, err := repositoryRel(r)
	if err != nil {
		return nil, err
	}
	top := filepath.Join(d.Work(), filepath.FromSlash(rel))
	paths, err := d.setupChanges(ctx, top, r)
	if err != nil {
		return nil, fmt.Errorf(textSetupStatus, shown(top), err)
	}
	return paths, nil
}

// setupChanges is [Dir.SetupChanges] of the copy of r at top.
func (d Dir) setupChanges(ctx context.Context, top string, r Repository) ([]string, error) {
	source := d.source(r.index)
	base, err := os.MkdirTemp(d.root, "status-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(base)
	private, mirror := filepath.Join(base, "git"), filepath.Join(base, "tree")
	for _, sub := range []string{filepath.Join(private, "objects"), filepath.Join(private, "refs"), mirror} {
		if err := os.MkdirAll(sub, 0o700); err != nil {
			return nil, err
		}
	}
	if err := copyFormat(ctx, source, private, r.Base); err != nil {
		return nil, err
	}
	w := &setupWalk{
		source: source, mirror: mirror, gitDir: private,
		tracked: map[string]treeFile{}, above: map[string]bool{}, gitlink: map[string]bool{}, seen: map[string]bool{},
		hashes: make(chan struct{}, maxHashes),
	}
	if err := w.readTree(ctx, r.Base); err != nil {
		return nil, err
	}
	root, err := unix.Open(top, dirFlags, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(root)
	err = w.folder(ctx, root, "", 0)
	w.waiting.Wait()
	if err == nil {
		err = w.err
	}
	if err != nil {
		return nil, err
	}
	for file := range w.tracked {
		if !w.seen[file] {
			w.changed = append(w.changed, file)
		}
	}
	slices.Sort(w.changed)
	return w.changed, nil
}

// maxHashes is the most files [setupWalk] pipes to git hash-object at one time.
const maxHashes = 8

// maxIgnoreFile is the most bytes [setupWalk] reads of one .gitignore. Patterns past it do not
// count, so a file they ignore is listed, and [Dir.SetupStates] records it as it is.
const maxIgnoreFile = 1 << 25

// treeFile is a file of the commit's tree: its mode and object id.
type treeFile struct {
	mode, id string
}

// setupWalk is the state of one [Dir.SetupChanges].
type setupWalk struct {
	source, mirror, gitDir string
	// tracked are the regular files and links of the commit's tree, by path, and above each
	// folder that has one or a submodule. gitlink are the paths of the submodules.
	tracked map[string]treeFile
	above   map[string]bool
	gitlink map[string]bool
	// seen are the tracked paths that the walk found of the same kind.
	seen    map[string]bool
	hashes  chan struct{}
	waiting sync.WaitGroup
	// mu guards changed and err, which the hashes write too.
	mu      sync.Mutex
	changed []string
	err     error
}

// readTree reads the tree of commit from the bare source, leaving out .qory.
func (w *setupWalk) readTree(ctx context.Context, commit string) error {
	out, err := git(ctx, w.source, "ls-tree", "-r", "-z", "--full-tree", commit)
	if err != nil {
		return err
	}
	for entry := range strings.SplitSeq(out, "\x00") {
		meta, file, ok := strings.Cut(entry, "\t")
		if !ok || folds(strings.SplitN(file, "/", 2)[0], checkout.Dir) {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 {
			return &gitError{subcommand: "ls-tree", line: "printed " + shown(meta) + ", which is no tree entry", exit: -1}
		}
		for up := path.Dir(file); up != "."; up = path.Dir(up) {
			w.above[up] = true
		}
		if fields[0] == "160000" {
			w.gitlink[file] = true
			continue
		}
		w.tracked[file] = treeFile{mode: fields[0], id: fields[2]}
	}
	return nil
}

// note adds file to the changed paths.
func (w *setupWalk) note(file string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.changed = append(w.changed, file)
}

// folder walks the folder dir at rel, "" for the top, and the folders under it.
func (w *setupWalk) folder(ctx context.Context, dir int, rel string, depth int) error {
	names, err := readNames(dir)
	if err != nil {
		return err
	}
	if err := w.mirrorIgnore(dir, rel); err != nil {
		return err
	}
	type entry struct {
		at     string
		name   string
		folder bool
	}
	var untracked []entry
	var inside []entry
	for _, name := range names {
		at := path.Join(rel, name)
		if folds(name, ".git") || rel == "" && folds(name, checkout.Dir) || w.gitlink[at] {
			continue
		}
		var st unix.Stat_t
		if err := unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		kind := st.Mode & unix.S_IFMT
		file, isTracked := w.tracked[at]
		switch {
		case isTracked && kind == unix.S_IFREG && file.mode != "120000":
			w.seen[at] = true
			if err := w.compareFile(ctx, dir, name, at, file, st.Mode&0o100 != 0); err != nil {
				return err
			}
			continue
		case isTracked && kind == unix.S_IFLNK && file.mode == "120000":
			w.seen[at] = true
			if err := w.compareLink(ctx, dir, name, at, file); err != nil {
				return err
			}
			continue
		case isTracked:
			// Another kind of entry at a tracked path is one change. A folder there is walked too.
			w.seen[at] = true
			w.note(at)
			if kind != unix.S_IFDIR {
				continue
			}
		}
		switch kind {
		case unix.S_IFDIR:
			if w.above[at] {
				inside = append(inside, entry{at: at, name: name, folder: true})
			} else {
				untracked = append(untracked, entry{at: at, name: name, folder: true})
			}
		case unix.S_IFREG, unix.S_IFLNK:
			untracked = append(untracked, entry{at: at, name: name})
		}
	}
	asked := make([]string, len(untracked))
	for i, e := range untracked {
		asked[i] = e.at
		if e.folder {
			asked[i] += "/"
		}
	}
	ignored, err := w.ignored(ctx, asked)
	if err != nil {
		return err
	}
	for i, e := range untracked {
		switch {
		case ignored[asked[i]]:
		case !e.folder:
			w.note(e.at)
		default:
			inside = append(inside, e)
		}
	}
	for _, e := range inside {
		sub, err := unix.Openat(dir, e.name, dirFlags, 0)
		if err != nil {
			return err
		}
		err = w.subfolder(ctx, sub, e.at, depth)
		unix.Close(sub)
		if err != nil {
			return err
		}
	}
	return nil
}

// subfolder walks the folder sub at rel: a new folder with a .git is a repository, one path
// that no walk enters, and so is a folder too deep.
func (w *setupWalk) subfolder(ctx context.Context, sub int, rel string, depth int) error {
	if depth >= maxOpenFolders {
		w.note(rel + "/")
		return nil
	}
	if !w.above[rel] {
		names, err := readNames(sub)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(names, func(n string) bool { return folds(n, ".git") }) {
			w.note(rel + "/")
			return nil
		}
	}
	return w.folder(ctx, sub, rel, depth+1)
}

// compareFile adds at to the changed paths when the regular file name in dir has another
// kind, another owner's execute bit or another content than file, by git hash-object in
// the bare source. A hash runs beside the walk, at most [maxHashes] at one time.
func (w *setupWalk) compareFile(ctx context.Context, dir int, name, at string, file treeFile, exec bool) error {
	if exec != (file.mode == "100755") {
		w.note(at)
		return nil
	}
	f, err := openFileAt(dir, name, at)
	if err != nil {
		return err
	}
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		f.Close()
		w.note(at)
		return err
	}
	w.hashes <- struct{}{}
	w.waiting.Add(1)
	go func() {
		defer func() { <-w.hashes; w.waiting.Done(); f.Close() }()
		id, err := blobID(ctx, w.source, f)
		w.mu.Lock()
		defer w.mu.Unlock()
		switch {
		case err != nil:
			if w.err == nil {
				w.err = err
			}
		case id != file.id:
			w.changed = append(w.changed, at)
		}
	}()
	return nil
}

// compareLink adds at to the changed paths when the link name in dir has another target than
// file. It does not follow the link.
func (w *setupWalk) compareLink(ctx context.Context, dir int, name, at string, file treeFile) error {
	buf := make([]byte, unix.PathMax)
	n, err := unix.Readlinkat(dir, name, buf)
	if err != nil {
		return err
	}
	id, err := blobID(ctx, w.source, strings.NewReader(string(buf[:n])))
	if err != nil {
		return err
	}
	if id != file.id {
		w.note(at)
	}
	return nil
}

// mirrorIgnore copies the regular .gitignore of the folder dir at rel to the same path under
// the mirror. git takes no .gitignore that is a link.
func (w *setupWalk) mirrorIgnore(dir int, rel string) error {
	var st unix.Stat_t
	err := unix.Fstatat(dir, ".gitignore", &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) || err == nil && st.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil
	}
	if err != nil {
		return err
	}
	f, err := openFileAt(dir, ".gitignore", path.Join(rel, ".gitignore"))
	if err != nil {
		return err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return err
	}
	folder := filepath.Join(w.mirror, filepath.FromSlash(rel))
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(filepath.Join(folder, ".gitignore"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, io.LimitReader(f, maxIgnoreFile))
	return err
}

// ignored is which of paths the .gitignore files in the mirror ignore, by git check-ignore,
// which exits 1 when it ignores none.
func (w *setupWalk) ignored(ctx context.Context, paths []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(paths) == 0 {
		return out, nil
	}
	env := []string{"GIT_DIR=" + w.gitDir, "GIT_WORK_TREE=" + w.mirror}
	listed, err := gitWith(ctx, w.mirror, env, strings.NewReader(strings.Join(paths, "\x00")+"\x00"), "check-ignore", "--no-index", "-z", "--stdin")
	if err != nil {
		if exitOf(err) == 1 {
			return out, nil
		}
		return nil, err
	}
	for p := range strings.SplitSeq(listed, "\x00") {
		if p != "" {
			out[p] = true
		}
	}
	return out, nil
}

// copyFormat writes the git folder private's HEAD, at base, and a configuration with the
// object format of the bare repository source and nothing else.
func copyFormat(ctx context.Context, source, private, base string) error {
	format, err := git(ctx, source, "rev-parse", "--show-object-format")
	if err != nil {
		return err
	}
	config := ""
	if strings.TrimSpace(format) != "sha1" {
		config = "[core]\n\trepositoryformatversion = 1\n[extensions]\n\tobjectformat = " + strings.TrimSpace(format) + "\n"
	}
	if err := writeNew(filepath.Join(private, "config"), config); err != nil {
		return err
	}
	return writeNew(filepath.Join(private, "HEAD"), base+"\n")
}

// setupStatesAt is [Dir.SetupStates] for the repository number index at top.
func (d Dir) setupStatesAt(ctx context.Context, top string, index int, paths []string) ([]SetupPath, error) {
	root, err := unix.Open(top, dirFlags, 0)
	if err != nil {
		return nil, fileErr(top, err)
	}
	defer unix.Close(root)
	out := make([]SetupPath, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSuffix(p, "/")
		if !cleanRel(p) {
			return nil, fmt.Errorf(textSetupPath, shown(p))
		}
		state, err := d.setupState(ctx, root, index, p)
		if err != nil {
			return nil, fileErr(filepath.Join(top, filepath.FromSlash(p)), err)
		}
		out = append(out, SetupPath{Path: p, State: state})
	}
	return out, nil
}

// setupState is what the folder root has at rel, in the form of [SetupPath.State].
func (d Dir) setupState(ctx context.Context, root, index int, rel string) (string, error) {
	dir, name, err := lookup(root, rel)
	if errors.Is(err, errNoEntry) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	defer unix.Close(dir)
	var st unix.Stat_t
	err = unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		buf := make([]byte, unix.PathMax)
		n, err := unix.Readlinkat(dir, name, buf)
		if err != nil {
			return "", err
		}
		id, err := blobID(ctx, d.source(index), strings.NewReader(string(buf[:n])))
		return "link:" + id, err
	case unix.S_IFREG:
		f, err := openFileAt(dir, name, rel)
		if err != nil {
			return "", err
		}
		defer f.Close()
		if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
			return "other", err
		}
		kind := "file:"
		if st.Mode&0o100 != 0 {
			kind = "exec:"
		}
		id, err := blobID(ctx, d.source(index), f)
		return kind + id, err
	}
	return "other", nil
}

// openRoot opens the folder at path with O_NOFOLLOW, for [lookup].
func openRoot(path string) (int, error) {
	fd, err := unix.Open(path, dirFlags, 0)
	if err != nil {
		return -1, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return fd, nil
}
