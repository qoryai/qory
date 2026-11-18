package runtimeinstall

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// maxEntries is the most entries of a release's folder that [extractFolder] writes.
const maxEntries = 10000

// folder is the folder of a runtime's versions, <Folder>/<runtime>.
type folder struct {
	base, runtime string
	// mounted returns the host folders that running containers mount, see
	// [Options.Mounted]; nil for none.
	mounted func() ([]string, error)
}

// complete reports whether the folder of version is a complete install of the tarball
// with npm's checksum integrity. A folder at that path is complete when it exists,
// since [folder.install] renames only a finished folder there; it must contain
// bin/<runtime>, a regular file everyone can read and run, and checksumFile with
// integrity. A folder of another tarball, or a path that is not a folder, is an error:
// the version's folder is never replaced, since a run may still use it.
func (f folder) complete(version, integrity string) (bool, error) {
	dir := filepath.Join(f.base, version)
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("Check that qory can read its state folder: %w", err)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("Remove the version's entry in the runtimes folder, then run again\n"+
			"%s is where %s %s is installed, and it is not a folder.", dir, f.runtime, version)
	}
	program := filepath.Join(dir, "bin", f.runtime)
	p, err := os.Lstat(program)
	if err != nil || !p.Mode().IsRegular() || p.Mode().Perm()&0o555 != 0o555 {
		return false, fmt.Errorf("Remove the version's folder once no run uses it, then run again\n"+
			"%s has no program bin/%s that everyone can read and run.", dir, f.runtime)
	}
	held, err := readSmall(filepath.Join(dir, checksumFile))
	if err != nil || strings.TrimSpace(held) != integrity {
		return false, fmt.Errorf("Remove the version's folder once no run uses it, then run again\n"+
			"%s was not installed from the tarball with the checksum %s", dir, integrity)
	}
	return true, nil
}

// readSmall reads a file of at most 1 KiB, without following a link.
func readSmall(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<10 {
		return "", errors.New("not a small regular file")
	}
	b, err := os.ReadFile(path)
	return string(b), err
}

// textState starts each error of a folder qory cannot write.
const textState = "Check that qory can write its state folder: "

// install reads the tarball of rel into a temporary file in the folder and checks it
// against sum, the sha512 digest of rel's checksum. It extracts the program, or rel's
// folder whole, into a temporary folder, and renames that folder to the version's folder. When another run
// renamed its folder there first, that folder is used, if [folder.complete] takes it.
// The temporary file and folder are removed on every path.
func (f folder) install(ctx context.Context, rel Release, sum []byte) error {
	tarball, err := os.CreateTemp(f.base, downloadPrefix+rel.Version+"-*")
	if err != nil {
		return fmt.Errorf(textState+"%w", err)
	}
	defer os.Remove(tarball.Name())
	defer tarball.Close()
	what := f.runtime + " " + rel.Version
	if err := download(ctx, rel, sum, tarball, what); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(f.base, installPrefix+rel.Version+"-*")
	if err != nil {
		return fmt.Errorf(textState+"%w", err)
	}
	defer os.RemoveAll(tmp)
	if _, err := tarball.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf(textState+"%w", err)
	}
	if rel.Folder != "" {
		err = extractFolder(tarball, tmp, f.runtime, rel.Folder, what)
	} else {
		entry := rel.Program
		if entry == "" {
			entry = "package/" + f.runtime
		}
		err = extract(tarball, tmp, f.runtime, entry, what)
	}
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(tmp, checksumFile), []byte(rel.Integrity+"\n"), 0o644); err != nil {
		return fmt.Errorf(textState+"%w", err)
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return fmt.Errorf(textState+"%w", err)
	}
	if err := syncDir(tmp); err != nil {
		return fmt.Errorf(textState+"%w", err)
	}
	if err := os.Rename(tmp, filepath.Join(f.base, rel.Version)); err != nil {
		// Another run renamed its folder there first.
		done, cerr := f.complete(rel.Version, rel.Integrity)
		if cerr != nil {
			return cerr
		}
		if done {
			return nil
		}
		return fmt.Errorf(textState+"%w", err)
	}
	return syncDir(f.base)
}

// download writes the tarball of rel to w, at most [maxTarball] bytes, and checks it
// against sum, the sha512 digest of npm's checksum. what is the release as an error
// shows it.
func download(ctx context.Context, rel Release, sum []byte, w io.Writer, what string) error {
	body, err := rel.Open(ctx)
	if err != nil {
		return err
	}
	defer body.Close()
	h := sha512.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(body, maxTarball+1))
	var check *checkError
	if errors.As(err, &check) {
		return check
	}
	if err != nil {
		return fmt.Errorf("Run again\nThe download of %s stopped: %w", what, err)
	}
	if n > maxTarball {
		return fmt.Errorf(textRelease+"The tarball of %s has more than %d bytes", what, maxTarball)
	}
	if subtle.ConstantTimeCompare(h.Sum(nil), sum) != 1 {
		return fmt.Errorf(textRelease+"The tarball of %s does not match the checksum %s", what, rel.Integrity)
	}
	return nil
}

// countingReader counts the bytes read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.n += int64(n)
	return n, err
}

// extract writes the one entry want of the gzip tar archive in r, the program's, to
// <dir>/bin/<runtime>, mode 0755, with bin mode 0755. No other entry is written. The
// entry must be a regular file of at most [maxProgram] bytes, there must be exactly one,
// and the archive must unpack to at most [maxUnpacked] bytes. what is the release as an
// error shows it.
func extract(r io.Reader, dir, runtime, want, what string) error {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf(textRelease+"The tarball of %s is not gzip", what)
	}
	defer zr.Close()
	unpacked := &countingReader{r: io.LimitReader(zr, maxUnpacked+1)}
	tr := tar.NewReader(unpacked)
	found := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if unpacked.n > maxUnpacked {
			return fmt.Errorf(textRelease+"The tarball of %s unpacks to more than %d bytes", what, maxUnpacked)
		}
		if err != nil {
			return fmt.Errorf(textRelease+"The tarball of %s is not a tar archive: %w", what, err)
		}
		if h.Name != want {
			continue
		}
		switch {
		case found:
			return fmt.Errorf(textRelease+"The tarball of %s has %s twice", what, want)
		case h.Typeflag != tar.TypeReg:
			return fmt.Errorf(textRelease+"The tarball of %s has %s as another kind of entry than a regular file", what, want)
		case h.Size < 1 || h.Size > maxProgram:
			return fmt.Errorf(textRelease+"The tarball of %s has %s of %d bytes, and a program has 1 to %d", what, want, h.Size, maxProgram)
		}
		found = true
		if err := writeProgram(tr, h.Size, filepath.Join(dir, "bin"), runtime); err != nil {
			if unpacked.n > maxUnpacked {
				return fmt.Errorf(textRelease+"The tarball of %s unpacks to more than %d bytes", what, maxUnpacked)
			}
			return err
		}
	}
	if unpacked.n > maxUnpacked {
		return fmt.Errorf(textRelease+"The tarball of %s unpacks to more than %d bytes", what, maxUnpacked)
	}
	if !found {
		return fmt.Errorf(textRelease+"The tarball of %s has no %s", what, want)
	}
	return nil
}

// writeProgram writes size bytes of r to <bin>/<name>, a new file, mode 0755, and
// makes bin, mode 0755.
func writeProgram(r io.Reader, size int64, bin, name string) error {
	if err := os.Mkdir(bin, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		return err
	}
	if err := writeEntry(r, size, filepath.Join(bin, name), 0o755); err != nil {
		return err
	}
	return syncDir(bin)
}

// writeEntry writes size bytes of r to path, a new file of mode perm, and syncs it.
func writeEntry(r io.Reader, size int64, path string, perm fs.FileMode) error {
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.CopyN(out, r, size); err != nil {
		out.Close()
		return fmt.Errorf(textRelease+"The tarball's entry for %s ends before its %d bytes: %w", filepath.Base(path), size, err)
	}
	if err := out.Chmod(perm); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// extractFolder writes each entry of the gzip tar archive in r whose name starts with
// prefix, a folder such as package/vendor/x86_64-unknown-linux-musl/, to dir, at its
// path below prefix, so the folder keeps its layout. No other entry is written. Each
// entry is a folder, mode 0755, or a regular file of at most [maxProgram] bytes, mode
// 0755 when its mode in the archive lets anyone run it and 0644 otherwise; its path is a
// plain relative path, with no . or .. segment. The folder must have at most
// [maxEntries] entries, no file named [checksumFile] at its top, and bin/<runtime>, a
// file of at least 1 byte, which is made 0755. The archive must unpack to at most
// [maxUnpacked] bytes. what is the release as an error shows it.
func extractFolder(r io.Reader, dir, runtime, prefix, what string) error {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf(textRelease+"The tarball of %s is not gzip", what)
	}
	defer zr.Close()
	unpacked := &countingReader{r: io.LimitReader(zr, maxUnpacked+1)}
	tr := tar.NewReader(unpacked)
	tooBig := fmt.Errorf(textRelease+"The tarball of %s unpacks to more than %d bytes", what, maxUnpacked)
	program := "bin/" + runtime
	made := map[string]bool{".": true}
	entries := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if unpacked.n > maxUnpacked {
			return tooBig
		}
		if err != nil {
			return fmt.Errorf(textRelease+"The tarball of %s is not a tar archive: %w", what, err)
		}
		rest, ok := strings.CutPrefix(h.Name, prefix)
		if !ok {
			continue
		}
		name := rest
		if h.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(rest, "/")
			if name == "" {
				continue
			}
		}
		if entries++; entries > maxEntries {
			return fmt.Errorf(textRelease+"The folder %s of the tarball of %s has more than %d entries", prefix, what, maxEntries)
		}
		if !filepath.IsLocal(name) || path.Clean(name) != name || strings.Contains(name, `\`) {
			return fmt.Errorf(textRelease+"The tarball of %s has the entry %q, whose path below %s is not a plain relative path", what, h.Name, prefix)
		}
		if name == checksumFile {
			return fmt.Errorf(textRelease+"The tarball of %s has %s, and qory writes its checksum file there", what, h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = makeDirs(dir, name, made)
		case tar.TypeReg:
			if h.Size > maxProgram {
				return fmt.Errorf(textRelease+"The tarball of %s has %s of %d bytes, and a file has at most %d", what, h.Name, h.Size, maxProgram)
			}
			perm := fs.FileMode(0o644)
			if h.Mode&0o111 == 0o111 || name == program {
				perm = 0o755
			}
			if err = makeDirs(dir, path.Dir(name), made); err == nil {
				err = writeEntry(tr, h.Size, filepath.Join(dir, filepath.FromSlash(name)), perm)
			}
		default:
			return fmt.Errorf(textRelease+"The tarball of %s has %s as another kind of entry than a regular file or a folder", what, h.Name)
		}
		switch {
		case err == nil:
		case unpacked.n > maxUnpacked:
			return tooBig
		case errors.Is(err, fs.ErrExist):
			return fmt.Errorf(textRelease+"The tarball of %s has %s twice, or as a file and a folder", what, h.Name)
		case strings.HasPrefix(err.Error(), textRelease):
			return err
		default:
			return fmt.Errorf(textState+"%w", err)
		}
	}
	if unpacked.n > maxUnpacked {
		return tooBig
	}
	if p, err := os.Lstat(filepath.Join(dir, "bin", runtime)); err != nil || !p.Mode().IsRegular() || p.Size() < 1 {
		return fmt.Errorf(textRelease+"The tarball of %s has no %s", what, prefix+program)
	}
	for name := range made {
		if err := syncDir(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			return fmt.Errorf(textState+"%w", err)
		}
	}
	return nil
}

// makeDirs makes the folder name below dir, and each folder above it, mode 0755, unless
// made records it, and records each folder it makes in made.
func makeDirs(dir, name string, made map[string]bool) error {
	if made[name] {
		return nil
	}
	if err := makeDirs(dir, path.Dir(name), made); err != nil {
		return err
	}
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.Mkdir(p, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(p, 0o755); err != nil {
		return err
	}
	made[name] = true
	return nil
}

// writeFile writes b to path, a new file of mode perm, and syncs it.
func writeFile(path string, b []byte, perm fs.FileMode) error {
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = out.Write(b)
	if err == nil {
		err = out.Chmod(perm)
	}
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// syncDir syncs the folder at path, so the names in it survive a crash.
func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		return err
	}
	return nil
}
