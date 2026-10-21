package checkout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/qoryai/qory/internal/hostgit"
)

// Root returns the checkout root for dir: git's toplevel for dir, or dir made absolute
// when dir is outside a working tree or git is not installed. The returned path is always
// absolute. Root fails when dir cannot be made absolute, with the
// [*hostgit.RefusedError] when the settings check refuses dir's repository, and with
// the error that kept the check from reading a record of the places.
func Root(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	out, err := gitPath(abs, "rev-parse", "--show-toplevel")
	if stopped(err) {
		return "", err
	}
	if err != nil {
		return abs, nil
	}
	return out, nil
}

// remotePath is the path part of a remote URL: what follows the host in a URL with a
// scheme, or the colon in an scp-like remote such as git@git.example.com:acme/app.git.
var remotePath = regexp.MustCompile(`^(?:[a-z+]+://[^/]*/|[^/]+:)?(.*)$`)

// RepoKey returns owner/name from the origin remote of the checkout at root, so
// git@git.example.com:acme/app.git and https://git.example.com/acme/app both return
// acme/app: the last two segments of the remote's path, without a .git suffix or a
// trailing slash. It returns the base name of root when root has no origin remote, when
// git cannot be run, or when the path has fewer than two segments, so a host is never
// taken for an owner. The error is the settings check's refusal of the repository, or
// the error that kept it from reading a record of the places.
func RepoKey(root string) (string, error) {
	url, err := git(root, "remote", "get-url", "origin")
	if stopped(err) {
		return "", err
	}
	if err == nil {
		path := strings.TrimSuffix(strings.TrimSuffix(remotePath.FindStringSubmatch(url)[1], "/"), ".git")
		parts := strings.Split(path, "/")
		if n := len(parts); n >= 2 && parts[n-1] != "" && parts[n-2] != "" {
			return parts[n-2] + "/" + parts[n-1], nil
		}
	}
	return filepath.Base(root), nil
}

// Origin returns the forge and the repository of the origin remote of the checkout at
// root, the labels a run sends to a receiver: the forge is the remote's host, and the
// repository its path without the leading slash and a trailing .git, so
// git@git.example.com:acme/app.git, ssh://git@git.example.com:2222/acme/app and
// https://git.example.com/acme/app all return git.example.com and acme/app. A user and a
// port are not part of the host. It returns ok false, and nothing else, when root has no
// origin remote, when git cannot be run, when the remote is a path of this machine, a
// file:// URL or a bare path, or when the host or the path is empty: a run of a
// repository that is nowhere else has no forge and no repository, and nothing invents
// one. The error is the settings check's refusal of the repository, or the error that
// kept it from reading a record of the places.
func Origin(root string) (forge, repository string, ok bool, err error) {
	remote, err := git(root, "remote", "get-url", "origin")
	if stopped(err) {
		return "", "", false, err
	}
	if err != nil {
		return "", "", false, nil
	}
	var host, path string
	switch {
	case strings.Contains(remote, "://"):
		u, err := url.Parse(remote)
		if err != nil {
			return "", "", false, nil
		}
		switch u.Scheme {
		case "ssh", "https", "http":
		default:
			return "", "", false, nil
		}
		host, path = u.Hostname(), u.Path
	case scpRemote.MatchString(remote):
		m := scpRemote.FindStringSubmatch(remote)
		host, path = m[1], m[2]
	default:
		return "", "", false, nil
	}
	path = strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/"), ".git")
	if host == "" || path == "" {
		return "", "", false, nil
	}
	return strings.ToLower(host), path, true, nil
}

// scpRemote is the scp-like remote, [user@]host:path, which has no scheme: the host is
// everything before the first colon, and a slash before the colon makes it a local path.
var scpRemote = regexp.MustCompile(`^(?:[^@/:]+@)?([^@/:]+):(.*)$`)

// Dir is the directory qory keeps in a checkout, excluded from git: the composed harness
// and its report.
const Dir = ".qory"

// Key identifies the checkout at root among every checkout on the machine, for its home
// under a directory outside it: the root's base name, so a person reads which checkout a
// home is for, and eight hex digits of the SHA-256 of the root's real path, so two
// checkouts of one name, such as a main checkout and a worktree of it, get two homes. The
// root's symlinks are resolved first, so a checkout reached through two paths has one
// key.
func Key(root string) string {
	real := root
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		real = resolved
	}
	sum := sha256.Sum256([]byte(real))
	return filepath.Base(real) + "-" + hex.EncodeToString(sum[:4])
}

// QoryDir returns the checkout's qory directory, [Dir] inside root. It joins the paths
// and does not create the directory or check that it exists.
func QoryDir(root string) string { return filepath.Join(root, Dir) }

// Init makes dir a git repository, for a directory that is inside none: the compose
// writes into a checkout only, so an example written outside one could not be composed.
func Init(dir string) error {
	_, err := git(dir, "init", "--quiet")
	return err
}

// ExcludeFile returns the absolute path of the clone-local exclude file of the checkout
// at root, info/exclude inside the git directory, which a worktree or a separate git
// directory can put outside root. It returns "" when root is outside a working tree or
// git is not installed. The file itself need not exist. The error is the settings
// check's refusal of the repository, or the error that kept it from reading a record of
// the places.
func ExcludeFile(root string) (string, error) {
	out, err := gitPath(root, "rev-parse", "--git-path", "info/exclude")
	if stopped(err) {
		return "", err
	}
	if err != nil {
		return "", nil
	}
	if !filepath.IsAbs(out) {
		out = filepath.Join(root, out)
	}
	return out, nil
}

// Worktrees returns the working trees of the repository that contains root, as git lists
// them, the one at root among them. They all share the repository's one [ExcludeFile], so
// a line in it hides the path it contains in every one of them at once. It returns nil
// when root is outside a working tree or git is not installed. The error is the settings
// check's refusal of the repository, or the error that kept it from reading a record of
// the places.
func Worktrees(root string) ([]string, error) {
	// -z ends each line with a NUL byte, so a path with a line end in it stays whole.
	out, err := git(root, "worktree", "list", "--porcelain", "-z")
	if stopped(err) {
		return nil, err
	}
	if err != nil {
		return nil, nil
	}
	var paths []string
	for _, line := range strings.Split(out, "\x00") {
		if path, ok := strings.CutPrefix(line, "worktree "); ok {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

// Restorable reports whether git checkout -- could bring path back after qory removes
// it, which is when path is tracked and unmodified. It returns "" then, and otherwise
// the reason, phrased to follow the path in a message: "is not tracked in git", "has
// uncommitted changes" or "contains files git does not track". A directory counts as
// tracked when it contains a tracked file, and as not restorable when git status reports
// anything under it, an untracked or an ignored file included, because checkout does not
// bring those back. A path outside a working tree, or a host without git, is not
// restorable. The error is the settings check's refusal of the repository, or the error
// that kept it from reading a record of the places.
func Restorable(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "is outside the checkout", nil
	}
	if _, err := git(root, "ls-files", "--error-unmatch", "--", rel); err != nil {
		if stopped(err) {
			return "", err
		}
		return "is not tracked in git", nil
	}
	// --untracked-files=all overrides a status.showUntrackedFiles=no in the repository's
	// configuration, which would hide the untracked files under a tracked directory.
	out, err := git(root, "status", "--porcelain", "--ignored", "--untracked-files=all", "--", rel)
	switch {
	case stopped(err):
		return "", err
	case err != nil:
		return "has uncommitted changes", nil
	case strings.Contains("\n"+out, "\n??") || strings.Contains("\n"+out, "\n!!"):
		return "contains files git does not track", nil
	case out != "":
		return "has uncommitted changes", nil
	}
	return "", nil
}

// Changes lists what git status reports under path, the files a git checkout -- of path
// would not bring back: modified, staged, deleted, untracked and ignored, each relative
// to the checkout root with forward slashes, in git's order. A path outside a working
// tree, or a host without git, lists none. The error is the settings check's refusal of
// the repository, or the error that kept it from reading a record of the places.
func Changes(root, path string) ([]string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, nil
	}
	cmd, err := hostgit.Command(context.Background(), root, "status", "--porcelain", "-z", "--ignored", "--untracked-files=all", "--", rel)
	if err != nil {
		return nil, err
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, nil
	}
	var paths []string
	entries := strings.Split(string(out), "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		paths = append(paths, e[3:])
		// A rename or a copy carries the path it came from as the next entry.
		if e[0] == 'R' || e[0] == 'C' {
			i++
		}
	}
	return paths, nil
}

// git runs one git command in dir through [hostgit.Command] and returns its standard
// output with surrounding space trimmed. The error carries git's message in the Stderr
// field of [os/exec.ExitError], and every caller in this package discards it and falls back
// instead, because a directory outside a working tree is a normal case here and not a
// failure; an error of the settings check, a [*hostgit.RefusedError] or one that kept it
// from reading a record of the places, is a [*checkError], which each caller returns.
func git(dir string, args ...string) (string, error) {
	cmd, err := hostgit.Command(context.Background(), dir, args...)
	if err != nil {
		return "", &checkError{err: err}
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// gitPath runs one git command in dir that prints one path, as [git] does, and returns
// the path: git prints it unchanged and then one line end, so a line end or a space in
// the path, also at its end, stays.
func gitPath(dir string, args ...string) (string, error) {
	cmd, err := hostgit.Command(context.Background(), dir, args...)
	if err != nil {
		return "", &checkError{err: err}
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

// checkError is an error of the settings check before git ran: no git ran, so it says
// nothing of whether dir is a working tree.
type checkError struct {
	err error
}

func (e *checkError) Error() string { return e.err.Error() }

func (e *checkError) Unwrap() error { return e.err }

// stopped reports whether err kept git from running, the settings check's refusal or an
// error of the check, which a caller returns rather than reading as no working tree.
func stopped(err error) bool {
	var c *checkError
	return hostgit.Refused(err) || errors.As(err, &c)
}
