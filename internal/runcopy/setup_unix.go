//go:build unix

package runcopy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/qoryai/qory/internal/checkout"
)

// SetupStates reads what the copy of the repository r, as [Dir.Clone] returned it, has
// at each of paths, the paths the setup changed as RecordSetup lists them, for
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
// returned it, for [Dir.SetupStates]: each path git status lists against the commit the
// copy starts at, by the rules of git status for untracked and ignored files. Call it after
// the setup and before the agent's session.
//
// The git here reads the copy's files alone, never its .git: its git folder is in the
// run's folder, with an empty configuration and no hooks, and its objects are those of
// the bare source of r, which no run binds. So .gitattributes finds no filter driver, and a
// repository in the copy stays one path that no git opens.
func (d Dir) SetupChanges(ctx context.Context, r Repository) ([]string, error) {
	rel, err := repositoryRel(r)
	if err != nil {
		return nil, err
	}
	top := filepath.Join(d.Work(), filepath.FromSlash(rel))
	private, err := os.MkdirTemp(d.root, "status-")
	if err != nil {
		return nil, fileErr(d.root, err)
	}
	defer os.RemoveAll(private)
	for _, sub := range []string{"objects", "refs"} {
		if err := os.Mkdir(filepath.Join(private, sub), 0o700); err != nil {
			return nil, fileErr(private, err)
		}
	}
	source := d.source(r.index)
	if err := copyFormat(ctx, source, private, r.Base); err != nil {
		return nil, fmt.Errorf(textSetupStatus, shown(top), err)
	}
	env := []string{
		"GIT_DIR=" + private, "GIT_WORK_TREE=" + top, "GIT_INDEX_FILE=" + filepath.Join(private, "index"),
		"GIT_OBJECT_DIRECTORY=" + filepath.Join(source, "objects"),
	}
	if _, err := gitWith(ctx, top, env, nil, "read-tree", r.Base); err != nil {
		return nil, fmt.Errorf(textSetupStatus, shown(top), err)
	}
	out, err := gitWith(ctx, top, env, nil, "status", "--porcelain", "-z", "--untracked-files=all", "--no-renames", "--ignore-submodules=all")
	if err != nil {
		return nil, fmt.Errorf(textSetupStatus, shown(top), err)
	}
	var paths []string
	for _, entry := range strings.Split(out, "\x00") {
		if len(entry) < 4 {
			continue
		}
		// The git session always leaves .qory out.
		if p := strings.TrimSuffix(entry[3:], "/"); !folds(strings.SplitN(p, "/", 2)[0], checkout.Dir) {
			paths = append(paths, entry[3:])
		}
	}
	return paths, nil
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
