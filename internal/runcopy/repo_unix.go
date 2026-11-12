//go:build unix

package runcopy

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// repoRecord is what [Dir.SetupStates] needs to read a repository that the setup made: a
// folder for its temporary files, and whether the copy ignores case, as the git session's git
// folders have it.
type repoRecord struct {
	tmp   string
	icase bool
}

// state is the blob id, in format, of the list that the git session's repo_state writes for the
// repository at the folder dir, at the path at, which the setup made: a line per file or change,
// sorted by bytes without the line breaks.
//
// The files are HEAD, packed-refs and each file below refs, reftable and logs of each git folder:
// .git, and each folder below .git/modules and .git/worktrees that has a regular file HEAD and a
// folder refs or a regular file commondir, such as the git folder of a submodule or of a
// worktree. They have the HEAD, refs and reflogs of the repository and of its submodules and
// worktrees. The other files are each .gitignore below dir but in a folder named .git, which have
// its ignore rules. Each such line is kind:id and the path
// below dir, where kind is file for a regular file, with the blob id of its content, and link for a
// link, with the blob id of its target.
//
// The changes are what git status lists in the repository, against its HEAD, as the git session's
// tree_status runs it: through a new git folder with an empty configuration and the repository's
// objects as alternates, so no program, hook or filter that the setup wrote runs. Each such line is
// status, the path's state as [setupState] reads it with a folder as other, and the path. So a
// change that the setup left, such as a file under a filter, which git compares with no filter,
// counts only when it changes again.
//
// It reads the files with no git and follows no link. It fails for a path with a line break or a
// carriage return, a file past [maxHashBytes], a folder more than [maxOpenFolders] deep, refs in a
// reftable, which it cannot read with no git, and any failed read or git.
func (rec repoRecord) state(ctx context.Context, dir int, format, at string) (string, error) {
	var lines []string
	if err := repoIgnores(ctx, dir, "", format, 0, &lines); err != nil {
		return "", err
	}
	git, err := unix.Openat(dir, ".git", dirFlags, 0)
	if err != nil {
		return "", err
	}
	defer unix.Close(git)
	if err := gitFolderFiles(ctx, git, ".git", format, &lines); err != nil {
		return "", err
	}
	for _, name := range []string{"modules", "worktrees"} {
		if err := gitFolders(ctx, git, name, ".git/"+name, format, 1, &lines); err != nil {
			return "", err
		}
	}
	changes, err := rec.status(ctx, dir, git, format, at)
	if err != nil {
		return "", err
	}
	lines = append(lines, changes...)
	slices.Sort(lines)
	text := ""
	if len(lines) > 0 {
		text = strings.Join(lines, "\n") + "\n"
	}
	return hashBlob(ctx, format, strings.NewReader(text), int64(len(text)))
}

// gitFolderFiles adds to lines the HEAD, packed-refs, refs, reftable and logs of the git folder
// git at rel, as [repoRecord.state] lists them.
func gitFolderFiles(ctx context.Context, git int, rel, format string, lines *[]string) error {
	for _, name := range []string{"HEAD", "packed-refs", "refs", "reftable", "logs"} {
		if err := repoEntry(ctx, git, name, rel+"/"+name, format, 0, true, lines); err != nil {
			return err
		}
	}
	return nil
}

// gitFolders adds to lines the files of each git folder at or below the folder name in dir, at
// rel, as [repoRecord.state] lists them: a folder with a regular file HEAD and a folder refs or a
// regular file commondir. It walks each folder below, and follows no link.
func gitFolders(ctx context.Context, dir int, name, rel, format string, depth int, lines *[]string) error {
	if depth >= maxOpenFolders {
		return errTooDeep
	}
	if strings.ContainsAny(rel, "\n\r") {
		return errRepoPath
	}
	var st unix.Stat_t
	err := unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) || err == nil && st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return nil
	}
	if err != nil {
		return err
	}
	sub, err := unix.Openat(dir, name, dirFlags, 0)
	if err != nil {
		return err
	}
	defer unix.Close(sub)
	if isKind(sub, "HEAD", unix.S_IFREG) && (isKind(sub, "refs", unix.S_IFDIR) || isKind(sub, "commondir", unix.S_IFREG)) {
		if err := gitFolderFiles(ctx, sub, rel, format, lines); err != nil {
			return err
		}
	}
	names, err := readNames(sub)
	if err != nil {
		return err
	}
	for _, n := range names {
		if err := gitFolders(ctx, sub, n, rel+"/"+n, format, depth+1, lines); err != nil {
			return err
		}
	}
	return nil
}

// isKind reports whether the entry name in the folder dir is of kind, a value of unix.S_IFMT, with
// no link followed.
func isKind(dir int, name string, kind uint32) bool {
	var st unix.Stat_t
	return unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW) == nil && uint32(st.Mode)&unix.S_IFMT == kind
}

// repoIgnores adds to lines each .gitignore below the folder dir at rel, as [repoRecord.state]
// lists it.
func repoIgnores(ctx context.Context, dir int, rel, format string, depth int, lines *[]string) error {
	if depth >= maxOpenFolders {
		return errTooDeep
	}
	names, err := readNames(dir)
	if err != nil {
		return err
	}
	for _, name := range names {
		if name == ".git" {
			continue
		}
		at := path.Join(rel, name)
		if name == ".gitignore" {
			if err := repoEntry(ctx, dir, name, at, format, depth, false, lines); err != nil {
				return err
			}
		}
		if !isKind(dir, name, unix.S_IFDIR) {
			continue
		}
		sub, err := unix.Openat(dir, name, dirFlags, 0)
		if err != nil {
			return err
		}
		err = repoIgnores(ctx, sub, at, format, depth+1, lines)
		unix.Close(sub)
		if err != nil {
			return err
		}
	}
	return nil
}

// repoEntry adds to lines the line of the entry name in the folder dir at rel, as
// [repoRecord.state] lists it: a regular file or a link, and nothing for another kind. With walk
// set, a folder there adds each regular file and link below it.
func repoEntry(ctx context.Context, dir int, name, rel, format string, depth int, walk bool, lines *[]string) error {
	if strings.ContainsAny(rel, "\n\r") {
		return errRepoPath
	}
	var st unix.Stat_t
	err := unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		buf := make([]byte, unix.PathMax)
		n, err := unix.Readlinkat(dir, name, buf)
		if err != nil {
			return err
		}
		id, err := hashBlob(ctx, format, bytes.NewReader(buf[:n]), int64(n))
		if err != nil {
			return err
		}
		*lines = append(*lines, "link:"+id+" "+rel)
	case unix.S_IFREG:
		f, err := openFileAt(dir, name, rel)
		if err != nil {
			return err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > maxHashBytes {
			return errRepoPath
		}
		id, err := hashBlob(ctx, format, f, info.Size())
		if err != nil {
			return err
		}
		*lines = append(*lines, "file:"+id+" "+rel)
	case unix.S_IFDIR:
		if !walk {
			return nil
		}
		if depth >= maxOpenFolders {
			return errTooDeep
		}
		sub, err := unix.Openat(dir, name, dirFlags, 0)
		if err != nil {
			return err
		}
		defer unix.Close(sub)
		names, err := readNames(sub)
		if err != nil {
			return err
		}
		for _, n := range names {
			if err := repoEntry(ctx, sub, n, rel+"/"+n, format, depth+1, true, lines); err != nil {
				return err
			}
		}
	}
	return nil
}

// status is the lines of the changes that git status lists in the repository at the folder dir,
// at the path at, whose git folder is git, as [repoRecord.state] lists them. git runs as the git
// session's tree_status runs it, with the settings of its g: a new git folder in rec.tmp with an
// empty configuration, the repository's objects as alternates, and HEAD and an index at the
// repository's HEAD commit, which this reads with no git. A HEAD on a branch with no commit has an
// empty index.
func (rec repoRecord) status(ctx context.Context, dir, git int, format, at string) ([]string, error) {
	if !isKind(git, "objects", unix.S_IFDIR) || isKind(git, "reftable", unix.S_IFDIR) {
		return nil, errRepoPath
	}
	commit, err := headCommit(git, format)
	if err != nil {
		return nil, err
	}
	private, err := os.MkdirTemp(rec.tmp, "repo-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(private)
	for _, sub := range []string{"objects/info", "refs"} {
		if err := os.MkdirAll(filepath.Join(private, filepath.FromSlash(sub)), 0o700); err != nil {
			return nil, err
		}
	}
	config := ""
	if format != "sha1" {
		config = "[core]\n\trepositoryformatversion = 1\n[extensions]\n\tobjectformat = " + format + "\n"
	}
	if err := writeNew(filepath.Join(private, "config"), config); err != nil {
		return nil, err
	}
	head := "ref: refs/heads/none\n"
	if commit != "" {
		head = commit + "\n"
	}
	if err := writeNew(filepath.Join(private, "HEAD"), head); err != nil {
		return nil, err
	}
	if err := writeNew(filepath.Join(private, "objects", "info", "alternates"), filepath.Join(at, ".git", "objects")+"\n"); err != nil {
		return nil, err
	}
	env := []string{
		"GIT_DIR=" + private, "GIT_WORK_TREE=" + at, "GIT_INDEX_FILE=" + filepath.Join(private, "index"),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_NO_REPLACE_OBJECTS=1",
		"GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1", "LC_ALL=C",
	}
	if commit != "" {
		if _, err := gitWith(ctx, at, env, nil, "read-tree", commit); err != nil {
			return nil, err
		}
	}
	args := []string{
		"-c", "core.fsmonitor=false", "-c", "core.useReplaceRefs=false", "-c", "core.commitGraph=false",
		"-c", "core.attributesFile=" + os.DevNull, "-c", "core.excludesFile=" + os.DevNull,
		"-c", "core.untrackedCache=false", "-c", "core.safecrlf=false", "-c", "core.ignorecase=" + strconv.FormatBool(rec.icase),
		"status", "--porcelain", "-z", "--untracked-files=all", "--ignore-submodules=dirty",
	}
	out, err := gitWith(ctx, at, env, nil, args...)
	if err != nil {
		return nil, err
	}
	var lines []string
	for entry := range strings.SplitSeq(out, "\x00") {
		if entry == "" {
			continue
		}
		if len(entry) < 4 {
			return nil, errRepoPath
		}
		p := strings.TrimSuffix(entry[3:], "/")
		if strings.ContainsAny(p, "\n\r") || !cleanRel(p) {
			return nil, errRepoPath
		}
		state, err := setupState(ctx, dir, format, p, nil)
		if err != nil {
			return nil, err
		}
		lines = append(lines, "status "+state+" "+p)
	}
	return lines, nil
}

// headCommit is the commit at HEAD of the git folder git, read with no git: HEAD itself, or the
// branch after its ref:, as a loose ref or a line of packed-refs. It is "" for a branch with no
// commit.
func headCommit(git int, format string) (string, error) {
	head, err := readSmall(git, "HEAD")
	if err != nil {
		return "", err
	}
	head = strings.TrimSpace(head)
	name, isRef := strings.CutPrefix(head, "ref: ")
	if !isRef {
		return checkedID(head, format)
	}
	if !strings.HasPrefix(name, "refs/") || !cleanRel(name) {
		return "", errRepoPath
	}
	dir, file, err := lookup(git, name)
	if err == nil {
		id, err := readSmall(dir, file)
		unix.Close(dir)
		if err == nil {
			return checkedID(strings.TrimSpace(id), format)
		}
		if !errors.Is(err, unix.ENOENT) {
			return "", err
		}
	} else if !errors.Is(err, errNoEntry) {
		return "", err
	}
	packed, err := readSmall(git, "packed-refs")
	if errors.Is(err, unix.ENOENT) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	for line := range strings.SplitSeq(packed, "\n") {
		if id, ref, ok := strings.Cut(strings.TrimSpace(line), " "); ok && ref == name {
			return checkedID(id, format)
		}
	}
	return "", nil
}

// checkedID is id when it is an object id of format.
func checkedID(id, format string) (string, error) {
	if !objectID.MatchString(id) || (len(id) == 64) != (format == "sha256") {
		return "", errRepoPath
	}
	return id, nil
}

// readSmall is what the regular file name in the folder dir has, at most [maxHashBytes] bytes,
// with no link followed. A missing file is unix.ENOENT.
func readSmall(dir int, name string) (string, error) {
	f, err := openFileAt(dir, name, name)
	if err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) && errors.Is(pathErr.Err, unix.ENOENT) {
			return "", unix.ENOENT
		}
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > maxHashBytes {
		return "", errRepoPath
	}
	var b bytes.Buffer
	_, err = b.ReadFrom(f)
	return b.String(), err
}

// errRepoPath and errTooDeep are what [repoRecord.state] cannot list: a path, a file or refs it
// cannot read with no git, and a folder too deep.
var (
	errRepoPath = errors.New("a path, a file or refs that the list cannot have")
	errTooDeep  = errors.New("a folder too deep")
)
