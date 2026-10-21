package prebuild

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/integration"
)

// The words of the check of git's configuration. The first %s is the repository's
// folder, the second the file, relative to the repository, such as .git/config, and
// the third the name of a key, never its value.
const (
	textGitCredential = "%s holds a credential for git in %s, %s; the image is built only from a repository fetched without one"
	textGitInclude    = "%s includes another file in %s, %s, which can hold a credential for git; the image is built only from a repository fetched without one"
	textGitUnread     = "%s has a git configuration git does not read, %s; the image is built only from a repository whose git configuration git reads"
)

// maxGitFile is the most bytes of a .git file the check reads for its gitdir: line.
const maxGitFile = 4096

// scheme is the scheme a URL starts with, as git and curl read one: a letter, then
// letters, digits, +, - and ., then a colon.
var scheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)

// checkGitConfig refuses a repository whose configuration of git, which the image
// keeps with the rest of the repository, holds a credential for git, or may:
// http.extraHeader, in any case and with or without a URL; any key of credential; a
// URL with a credential, as [credentialURL] has it, as any value or in any subsection,
// such as a remote's url, a submodule's url, a proxy or the URL of an http or url
// section; and an include, include.path or includeIf.<condition>.path, which reads
// another file. The files are those [gitConfigs] finds. A repository with none passes.
//
// git reads each file with git config --file --no-includes --list -z: no other
// configuration, from the temporary folder, which is in no repository. qory runs it
// through [hostgit.Unchecked], so git is the one the PATH the build's programs get
// finds after hostgit's rules, never one whose path, with its symbolic links resolved,
// lies in a place a walled run used (a hard link is not seen), and a git older than
// 2.36 stops the build. Neither a value nor what git prints is shown; a refusal
// names the file and the key, with * in place of a subsection.
func (b *build) checkGitConfig(ctx context.Context) error {
	files := gitConfigs(b.repo)
	if len(files) == 0 {
		return nil
	}
	env := []string{
		"PATH=" + lastValue(b.e.env, "PATH"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CEILING_DIRECTORIES=" + filepath.Dir(b.tmp),
		"LC_ALL=C",
	}
	repo := integration.Printable(exports.Value(b.o.Repository, false))
	for _, f := range files {
		c, err := hostgit.Unchecked(ctx, b.tmp, env, "config", "--file", f.path, "--no-includes", "--list", "-z")
		if err != nil {
			return err
		}
		if errors.Is(c.Err, exec.ErrNotFound) {
			return errors.New("no git command on the PATH")
		}
		var out bytes.Buffer
		c.Stdout = &out
		c.WaitDelay = time.Second
		err = c.Run()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		shown := integration.Printable(exports.HidePath(f.shown))
		var ee *exec.ExitError
		switch {
		case errors.As(err, &ee):
			return refused(fmt.Errorf(textGitUnread, repo, shown))
		case err != nil:
			return fmt.Errorf("git could not start: %s", integration.Printable(err.Error()))
		}
		for _, entry := range strings.Split(out.String(), "\x00") {
			name, value, _ := strings.Cut(entry, "\n")
			if name == "" {
				continue
			}
			if key, include := gitCredential(name, value); key != "" {
				text := textGitCredential
				if include {
					text = textGitInclude
				}
				return refused(fmt.Errorf(text, repo, shown, accesskey.ShownName(key, false)))
			}
		}
	}
	return nil
}

// gitFile is a configuration file of git: its path, and the path shown, relative to the
// repository with slashes.
type gitFile struct{ path, shown string }

// gitConfigs are the configuration files of git the image takes with the repository at
// repo, in its git folder, [gitFolder]: config and config.worktree there and in each
// submodule's git folder under modules, nested ones included, and config.worktree of
// each worktree under worktrees. A git folder of a submodule is a folder under modules
// that holds HEAD, a file or a link, as git writes it with core.preferSymlinkRefs,
// which is not followed; one under modules that does not, such as vendor of a
// submodule named vendor/lib, is looked in. Each is a regular file, through links; a
// link to another folder is not followed, as the copy keeps it as a link.
func gitConfigs(repo string) []gitFile {
	dir, shown, ok := gitFolder(repo)
	if !ok {
		return nil
	}
	var out []gitFile
	add := func(p, s string) {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			out = append(out, gitFile{p, s})
		}
	}
	var folder func(dir, shown string)
	folder = func(dir, shown string) {
		add(filepath.Join(dir, "config"), shown+"/config")
		add(filepath.Join(dir, "config.worktree"), shown+"/config.worktree")
		if entries, err := os.ReadDir(filepath.Join(dir, "worktrees")); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					add(filepath.Join(dir, "worktrees", e.Name(), "config.worktree"), shown+"/worktrees/"+e.Name()+"/config.worktree")
				}
			}
		}
		modules := filepath.Join(dir, "modules")
		_ = filepath.WalkDir(modules, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() || p == modules {
				return nil
			}
			if fi, err := os.Lstat(filepath.Join(p, "HEAD")); err != nil || (!fi.Mode().IsRegular() && fi.Mode()&fs.ModeSymlink == 0) {
				return nil
			}
			rel, _ := filepath.Rel(dir, p)
			folder(p, shown+"/"+filepath.ToSlash(rel))
			return filepath.SkipDir
		})
	}
	folder(dir, shown)
	return out
}

// gitFolder is the git folder of the repository at repo, and its path relative to the
// repository, with slashes: .git when it is a folder; and where .git leads, as a link
// or as a file with a gitdir: line, the git folder of a worktree or a submodule, when
// that is a folder inside the repository. A git folder outside the repository is not
// one the image takes: the copy keeps .git, the link or the file, alone, and it is not
// looked in.
func gitFolder(repo string) (dir, shown string, ok bool) {
	p := filepath.Join(repo, ".git")
	fi, err := os.Lstat(p)
	if err != nil {
		return "", "", false
	}
	target := p
	switch {
	case fi.IsDir():
		return p, ".git", true
	case fi.Mode().IsRegular():
		f, err := os.Open(p)
		if err != nil {
			return "", "", false
		}
		data, _ := io.ReadAll(io.LimitReader(f, maxGitFile))
		_ = f.Close()
		line, _, _ := strings.Cut(string(data), "\n")
		t, found := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
		if !found || strings.TrimSpace(t) == "" {
			return "", "", false
		}
		target = strings.TrimSpace(t)
		if !filepath.IsAbs(target) {
			target = filepath.Join(repo, target)
		}
	case fi.Mode()&fs.ModeSymlink == 0:
		return "", "", false
	}
	real, err1 := filepath.EvalSymlinks(repo)
	resolved, err2 := filepath.EvalSymlinks(target)
	if err1 != nil || err2 != nil {
		return "", "", false
	}
	rel, err := filepath.Rel(real, resolved)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", false
	}
	if fi, err := os.Stat(resolved); err != nil || !fi.IsDir() {
		return "", "", false
	}
	return resolved, filepath.ToSlash(rel), true
}

// gitCredential reads one entry of git config --list: its name, section.key or
// section.subsection.key, the section and the key in lower case as git prints them,
// and its value. It returns the name to show when the entry holds a credential, or
// includes another file, and "" when it does neither. The name shown has * in place
// of the subsection, which may be a URL with a credential: section.*.key.
func gitCredential(name, value string) (shown string, include bool) {
	section, rest, _ := strings.Cut(name, ".")
	sub, key := "", rest
	shown = section + "." + key
	if i := strings.LastIndex(rest, "."); i >= 0 {
		sub, key = rest[:i], rest[i+1:]
		shown = section + ".*." + key
	}
	switch {
	case section == "include" || section == "includeif":
		return shown, true
	case section == "credential", section == "http" && key == "extraheader":
		return shown, false
	case credentialURL(sub) || credentialURL(value):
		return shown, false
	}
	return "", false
}

// credentialURL reports whether s is a URL with a credential in its user information,
// what comes before the last @ of its authority, which follows the scheme's colon and
// slashes up to the next /, ? or #: a password, user:password, with any scheme, and a
// user name alone with any scheme but ssh and git+ssh, as https often carries a token
// as the user name. ssh://git@example.com/acme/shop.git names the user of ssh, and so
// does git@example.com:acme/shop.git, which has no scheme: no credential.
func credentialURL(s string) bool {
	m := scheme.FindString(s)
	if m == "" {
		return false
	}
	authority := strings.TrimLeft(s[len(m):], "/")
	if i := strings.IndexAny(authority, "/?#"); i >= 0 {
		authority = authority[:i]
	}
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return false
	}
	if strings.Contains(authority[:at], ":") {
		return true
	}
	switch strings.ToLower(strings.TrimSuffix(m, ":")) {
	case "ssh", "git+ssh":
		return false
	}
	return true
}
