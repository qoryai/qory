package preparation

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/places"
	"github.com/qoryai/qory/internal/proxygit"
)

// safeRepository stops on a working folder whose repository git on this machine must not
// fetch into, before any git runs there with the run's host proxy: the fetch runs on
// this machine, and a walled run of an earlier run could have written the working
// folder's .git. A key of the repository's configuration can make git run a program, as
// a filter does on checkout, or take the fetch past the gateway or past its check of
// the host's certificate, as remote.<name>.proxy and http.sslVerify do. A link under
// .git can send git's reads and writes outside the working folder, and alternates make
// git read objects from a store outside it.
//
// It stops on the folder, in this order, when:
//   - its .git has a link anywhere under it ([textGitLink]); it, outside objects, or
//     .qory has a regular file with another name, a hard link ([textHardLink]); or it
//     has a commondir file ([textCommonDir]), objects/info/alternates or
//     objects/info/http-alternates ([textAlternates]), or a config or config.worktree
//     that is not a regular file ([textConfigNotFile]);
//   - hostgit's settings check stops on it ([textPlaceRefused]): the folder is in a place
//     a walled run used, and its configuration has a key qory and git do not write, or
//     changed since the place was recorded, or a walled run uses it now;
//   - its configuration has a remote's key ([textHasRemote]), such as
//     remote.origin.proxy, or another key git init does not write ([textConfigKey]), in
//     any folder, recorded or not.
//
// The files are checked once before hostgit's check, so its line is the one for what
// the folder has, and again after it, under the hold: [hostgit.Hold] takes the lock of
// the folder's place, so a walled run that starts there waits for the release.
// safeRepository returns that hold, which its caller releases once the fetch and the
// checkout end; on a stop, it releases the hold first.
func safeRepository(ctx context.Context, dir string) (*places.Hold, error) {
	gitDir := filepath.Join(dir, ".git")
	if _, err := gitFiles(gitDir); err != nil {
		return nil, err
	}
	if err := qoryFiles(dir); err != nil {
		return nil, err
	}
	hold, err := hostgit.Hold(ctx, dir)
	if err != nil {
		if hostgit.Refused(err) {
			return nil, fmt.Errorf(textPlaceRefused, integration.Printable(exports.HideError(err).Error()))
		}
		return nil, gitFailed(err)
	}
	if err := keysOf(ctx, gitDir); err != nil {
		hold.Release()
		return nil, err
	}
	if err := qoryFiles(dir); err != nil {
		hold.Release()
		return nil, err
	}
	return hold, nil
}

// keysOf checks the files of the git directory gitDir, as [gitFiles] does, and the keys
// of each configuration file it has.
func keysOf(ctx context.Context, gitDir string) error {
	files, err := gitFiles(gitDir)
	if err != nil {
		return err
	}
	for _, name := range files {
		keys, err := configKeys(ctx, filepath.Join(gitDir, name))
		if err != nil {
			return gitFailed(err)
		}
		for _, key := range keys {
			if name == "config" && strings.HasPrefix(key, "remote.") {
				return errors.New(textHasRemote)
			}
			if name == "config.worktree" || !initKey(key) {
				// A key's subsection can be a URL with a password, as in
				// http.<url>.sslVerify: it is shown without its userinfo, as hostgit shows it.
				return fmt.Errorf(textConfigKey, hostgit.ShownKey(accesskey.ShownName(key, false)), ".git/"+name)
			}
		}
	}
	return nil
}

// hardLinked is [textHardLink] for the entry d, at rel from the working folder,
// when it is a regular file that has another name, by [linkCount], and nil otherwise:
// qory and git write some files in place, such as .git/info/exclude and FETCH_HEAD, so
// a write there would reach the file of the other name, which can be outside the
// working folder.
func hardLinked(rel string, d fs.DirEntry) error {
	if !d.Type().IsRegular() {
		return nil
	}
	info, err := d.Info()
	if err != nil {
		return local(err)
	}
	if linkCount(info) > 1 {
		return fmt.Errorf(textHardLink, integration.Printable(filepath.ToSlash(rel)))
	}
	return nil
}

// qoryFiles stops on a regular file under the working folder's .qory, dir/.qory, that
// has another name, as [hardLinked] reads it. A .qory that is not there is none.
func qoryFiles(dir string) error {
	var link error
	err := filepath.WalkDir(filepath.Join(dir, ".qory"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if link = hardLinked(rel, d); link != nil {
			return filepath.SkipAll
		}
		return nil
	})
	switch {
	case link != nil:
		return link
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return local(err)
	}
	return nil
}

// gitFiles stops on a git directory with a link anywhere under it, a regular file
// outside objects that has another name ([hardLinked]), a commondir file, alternates,
// or a configuration file that is not a regular file, and returns the names of the
// configuration files it has, config and config.worktree, each a regular file. The
// walk follows no link.
func gitFiles(gitDir string) ([]string, error) {
	var link error
	err := filepath.WalkDir(gitDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(filepath.Dir(gitDir), path)
		if d.Type()&fs.ModeSymlink != 0 {
			link = fmt.Errorf(textGitLink, integration.Printable(filepath.ToSlash(rel)))
			return filepath.SkipAll
		}
		// Git writes each object to a temporary file and renames it, so a hard link under
		// .git/objects, as git clone --local makes, cannot pass a write to another file.
		if objects := filepath.ToSlash(rel); objects == ".git/objects" || strings.HasPrefix(objects, ".git/objects/") {
			return nil
		}
		if link = hardLinked(rel, d); link != nil {
			return filepath.SkipAll
		}
		return nil
	})
	switch {
	case link != nil:
		return nil, link
	case err != nil:
		return nil, local(err)
	}
	for _, name := range []string{"commondir", "objects/info/alternates", "objects/info/http-alternates"} {
		_, err := os.Lstat(filepath.Join(gitDir, filepath.FromSlash(name)))
		switch {
		case err == nil && name == "commondir":
			return nil, errors.New(textCommonDir)
		case err == nil:
			return nil, fmt.Errorf(textAlternates, ".git/"+name)
		case !errors.Is(err, fs.ErrNotExist):
			return nil, local(err)
		}
	}
	var files []string
	for _, name := range []string{"config", "config.worktree"} {
		info, err := os.Lstat(filepath.Join(gitDir, name))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return nil, local(err)
		case !info.Mode().IsRegular():
			return nil, fmt.Errorf(textConfigNotFile, ".git/"+name)
		}
		files = append(files, name)
	}
	return files, nil
}

// initKey reports whether git init writes key in a repository: a key of [hostgit.Written]
// with no subsection, such as core.bare. A remote's or a branch's key is not one: the
// repository the run's repository is fetched into has no remote and no commit.
func initKey(key string) bool {
	return hostgit.Written(key) && strings.Count(key, ".") == 1
}

// configKeys lists the keys of the configuration file at path, as git reads it, without
// following an include and without acting on any key: git config --file from the root
// folder, with no global or system configuration, run as a git on this machine is, from
// the environment [hostgit.Base] of ctx.
func configKeys(ctx context.Context, path string) ([]string, error) {
	out, err := runGit(ctx, nil, func(ctx context.Context) (*exec.Cmd, error) {
		cmd, err := hostgit.Unchecked(ctx, "/", hostgit.Base(ctx), "config", "--file", path, "--list", "--null", "--name-only")
		if err != nil {
			return nil, err
		}
		cmd.Env = proxygit.Env(cmd.Env, nil)
		return cmd, nil
	}, "config")
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, key := range strings.Split(out, "\x00") {
		if key = strings.TrimSpace(key); key != "" {
			keys = append(keys, key)
		}
	}
	return keys, nil
}
