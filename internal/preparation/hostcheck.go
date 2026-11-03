package preparation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/places"
	"github.com/qoryai/qory/internal/proxygit"
	"github.com/qoryai/qory/internal/render"
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
//     has a commondir file ([textCommonDir]), info/grafts ([textGrafts]),
//     objects/info/alternates or objects/info/http-alternates ([textAlternates]), or a
//     config or config.worktree
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
	return checkedRepository(ctx, dir, false)
}

// safeAfterRun is [safeRepository] for a repository that qory fetched and an agent had
// open for writing: it also accepts the keys that the fetch writes, see [fetchKey], and
// the two keys of a commit's identity, see [hostgit.Identity], since an agent sets those to
// commit and the commit of the push step sets all four of git's identity variables. git
// reaches the repository's remote by the URL qory builds, so this check takes any value of
// remote.origin.url, and hostgit's settings check of a walled run's place takes only the
// URL it recorded. Every other stop of safeRepository applies, and the error is an
// [*unsafeError].
func safeAfterRun(ctx context.Context, dir string) (*places.Hold, error) {
	return checkedRepository(ctx, dir, true)
}

// safeFurther is [safeAfterRun] for a further repository at dir in the working folder
// root, without hostgit's settings check: a walled run's record has the fingerprint of
// the working folder's repository alone, so that check stops on any remote URL here. git
// reaches the remote by the URL qory builds, the keys and the files are checked, and every
// git qory runs here has replace refs and the commit-graph off. The caller holds root.
// The check also stops on a link anywhere on the way from root to dir, which would take
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

// privateGitDir fills the folder private as a git directory for git status of the
// repository at dir, for a repository that a process may still write: [safeFurther] with
// afterRun false stops on what git would act on, but in a folder that stays open for
// writing, a check of the live files holds only at the moment of the check. So each
// configuration file is read once, into private, and the keys of those copies are
// checked, and git reads only those copies, so a key that dir gets after this read has
// no effect. private also has HEAD at commit, and copies of the index and of
// info/exclude, as far as dir has them. It has no hooks and no attributes. git reads the
// objects from dir, which runs no program.
func privateGitDir(ctx context.Context, root, dir, private, commit string) error {
	if err := noLinkTo(root, dir); err != nil {
		return err
	}
	gitDir := filepath.Join(dir, ".git")
	if err := gitParts(gitDir); err != nil {
		return err
	}
	files, err := gitFiles(gitDir)
	if err != nil {
		return err
	}
	for _, name := range files {
		if err := copyRegular(filepath.Join(gitDir, name), filepath.Join(private, name)); err != nil {
			return local(err)
		}
	}
	if err := keysIn(ctx, private, files, true, false); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(private, "HEAD"), []byte(commit+"\n"), 0o600); err != nil {
		return local(err)
	}
	for _, name := range []string{"refs", "info"} {
		if err := os.Mkdir(filepath.Join(private, name), 0o700); err != nil {
			return local(err)
		}
	}
	for _, name := range []string{"index", filepath.Join("info", "exclude")} {
		err := copyRegular(filepath.Join(gitDir, name), filepath.Join(private, name))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return local(err)
		}
	}
	return nil
}

// copyRegular reads the file at path once, following no link, and writes it to the new
// file target. It stops on anything but a regular file. The copy has the time of path:
// for a file whose size and times match its index entry, git status reads the content
// only if the index file is not newer than the entry, so a newer copy shows no change.
func copyRegular(path, target string) error {
	from, err := os.OpenFile(path, os.O_RDONLY|render.OpenFlags, 0)
	if err != nil {
		return err
	}
	defer from.Close()
	info, err := from.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", integration.Printable(filepath.Base(path)))
	}
	to, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(to, from); err != nil {
		to.Close()
		return err
	}
	if err := to.Close(); err != nil {
		return err
	}
	return os.Chtimes(target, time.Time{}, info.ModTime())
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
			return &unsafeError{fmt.Sprintf(textGitLink, shown), fmt.Sprintf(causeGitLink, shown)}
		}
	}
	return nil
}

// gitParts stops on a git directory without HEAD, objects or refs: git does not take such
// a directory for a repository, and goes on to the folders above it.
func gitParts(gitDir string) error {
	for _, name := range []string{"HEAD", "objects", "refs"} {
		_, err := os.Lstat(filepath.Join(gitDir, name))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			cause := fmt.Sprintf(causeNoGitPart, name)
			return &unsafeError{cause, cause}
		case err != nil:
			return local(err)
		}
	}
	return nil
}

// unsafeError is a stop of [safeRepository]. Its text is the line for a run that has not
// started, and cause is what the folder has, for a line after the agent exited.
type unsafeError struct{ line, cause string }

func (e *unsafeError) Error() string { return e.line }

// checkedRepository is [safeRepository], and [safeAfterRun] with fetched set.
func checkedRepository(ctx context.Context, dir string, fetched bool) (*places.Hold, error) {
	gitDir := filepath.Join(dir, ".git")
	if _, err := gitFiles(gitDir); err != nil {
		return nil, err
	}
	if fetched {
		if err := gitParts(gitDir); err != nil {
			return nil, err
		}
	}
	if err := qoryFiles(dir); err != nil {
		return nil, err
	}
	holdCtx := ctx
	if fetched {
		holdCtx = hostgit.WithIdentity(ctx)
	}
	hold, err := hostgit.Hold(holdCtx, dir)
	if err != nil {
		if hostgit.Refused(err) {
			line := integration.Printable(exports.HideError(err).Error())
			cause := line
			if refused := (*hostgit.RefusedError)(nil); errors.As(err, &refused) {
				cause = integration.Printable(exports.HideError(errors.New(refused.Cause())).Error())
			}
			return nil, &unsafeError{fmt.Sprintf(textPlaceRefused, line), cause}
		}
		return nil, gitFailed(err)
	}
	if err := keysOf(ctx, gitDir, fetched, fetched); err != nil {
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
			if name == "config" && !fetched && strings.HasPrefix(key, "remote.") {
				return errors.New(textHasRemote)
			}
			if name == "config.worktree" || !initKey(key) {
				// A key's subsection can be a URL with a password, as in
				// http.<url>.sslVerify: it is shown without its userinfo, as hostgit shows it.
				shown := hostgit.ShownKey(accesskey.ShownName(key, false))
				return &unsafeError{fmt.Sprintf(textConfigKey, shown, ".git/"+name), fmt.Sprintf(causeConfigKey, shown, ".git/"+name)}
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
		return &unsafeError{fmt.Sprintf(textHardLink, shown), fmt.Sprintf(causeHardLink, shown)}
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
			link = &unsafeError{fmt.Sprintf(textGitLink, shown), fmt.Sprintf(causeGitLink, shown)}
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
			return nil, &unsafeError{textCommonDir, causeCommonDir}
		case err == nil && name == "info/grafts":
			return nil, &unsafeError{textGrafts, causeGrafts}
		case err == nil:
			return nil, &unsafeError{fmt.Sprintf(textAlternates, ".git/"+name), fmt.Sprintf(causeAlternates, ".git/"+name)}
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
			return nil, &unsafeError{fmt.Sprintf(textConfigNotFile, ".git/"+name), fmt.Sprintf(causeConfigNotFile, ".git/"+name)}
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
