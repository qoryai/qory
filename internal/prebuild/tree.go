package prebuild

import (
	"archive/tar"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
)

// textOddFile is a file of the repository that is not copied: the path relative to the
// repository.
const textOddFile = "the repository's %s is neither a file, a folder nor a link; only those are copied to " + Workspace

// writeTree writes, as a tar stream to w, the folders [Workspace] and [SeedPath],
// [AgentHome] with mode 0700 when home is true, and, in the first, the repository at
// root, its .git included: every folder, regular file
// and symbolic link, the link as it is, never followed. Every entry belongs to uid:gid
// and keeps its permission bits, without setuid, setgid or sticky. The paths are
// relative to the container's root, as docker cp extracts them at /. A file of another
// kind is refused.
func writeTree(w io.Writer, root string, uid, gid int, home bool) error {
	tw := tar.NewWriter(w)
	now := time.Now()
	dirs := []struct {
		path string
		mode int64
	}{{Workspace, 0o755}, {SeedPath, 0o755}}
	if home {
		dirs = append(dirs, struct {
			path string
			mode int64
		}{AgentHome, 0o700})
	}
	for _, dir := range dirs {
		h := &tar.Header{Typeflag: tar.TypeDir, Name: strings.TrimPrefix(dir.path, "/") + "/", Mode: dir.mode, Uid: uid, Gid: gid, ModTime: now}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == "." {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		name := path.Join(strings.TrimPrefix(Workspace, "/"), filepath.ToSlash(rel))
		h := &tar.Header{Name: name, Mode: int64(info.Mode().Perm()), Uid: uid, Gid: gid, ModTime: info.ModTime()}
		switch {
		case info.IsDir():
			h.Typeflag, h.Name = tar.TypeDir, name+"/"
			return tw.WriteHeader(h)
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			h.Typeflag, h.Linkname = tar.TypeSymlink, target
			return tw.WriteHeader(h)
		case info.Mode().IsRegular():
			h.Typeflag, h.Size = tar.TypeReg, info.Size()
			if err := tw.WriteHeader(h); err != nil {
				return err
			}
			return copyFile(tw, p)
		}
		return refused(fmt.Errorf(textOddFile, integration.Printable(exports.HidePath(filepath.ToSlash(rel)))))
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

// copyFile writes the file at p to w.
func copyFile(w io.Writer, p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}
