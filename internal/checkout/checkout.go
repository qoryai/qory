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

// remoteName captures the last two segments of a remote URL, the owner and the name, with
// an optional .git suffix and trailing slash. The owner may hold no slash and no colon,
// which keeps the host out of it in an scp-like remote such as
// git@git.example.com:acme/app.git.
var remoteName = regexp.MustCompile(`([^/:]+)/([^/]+?)(?:\.git)?/?$`)

// RepoKey returns owner/name from the origin remote of the checkout at root, so
// git@git.example.com:acme/app.git and https://git.example.com/acme/app both give
// acme/app. It returns the base name of root when root has no origin remote, when git
// cannot be run, or when the remote URL carries no owner segment.
func RepoKey(root string) string {
	if url, err := git(root, "remote", "get-url", "origin"); err == nil {
		if m := remoteName.FindStringSubmatch(url); m != nil {
			return m[1] + "/" + m[2]
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
