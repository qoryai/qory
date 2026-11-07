package preparation

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/proxygit"
)

// safeRepository stops on a working folder whose repository git on this machine must not
// fetch into, before any git reaches a remote from there through the run's host proxy: the
// fetch runs on this machine, and an earlier run could have written the working folder's
// .git. A key of the repository's configuration can make git run a program, as a filter does
// on checkout, or take the fetch past the gateway or past its check of the host's
// certificate, as remote.<name>.proxy and http.sslVerify do. A link under .git can send git's
// reads and writes outside the working folder, and alternates make git read objects from a
// store outside it.
//
// It stops on the folder, in this order, when:
//   - its .git has a link anywhere under it ([textGitLink]); a regular file outside objects
//     with another name, a hard link ([textHardLink]); a commondir file ([textCommonDir]),
//     info/grafts ([textGrafts]), objects/info/alternates or objects/info/http-alternates
//     ([textAlternates]); a config or config.worktree that is not a regular file
//     ([textConfigNotFile]); or no HEAD, objects or refs ([textNoGitPart]);
//   - its .qory has a regular file with another name ([textHardLink]);
//   - its configuration has a remote's key ([textHasRemote]), such as
//     remote.origin.proxy, or another key git init does not write ([textConfigKey]).
func safeRepository(ctx context.Context, dir string) error {
	return checkedRepository(ctx, dir, false)
}

// safeAfterRun is [safeRepository] for a repository that qory fetched and an agent had
// open for writing: it also accepts the keys that the fetch writes, see [fetchKey], and
// the two keys of a commit's identity, see [hostgit.Identity], since an agent sets those to
// commit and the commit of the push step sets all four of git's identity variables. git
// reaches the repository's remote by the URL qory builds, so this check takes any value of
// remote.origin.url. Every other stop of safeRepository applies, and the error is an
// [*unsafeError].
func safeAfterRun(ctx context.Context, dir string) error {
	return checkedRepository(ctx, dir, true)
}

// safeFurther is [safeAfterRun] for a further repository at dir in the working folder
// root. git reaches the remote by the URL qory builds, the keys and the files are
// checked, and every git qory runs here has replace refs and the commit-graph off. The
// check also stops on a link anywhere on the way from root to dir, which would take
// every read and write outside the working folder. afterRun is for the check after the
// agent exited, which takes the keys of a commit's identity as [safeAfterRun] does.
func safeFurther(ctx context.Context, root, dir string, afterRun bool) error {
	if err := noLinkTo(root, dir); err != nil {
		return err
	}
	gitDir := filepath.Join(dir, ".git")
	if err := gitParts(gitDir); err != nil {
		return err
	}
	return keysOf(ctx, gitDir, true, afterRun)
}

// noLinkTo stops on a link in the folders of the path from root to dir, root excluded.
func noLinkTo(root, dir string) error {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return local(err)
	}
	at := root
	for part := range strings.SplitSeq(rel, string(filepath.Separator)) {
		at = filepath.Join(at, part)
		info, err := os.Lstat(at)
		switch {
		case err != nil:
			return local(err)
		case info.Mode()&fs.ModeSymlink != 0:
			shown, _ := filepath.Rel(root, at)
			shown = integration.Printable(filepath.ToSlash(shown))
			return &unsafeError{lineOf(textGitLink, shown), fmt.Sprintf(causeGitLink, shown)}
		}
	}
	return nil
}

// gitParts stops on a git directory without HEAD, objects or refs ([textNoGitPart]): git
// does not take such a directory for a repository, and goes on to the folders above it.
func gitParts(gitDir string) error {
	for _, name := range []string{"HEAD", "objects", "refs"} {
		_, err := os.Lstat(filepath.Join(gitDir, name))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return &unsafeError{lineOf(textNoGitPart, name), fmt.Sprintf(causeNoGitPart, name)}
		case err != nil:
			return local(err)
		}
	}
	return nil
}

// unsafeError is a stop of [safeRepository]. Its text is the line for a run that has not
// started, and cause is what the folder has, for a line after the agent exited.
type unsafeError struct {
	line  *runLine
	cause string
}

func (e *unsafeError) Error() string { return e.line.Error() }

// Unwrap is the line, so [Target.stop] finds it.
func (e *unsafeError) Unwrap() error { return e.line }

// checkedRepository is [safeRepository], and [safeAfterRun] with fetched set.
func checkedRepository(ctx context.Context, dir string, fetched bool) error {
	gitDir := filepath.Join(dir, ".git")
	if _, err := gitFiles(gitDir); err != nil {
		return err
	}
	if err := gitParts(gitDir); err != nil {
		return err
	}
	if err := qoryFiles(dir); err != nil {
		return err
	}
	return keysOf(ctx, gitDir, fetched, fetched)
}

// keysOf checks the files of the git directory gitDir, as [gitFiles] does, and the keys
// of each configuration file it has. With fetched set, its config may also have each key
// of [fetchKey], and with afterRun each key of [hostgit.Identity].
func keysOf(ctx context.Context, gitDir string, fetched, afterRun bool) error {
	files, err := gitFiles(gitDir)
	if err != nil {
		return err
	}
	return keysIn(ctx, gitDir, files, fetched, afterRun)
}

// keysIn is the check of [keysOf] of the configuration files files, read from the folder
// dir, a git directory or copies of its files.
func keysIn(ctx context.Context, dir string, files []string, fetched, afterRun bool) error {
	for _, name := range files {
		keys, err := configKeys(ctx, filepath.Join(dir, name))
		if err != nil {
			return gitFailed(err)
		}
		for _, key := range keys {
			if name == "config" && (fetched && fetchKey(key) || afterRun && hostgit.Identity(key)) {
				continue
			}
			if name == "config" && !fetched && strings.HasPrefix(key, "remote.") && strings.Count(key, ".") >= 2 {
				return errors.New(textHasRemote)
			}
			if name == "config.worktree" || !initKey(key) {
				// A key's subsection can be a URL with a password, as in
				// http.<url>.sslVerify: it is shown without its userinfo, as hostgit shows it.
				shown := hostgit.ShownKey(accesskey.ShownName(key, false))
				return &unsafeError{lineOf(textConfigKey, shown, ".git/"+name), fmt.Sprintf(causeConfigKey, shown, ".git/"+name)}
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
		shown := integration.Printable(filepath.ToSlash(rel))
		return &unsafeError{lineOf(textHardLink, shown), fmt.Sprintf(causeHardLink, shown)}
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
// info/grafts, or a configuration file that is not a regular file, and returns the names of the
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
			shown := integration.Printable(filepath.ToSlash(rel))
			link = &unsafeError{lineOf(textGitLink, shown), fmt.Sprintf(causeGitLink, shown)}
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
	for _, name := range []string{"commondir", "info/grafts", "objects/info/alternates", "objects/info/http-alternates"} {
		_, err := os.Lstat(filepath.Join(gitDir, filepath.FromSlash(name)))
		switch {
		case err == nil && name == "commondir":
			return nil, &unsafeError{lineOf(textCommonDir), causeCommonDir}
		case err == nil && name == "info/grafts":
			return nil, &unsafeError{lineOf(textGrafts), causeGrafts}
		case err == nil:
			return nil, &unsafeError{lineOf(textAlternates, ".git/"+name), fmt.Sprintf(causeAlternates, ".git/"+name)}
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
			return nil, &unsafeError{lineOf(textConfigNotFile, ".git/"+name), fmt.Sprintf(causeConfigNotFile, ".git/"+name)}
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

// fetchKey reports whether the fetch of a run's repository writes key: origin's url and
// refspec, and a local branch's upstream, which a clone of a further repository has.
func fetchKey(key string) bool {
	return key == "remote.origin.url" || key == "remote.origin.fetch" || strings.HasPrefix(key, "branch.") && hostgit.Written(key)
}

// configKeys lists the keys of the configuration file at path, as git reads it, without
// following an include and without acting on any key: git config --file from the root
// folder, with no global or system configuration, run as a git on this machine is, from
// the environment [hostgit.Base] of ctx.
func configKeys(ctx context.Context, path string) ([]string, error) {
	out, err := runGit(ctx, nil, func(ctx context.Context) (*exec.Cmd, error) {
		args := append(slices.Clone(hardened), "config", "--file", path, "--list", "--null", "--name-only")
		cmd, err := hostgit.Unchecked(ctx, "/", hostgit.Base(ctx), args...)
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
