package hookrun

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/proxygit"
	"github.com/qoryai/qory/internal/render"
)

// restoreRounds is the most restores after one hook, and the most removals of rule files in one
// restore. git can ignore the folder of one that a hook added by a rule of another, which the next
// removal then finds.
const restoreRounds = 4

// Snapshot is the checkout at dir as a tree of git, every path that git does not ignore, from
// HEAD and the working folder, and the index that has it, in a folder outside the checkout.
type Snapshot struct {
	dir, folder string
	// head is the checkout's HEAD when the snapshot was taken.
	head string
	// Tree is the snapshot's tree, and entries its entries, as git ls-files -z --stage prints them.
	Tree    string
	entries string
	// gitlinks is the state of the folder of each submodule entry of the snapshot, by its path
	// relative to dir, see [folderState]: qory leaves such a folder out of the add, see [addAll].
	gitlinks map[string]string
	// attributes is the state of each file of [Snapshot.hiddenAttributes].
	attributes map[string]string
	// special is the type of each pipe, socket and device of the checkout, see [noRepository].
	special map[string]fs.FileMode
	// modes is the mode, with the setuid, setgid and sticky bits, of each regular file of the
	// snapshot and of each folder above one.
	modes map[string]fs.FileMode
}

// index is the snapshot's index file, and the copies that a restore and its check use.
func (s *Snapshot) index(name string) string { return filepath.Join(s.folder, name+"-index") }

// Take takes the snapshot of the checkout at dir, with its index files in folder. The snapshot
// also has each .gitignore and .gitattributes that git ignores by a rule of its folder, see
// [showRules], such as the .gitignore of a tool's cache folder: a restore removes the ones that it
// does not have. git runs with a private git directory, see [private]: it adds files to an index,
// which would run a filter that .git/config sets, and a process of the run's setup can write .git
// while the hooks run. The checkout's index, HEAD and refs do not change; git writes the objects
// of changed files into .git/objects, as the push's commit would.
func Take(ctx context.Context, dir, folder string) (*Snapshot, error) {
	head, err := headOf(filepath.Join(dir, ".git"))
	if err != nil {
		return nil, err
	}
	s := &Snapshot{dir: dir, folder: folder, head: head, gitlinks: map[string]string{}, modes: map[string]fs.FileMode{}}
	env, done, err := s.private(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	// shortcut: memory has no limit: git's index and the listing of it in qory have one entry for
	// each path of the commit, and a commit can have 2^20 paths and more. Take fails closed: a git
	// without memory ends, and each hook is red and does not run; a qory without memory ends, and
	// no hook runs and no push follows. On a machine with several runs, the memory use can also end
	// other runs. Replace it when the snapshot ends at a set number of entries of ls-tree.
	//
	// The index has HEAD's entries and no folder's tree: read-tree would keep the folders' tree
	// ids, and write-tree would take them with no check of what the folders have.
	entries, err := git(ctx, dir, env, "ls-tree", "-r", "-z", "--full-tree", "HEAD")
	if err != nil {
		return nil, err
	}
	if entries, err = withoutMissing(dir, entries); err != nil {
		return nil, err
	}
	if env, err = s.fillIndex(ctx, env, "snapshot", entries); err != nil {
		return nil, err
	}
	links, err := gitlinks(ctx, gitCommand, dir, env)
	if err != nil {
		return nil, err
	}
	// Before the add: after it, the index is only read and written as a tree.
	rules, err := ruleFiles(ctx, dir, env)
	if err != nil {
		return nil, err
	}
	if _, err := gitInput(ctx, dir, env, rules, "update-index", "-z", "--add", "--stdin"); err != nil {
		return nil, err
	}
	if s.special, err = noRepository(ctx, dir, env, links); err != nil {
		return nil, err
	}
	if err := addAll(ctx, gitCommand, dir, env); err != nil {
		return nil, err
	}
	tree, err := git(ctx, dir, env, "write-tree")
	if err != nil {
		return nil, err
	}
	s.Tree = strings.TrimSpace(tree)
	if s.entries, err = git(ctx, dir, env, "ls-files", "-z", "--stage"); err != nil {
		return nil, err
	}
	if links, err = gitlinks(ctx, gitCommand, dir, env); err != nil {
		return nil, err
	}
	for _, name := range links {
		if s.gitlinks[name], err = folderState(ctx, dir, name); err != nil {
			return nil, err
		}
	}
	for entry := range strings.SplitSeq(s.entries, "\x00") {
		about, name, _ := strings.Cut(entry, "\t")
		for at := name; strings.HasPrefix(about, "100") && at != "."; at = path.Dir(at) {
			if info, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(at))); err == nil {
				s.modes[at] = info.Mode()
			}
		}
	}
	s.attributes, err = s.hiddenAttributes(ctx)
	return s, err
}

// rawAttributes is info/attributes of a snapshot's private git directory: git applies it over each
// .gitattributes of the checkout, so it adds and writes the bytes of each file as they are, and
// a .gitattributes that a hook adds or changes has no effect on a restore or its check.
const rawAttributes = "* -text -ident -filter !eol !working-tree-encoding\n"

// private is [private] for a git of the snapshot, with [rawAttributes].
func (s *Snapshot) private(ctx context.Context) ([]string, func(), error) {
	folder, env, done, err := private(ctx, s.dir, s.folder, s.head)
	if err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(filepath.Join(folder, "info", "attributes"), []byte(rawAttributes), 0o600); err != nil {
		done()
		return nil, nil, err
	}
	return env, done, nil
}

// fillIndex makes the index file name with entries, the output of git ls-tree -z or git ls-files
// -z --stage, and nothing of a file that was there, and returns env with it. The index has no
// size and no time of a file: with those, git takes a file that a hook changed in the same
// second, and kept the size of, for unchanged.
func (s *Snapshot) fillIndex(ctx context.Context, env []string, name, entries string) ([]string, error) {
	if err := os.Remove(s.index(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	env = append(slices.Clip(env), "GIT_INDEX_FILE="+s.index(name))
	_, err := gitInput(ctx, s.dir, env, entries, "update-index", "-z", "--index-info")
	return env, err
}

// showRules are the options of git ls-files and git clean, -x and -e, with a pattern each that
// shows a .gitignore or a .gitattributes, in any case, that git ignores by a rule of its folder,
// such as the line *. A rule file in a folder that git ignores is not shown.
var showRules = []string{"!.[gG][iI][tT][iI][gG][nN][oO][rR][eE]", "!.[gG][iI][tT][aA][tT][tT][rR][iI][bB][uU][tT][eE][sS]"}

// rulePaths are the paths of [showRules], in every folder.
var rulePaths = []string{":(top,glob,icase)**/.gitignore", ":(top,glob,icase)**/.gitattributes"}

// ruleFiles is the path of each .gitignore and .gitattributes in the checkout at dir, see
// [showRules], that the index of env does not have, each ended by a NUL.
func ruleFiles(ctx context.Context, dir string, env []string) (string, error) {
	args := []string{"ls-files", "-z", "--others", "--exclude-standard", "-x", showRules[0], "-x", showRules[1], "--"}
	return git(ctx, dir, env, append(args, rulePaths...)...)
}

// withoutMissing is entries, the output of git ls-tree -z, with no submodule entry whose path in
// the checkout at dir is no folder: [addAll] leaves the paths of submodule entries as they are,
// so the index would keep an entry that the working folder does not have.
func withoutMissing(dir, entries string) (string, error) {
	var kept strings.Builder
	for entry := range strings.SplitAfterSeq(entries, "\x00") {
		if name, ok := gitlinkOf(strings.TrimSuffix(entry, "\x00")); ok {
			isFolder, err := folderAt(dir, name)
			if err != nil {
				return "", err
			}
			if !isFolder {
				continue
			}
		}
		kept.WriteString(entry)
	}
	return kept.String(), nil
}

// folderAt reports whether name is a folder of the checkout at dir with no link on the way to it.
func folderAt(dir, name string) (bool, error) {
	for i := range len(name) + 1 {
		if i < len(name) && name[i] != '/' {
			continue
		}
		info, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(name[:i])))
		switch {
		case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
			return false, nil
		case err != nil || !info.IsDir():
			return false, err
		}
	}
	return true, nil
}

// gitlinkOf is the path of entry, one entry of git ls-files -z --stage or git ls-tree -z, when it
// is a submodule entry, mode 160000. A path can have a tab: the first tab ends the mode and id.
func gitlinkOf(entry string) (string, bool) {
	about, name, ok := strings.Cut(entry, "\t")
	return name, ok && strings.HasPrefix(about, "160000 ")
}

// gitlinks is the path of each submodule entry in the index of env, read by a git that command
// makes in dir.
func gitlinks(ctx context.Context, command commandMaker, dir string, env []string) ([]string, error) {
	staged, err := runGit(ctx, command, dir, env, "ls-files", "-z", "--stage")
	if err != nil {
		return nil, err
	}
	var names []string
	for entry := range strings.SplitSeq(staged, "\x00") {
		if name, ok := gitlinkOf(entry); ok {
			names = append(names, name)
		}
	}
	return names, nil
}

// textRepository is the error of a checkout with a repository in a folder, see [noRepository] and
// [addAll].
const textRepository = "the checkout has a repository in %s"

// addAll adds the whole checkout at dir to the index of env with [hostgit.AddAll], with gits that
// command makes, for at most [gitLimit]. The add leaves out the path of each submodule entry that
// the index has, also one whose folder has no repository. An entry that the add makes is an error
// with its path, so an index after a nil error has no new submodule entry; after an error, the
// index must not be used again. The error of a git that fails is as [git] describes it.
func addAll(ctx context.Context, command commandMaker, dir string, env []string) error {
	ctx, cancel := context.WithTimeout(ctx, gitLimit)
	defer cancel()
	made, err := hostgit.AddAll(ctx, hostgit.Maker(command), dir, env, nil)
	if addErr := (*hostgit.AddError)(nil); errors.As(err, &addErr) {
		return gitError(ctx, addErr.Subcommand, dir, addErr.Stderr, addErr.Err)
	}
	if err != nil {
		return err
	}
	if len(made) > 0 {
		return fmt.Errorf(textRepository, preparation.ShownPath(made[0]))
	}
	return nil
}

// noRepository returns an error for a folder below the top of the checkout at dir that has an
// entry .git: git lists no file of that name below the top, so a restore would leave one that a
// hook made, and git add makes a submodule entry of a repository. The name counts in any case,
// and as the file system finds it. It returns the type of each pipe, socket and device that it
// finds: git lists none, so a restore would leave one that a hook made. The folders of submodule
// entries, links, and the folders that git ignores with the index of env are left out. The walk
// follows no link.
//
// shortcut: the hooks are red in a checkout with a repository that is neither a submodule entry
// of HEAD nor in an ignored folder. Replace it when a ruling allows such folders.
func noRepository(ctx context.Context, dir string, env, links []string) (map[string]fs.FileMode, error) {
	listed, err := git(ctx, dir, env, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	if err != nil {
		return nil, err
	}
	// git lists a folder that it ignores with nothing below it. It also lists a folder that it
	// reads and where it ignores every file, followed by those files.
	skip := map[string]bool{}
	entries := strings.Split(listed, "\x00")
	for _, entry := range entries {
		if name, isDir := strings.CutSuffix(entry, "/"); isDir {
			skip[name] = true
		}
	}
	for _, entry := range entries {
		for at := path.Dir(strings.TrimSuffix(entry, "/")); at != "."; at = path.Dir(at) {
			delete(skip, at)
		}
	}
	for _, name := range links {
		skip[name] = true
	}
	special := map[string]fs.FileMode{}
	err = filepath.WalkDir(dir, func(at string, d fs.DirEntry, err error) error {
		if err == nil {
			err = ctx.Err()
		}
		if at != dir && errors.Is(err, fs.ErrNotExist) {
			return nil // the folder went away during the walk
		}
		if err != nil || at == dir {
			return err
		}
		rel, err := filepath.Rel(dir, at)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		switch {
		case d.IsDir() && (name == ".git" || skip[name]):
			return fs.SkipDir
		case path.Dir(name) != "." && strings.EqualFold(d.Name(), ".git"):
			return fmt.Errorf(textRepository, preparation.ShownPath(path.Dir(name)))
		case d.Type()&(fs.ModeNamedPipe|fs.ModeSocket|fs.ModeDevice) != 0:
			special[name] = d.Type()
			return nil
		case !d.IsDir():
			return nil
		}
		switch _, err := os.Lstat(filepath.Join(at, ".git")); {
		case err == nil:
			return fmt.Errorf(textRepository, preparation.ShownPath(name))
		case !errors.Is(err, fs.ErrNotExist):
			return err
		}
		return nil
	})
	if err == nil {
		afterWalk(dir)
	}
	return special, fromCheckout(dir, err)
}

// fromCheckout is err with the path of an [fs.PathError] below dir as a path from dir, so an
// error of a file of the checkout shows no folder of this machine.
func fromCheckout(dir string, err error) error {
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) {
		return err
	}
	rel, relErr := filepath.Rel(dir, pathErr.Path)
	if relErr != nil || !filepath.IsLocal(rel) {
		return err
	}
	return &fs.PathError{Op: pathErr.Op, Path: preparation.ShownPath(filepath.ToSlash(rel)), Err: pathErr.Err}
}

// The errors of a folder of a submodule entry that a hook changed, of a file there that changed
// during the read of it, of a file of [Snapshot.hiddenAttributes] that a hook changed, and of a
// pipe, socket or device that a hook made.
const (
	textSubmoduleChanged = "a hook changed the folder %s of a submodule entry"
	textReadChanged      = "%s changed while qory read it"
	textAttributes       = "a hook changed %s in an ignored folder with tracked files"
	textSpecial          = "a hook made the pipe, socket or device %s"
)

// folderState is a sum of what the folder name of the checkout at dir has: each entry below it,
// walked with no link followed, with its path and mode, a regular file's size and bytes and a
// link's target. A folder that changed in any of these has another state; the mode of name itself
// does not count, since a restore makes the folder again without it. The end of ctx ends the walk
// and a file's read.
//
// shortcut: a file that the submodule's rules ignore, such as __pycache__, counts as any other: a
// hook that writes one there fails the restore. qory reads no ignore rules of a nested
// repository. Replace it when a ruling sets which of its rules count.
func folderState(ctx context.Context, dir, name string) (string, error) {
	sum := sha256.New()
	path := filepath.Join(dir, filepath.FromSlash(name))
	err := filepath.WalkDir(path, func(at string, d fs.DirEntry, err error) error {
		if err == nil {
			err = ctx.Err()
		}
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(path, at)
		mode := info.Mode()
		if at == path {
			mode = mode.Type()
		}
		fmt.Fprintf(sum, "%q %v\x00", rel, mode)
		switch {
		case mode.IsRegular():
			return addFile(ctx, sum, dir, at, info)
		case mode&fs.ModeSymlink != 0:
			target, err := os.Readlink(at)
			fmt.Fprintf(sum, "%d %q\x00", info.Size(), target)
			return err
		}
		return nil
	})
	return hex.EncodeToString(sum.Sum(nil)), fromCheckout(dir, err)
}

// addFile adds the size and the bytes of the regular file at path to sum, for [folderState]: the
// file that the walk found, info, with the size that it had there. An error of a file that
// changed has its path from dir, the checkout.
func addFile(ctx context.Context, sum io.Writer, dir, path string, info fs.FileInfo) error {
	rel, _ := filepath.Rel(dir, path)
	f, err := os.OpenFile(path, os.O_RDONLY|render.OpenFlags, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	// SameFile is false for a file of another type too; IsRegular stays as a guard.
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) || opened.Size() != info.Size() {
		return fmt.Errorf(textReadChanged, preparation.ShownPath(rel))
	}
	fmt.Fprintf(sum, "%d\x00", info.Size())
	n, err := io.Copy(sum, io.LimitReader(ctxReader{ctx, f}, info.Size()))
	if err == nil && n != info.Size() {
		err = fmt.Errorf(textReadChanged, preparation.ShownPath(rel))
	}
	return err
}

// ctxReader is r, whose reads end at the end of ctx. runcopy has the same type, which its tests
// use, and no package that both import fits it.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (r ctxReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(b)
}

// hiddenAttributes is the state, see [folderState], of each .gitattributes that the snapshot does
// not have, in a folder above a path that it has: git ignores the folder, so a restore leaves the
// file, and a later git add applies it to the tracked files there.
func (s *Snapshot) hiddenAttributes(ctx context.Context) (map[string]string, error) {
	had, folders := map[string]bool{}, map[string]bool{}
	for entry := range strings.SplitSeq(s.entries, "\x00") {
		_, name, _ := strings.Cut(entry, "\t")
		had[name] = true
		for at := path.Dir(name); !folders[at]; at = path.Dir(at) {
			folders[at] = true
		}
	}
	states := map[string]string{}
	for folder := range folders {
		name := path.Join(folder, ".gitattributes")
		if had[name] {
			continue
		}
		state, err := folderState(ctx, s.dir, name)
		if err == nil {
			states[name] = state
		} else if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			return nil, err
		}
	}
	return states, nil
}

// restoreModes sets the modes of s.modes, for a path that has the type it had, a folder before the
// paths below it. The root follows no link out of the checkout. With writable, it sets the folders
// alone, each with the owner's read, write and search bits added, so that git can write there,
// and a permission error of a chmod does not fail it.
func (s *Snapshot) restoreModes(writable bool) error {
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, name := range slices.Sorted(maps.Keys(s.modes)) {
		mode := s.modes[name]
		if writable {
			if !mode.IsDir() {
				continue
			}
			mode |= 0o700
		}
		if info, e := root.Lstat(name); e == nil && info.Mode().Type() == mode.Type() && info.Mode() != mode {
			e = chmodAt(root, name, mode)
			// The pass only lets git write. If git must write in a folder that this user cannot
			// change, git's error or the final compare fails the restore.
			if writable && errors.Is(e, fs.ErrPermission) {
				e = nil
			}
			err = errors.Join(err, e)
		}
	}
	return err
}

// chmodAt sets the mode of name below root; a test sets a function that fails.
var chmodAt = (*os.Root).Chmod

// Restore makes the checkout what the snapshot has: it writes each file of the snapshot that
// differs or is missing, and removes each file and folder that git does not ignore and the
// snapshot does not have, with git clean -fd. A file under a path that the snapshot's rules
// ignore stays as it is, whatever rules the hook added or changed. It compares the bytes of each
// file, also of one whose size and times are the snapshot's, with no attribute applied. It
// then checks that the checkout's tree is the snapshot's, and restores again up to
// [restoreRounds] times; an error is a checkout that still differs. Each round checks .git
// again and runs git with a new private git directory, see [private].
//
// shortcut: a hook's change to a file under a path that git ignores, such as node_modules or
// .env, stays after the restore, and the push step leaves it out. Replace it when a ruling sets
// how a restore covers ignored paths.
//
// shortcut: git clean -fd leaves a repository that a hook made inside the checkout, so the check
// finds it, see [noRepository], and the restore fails, which ends the run with no push. Replace
// it when a ruling allows git clean -ffd.
func (s *Snapshot) Restore(ctx context.Context) error {
	for range restoreRounds {
		same, err := s.restoreOnce(ctx)
		if err != nil || same {
			return err
		}
	}
	return fmt.Errorf("the checkout still differs from the snapshot %s after %d restores", shortID(s.Tree), restoreRounds)
}

// restoreOnce is one round of [Snapshot.Restore]: it reports whether the checkout's tree is the
// snapshot's after the round.
//
// Each round reads every file twice. The refresh keeps git checkout-index from writing a file
// that has the snapshot's bytes. The check's index has no size and no time, so its add reads each
// file again. Before git writes, each folder gets its mode with the owner's bits added, so a
// read-only folder does not stop git. After, each path gets its exact mode, since git
// checkout-index sets the mode of each file that it writes.
func (s *Snapshot) restoreOnce(ctx context.Context) (bool, error) {
	env, done, err := s.private(ctx)
	if err != nil {
		return false, err
	}
	defer done()
	// A read-only folder would keep git from writing and removing files there.
	if err := s.restoreModes(true); err != nil {
		return false, err
	}
	withIndex, err := s.fillIndex(ctx, env, "restore", s.entries)
	if err != nil {
		return false, err
	}
	// The refresh writes the size and time of each file with the snapshot's bytes into its entry,
	// so git checkout-index writes the other files alone.
	if _, err := git(ctx, s.dir, withIndex, "update-index", "-q", "--refresh"); err != nil {
		return false, err
	}
	if _, err := git(ctx, s.dir, withIndex, "checkout-index", "--all", "--force", "--quiet"); err != nil {
		return false, err
	}
	if err := s.cleanRules(ctx, withIndex); err != nil {
		return false, err
	}
	if _, err := git(ctx, s.dir, withIndex, "clean", "-fd", "--quiet"); err != nil {
		return false, err
	}
	if err := s.restoreModes(false); err != nil {
		return false, err
	}
	return s.same(ctx, env)
}

// cleanRules removes each .gitignore and .gitattributes that the snapshot does not have, so that
// the next git clean removes and keeps files by the snapshot's rules alone. git can
// ignore the folder of one by a rule of another, so it removes up to [restoreRounds] times.
func (s *Snapshot) cleanRules(ctx context.Context, env []string) error {
	for range restoreRounds {
		rules, err := ruleFiles(ctx, s.dir, env)
		if err != nil || rules == "" {
			return err
		}
		args := []string{"clean", "-fd", "--quiet", "-e", showRules[0], "-e", showRules[1], "--"}
		if _, err := git(ctx, s.dir, env, append(args, rulePaths...)...); err != nil {
			return err
		}
	}
	return nil
}

// same reports whether the checkout's tree is the snapshot's, with git's variables env. A folder
// of a submodule entry that has another state than in the snapshot is an error: a restore puts
// back an empty folder that a hook replaced, and undoes no other change there.
func (s *Snapshot) same(ctx context.Context, env []string) (bool, error) {
	links := slices.Sorted(maps.Keys(s.gitlinks))
	for _, name := range links {
		now, err := folderState(ctx, s.dir, name)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// The next restore makes the folder again.
			return false, nil
		case err != nil:
			return false, err
		case now != s.gitlinks[name]:
			return false, fmt.Errorf(textSubmoduleChanged, preparation.ShownPath(name))
		}
	}
	attributes, err := s.hiddenAttributes(ctx)
	if err != nil {
		return false, err
	}
	names := maps.Clone(s.attributes)
	maps.Copy(names, attributes)
	for _, name := range slices.Sorted(maps.Keys(names)) {
		if attributes[name] != s.attributes[name] {
			return false, fmt.Errorf(textAttributes, preparation.ShownPath(name))
		}
	}
	env, err = s.fillIndex(ctx, env, "check", s.entries)
	if err != nil {
		return false, err
	}
	special, err := noRepository(ctx, s.dir, env, links)
	if err != nil {
		return false, err
	}
	for _, name := range slices.Sorted(maps.Keys(special)) {
		if s.special[name] != special[name] {
			return false, fmt.Errorf(textSpecial, preparation.ShownPath(name))
		}
	}
	if err := addAll(ctx, gitCommand, s.dir, env); err != nil {
		return false, err
	}
	tree, err := git(ctx, s.dir, env, "write-tree")
	if err != nil || strings.TrimSpace(tree) != s.Tree {
		return false, err
	}
	// The add leaves out a rule file that is left when a rule of it matches its name.
	if rules, err := ruleFiles(ctx, s.dir, env); err != nil || rules != "" {
		return false, err
	}
	return true, nil
}

// gitLimit is the longest one git of a snapshot or a restore may run. [addAll] runs several
// gits, and the limit is for all of them together.
const gitLimit = 10 * time.Minute

// git runs git in dir with args and env added, as [gitCommand] makes it, and returns what it
// printed on stdout. An error has git's first error line, see [isErrorLine], else git's last
// line on stderr, else git's exit status, as [integration.Printable] shows it. After the end of
// ctx or of [gitLimit], an error has the error of the run, such as signal: killed.
func git(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	return runGit(ctx, gitCommand, dir, env, args...)
}

// gitInput is [git] with input on git's stdin.
func gitInput(ctx context.Context, dir string, env []string, input string, args ...string) (string, error) {
	return runGit(ctx, func(ctx context.Context, dir string, env []string, args ...string) (*exec.Cmd, error) {
		cmd, err := gitCommand(ctx, dir, env, args...)
		if err == nil {
			cmd.Stdin = strings.NewReader(input)
		}
		return cmd, err
	}, dir, env, args...)
}

// commandMaker makes the git command that [runGit] runs, as [gitCommand] does.
type commandMaker func(ctx context.Context, dir string, env []string, args ...string) (*exec.Cmd, error)

// isErrorLine reports whether line of git's stderr is an error: the lines before it can be
// warnings with text of the checkout's files.
func isErrorLine(line string) bool {
	return strings.HasPrefix(line, "error: ") || strings.HasPrefix(line, "fatal: ")
}

// runGit runs the git that command makes, as [git] describes.
func runGit(ctx context.Context, command commandMaker, dir string, env []string,
	args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitLimit)
	defer cancel()
	cmd, err := command(ctx, dir, env, args...)
	if err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = time.Second
	hostgit.Group(cmd)
	err = cmd.Run()
	hostgit.StopGroup(cmd)
	if err != nil {
		return "", gitError(ctx, args[0], dir, stderr.String(), err)
	}
	return stdout.String(), nil
}

// gitError is the error of the git subcommand in dir that failed with err and printed stderr.
// It is as [git] describes it, with ctx the context of that git.
func gitError(ctx context.Context, subcommand, dir, stderr string, err error) error {
	line := err.Error()
	// A git that the end of ctx stopped can have printed a warning last, not the cause.
	if ctx.Err() == nil {
		lines := strings.Split(strings.TrimSpace(stderr), "\n")
		if at := slices.IndexFunc(lines, isErrorLine); at >= 0 {
			line = lines[at]
		} else if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
			line = last
		}
	}
	return fmt.Errorf("git %s in %s failed: %s", subcommand, dir, integration.Printable(line))
}

// pinnedCore are the core keys of a file system with case, modes and symlinks, over the copy of
// .git/config, which may set them: with others, git add records no change of a file's mode, a
// link that a file replaced, or a name that differs in case alone. The excludes file is the one of
// the push step, which has none.
var pinnedCore = []string{
	"-c", "core.filemode=true", "-c", "core.symlinks=true", "-c", "core.ignorecase=false",
	"-c", "core.precomposeunicode=false", "-c", "core.excludesFile=/dev/null",
}

// gitCommand is git with args in dir, on this machine: [hostgit.Command] with the options
// [proxygit.Hardened] and [pinnedCore], the environment [proxygit.Env] makes, which reads no
// global or system configuration, git's search for a repository stopped above dir, and env
// added. git reaches no remote, and its lines are in English, see [runGit].
func gitCommand(ctx context.Context, dir string, env []string, args ...string) (*exec.Cmd, error) {
	ctx = hostgit.WithBase(ctx, proxygit.Env(hostgit.Base(ctx), nil))
	cmd, err := hostgit.Command(ctx, dir, slices.Concat(proxygit.Hardened, pinnedCore, args)...)
	if err != nil {
		return nil, err
	}
	cmd.Env = append(append(cmd.Env, "GIT_CEILING_DIRECTORIES="+filepath.Dir(dir), "LC_ALL=C",
		"LANGUAGE="), env...)
	return cmd, nil
}

// afterWalk runs once [noRepository] passed, before git add runs; a test writes the checkout
// there.
var afterWalk = func(dir string) {}

// afterCheck runs once [private] made a private git directory of dir, before git runs; a test
// writes .git there.
var afterCheck = func(dir string) {}

// private makes a private git directory of the checkout at dir with HEAD at head, in a new
// folder below parent, and returns that folder, git's variables for it and a function that
// removes it. It first checks .git as the push step does, [preparation.SafeAfterRun], then copies
// and checks the configuration, [preparation.PrivateGitDir]. git with these variables reads only
// those copies, which have no filter, and the private directory has no attributes, so no process
// that writes .git after the check makes git on this machine run a program. git reads the
// objects from .git/objects and the files from dir.
func private(ctx context.Context, dir, parent, head string) (string, []string, func(), error) {
	if err := preparation.SafeAfterRun(ctx, dir); err != nil {
		return "", nil, nil, err
	}
	folder, err := os.MkdirTemp(parent, "git-")
	if err != nil {
		return "", nil, nil, err
	}
	done := func() { _ = os.RemoveAll(folder) }
	if err := preparation.PrivateGitDir(ctx, dir, folder, head); err != nil {
		done()
		return "", nil, nil, err
	}
	afterCheck(dir)
	return folder, []string{
		"GIT_DIR=" + folder, "GIT_WORK_TREE=" + dir,
		"GIT_OBJECT_DIRECTORY=" + filepath.Join(dir, ".git", "objects"),
	}, done, nil
}

// Head is the commit id that HEAD of the checkout at dir points at, read from the files of
// its .git with no git, as [headOf] reads it, so no configuration of the checkout takes effect.
func Head(dir string) (string, error) { return headOf(filepath.Join(dir, ".git")) }

// sha1ID matches an object id of sha1.
var sha1ID = regexp.MustCompile(`^[0-9a-f]{40}$`)

// maxRefFile is the most bytes of a ref file, and of packed-refs, that [headOf] reads.
const maxRefFile = 64 << 20

// headOf is the commit id that HEAD of the git directory gitDir points at, read from its files
// with no git, so that no configuration in gitDir takes effect: HEAD, then the refs it names, up
// to 5 in a chain, from a loose ref file, or from packed-refs for a ref with no loose file or
// with a folder in its place, as git reads it. A link, a name outside refs/ or a ref of another
// format is an error. These are errors too, since git can read another commit from them: refs in
// other files, see [filesRefs], a HEAD or a loose ref of another form than git writes, see
// [refLine], and such a packed-refs, see [packedRef].
func headOf(gitDir string) (string, error) {
	if err := filesRefs(gitDir); err != nil {
		return "", err
	}
	name := "HEAD"
	for range 5 {
		data, err := readRef(filepath.Join(gitDir, filepath.FromSlash(name)))
		if name != "HEAD" && (errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.EISDIR)) {
			return packedRef(gitDir, name)
		}
		if err != nil {
			return "", err
		}
		if !refLine(string(data)) {
			return "", fmt.Errorf("%s in .git is not one line of a ref or a commit id", name)
		}
		// TrimSpace would remove characters that git reads as part of a ref name, such as U+00A0.
		text := strings.TrimSuffix(string(data), "\n")
		ref, isRef := strings.CutPrefix(text, "ref: ")
		if !isRef {
			return text, nil
		}
		if !strings.HasPrefix(ref, "refs/") {
			return "", fmt.Errorf("%s in .git points at %q, which is not a ref", name, ref)
		}
		name = ref
	}
	return "", errors.New("HEAD in .git points at a chain of more than 5 refs")
}

// The keys of a configuration that [filesRefs] looks for, in any case and section. git reads a key
// at the start of a line or after the ] of a section's header, and spaces, tabs and carriage
// returns before it. A key has letters, digits and -.
var (
	otherRefs  = regexp.MustCompile(`(?im)(?:^|\])[ \t\r]*(?:refstorage|(?:compat)?objectformat)(?:[^a-z0-9-]|$)`)
	formatKey  = regexp.MustCompile(`(?im)(?:^|\])[ \t\r]*repositoryformatversion\b`)
	knownValue = regexp.MustCompile(`^[ \t]*=[ \t]*[01][ \t]*(?:\n|$)`)
)

// filesRefs returns an error when git can read the refs of gitDir from other files than HEAD,
// refs/ and packed-refs, or with ids of another length than sha1's. These are: a commondir file,
// which git follows to another git directory; a reftable folder; refs or objects that is not a
// folder that qory can search, since git then takes gitDir for no repository and can read one at
// the top of the checkout; and these keys in the config, see [otherRefs]: refStorage, which can
// select reftable, objectFormat, compatObjectFormat, and repositoryFormatVersion with another
// value than 0 or 1, which git does not read. A config that git init writes for a sha1 repository
// has none. The text of a value or comment is an error too where it looks like such a key: after
// a ], or on a line that continues a value.
//
// shortcut: a SHA-256 repository and one with reftable fail the read, so the full suite of a
// session runs there also when HEAD did not move. Replace it when qory parses .git/config as git
// does.
func filesRefs(gitDir string) error {
	for _, name := range []string{"commondir", "reftable"} {
		_, err := os.Lstat(filepath.Join(gitDir, name))
		if err == nil {
			return fmt.Errorf("the .git has %s, so git reads refs from other files", name)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	for _, name := range []string{"refs", "objects"} {
		folder := filepath.Join(gitDir, name)
		info, err := os.Lstat(folder)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%s in .git is not a folder", name)
		}
		// The path with /. needs the permission to search the folder, as git does.
		if _, err := os.Lstat(folder + string(filepath.Separator) + "."); err != nil {
			return err
		}
	}
	data, err := readRef(filepath.Join(gitDir, "config"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if otherRefs.Match(data) {
		return errors.New("the .git/config has refStorage or objectFormat, so git can read other refs or ids")
	}
	for _, key := range formatKey.FindAllIndex(data, -1) {
		if !knownValue.Match(data[key[1]:]) {
			return errors.New("the .git/config has a repositoryFormatVersion that is not 0 or 1")
		}
	}
	return nil
}

// refLine reports whether data is the form of HEAD and of a loose ref that git writes: one line,
// ended by a line feed, of "ref: " and a ref name that git accepts, see [refName], or of an
// object id of sha1. Neither has a carriage return or a line feed.
func refLine(data string) bool {
	line, ok := strings.CutSuffix(data, "\n")
	if !ok {
		return false
	}
	if ref, isRef := strings.CutPrefix(line, "ref: "); isRef {
		return refName(ref)
	}
	return sha1ID.MatchString(line)
}

// refName reports whether name is a ref name that git accepts, as git check-ref-format checks
// it: parts between slashes that are not empty and do not start with a dot or end with .lock,
// no end with a slash or a dot, and no .., @{, control character, space, ~, ^, :, ?, *, [ or
// \.
func refName(name string) bool {
	if name == "" || strings.HasSuffix(name, ".") || strings.Contains(name, "..") ||
		strings.Contains(name, "@{") ||
		strings.ContainsFunc(name, func(r rune) bool {
			return r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r)
		}) {
		return false
	}
	for part := range strings.SplitSeq(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

// readRef is the file at path, a regular file of at most [maxRefFile] bytes. A folder is the
// error [syscall.EISDIR]. The open follows no link at the last part of path; a link at a folder above it, such as refs/heads, is
// followed.
func readRef(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|render.OpenFlags, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, &fs.PathError{Op: "read", Path: path, Err: syscall.EISDIR}
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s in .git is not a file", filepath.Base(path))
	}
	data, err := io.ReadAll(io.LimitReader(f, maxRefFile+1))
	if err == nil && len(data) > maxRefFile {
		err = fmt.Errorf("%s in .git has more than %d bytes", filepath.Base(path), maxRefFile)
	}
	return data, err
}

// packedRef is the id of the ref name in packed-refs of gitDir, when packed-refs has the form
// that git writes, see [parsePackedRefs].
func packedRef(gitDir, name string) (string, error) {
	data, err := readRef(filepath.Join(gitDir, "packed-refs"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	return parsePackedRefs(string(data), name)
}

// parsePackedRefs is the id of the ref name in data, a packed-refs file, when data has the form
// that git writes: lines ended by a line feed, with no carriage return; an optional first line
// "# pack-refs with:"; then lines of an object id of sha1 and a ref name, each optionally followed
// by one line of ^ and an id. Each ref name comes after the one before it in byte order, since git
// searches by halves and can find another ref or none in a file with another order. Any other
// line or order is an error.
func parsePackedRefs(data, name string) (string, error) {
	bad := errors.New("packed-refs in .git has a line of another form than git writes")
	if strings.Contains(data, "\r") || (data != "" && !strings.HasSuffix(data, "\n")) {
		return "", bad
	}
	if data == "" {
		return "", fmt.Errorf("%s in .git has no commit", name)
	}
	id, last, afterRef := "", "", false
	for i, line := range strings.Split(strings.TrimSuffix(data, "\n"), "\n") {
		peeled, isPeeled := strings.CutPrefix(line, "^")
		switch {
		case i == 0 && strings.HasPrefix(line, "# pack-refs with:"):
			continue
		case isPeeled:
			if !afterRef || !sha1ID.MatchString(peeled) {
				return "", bad
			}
			afterRef = false
			continue
		}
		// A line with no space has no ref name, which refName does not accept.
		hex, ref, _ := strings.Cut(line, " ")
		if !sha1ID.MatchString(hex) || !refName(ref) {
			return "", bad
		}
		if ref <= last {
			return "", fmt.Errorf("packed-refs in .git has %s not in order", ref)
		}
		last, afterRef = ref, true
		if ref == name {
			id = hex
		}
	}
	if id == "" {
		return "", fmt.Errorf("%s in .git has no commit", name)
	}
	return id, nil
}
