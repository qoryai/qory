package checkout

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Root returns the checkout root for dir: git's toplevel for dir, or dir made absolute
// when dir is outside a working tree or git is not installed. The returned path is always
// absolute. Root fails only when dir cannot be made absolute.
func Root(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	out, err := git(abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return abs, nil
	}
	return out, nil
}

// remotePath is the path part of a remote URL: what follows the host in a URL with a
// scheme, or the colon in an scp-like remote such as git@git.example.com:acme/app.git.
var remotePath = regexp.MustCompile(`^(?:[a-z+]+://[^/]*/|[^/]+:)?(.*)$`)

// RepoKey returns owner/name from the origin remote of the checkout at root, so
// git@git.example.com:acme/app.git and https://git.example.com/acme/app both give
// acme/app: the last two segments of the remote's path, without a .git suffix or a
// trailing slash. It returns the base name of root when root has no origin remote, when
// git cannot be run, or when the path has fewer than two segments, so a host is never
// taken for an owner.
func RepoKey(root string) string {
	if url, err := git(root, "remote", "get-url", "origin"); err == nil {
		path := strings.TrimSuffix(strings.TrimSuffix(remotePath.FindStringSubmatch(url)[1], "/"), ".git")
		parts := strings.Split(path, "/")
		if n := len(parts); n >= 2 && parts[n-1] != "" && parts[n-2] != "" {
			return parts[n-2] + "/" + parts[n-1]
		}
	}
	return filepath.Base(root)
}

// Origin returns the forge and the repository the origin remote of the checkout at root
// names, the labels a run carries to a receiver: the forge is the remote's host, and
// the repository its path without the leading slash and a trailing .git, so
// git@git.example.com:acme/app.git, ssh://git@git.example.com:2222/acme/app and
// https://git.example.com/acme/app all give git.example.com and acme/app. A user and a
// port are not part of the host. It returns ok false, and nothing else, when root has no
// origin remote, when git cannot be run, when the remote is a path of this machine, a
// file:// URL or a bare path, or when the host or the path is empty: a run of a
// repository that is nowhere else has no forge and no repository, and nothing invents
// one.
func Origin(root string) (forge, repository string, ok bool) {
	remote, err := git(root, "remote", "get-url", "origin")
	if err != nil {
		return "", "", false
	}
	var host, path string
	switch {
	case strings.Contains(remote, "://"):
		u, err := url.Parse(remote)
		if err != nil {
			return "", "", false
		}
		switch u.Scheme {
		case "ssh", "https", "http":
		default:
			return "", "", false
		}
		host, path = u.Hostname(), u.Path
	case scpRemote.MatchString(remote):
		m := scpRemote.FindStringSubmatch(remote)
		host, path = m[1], m[2]
	default:
		return "", "", false
	}
	path = strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/"), ".git")
	if host == "" || path == "" {
		return "", "", false
	}
	return strings.ToLower(host), path, true
}

// scpRemote is the scp-like remote, [user@]host:path, which has no scheme: the host is
// everything before the first colon, and a slash before the colon makes it a local path.
var scpRemote = regexp.MustCompile(`^(?:[^@/:]+@)?([^@/:]+):(.*)$`)

// Dir is the directory qory keeps in a checkout, excluded from git: the composed harness
// and its report.
const Dir = ".qory"

// Key names the checkout at root among every checkout on the machine, for its home under
// a directory outside it: the root's base name, so a person reads which checkout a home
// is for, and eight hex digits of the SHA-256 of the root's real path, so two checkouts
// of one name, a main checkout and a worktree of it say, get two homes. The root's
// symlinks are resolved first, so a checkout reached through two paths has one key.
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
// git is not installed. The file itself need not exist yet.
func ExcludeFile(root string) string {
	out, err := git(root, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return ""
	}
	if !filepath.IsAbs(out) {
		out = filepath.Join(root, out)
	}
	return out
}

// Worktrees returns the working trees of the repository holding root, as git lists them
// and root's own among them. They all share the repository's one [ExcludeFile], so a line
// in it hides the path it names in every one of them at once. It returns nil when root is
// outside a working tree or git is not installed.
func Worktrees(root string) []string {
	out, err := git(root, "worktree", "list", "--porcelain")
	if err != nil {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if path, ok := strings.CutPrefix(line, "worktree "); ok {
			paths = append(paths, path)
		}
	}
	return paths
}

// Restorable reports whether git checkout -- could bring path back after qory removes it,
// which is when path is tracked and unmodified. It returns "" then, and otherwise the
// reason, phrased to follow the path in a message: "is not tracked in git", "has
// uncommitted changes" or "holds files git does not track". A directory counts as
// tracked when it holds a tracked file, and as not restorable when git status reports
// anything under it, an untracked or an ignored file included, because checkout would
// not bring those back. A path outside a working tree, or a host without git, is not
// restorable.
func Restorable(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "is outside the checkout"
	}
	if _, err := git(root, "ls-files", "--error-unmatch", "--", rel); err != nil {
		return "is not tracked in git"
	}
	// --untracked-files=all overrides a status.showUntrackedFiles=no in the repository's
	// configuration, which would hide the untracked files under a tracked directory.
	out, err := git(root, "status", "--porcelain", "--ignored", "--untracked-files=all", "--", rel)
	switch {
	case err != nil:
		return "has uncommitted changes"
	case strings.Contains("\n"+out, "\n??") || strings.Contains("\n"+out, "\n!!"):
		return "holds files git does not track"
	case out != "":
		return "has uncommitted changes"
	}
	return ""
}

// git runs one git command in dir and returns its standard output with surrounding space
// trimmed. The error carries git's own message in the Stderr field of [exec.ExitError],
// and every caller in this package discards it and falls back instead, because a
// directory outside a working tree is a normal case here and not a failure.
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
