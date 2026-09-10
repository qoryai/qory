package checkout

import (
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

// Dir is the directory qory keeps in a checkout, excluded from git: the composed harness
// and its report.
const Dir = ".qory"

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
