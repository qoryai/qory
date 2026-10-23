package hostgit

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/places"
)

// Reasons the check refuses.
const (
	// RefusedSetting is a key outside the accepted ones.
	RefusedSetting = iota
	// RefusedUnreadable is a configuration git cannot read.
	RefusedUnreadable
	// RefusedPointer is a .git that points somewhere else than when the place was
	// recorded.
	RefusedPointer
	// RefusedNoRecords is a state directory qory cannot find or take, so it cannot read
	// which places a walled run used; Err says why.
	RefusedNoRecords
	// RefusedRemote is a remote's URL the fingerprint does not hold.
	RefusedRemote
	// RefusedRemoteFile is a file in the git directory's remotes or branches folder,
	// which git reads for a remote's URL; Key is its path.
	RefusedRemoteFile
	// RefusedNoGit is a configuration the check cannot read because git is not on the
	// PATH.
	RefusedNoGit
	// RefusedRemoteUnrecorded is a remote's URL in a repository the records hold no
	// fingerprint of.
	RefusedRemoteUnrecorded
	// RefusedRemoteNoURL is a key of a remote the configuration gives no URL, which git
	// then takes the remote's name for.
	RefusedRemoteNoURL
	// RefusedRemoteFolder is a remotes or branches folder of the git directory qory
	// cannot list: one it may not read, or a link or a file where the folder goes; Key
	// is its path.
	RefusedRemoteFolder
	// RefusedInUse is a directory, or its repository's git directory or common
	// directory, in a place a walled run uses, see [places.InUse].
	RefusedInUse
	// RefusedObjectsFile is a file of [objectFiles] in the common directory that the
	// record does not hold as it is now; Key is its path.
	RefusedObjectsFile
	// RefusedObjectsFileUnrecorded is a file of [objectFiles] in the common directory of
	// a repository whose record holds none of them: one written before qory took them,
	// one of another common directory, or none at all; Key is its path.
	RefusedObjectsFileUnrecorded
	// RefusedObjectsLink is a link among the objects of the common directory, see
	// [objectLinks], that the record does not hold as it is now; Key is its path, Target
	// where it leads.
	RefusedObjectsLink
	// RefusedRefLink is a link among the refs of the common directory, see [refLinks],
	// that leads out of the place it lies in and that the record does not hold as it is
	// now; Key is its path, Target where it leads.
	RefusedRefLink
	// RefusedEngineUnreachable is a directory, or its repository's git directory or
	// common directory, in a recorded place, while Docker cannot be asked whether an
	// earlier walled run is still going, see [places.Unreachable]; Key is the run's id,
	// Target the path of its entry in Forager's registry of walled runs, empty when the
	// registry names none.
	RefusedEngineUnreachable
	// RefusedEntryUnreadable is a directory, or its repository's git directory or common
	// directory, in a recorded place, while the entry of an earlier walled run in
	// Forager's registry of walled runs cannot be read, see [places.Unreachable]; Key is
	// the run's id, Target the path of its entry.
	RefusedEntryUnreadable
	// RefusedUnlistedFolder is a folder among the objects or the refs of the common
	// directory, see [objectLinks] and [refLinks], that qory cannot list; Key is its
	// path.
	RefusedUnlistedFolder
)

// RefusedError is the settings check refusing a repository: qory runs no git there.
type RefusedError struct {
	// Dir is the directory the git command was to run in.
	Dir string
	// Key is the key refused, as git lists it, for [RefusedSetting], and the walled
	// run's id for [RefusedEngineUnreachable] and [RefusedEntryUnreadable].
	Key string
	// Reason is [RefusedSetting], [RefusedUnreadable], [RefusedPointer],
	// [RefusedNoRecords], [RefusedRemote], [RefusedRemoteFile], [RefusedNoGit],
	// [RefusedRemoteUnrecorded], [RefusedRemoteNoURL], [RefusedRemoteFolder],
	// [RefusedInUse], [RefusedObjectsFile], [RefusedObjectsFileUnrecorded],
	// [RefusedObjectsLink], [RefusedRefLink], [RefusedEngineUnreachable],
	// [RefusedEntryUnreadable] or [RefusedUnlistedFolder].
	Reason int
	// Target is where the link Key leads, with its links resolved, for
	// [RefusedObjectsLink] and [RefusedRefLink], and the path of the walled run's entry
	// for [RefusedEngineUnreachable] and [RefusedEntryUnreadable].
	Target string
	// Err is why the records cannot be read, for [RefusedNoRecords].
	Err error
}

// Unwrap is the error that kept the records from being read, nil for another reason.
func (e *RefusedError) Unwrap() error { return e.Err }

// Error says what was refused. A directory or key that carries a control character is
// quoted as Go quotes a string, so it cannot act on the terminal.
func (e *RefusedError) Error() string {
	switch e.Reason {
	case RefusedNoRecords:
		return e.Err.Error()
	case RefusedNoGit:
		return fmt.Sprintf("git is not on the PATH, so qory cannot check the git configuration of %s; qory runs no git there, since a run could have set it to make git on this machine run a program", Shown(e.Dir))
	case RefusedUnreadable:
		return fmt.Sprintf("the git configuration of %s cannot be read; qory runs no git there, since a run could have set it to make git on this machine run a program", Shown(e.Dir))
	case RefusedRemote:
		return fmt.Sprintf("the git configuration of %s sets %s to a URL it did not have when a walled run began here; qory runs no git there, since the run could have pointed it at another server, which git on this machine would reach with this machine's credentials. qory setup repo --accept-checkout accepts it", Shown(e.Dir), ShownKey(accesskey.ShownName(e.Key, false)))
	case RefusedRemoteUnrecorded:
		return fmt.Sprintf("the git configuration of %s sets %s to a URL, and qory holds no record of the repository from when a walled run began here; qory runs no git there, since a run could have pointed it at another server, which git on this machine would reach with this machine's credentials. qory setup repo --accept-checkout accepts it", Shown(e.Dir), ShownKey(accesskey.ShownName(e.Key, false)))
	case RefusedRemoteNoURL:
		return fmt.Sprintf("the git configuration of %s sets %s for a remote it gives no URL; qory runs no git there, since git takes the remote's name for its URL then, which a run could have chosen to reach another server. Remove it to go on", Shown(e.Dir), ShownKey(accesskey.ShownName(e.Key, false)))
	case RefusedInUse:
		return fmt.Sprintf("%s is in use by a walled run; qory runs no git there until the run ends, since the run could change git's configuration between qory's check and git's read", Shown(e.Dir))
	case RefusedEngineUnreachable:
		if e.Target == "" {
			return fmt.Sprintf("The check for a container of the walled run %s failed. That run used %s, so qory runs no git there", Shown(e.Key), Shown(e.Dir))
		}
		return fmt.Sprintf("The check for a container of the walled run %s failed. That run used %s, so qory runs no git there. If docker ps --all --filter label=dev.qory.run=%s lists no container, or the Docker engine of that run no longer exists, delete %s", Shown(e.Key), Shown(e.Dir), Shown(e.Key), Shown(e.Target))
	case RefusedEntryUnreadable:
		return fmt.Sprintf("qory cannot read %s, the record of the walled run %s, so it cannot tell whether that run used %s or is still going, and runs no git there. If docker ps --all --filter label=dev.qory.run=%s lists no container, delete %s", Shown(e.Target), Shown(e.Key), Shown(e.Dir), Shown(e.Key), Shown(e.Target))
	case RefusedUnlistedFolder:
		return fmt.Sprintf("the git directory of %s holds %s, which qory cannot list; qory runs no git there, since a run could have hidden in it a link to another repository's objects or history. Remove it to go on", Shown(e.Dir), Shown(exports.HidePath(e.Key)))
	case RefusedObjectsLink:
		return fmt.Sprintf("the git directory of %s holds %s, a link to %s; qory runs no git there, since a run could have pointed it at another repository's objects. Remove the link to go on", Shown(e.Dir), Shown(exports.HidePath(e.Key)), Shown(exports.HidePath(e.Target)))
	case RefusedRefLink:
		return fmt.Sprintf("the git directory of %s holds %s, a link to %s; qory runs no git there, since a run could have pointed it at another repository's history. Remove the link to go on", Shown(e.Dir), Shown(exports.HidePath(e.Key)), Shown(exports.HidePath(e.Target)))
	case RefusedObjectsFileUnrecorded:
		return fmt.Sprintf("the git directory of %s holds %s, and qory holds no record of whether it did when a walled run began here; qory runs no git there, since a run could have written it to change which objects or history git reads. Remove it to go on", Shown(e.Dir), Shown(exports.HidePath(e.Key)))
	case RefusedObjectsFile:
		return fmt.Sprintf("the git directory of %s holds %s, which it did not hold when a walled run began here; qory runs no git there, since a run could have written it to change which objects or history git reads. Remove it to go on", Shown(e.Dir), Shown(exports.HidePath(e.Key)))
	case RefusedRemoteFolder:
		return fmt.Sprintf("the git directory of %s holds %s, which qory cannot list; qory runs no git there, since a run could have hidden in it a file that points a remote at another server. Remove it to go on", Shown(e.Dir), Shown(exports.HidePath(e.Key)))
	case RefusedRemoteFile:
		return fmt.Sprintf("the git directory of %s holds %s, which qory does not write; qory runs no git there, since a run could have written it to point a remote at another server. Remove it to go on", Shown(e.Dir), Shown(exports.HidePath(e.Key)))
	case RefusedPointer:
		return "the checkout's .git points somewhere else since a walled run began here; qory runs no git there, since the run could have pointed it. qory setup repo --accept-checkout accepts it"
	}
	return fmt.Sprintf("the git configuration of %s sets %s, which qory does not write; qory runs no git there, since a run could have set it to make git on this machine run a program", Shown(e.Dir), ShownKey(accesskey.ShownName(e.Key, false)))
}

// unreadable is the refusal of a configuration of the repository at dir that git could
// not read, err being why: [RefusedNoGit] when there is no git to read it with,
// [RefusedUnreadable] otherwise.
func unreadable(dir string, err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		return &RefusedError{Dir: dir, Reason: RefusedNoGit}
	}
	return &RefusedError{Dir: dir, Reason: RefusedUnreadable}
}

// Shown is s as it can be printed: as it is, or quoted as Go quotes a string when it
// carries a rune Go does not print as it is, such as a control character, a mark that
// turns the text's direction or one of no width, or is not UTF-8.
func Shown(s string) string {
	if !utf8.ValidString(s) || strings.ContainsFunc(s, func(r rune) bool { return !strconv.IsPrint(r) }) {
		return strconv.Quote(s)
	}
	return s
}

// written are the keys qory and git write in a repository, without a subsection: git
// init's, with the ones git init writes on a file system without symlinks or one that
// ignores case, on macOS, and for a repository of another object or ref format.
var written = []string{
	"core.repositoryformatversion", "core.filemode", "core.bare", "core.logallrefupdates",
	"core.ignorecase", "core.precomposeunicode", "core.symlinks",
	"extensions.objectformat", "extensions.refstorage",
}

// writtenUnder are the variables of the keys qory and git write under a subsection of
// any name: remote add's and clone's refspec, and the branch keys a worktree add, a
// clone and qory write, qory-base among them. A remote's url is not one: git on this
// machine reaches it with the person's credentials, so it is accepted only as the
// fingerprint holds it, value and all, see [remoteURL]. A branch's remote is: every
// remote qory fetches from, pushes to or asks names the remote itself.
var writtenUnder = map[string][]string{
	"remote": {"fetch"},
	"branch": {"remote", "merge", "qory-base"},
}

// remoteURL reports whether key is a remote's url, remote.<name>.url.
func remoteURL(key string) bool {
	section, rest, ok := strings.Cut(key, ".")
	return ok && section == "remote" && strings.HasSuffix(rest, ".url") && rest != ".url"
}

// remoteName is the name of the remote a key remote.<name>.<variable> is of, ok false
// for any other key.
func remoteName(key string) (name string, ok bool) {
	rest, ok := strings.CutPrefix(key, "remote.")
	if !ok {
		return "", false
	}
	i := strings.LastIndex(rest, ".")
	if i <= 0 {
		return "", false
	}
	return rest[:i], true
}

// noURL reports whether key is a remote's key, other than its url, of a remote that
// urls, the names of the remotes the configuration gives a url, does not hold: git takes
// the name of such a remote for its URL, or a path, and a run could have chosen it.
func noURL(key string, urls map[string]bool) bool {
	name, ok := remoteName(key)
	return ok && !remoteURL(key) && !urls[name]
}

// configFile is a configuration file of a repository that is there, as the check read it.
type configFile struct {
	path     string
	settings []Setting
	// checked is whether the check reads it, see [checked].
	checked bool
	// held is the hashes the fingerprint holds for it, nil with no fingerprint.
	held []string
	// read is whether git reads it: the common config always, config.worktree only when
	// the common config sets extensions.worktreeConfig true. The check reads both.
	read bool
}

// readConfigs lists each configuration file of repo that is there, its common config
// and its config.worktree, against fp, its fingerprint in recs: once, so what the check
// compares and what an accept takes are the same reads. A file the check reads that git
// cannot list is an error, and so is any with all set; one it does not read lists no
// setting then.
func readConfigs(ctx context.Context, recs []places.Record, repo Repo, fp *places.Repository, all bool) ([]configFile, error) {
	config, worktree := repo.files()
	var out []configFile
	for _, f := range []struct {
		path   string
		hashes func(*places.Repository) []string
	}{
		{config, func(e *places.Repository) []string { return e.Settings }},
		{worktree, func(e *places.Repository) []string { return e.WorktreeSettings }},
	} {
		if _, err := os.Lstat(f.path); err != nil {
			continue
		}
		c := configFile{path: f.path, checked: checked(recs, repo, fp, f.path)}
		settings, err := list(ctx, f.path)
		if err != nil && (c.checked || all) {
			return nil, unreadable(repo.dir(), err)
		}
		c.settings = settings
		if fp != nil {
			c.held = f.hashes(fp)
		}
		c.read = f.path == config || (len(out) > 0 && out[0].path == config && worktreeConfig(out[0].settings))
		out = append(out, c)
	}
	return out, nil
}

// worktreeConfig reports whether settings, a common config's, set
// extensions.worktreeConfig true, the last value as git takes it: without config.worktree
// is not read.
func worktreeConfig(settings []Setting) bool {
	on := false
	for _, s := range settings {
		if s.Key != "extensions.worktreeconfig" {
			continue
		}
		on = s.Value == nil || gitTrue(*s.Value)
	}
	return on
}

// gitTrue reports whether git takes value as a true boolean: yes, on or true in any
// case, or a number other than 0 as [gitInt] reads it. Anything else is false here: no,
// off, false and the empty value, and what git refuses, for which it runs nothing.
func gitTrue(value string) bool {
	switch strings.ToLower(value) {
	case "yes", "on", "true":
		return true
	}
	n, ok := gitInt(value)
	return ok && n != 0
}

// gitInt reads value as git reads a number in its configuration: as C's strtoimax does
// with base 0, leading spaces and a sign, then 0x and hex digits, 0b and binary digits,
// 0 and octal digits, or decimal digits, and then nothing, or one unit, k, m or g in any
// case, which multiplies it by 1024, 1024² or 1024³. ok is false for what git refuses: no
// digit, anything else after them, or a number past a C int's 32 bits, which a boolean is
// read as. A C library that reads no 0b, such as macOS's, has git refuse 0b1, and git then
// runs nothing; one that does, such as glibc's since 2.38, has git read it as 1.
func gitInt(value string) (n int64, ok bool) {
	s := strings.TrimLeft(value, " \t\n\v\f\r")
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg, s = s[0] == '-', s[1:]
	}
	base := 10
	switch {
	case len(s) > 2 && (s[:2] == "0x" || s[:2] == "0X") && digitIn(s[2], 16):
		base, s = 16, s[2:]
	case len(s) > 2 && (s[:2] == "0b" || s[:2] == "0B") && digitIn(s[2], 2):
		base, s = 2, s[2:]
	case s != "" && s[0] == '0':
		base = 8
	}
	end := 0
	for end < len(s) && digitIn(s[end], base) {
		end++
	}
	if end == 0 {
		return 0, false
	}
	u, err := strconv.ParseUint(s[:end], base, 64)
	if err != nil {
		return 0, false
	}
	factor := map[string]uint64{"": 1, "k": 1 << 10, "m": 1 << 20, "g": 1 << 30}
	f, known := factor[strings.ToLower(s[end:])]
	if !known || u > (1<<31)/f {
		return 0, false
	}
	u *= f
	if neg {
		return -int64(u), true
	}
	if u > 1<<31-1 {
		return 0, false
	}
	return int64(u), true
}

// digitIn reports whether c is a digit of base, 2, 8, 10 or 16.
func digitIn(c byte, base int) bool {
	switch {
	case c == '0' || c == '1':
		return true
	case c >= '2' && c <= '7':
		return base >= 8
	case c == '8' || c == '9':
		return base >= 10
	case c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F':
		return base == 16
	}
	return false
}

// urlsOf is the names of the remotes files give a url, in any of them git reads.
func urlsOf(files []configFile) map[string]bool {
	urls := map[string]bool{}
	for _, f := range files {
		if !f.read {
			continue
		}
		for _, s := range f.settings {
			if remoteURL(s.Key) {
				name, _ := remoteName(s.Key)
				urls[name] = true
			}
		}
	}
	return urls
}

// Written reports whether qory or git writes key, with any value.
func Written(key string) bool {
	if slices.Contains(written, key) {
		return true
	}
	section, rest, ok := strings.Cut(key, ".")
	if !ok {
		return false
	}
	i := strings.LastIndex(rest, ".")
	if i <= 0 {
		return false
	}
	return slices.Contains(writtenUnder[section], rest[i+1:])
}

// include reports whether key includes another file: include.path, or
// includeIf.<condition>.path.
func include(key string) bool {
	section, rest, _ := strings.Cut(key, ".")
	switch section {
	case "include":
		return rest == "path"
	case "includeif":
		return strings.HasSuffix(rest, ".path")
	}
	return false
}

// Setting is one entry of a configuration file: its key as git lists it, the section
// and the variable lower-cased and the subsection as written, and its value, nil for a
// key written without one.
type Setting struct {
	Key   string
	Value *string
}

// list reads the configuration file at path as git reads it, without following an
// include and without acting on any key: git config --file, from the root directory,
// with the system and global files off.
func list(ctx context.Context, path string) ([]Setting, error) {
	// git reads the file through a link and waits on a pipe or a terminal: anything but
	// a regular file there is one qory cannot read.
	if info, err := os.Stat(path); err == nil && !info.Mode().IsRegular() {
		return nil, errNotRegular
	}
	c := command(ctx, "/", "config", "--file", path, "--list", "--null")
	c.Env = append(c.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := c.Output()
	if err != nil {
		return nil, err
	}
	var settings []Setting
	for _, entry := range strings.Split(string(out), "\x00") {
		if entry == "" {
			continue
		}
		key, value, ok := strings.Cut(entry, "\n")
		s := Setting{Key: key}
		if ok {
			s.Value = &value
		}
		settings = append(settings, s)
	}
	return settings, nil
}

// Repo is a repository as git resolves it from a directory.
type Repo struct {
	// Worktree is the nearest directory whose .git leads to the git directory, the
	// directory itself or one of its parents, "" for a bare repository. When [Resolve]
	// returns false, it is the nearest directory holding a .git of any kind, "" for none.
	Worktree string
	// GitDir is the git directory, its real path.
	GitDir string
	// CommonDir is the common directory, its real path.
	CommonDir string
}

// files are the configuration files of the repository: the common config, and the git
// directory's config.worktree.
func (r Repo) files() (config, worktree string) {
	return filepath.Join(r.CommonDir, "config"), filepath.Join(r.GitDir, "config.worktree")
}

// Resolve finds the repository dir is in, with the flags and environment of [Command]
// and without the check: git rev-parse reads a configuration and acts on none of it. ok
// is false when dir is in no repository, or git could not say which.
func Resolve(ctx context.Context, dir string) (repo Repo, ok bool) {
	tops, _ := dotGit(dir)
	if len(tops) > 0 {
		repo.Worktree = tops[0]
	}
	for _, top := range tops {
		if irregularConfig(top) {
			return repo, false
		}
	}
	gitDir, ok := revParsePath(ctx, dir, "--absolute-git-dir")
	if !ok {
		return repo, false
	}
	common, ok := revParsePath(ctx, dir, "--git-common-dir")
	if !ok {
		return repo, false
	}
	repo.GitDir, repo.CommonDir = places.Real(gitDir), places.Real(common)
	// The work tree is the nearest directory whose .git leads to the git directory git
	// uses: git looks past a .git it cannot use, such as an empty folder. With none, git
	// uses a bare repository, which has no work tree.
	repo.Worktree = ""
	for _, top := range tops {
		if leadsTo(top, repo.GitDir) {
			repo.Worktree = top
			break
		}
	}
	if repo.Worktree != "" {
		repo.Worktree = places.Real(repo.Worktree)
	}
	return repo, true
}

// revParsePath runs git rev-parse with option, which prints one path, and returns the
// absolute path: git prints it unchanged and then one line end, so a line end in the
// path, also at its end, stays. ok is false when git fails or prints no path.
func revParsePath(ctx context.Context, dir, option string) (string, bool) {
	out, err := command(ctx, dir, "rev-parse", "--path-format=absolute", option).Output()
	if err != nil {
		return "", false
	}
	path, ok := strings.CutSuffix(string(out), "\n")
	if !ok || !filepath.IsAbs(path) {
		return "", false
	}
	return path, true
}

// irregularConfig reports whether a configuration file git reads for the repository
// whose .git lies in worktree leads to something other than a regular file, such as a
// pipe or a device, which git would wait on or read without end: config and
// config.worktree in the git directory .git is or names, and config in the common
// directory its commondir names. What qory cannot read of the way there counts as no
// such file.
func irregularConfig(worktree string) bool {
	if worktree == "" {
		return false
	}
	gitDir := gitDirOf(worktree)
	if gitDir == "" {
		return false
	}
	files := []string{filepath.Join(gitDir, "config"), filepath.Join(gitDir, "config.worktree")}
	if common := commonDirOf(gitDir); common != "" {
		files = append(files, filepath.Join(common, "config"))
	}
	for _, f := range files {
		if info, err := os.Stat(f); err == nil && !info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// gitDirOf is the git directory the .git in worktree is or names, as git 2.55 reads a
// .git file, see [gitText] and [gitPath]; "" when qory cannot read it.
func gitDirOf(worktree string) string {
	gitDir := filepath.Join(worktree, ".git")
	info, err := os.Stat(gitDir)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		return gitDir
	}
	data, err := readSmall(gitDir)
	if err != nil || len(bytes.TrimRight(data, "\r\n")) <= len("gitdir: ") {
		return ""
	}
	target, ok := strings.CutPrefix(gitText(data), "gitdir: ")
	if !ok {
		return ""
	}
	return gitPath(worktree, target)
}

// commonDirOf is the common directory the commondir of gitDir names, as git 2.55 reads
// it, see [gitText] and [gitPath]; "" when there is none or qory cannot read it.
func commonDirOf(gitDir string) string {
	data, err := readSmall(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return ""
	}
	return gitPath(gitDir, gitText(data))
}

// gitText is the text git 2.55 takes from the bytes of a .git file or a commondir: it
// strips the line ends at the end of the whole file, then reads what is left as a C
// string, up to the first NUL byte.
func gitText(data []byte) string {
	data = bytes.TrimRight(data, "\r\n")
	if i := bytes.IndexByte(data, 0); i >= 0 {
		data = data[:i]
	}
	return string(data)
}

// gitPath is the path git takes for text from a file in base: text when it is absolute,
// otherwise text appended to base unchanged, as its real path when it resolves. The real
// path counts a link before each .. of the text, which filepath.Join would remove first.
func gitPath(base, text string) string {
	full := text
	if !filepath.IsAbs(full) {
		full = base + string(filepath.Separator) + text
	}
	if real, err := filepath.EvalSymlinks(full); err == nil {
		return real
	}
	return filepath.Clean(full)
}

// maxGitFile is the most qory reads of a .git file or a commondir: git 2.55 exits on a
// .git file over 1 MiB, and reads a commondir whole.
const maxGitFile = 1 << 20

// errTooLarge is a file over [maxGitFile].
var errTooLarge = errors.New("larger than qory reads")

// readSmall reads the regular file at path, through a link, whole, or errTooLarge when
// it is over [maxGitFile]: never waiting on a pipe, and never becoming the terminal of
// the process.
func readSmall(path string) ([]byte, error) {
	if info, err := os.Stat(path); err != nil {
		return nil, err
	} else if !info.Mode().IsRegular() {
		return nil, errNotRegular
	}
	f, err := os.OpenFile(path, os.O_RDONLY|noWaitFlags, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, errNotRegular
	}
	data, err := io.ReadAll(io.LimitReader(f, maxGitFile+1))
	if err == nil && len(data) > maxGitFile {
		return nil, errTooLarge
	}
	return data, err
}

// dotGit walks from the real path of dir up to the root, the way git looks for a
// repository, and returns the directories that hold a .git, of any kind, and the .git
// entries it looked at, each nearest first. git looks past a .git that is not there, such
// as a link that leads nowhere, and a .git folder that is no git directory, such as an
// empty one, to the next one up, so dotGit walks up to the root. The table in
// discovery_test.go lists each step of git's search and what the check watches of it.
func dotGit(dir string) (tops, seen []string) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, nil
	}
	for d := places.Real(abs); ; d = filepath.Dir(d) {
		entry := filepath.Join(d, ".git")
		seen = append(seen, entry)
		if _, err := os.Lstat(entry); err == nil {
			tops = append(tops, d)
		}
		if d == filepath.Dir(d) {
			return tops, seen
		}
	}
}

// Fingerprint takes the fingerprint of repo's settings: the hash of each setting of its
// common config and its config.worktree, sorted.
func Fingerprint(ctx context.Context, repo Repo) (places.Repository, error) {
	if err := Ready(ctx); err != nil {
		return places.Repository{Worktree: repo.Worktree, GitDir: repo.GitDir, CommonDir: repo.CommonDir, Settings: []string{}}, err
	}
	files, err := readConfigs(ctx, nil, repo, nil, true)
	if err != nil {
		return places.Repository{Worktree: repo.Worktree, GitDir: repo.GitDir, CommonDir: repo.CommonDir, Settings: []string{}}, err
	}
	return fingerprintOf(repo, files), nil
}

// fingerprintOf is the fingerprint of repo whose configuration files read as files.
func fingerprintOf(repo Repo, files []configFile) places.Repository {
	fp := places.Repository{Worktree: repo.Worktree, GitDir: repo.GitDir, CommonDir: repo.CommonDir, Settings: []string{}}
	config, _ := repo.files()
	for _, f := range files {
		to := &fp.WorktreeSettings
		if f.path == config {
			to = &fp.Settings
		}
		for _, s := range f.settings {
			*to = append(*to, places.Setting(s.Key, s.Value))
		}
		sort.Strings(*to)
	}
	fp.Objects = map[string]string{}
	for _, f := range objectFiles {
		fp.Objects[f] = sum(filepath.Join(repo.CommonDir, filepath.FromSlash(f)))
	}
	fp.ObjectLinks, _, _ = objectLinks(repo.CommonDir)
	fp.RefLinks, _, _ = refLinks(repo.CommonDir)
	return fp
}

// dir is the directory a refusal names for the repository: its work tree, or its git
// directory without one.
func (r Repo) dir() string {
	if r.Worktree != "" {
		return r.Worktree
	}
	return r.GitDir
}

// entry is the fingerprint the records hold for repo: the one recorded for its work
// tree, else the one for its common directory, nil for none.
func entry(records []places.Record, repo Repo) *places.Repository {
	for _, r := range records {
		for i, e := range r.Git {
			if repo.Worktree != "" && e.Worktree == repo.Worktree {
				return &r.Git[i]
			}
		}
	}
	for _, r := range records {
		for i, e := range r.Git {
			if e.CommonDir == repo.CommonDir {
				return &r.Git[i]
			}
		}
	}
	return nil
}

// Change is a reason the check would refuse a repository today.
type Change struct {
	// Keys are the keys of the settings the fingerprint does not hold, in the order git
	// lists them, each once, but for Includes.
	Keys []string
	// URLs are the values of each remote's url among Keys the fingerprint does not hold,
	// in the order git lists them.
	URLs map[string][]string
	// Includes are the includes the check refuses whatever the fingerprint holds: one of
	// a file in a recorded place, or by a relative path, which no accept takes. In the
	// order git lists them, each once.
	Includes []string
	// Moved is set when the repository's .git points somewhere else than when its
	// fingerprint was taken.
	Moved bool
	// GitDir is the git directory the .git points to now.
	GitDir string
	// Fingerprint is the fingerprint of the repository as these changes were read from
	// it, the one an accept of them takes.
	Fingerprint places.Repository
}

// Changes compares repo with the fingerprint records holds for it: the keys the check
// would refuse and whether its .git moved. A repository with no fingerprint has every key
// qory and git do not write as a change.
func Changes(ctx context.Context, records []places.Record, repo Repo) (Change, error) {
	if err := Ready(ctx); err != nil {
		return Change{}, err
	}
	ch := Change{GitDir: repo.GitDir, URLs: map[string][]string{}}
	fp := entry(records, repo)
	if fp != nil && repo.Worktree != "" && fp.Worktree == repo.Worktree && (fp.GitDir != repo.GitDir || fp.CommonDir != repo.CommonDir) {
		ch.Moved = true
	}
	files, err := readConfigs(ctx, records, repo, fp, true)
	if err != nil {
		return ch, err
	}
	ch.Fingerprint = fingerprintOf(repo, files)
	urls := urlsOf(files)
	for _, f := range files {
		if !f.checked {
			continue
		}
		hashes := f.held
		if ch.Moved {
			hashes = nil
		}
		for _, s := range f.settings {
			// A remote with no url is no setting to accept: the check refuses it until it
			// is removed.
			if noURL(s.Key, urls) {
				return ch, &RefusedError{Dir: repo.dir(), Key: s.Key, Reason: RefusedRemoteNoURL}
			}
			if accepted(records, s, hashes) {
				continue
			}
			if remoteURL(s.Key) && s.Value != nil {
				ch.URLs[s.Key] = append(ch.URLs[s.Key], *s.Value)
			}
			if include(s.Key) && (s.Value == nil || !outside(records, *s.Value)) {
				if !slices.Contains(ch.Includes, s.Key) {
					ch.Includes = append(ch.Includes, s.Key)
				}
				continue
			}
			if !slices.Contains(ch.Keys, s.Key) {
				ch.Keys = append(ch.Keys, s.Key)
			}
		}
	}
	// A remote's file, or a file that leads git to other objects, is no setting to
	// accept: the check refuses it until it is removed.
	if err := remoteFilesRefused(records, repo, fp, repo.dir()); err != nil {
		return ch, err
	}
	if err := objectLinksRefused(records, repo, fp, repo.dir()); err != nil {
		return ch, err
	}
	if err := refLinksRefused(records, repo, fp, repo.dir()); err != nil {
		return ch, err
	}
	if err := objectFilesRefused(records, repo, fp, repo.dir()); err != nil {
		return ch, err
	}
	return ch, nil
}

// accepted reports whether the check accepts setting s against the fingerprint hashes:
// an include only when the fingerprint holds it and it names a file outside every
// recorded place by an absolute path or one under ~/; any other key when qory or git
// writes it, or when the fingerprint holds it.
func accepted(records []places.Record, s Setting, hashes []string) bool {
	held := places.Holds(hashes, places.Setting(s.Key, s.Value))
	if include(s.Key) {
		return held && s.Value != nil && outside(records, *s.Value)
	}
	return Written(s.Key) || held
}

// outside reports whether an include's path is absolute, or under ~/, and names a file
// outside every recorded place.
func outside(records []places.Record, path string) bool {
	switch {
	case strings.HasPrefix(path, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		path = filepath.Join(home, path[2:])
	case !filepath.IsAbs(path):
		return false
	}
	for _, r := range records {
		if places.Within(r.Path, path) {
			return false
		}
	}
	return true
}

// state is what the check keeps for the life of the process: the records as last read,
// the repositories it last found in order, by directory, and for each the paths whose
// places the check held before it read them, see [fullCheck].
var state struct {
	sync.Mutex
	dir      string
	stamp    string
	records  []places.Record
	verified map[string]map[string]string
	gitDirs  map[string][]string
}

// records returns the records of the places, read again when the directory that holds
// them changed since the last read. No state directory is a [RefusedNoRecords]: with no
// record to read, qory cannot tell a place a walled run used from any other.
func records() ([]places.Record, error) {
	dir, err := places.Dir()
	if err != nil {
		return nil, &RefusedError{Reason: RefusedNoRecords, Err: err}
	}
	stamp := ""
	if info, err := os.Stat(dir); err == nil {
		stamp = fmt.Sprint(info.ModTime().UnixNano(), info.Size())
	}
	if dir == state.dir && stamp == state.stamp && state.verified != nil {
		return state.records, nil
	}
	recs, err := places.Load()
	if err != nil {
		return nil, err
	}
	state.dir, state.stamp, state.records = dir, stamp, recs
	state.verified = map[string]map[string]string{}
	state.gitDirs = map[string][]string{}
	return recs, nil
}

// Forget drops what the check keeps between commands, so the next one reads every
// record and every file again.
func Forget() {
	places.Forget()
	state.Lock()
	defer state.Unlock()
	state.dir, state.stamp, state.records, state.verified, state.gitDirs = "", "", nil, nil, nil
}

// check is the settings check of the repository dir is in. With no place recorded it
// does nothing. dir, or its repository's git directory or common directory, in a place a
// walled run uses is refused, every time: the run could change the configuration
// between the check and git's read. A directory checked earlier in the process whose
// files are what they were is not checked again. The places checked stay held in the
// returned hold, which the caller releases once git has run; on an error nothing stays
// held.
func check(ctx context.Context, dir string) (*places.Hold, error) {
	state.Lock()
	defer state.Unlock()
	recs, err := records()
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	u := &using{ctx: ctx, h: &places.Hold{}, recs: recs, dir: abs, held: map[string]bool{}}
	// The places of the files the sums were taken of are held before the files are read
	// again: the git directory and the common directory may lie in another place than
	// dir.
	if err := u.hold(abs); err != nil {
		u.h.Release()
		return nil, err
	}
	if sums, ok := state.verified[abs]; ok {
		if err := u.hold(state.gitDirs[abs]...); err != nil {
			u.h.Release()
			return nil, err
		}
		if same(sums) {
			return u.h, nil
		}
	}
	sums, held, err := fullCheck(u)
	if err != nil {
		u.h.Release()
		return nil, err
	}
	state.verified[abs], state.gitDirs[abs] = sums, held
	return u.h, nil
}

// using holds, in h, the recorded places of recs a check reads from, each once, and
// refuses dir when a walled run uses one.
type using struct {
	ctx  context.Context
	h    *places.Hold
	recs []places.Record
	dir  string
	// held is the paths of the places held, by their record.
	held map[string]bool
}

// hold refuses dir when any of paths lies in a recorded place that a walled run uses,
// see [places.InUse], and holds each such place.
func (u *using) hold(paths ...string) error {
	for _, p := range paths {
		for _, r := range places.Containing(u.recs, p) {
			if u.held[r.Path] {
				continue
			}
			busy, err := places.InUse(u.ctx, u.h, r.Path)
			if err := usedRefusal(u.dir, busy, err); err != nil {
				return err
			}
			u.held[r.Path] = true
		}
	}
	return nil
}

// holds reports whether every recorded place path lies in is held.
func (u *using) holds(path string) bool {
	for _, r := range places.Containing(u.recs, path) {
		if !u.held[r.Path] {
			return false
		}
	}
	return true
}

// usedRefusal is the refusal of dir for what [places.InUse] returned, busy and err: nil
// when no walled run uses the place, and [RefusedInUse], [RefusedEngineUnreachable] or
// [RefusedEntryUnreadable] otherwise.
func usedRefusal(dir string, busy bool, err error) error {
	if id, entry, unreadable, ok := places.Unreachable(err); ok {
		reason := RefusedEngineUnreachable
		if unreadable {
			reason = RefusedEntryUnreadable
		}
		return &RefusedError{Dir: dir, Key: id, Target: entry, Reason: reason}
	}
	if err != nil {
		return err
	}
	if busy {
		return &RefusedError{Dir: dir, Reason: RefusedInUse}
	}
	return nil
}

// fullCheck checks the repository u.dir is in against the records, and returns the hash
// of every file it depends on, for [same], and the paths whose places it held before it
// read them: each .git git looks at, the git directory and the common directory.
func fullCheck(u *using) (map[string]string, []string, error) {
	ctx, recs, dir := u.ctx, u.recs, u.dir
	// Each place is held before qory reads from it: the places of the .git entries up to
	// the root, then of the git directory each names, then of the common directory that
	// one's commondir names. git rev-parse then reads them under the hold.
	tops, seen := dotGit(dir)
	held := append([]string{}, seen...)
	if err := u.hold(seen...); err != nil {
		return nil, nil, err
	}
	for _, top := range tops {
		if gitDir := gitDirOf(top); gitDir != "" {
			held = append(held, gitDir)
			if err := u.hold(gitDir); err != nil {
				return nil, nil, err
			}
			if common := commonDirOf(gitDir); common != "" {
				held = append(held, common)
				if err := u.hold(common); err != nil {
					return nil, nil, err
				}
			}
		}
	}
	// git rev-parse resolves the .git as it is now. A git directory or common directory
	// it names in a place not held yet is held, and git rev-parse runs again under that
	// hold; one that moves on each run is refused as in use.
	var repo Repo
	ok := false
	for round := 0; ; round++ {
		repo, ok = Resolve(ctx, dir)
		if !ok || u.holds(repo.GitDir) && u.holds(repo.CommonDir) {
			break
		}
		if round == 2 {
			return nil, nil, &RefusedError{Dir: dir, Reason: RefusedInUse}
		}
		held = append(held, repo.GitDir, repo.CommonDir)
		if err := u.hold(repo.GitDir, repo.CommonDir); err != nil {
			return nil, nil, err
		}
	}
	if !ok {
		// No repository git can name. One whose .git lies in a recorded place may be one
		// git cannot read, and runs no git.
		if repo.Worktree != "" && len(places.Containing(recs, repo.Worktree)) > 0 {
			_, err := gitIn(envOf(ctx))
			return nil, nil, unreadable(dir, err)
		}
		return sumsOf(slices.Concat(lookedPast(seen, tops, ""), []string{dir}, bareFolders(dir, ""))), held, nil
	}
	held = append(held, repo.GitDir, repo.CommonDir)
	config, worktree := repo.files()
	// dir, the git directory and the common directory sum as their real paths: a link put
	// on the way to one of them since makes git read other files.
	watched := slices.Concat(lookedPast(seen, tops, repo.GitDir), bareFolders(dir, repo.GitDir), []string{dir, repo.GitDir, repo.CommonDir, testedGitDir + repo.GitDir, config, worktree, filepath.Join(repo.GitDir, "commondir"), filepath.Join(repo.GitDir, "gitdir")})
	fp := entry(recs, repo)
	if fp != nil && repo.Worktree != "" && fp.Worktree == repo.Worktree && (fp.GitDir != repo.GitDir || fp.CommonDir != repo.CommonDir) {
		return nil, nil, &RefusedError{Dir: dir, Reason: RefusedPointer}
	}
	files, err := readConfigs(ctx, recs, repo, fp, false)
	if err != nil {
		var r *RefusedError
		if errors.As(err, &r) {
			r.Dir = dir
		}
		return nil, nil, err
	}
	urls := urlsOf(files)
	for _, f := range files {
		if !f.checked {
			continue
		}
		for _, s := range f.settings {
			if noURL(s.Key, urls) {
				return nil, nil, &RefusedError{Dir: dir, Key: s.Key, Reason: RefusedRemoteNoURL}
			}
			if !accepted(recs, s, f.held) {
				reason := RefusedSetting
				switch {
				case remoteURL(s.Key) && fp == nil:
					reason = RefusedRemoteUnrecorded
				case remoteURL(s.Key):
					reason = RefusedRemote
				}
				return nil, nil, &RefusedError{Dir: dir, Key: s.Key, Reason: reason}
			}
		}
	}
	if err := remoteFilesRefused(recs, repo, fp, dir); err != nil {
		return nil, nil, err
	}
	if err := objectLinksRefused(recs, repo, fp, dir); err != nil {
		return nil, nil, err
	}
	if err := refLinksRefused(recs, repo, fp, dir); err != nil {
		return nil, nil, err
	}
	if err := objectFilesRefused(recs, repo, fp, dir); err != nil {
		return nil, nil, err
	}
	for _, f := range remoteFiles {
		watched = append(watched, listed+filepath.Join(repo.CommonDir, f))
	}
	watched = append(watched, linked+repo.CommonDir, refsLinked+repo.CommonDir)
	for _, f := range objectFiles {
		watched = append(watched, filepath.Join(repo.CommonDir, filepath.FromSlash(f)))
	}
	return sumsOf(watched), held, nil
}

// checked reports whether the check reads path, a configuration file of repo whose
// fingerprint in the records is fp: when path lies in a recorded place, and wherever it
// lies when the .git git found, or the git directory with its commondir, lies in a
// recorded place and does not lead where fp says it led when the place was recorded. A
// walled run can write such a .git, or a commondir, to lead git on this machine to a
// configuration outside every recorded place, one a writable bind of its wall holds, or
// any other repository. The configuration of a recorded checkout's repository outside
// the place, as it was when the place was recorded, is the person's, and is not read.
func checked(recs []places.Record, repo Repo, fp *places.Repository, path string) bool {
	if len(places.Containing(recs, path)) > 0 {
		return true
	}
	chosen := len(places.Containing(recs, repo.GitDir)) > 0
	if repo.Worktree != "" && len(places.Containing(recs, filepath.Join(repo.Worktree, ".git"))) > 0 {
		chosen = true
	}
	same := fp != nil && repo.Worktree != "" && fp.Worktree == repo.Worktree && fp.GitDir == repo.GitDir && fp.CommonDir == repo.CommonDir
	return chosen && !same
}

// remoteFilesRefused refuses a file in a folder of [remoteFiles] of repo's common
// directory when the check reads its configuration, see [checked]: a run that took a
// remote's url out of the configuration could name the remote's URL there instead. dir
// is the directory a refusal names.
func remoteFilesRefused(recs []places.Record, repo Repo, fp *places.Repository, dir string) error {
	if !checked(recs, repo, fp, filepath.Join(repo.CommonDir, "config")) {
		return nil
	}
	f, unlisted := remoteFile(repo.CommonDir)
	if unlisted != "" {
		return &RefusedError{Dir: dir, Key: unlisted, Reason: RefusedRemoteFolder}
	}
	if f != "" {
		return &RefusedError{Dir: dir, Key: f, Reason: RefusedRemoteFile}
	}
	return nil
}

// objectFiles are the files of a common directory, relative to it with forward slashes,
// that lead git to another repository's objects, or give commits other parents:
// objects/info/alternates and info/grafts.
var objectFiles = []string{"objects/info/alternates", "info/grafts"}

// objectFilesRefused refuses a file of [objectFiles] in repo's common directory when the
// check reads its configuration, see [checked], unless fp, the fingerprint the records
// hold for repo, holds it as it is now: a file there when the place was recorded, as in
// a clone made with --reference, is the person's. A fingerprint of another common
// directory, or one recorded before qory took these files, holds none. dir is the
// directory a refusal names.
func objectFilesRefused(recs []places.Record, repo Repo, fp *places.Repository, dir string) error {
	if !checked(recs, repo, fp, filepath.Join(repo.CommonDir, "config")) {
		return nil
	}
	var held map[string]string
	if fp != nil && fp.CommonDir == repo.CommonDir {
		held = fp.Objects
	}
	for _, f := range objectFiles {
		path := filepath.Join(repo.CommonDir, filepath.FromSlash(f))
		now := sum(path)
		if now == "-" {
			continue
		}
		was, ok := held[f]
		if !ok {
			return &RefusedError{Dir: dir, Key: path, Reason: RefusedObjectsFileUnrecorded}
		}
		if was == now && now != "unreadable" {
			continue
		}
		return &RefusedError{Dir: dir, Key: path, Reason: RefusedObjectsFile}
	}
	return nil
}

// objectFolders are the folders of a common directory, relative to it with forward
// slashes, among whose entries git reads its objects: the loose objects' folders and
// pack and info in objects, the packs in objects/pack, and alternates in objects/info.
// Each loose objects' folder, objects/ and two hex digits, holds objects too, and so may
// any other folder in objects: on a file system that ignores case, git reads
// objects/d9/<rest> through objects/D9. A link there, or objects itself as a link, leads
// git to another repository's objects.
var objectFolders = []string{"objects", "objects/pack", "objects/info"}

// objectLinks returns the links among the objects of the common directory dir: objects
// itself, and each entry of a folder of [objectFolders] and of every other folder in
// objects, whatever its name, by Lstat, each relative to dir with forward slashes, mapped to "link " and the
// path it leads to with its links resolved. order is their paths in the order found. A
// folder that is not there, or a file where a folder goes, adds none. unlisted is the
// path of the first folder qory cannot list, empty when qory lists every folder.
func objectLinks(dir string) (links map[string]string, order []string, unlisted string) {
	links = map[string]string{}
	add := func(rel string) {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		links[rel] = "link " + places.Real(path)
		order = append(order, rel)
	}
	if info, err := os.Lstat(filepath.Join(dir, "objects")); err == nil && info.Mode()&os.ModeSymlink != 0 {
		add("objects")
	}
	folders := append([]string{}, objectFolders...)
	for i := 0; i < len(folders); i++ {
		f := folders[i]
		folder := filepath.Join(dir, filepath.FromSlash(f))
		entries, err := os.ReadDir(folder)
		if err != nil {
			if unlisted == "" && cannotList(err) {
				unlisted = folder
			}
			continue
		}
		for _, e := range entries {
			switch {
			case e.Type()&os.ModeSymlink != 0:
				add(f + "/" + e.Name())
			case f == "objects" && e.IsDir() && e.Name() != "pack" && e.Name() != "info":
				folders = append(folders, f+"/"+e.Name())
			}
		}
	}
	return links, order, unlisted
}

// cannotList reports whether err, from listing a folder, is one other than nothing
// there or a file where the folder goes.
func cannotList(err error) bool {
	return !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR)
}

// linksSum is what [objectLinks] or [refLinks] returns, as one string, for the sums the
// cache compares.
func linksSum(links map[string]string, order []string, unlisted string) string {
	var b strings.Builder
	if unlisted != "" {
		b.WriteString("unlisted\x00" + unlisted + "\x00")
	}
	for _, rel := range order {
		b.WriteString(rel + "\x00" + links[rel] + "\x00")
	}
	return b.String()
}

// objectLinksRefused refuses a link among the objects of repo's common directory, see
// [objectLinks], when the check reads its configuration, see [checked], unless fp, the
// fingerprint the records hold for repo, holds it as the same link, leading to the same
// path: a link there when the place was recorded is the person's. A fingerprint of
// another common directory, or one recorded before qory took these links, holds none.
// dir is the directory a refusal names.
func objectLinksRefused(recs []places.Record, repo Repo, fp *places.Repository, dir string) error {
	if !checked(recs, repo, fp, filepath.Join(repo.CommonDir, "config")) {
		return nil
	}
	var held map[string]string
	if fp != nil && fp.CommonDir == repo.CommonDir {
		held = fp.ObjectLinks
	}
	links, order, unlisted := objectLinks(repo.CommonDir)
	if unlisted != "" {
		return &RefusedError{Dir: dir, Key: unlisted, Reason: RefusedUnlistedFolder}
	}
	for _, rel := range order {
		if held[rel] == links[rel] {
			continue
		}
		target, _ := strings.CutPrefix(links[rel], "link ")
		return &RefusedError{Dir: dir, Key: filepath.Join(repo.CommonDir, filepath.FromSlash(rel)), Target: target, Reason: RefusedObjectsLink}
	}
	return nil
}

// refLinks returns the links among the refs of the common directory dir, by Lstat,
// never through a link: HEAD, packed-refs, refs itself and every entry under refs, and
// in each worktree's git directory under worktrees, the folder itself, its HEAD, and
// its refs and every entry under them; worktrees itself too. Each is relative to dir
// with forward slashes, mapped to "link " and the path it leads to with its links
// resolved. order is their paths in the order found. A folder that is not there, or a
// file where a folder goes, adds none. unlisted is the path of the first folder qory
// cannot list, empty when qory lists every folder.
func refLinks(dir string) (links map[string]string, order []string, unlisted string) {
	links = map[string]string{}
	add := func(rel string) {
		links[rel] = "link " + places.Real(filepath.Join(dir, filepath.FromSlash(rel)))
		order = append(order, rel)
	}
	isLink := func(rel string) bool {
		info, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel)))
		return err == nil && info.Mode()&os.ModeSymlink != 0
	}
	// gitDir adds the links of the git directory at rel, "" for dir itself.
	gitDir := func(rel string, names ...string) {
		for _, name := range names {
			if isLink(path.Join(rel, name)) {
				add(path.Join(rel, name))
			}
		}
		refs := path.Join(rel, "refs")
		if isLink(refs) {
			add(refs)
			return
		}
		_ = filepath.WalkDir(filepath.Join(dir, filepath.FromSlash(refs)), func(p string, e fs.DirEntry, err error) error {
			if err != nil && unlisted == "" && cannotList(err) {
				unlisted = p
			}
			if err == nil && e.Type()&os.ModeSymlink != 0 {
				r, _ := filepath.Rel(dir, p)
				add(filepath.ToSlash(r))
			}
			return nil
		})
	}
	gitDir("", "HEAD", "packed-refs")
	if isLink("worktrees") {
		add("worktrees")
		return links, order, unlisted
	}
	entries, err := os.ReadDir(filepath.Join(dir, "worktrees"))
	if err != nil && unlisted == "" && cannotList(err) {
		unlisted = filepath.Join(dir, "worktrees")
	}
	for _, e := range entries {
		rel := "worktrees/" + e.Name()
		switch {
		case e.Type()&os.ModeSymlink != 0:
			add(rel)
		case e.IsDir():
			gitDir(rel, "HEAD")
		}
	}
	return links, order, unlisted
}

// refLinksRefused refuses a link among the refs of repo's common directory, see
// [refLinks], that leads out of the place it lies in, see [leadsOut], when the check
// reads its configuration, see [checked], unless fp, the fingerprint the records hold
// for repo, holds it as the same link, leading to the same path. A fingerprint of
// another common directory, or one recorded before qory took these links, holds none.
// dir is the directory a refusal names.
func refLinksRefused(recs []places.Record, repo Repo, fp *places.Repository, dir string) error {
	if !checked(recs, repo, fp, filepath.Join(repo.CommonDir, "config")) {
		return nil
	}
	var held map[string]string
	if fp != nil && fp.CommonDir == repo.CommonDir {
		held = fp.RefLinks
	}
	links, order, unlisted := refLinks(repo.CommonDir)
	if unlisted != "" {
		return &RefusedError{Dir: dir, Key: unlisted, Reason: RefusedUnlistedFolder}
	}
	for _, rel := range order {
		path := filepath.Join(repo.CommonDir, filepath.FromSlash(rel))
		if held[rel] == links[rel] || !leadsOut(recs, path) {
			continue
		}
		target, _ := strings.CutPrefix(links[rel], "link ")
		return &RefusedError{Dir: dir, Key: path, Target: target, Reason: RefusedRefLink}
	}
	return nil
}

// leadsOut reports whether the link at path leads out of the nearest place of recs it
// lies in: where it points as written, made absolute, or where it leads with its links
// resolved, lies outside that place. A link in no place leads out.
func leadsOut(recs []places.Record, path string) bool {
	in := places.Containing(recs, path)
	if len(in) == 0 {
		return true
	}
	target, err := os.Readlink(path)
	if err != nil {
		return true
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	return !places.Inside(in[0].Path, target)
}

// remoteFiles are the folders of a common directory whose files git reads for the URL
// of a remote the configuration gives none: remotes/<name> and branches/<name>.
var remoteFiles = []string{"remotes", "branches"}

// remoteFile returns the first file in a folder of [remoteFiles] of the common directory
// dir, "" for none: an empty folder, such as git init leaves, holds none. unlisted is
// the first such folder qory cannot list, one it may not read or a link or a file where
// the folder goes, "" for none: git may still open a file in it by name.
func remoteFile(dir string) (file, unlisted string) {
	for _, f := range remoteFiles {
		folder := filepath.Join(dir, f)
		entries, err := listFolder(folder)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return "", folder
		case len(entries) > 0:
			return filepath.Join(folder, entries[0].Name()), ""
		}
	}
	return "", ""
}

// errNotAFolder is a link or a file where a folder goes.
var errNotAFolder = errors.New("not a folder")

// errNotRegular is a configuration file that leads to something other than a regular
// file, such as a pipe or a device.
var errNotRegular = errors.New("not a regular file")

// listFolder lists the folder at path, never through a link: nothing there is
// [fs.ErrNotExist], and a link or a file there is errNotAFolder.
func listFolder(path string) ([]os.DirEntry, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errNotAFolder
	}
	return os.ReadDir(path)
}

// listed is the prefix of a watched path whose sum is the names in the folder, see [sum].
const listed = "ls:"

// linked is the prefix of a watched common directory whose sum is the links among its
// objects, see [linksSum].
const linked = "links:"

// refsLinked is the prefix of a watched common directory whose sum is the links among
// its refs, see [linksSum].
const refsLinked = "reflinks:"

// testedGitDir is the prefix of a watched folder git tests as a git directory as it
// looks for a repository: the git directory it uses, and each folder it tests as a bare
// repository before it, see [bareFolders]. Its sum is [usable].
const testedGitDir = "isgitdir:"

// usable is the sum of what git 2.55 reads when it tests whether dir is a git directory:
// the owner of dir, its HEAD and commondir as [sum] hashes them, the path of the common
// directory as git takes it from commondir, see [gitText] and [gitPath], and the objects
// and refs of the common directory, each by its kind, its mode and whether this process
// may search it. A link or a pipe at objects or refs, and a commondir qory cannot read
// whole, match no sum. The table in discovery_test.go lists the other sums the kept
// check needs, and the steps of git's search they cover.
func usable(dir string) string {
	b := strings.Builder{}
	fmt.Fprintf(&b, "owner %s HEAD %s commondir %s", owner(dir), sum(filepath.Join(dir, "HEAD")), sum(filepath.Join(dir, "commondir")))
	common := dir
	data, err := readSmall(filepath.Join(dir, "commondir"))
	switch {
	case err == nil:
		common = gitPath(dir, gitText(data))
		b.WriteString(" common " + common)
	case !errors.Is(err, fs.ErrNotExist):
		// A commondir qory cannot read whole: git reads it, or exits on it.
		return unmatched()
	}
	for _, name := range []string{"objects", "refs"} {
		path := filepath.Join(common, name)
		info, err := os.Lstat(path)
		switch {
		case err != nil:
			b.WriteString(" " + name + " -")
		case info.IsDir() || info.Mode().IsRegular():
			fmt.Fprintf(&b, " %s %v %t", name, info.Mode(), searchable(path))
		default:
			return unmatched()
		}
	}
	return b.String()
}

// bareFolders are the folders git tests as a bare repository as it looks for one from
// dir: the real path of dir and each folder above it, nearest first, up to the folder
// whose .git leads to gitDir or that is gitDir, without that folder. With gitDir "", or
// with no such folder, they go up to the root.
func bareFolders(dir, gitDir string) []string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil
	}
	var out []string
	for d := places.Real(abs); ; d = filepath.Dir(d) {
		if gitDir != "" && (d == gitDir || leadsTo(d, gitDir)) {
			return out
		}
		out = append(out, testedGitDir+d)
		if d == filepath.Dir(d) {
			return out
		}
	}
}

// leadsTo reports whether the .git in top leads to gitDir, the real path of a git
// directory.
func leadsTo(top, gitDir string) bool {
	to := gitDirOf(top)
	return gitDir != "" && to != "" && places.Real(to) == gitDir
}

// passed is the prefix of a watched .git that git looks past to one further up, or to
// none: its sum is "-" while nothing is there and [unmatched] otherwise, see [sum].
const passed = "passed:"

// lookedPast is seen, the .git entries [dotGit] looked at, with the prefix [passed] on
// each that git looks past: each that is there, nearer than the first .git that leads to
// gitDir, the real path of the git directory git uses, and each that is there when no
// .git of tops leads to gitDir or gitDir is "", as for a bare repository git finds
// further up. A run could fill such a .git, an empty folder for one, with a repository
// and its configuration, which git then uses; a sum of the folder's path alone would
// still match.
func lookedPast(seen, tops []string, gitDir string) []string {
	out := append([]string{}, seen...)
	for _, top := range tops {
		if leadsTo(top, gitDir) {
			break
		}
		entry := filepath.Join(top, ".git")
		if i := slices.Index(out, entry); i >= 0 {
			out[i] = passed + entry
		}
	}
	return out
}

// sumsOf hashes each file, see [sum]: its bytes, "dir" and its real path for a
// directory, "-" for nothing there.
func sumsOf(paths []string) map[string]string {
	sums := map[string]string{}
	for _, p := range paths {
		sums[p] = sum(p)
	}
	return sums
}

func sum(path string) string {
	if dir, ok := strings.CutPrefix(path, linked); ok {
		return linksSum(objectLinks(dir))
	}
	if dir, ok := strings.CutPrefix(path, refsLinked); ok {
		return linksSum(refLinks(dir))
	}
	if gitDir, ok := strings.CutPrefix(path, testedGitDir); ok {
		return usable(gitDir)
	}
	if entry, ok := strings.CutPrefix(path, passed); ok {
		if _, err := os.Lstat(entry); err != nil {
			return "-"
		}
		return unmatched()
	}
	if dir, ok := strings.CutPrefix(path, listed); ok {
		entries, err := listFolder(dir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return "-"
		case err != nil:
			// A folder qory cannot list sums apart from a missing one, so the check runs
			// again and refuses it.
			return "unlisted"
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return "ls " + strings.Join(names, "/")
	}
	info, err := os.Lstat(path)
	switch {
	case err != nil:
		return "-"
	case info.IsDir():
		return "dir " + places.Real(path)
	case !info.Mode().IsRegular():
		return unmatched()
	}
	// Opened as a regular file at path alone: never through a link put there since,
	// never waiting on a pipe, and never becoming the terminal of the process.
	f, err := os.OpenFile(path, os.O_RDONLY|openFlags, 0)
	if err != nil {
		return "unreadable"
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() || info.Size() > maxSummed {
		return unmatched()
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxSummed+1))
	switch {
	case err != nil:
		return "unreadable"
	case n > maxSummed:
		return unmatched()
	}
	return hex.EncodeToString(h.Sum(nil))
}

// maxSummed is the largest file [sum] hashes, 16 MiB, far above what git writes in a
// configuration, an alternates or a grafts file.
const maxSummed = 16 << 20

// unmatched is the sum of a file [sum] does not hash: a link, a pipe, a device, or one
// past [maxSummed]. It is new each time, so it matches no sum taken before, in the
// cache or in a record: the check reads such a file again each time, and refuses one it
// compares with a record as one the record does not hold.
func unmatched() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "unmatched " + hex.EncodeToString(b)
}

// same reports whether every file in sums still hashes as it did. It is a variable so
// that a test can tell whether the check read the files.
var same = func(sums map[string]string) bool {
	for p, s := range sums {
		if sum(p) != s {
			return false
		}
	}
	return true
}

// Refused reports whether err is a refusal of the settings check.
func Refused(err error) bool {
	var r *RefusedError
	return errors.As(err, &r)
}
