package preparation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
)

// Seed is where a prebuilt image keeps its copy of the run's repository: a folder that
// holds the working tree its build left, with its .git, the dependencies the build
// installed among its untracked and ignored files.
const Seed = "/qory/seed"

// The words of a run that starts from the image's copy. The first %s is the copy's
// folder.
const (
	textSeedNotDir   = "qory run prepare: %s is a file or a link; the image's copy of the run's repository is taken only from a folder"
	textSeedNoGit    = "qory run prepare: %s has no .git folder with a config file; the image's copy of the run's repository is a git working tree"
	textSeedConfig   = "qory run prepare: the configuration of the image's copy in %s could not be read: %s"
	textSeedOrigin   = "qory run prepare: the image's copy in %s is not of %s: its origin is not %s"
	textSeedEntry    = "qory run prepare: the image's copy in %s holds %s, which is not a file, a folder or a link; nothing is copied from it"
	textSeedReplaced = "qory run prepare: added from the image's copy the files that the commit of %s does not track"
)

// notCopied are the entries at the top of the image's copy that are never copied: its
// .git, which a build that ran the repository's commands left, so that no object,
// ref, configuration, hook or link of it reaches the run, and its .qory, where qory
// keeps what the working folder has already.
var notCopied = []string{".git", checkout.Dir}

// Prepare is what qory run prepare does with the run's code inside the wall, before the
// start commands: with [EnvRepositories] unset or empty it does nothing and returns
// false. Otherwise it checks the list as [Check] does, and when seed, a prebuilt image's
// copy of the run's repository such as [Seed], is there, it adds the copy's files that
// the fetched commit does not track to dir, the working folder qory run fetched the
// run's repository into, as [fromSeed] has it, and prints a line that says so on report.
// It returns true. Every error's text is one line, ready for stderr.
func Prepare(ctx context.Context, dir, seed string, getenv func(string) string, report io.Writer) (bool, error) {
	list := getenv(EnvRepositories)
	if list == "" {
		return false, nil
	}
	system := getenv(EnvSystem)
	paths, err := Check(system, list)
	if err != nil {
		return false, err
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return false, localIn(err)
	}
	seeded, err := fromSeed(ctx, dir, seed, system, paths[0])
	if err != nil {
		return false, err
	}
	if seeded {
		fmt.Fprintf(report, textSeedReplaced+"\n", integration.Printable(exports.Value(paths[0], false)))
	}
	return true, nil
}

// fromSeed adds to dir the files of the copy in seed that the fetched commit of the run's
// repository, path on system, does not track, when seed is there, and reports whether
// it did; with nothing at seed it does nothing. dir is the working folder qory run
// fetched the run's repository into.
//
// fromSeed refuses a seed that is not a folder, one with no .git folder holding a
// config file, and one whose origin is not the run's repository, as [checkOrigin]
// reads it: a check that the copy is of the run's repository, and nothing of the
// copy's .git is used past it. It copies each entry of the copy's working tree but
// [notCopied] that dir does not have, as [addTree] adds it: the files the fetched commit
// tracks are in dir already and stay as they are, and the untracked and ignored ones of
// the copy, the installed dependencies, are added.
func fromSeed(ctx context.Context, dir, seed, system, path string) (bool, error) {
	if seed == "" {
		return false, nil
	}
	fi, err := os.Lstat(seed)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, localIn(err)
	}
	if !fi.IsDir() {
		return false, fmt.Errorf(textSeedNotDir, seed)
	}
	for _, name := range []string{".git", ".git/config"} {
		fi, err := os.Lstat(filepath.Join(seed, filepath.FromSlash(name)))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return false, localIn(err)
		}
		if err != nil || (name == ".git" && !fi.IsDir()) || (name == ".git/config" && !fi.Mode().IsRegular()) {
			return false, fmt.Errorf(textSeedNoGit, seed)
		}
	}
	if err := checkOrigin(ctx, dir, seed, system, path); err != nil {
		return false, err
	}
	if err := copySeed(dir, seed); err != nil {
		return false, err
	}
	return true, nil
}

// checkOrigin reads the configuration of the copy in seed, its .git/config file alone,
// with git and the [hardened] options and without following an include, and refuses a
// copy whose remote origin has no url, more than one, or one that is not the run's
// repository: https://<system>/<path>, with .git at the end or without, the path in any
// case on github.com. No error quotes the copy's url.
func checkOrigin(ctx context.Context, dir, seed, system, path string) error {
	file := filepath.Join(seed, ".git", "config")
	out, err := git(ctx, dir, "config", "--file", file, "--no-includes", "--null", "--list")
	if err != nil {
		return fmt.Errorf(textSeedConfig, seed, integration.Printable(exports.HideURLError(err).Error()))
	}
	var urls []string
	for _, entry := range strings.Split(out, "\x00") {
		// git lists a key's section and name in lower case, and its subsection as
		// written: remote.Origin.url is another remote's.
		if key, value, _ := strings.Cut(entry, "\n"); key == "remote.origin.url" {
			urls = append(urls, value)
		}
	}
	if len(urls) != 1 || !sameOrigin(system == config.ForgeGitHub, urls[0], system, path) {
		want := strings.TrimSuffix(repoURL(system, path), ".git")
		return fmt.Errorf(textSeedOrigin, seed, integration.Printable(exports.Value(path, false)), exports.URL(want))
	}
	return nil
}

// sameOrigin reports whether url is the URL of the repository at path on system, as
// [repoURL] makes it, with .git at the end or without; on github.com, where names are
// not case-sensitive, the path compares in any case.
func sameOrigin(github bool, url, system, path string) bool {
	prefix := strings.TrimSuffix(repoURL(system, path), path+".git")
	rest, ok := strings.CutPrefix(url, prefix)
	if !ok {
		return false
	}
	return sameRepository(github, strings.TrimSuffix(rest, ".git"), path)
}

// copySeed adds the entries at the top of seed but [notCopied] to dir, as [addTree]
// adds them. Every path is opened through an [os.Root] of dir and one of seed, and every
// entry is made new, so no link in dir is followed and nothing there is written over.
func copySeed(dir, seed string) error {
	dst, err := os.OpenRoot(dir)
	if err != nil {
		return localIn(err)
	}
	defer dst.Close()
	src, err := os.OpenRoot(seed)
	if err != nil {
		return localIn(err)
	}
	defer src.Close()
	return addTree(src, dst, seed, ".", notCopied)
}

// addTree adds the entries of the folder name of src, but the ones skip names, to the
// folder of that name in dst: an entry dst does not have is copied as [copyTree] copies
// it; a folder dst has as a folder, not a link, gets what it lacks the same way; and any
// other entry dst has stays as it is, so no file of dst is written over.
func addTree(src, dst *os.Root, seed, name string, skip []string) error {
	f, err := src.Open(name)
	if err != nil {
		return localIn(err)
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		return localIn(err)
	}
	for _, e := range entries {
		if slices.Contains(skip, e.Name()) {
			continue
		}
		p := filepath.Join(name, e.Name())
		have, err := dst.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			if err := copyEntry(src, dst, seed, p); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return localIn(err)
		}
		fi, err := src.Lstat(p)
		if err != nil {
			return localIn(err)
		}
		if have.IsDir() && fi.IsDir() {
			if err := addTree(src, dst, seed, p, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// copyTree copies the folder name of src into dst, the same name, entry by entry, as
// the user the preparation runs as, but the entries of name that skip names, each as
// [copyEntry] copies it. An entry is never written over.
func copyTree(src, dst *os.Root, seed, name string, skip []string) error {
	f, err := src.Open(name)
	if err != nil {
		return localIn(err)
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		return localIn(err)
	}
	for _, e := range entries {
		if slices.Contains(skip, e.Name()) {
			continue
		}
		if err := copyEntry(src, dst, seed, filepath.Join(name, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// copyEntry copies the entry p of src to a new entry of that name in dst: a folder with
// its contents, a regular file with its bytes, each with its permission bits and its
// time of change, and a link as a link to the same target, never followed. It fails on
// an entry of any other kind, naming it relative to seed, src's folder.
func copyEntry(src, dst *os.Root, seed, p string) error {
	fi, err := src.Lstat(p)
	if err != nil {
		return localIn(err)
	}
	switch mode := fi.Mode(); {
	case mode&fs.ModeSymlink != 0:
		target, err := src.Readlink(p)
		if err != nil {
			return localIn(err)
		}
		if err := dst.Symlink(target, p); err != nil {
			return localIn(err)
		}
	case mode.IsDir():
		if err := dst.Mkdir(p, 0o700); err != nil {
			return localIn(err)
		}
		if err := copyTree(src, dst, seed, p, nil); err != nil {
			return err
		}
		if err := keepMode(dst, p, fi); err != nil {
			return err
		}
	case mode.IsRegular():
		if err := copyFile(src, dst, p); err != nil {
			return err
		}
		if err := keepMode(dst, p, fi); err != nil {
			return err
		}
	default:
		return fmt.Errorf(textSeedEntry, seed, integration.Printable(exports.HidePath(filepath.ToSlash(p))))
	}
	return nil
}

// copyFile copies the regular file name of src to a new file of that name in dst.
func copyFile(src, dst *os.Root, name string) error {
	in, err := src.Open(name)
	if err != nil {
		return localIn(err)
	}
	defer in.Close()
	out, err := dst.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return localIn(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return localIn(err)
	}
	if err := out.Close(); err != nil {
		return localIn(err)
	}
	return nil
}

// keepMode gives the entry name of dst the permission bits and the time of change of
// fi, the entry it was copied from.
func keepMode(dst *os.Root, name string, fi fs.FileInfo) error {
	if err := dst.Chmod(name, fi.Mode().Perm()); err != nil {
		return localIn(err)
	}
	if err := dst.Chtimes(name, fi.ModTime(), fi.ModTime()); err != nil {
		return localIn(err)
	}
	return nil
}
