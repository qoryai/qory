// Package source resolves a layer's source to a directory on disk and a pin.
//
// A path source is a directory as it stands, pinned by nothing: its pin is [WorkingTree],
// and [Resolved.Dirty] says whether git sees uncommitted changes under it. A git source is
// a repository at a ref, fetched once into the cache under [CacheDir], one clone per
// commit, and pinned by the commit the ref resolved to. The report carries pin and dirty
// mark, so a reader of one compose knows what it ran on.
//
// [Resolve] joins a relative path onto the profile's directory and checks that the result
// is a directory, or fetches the git source and returns the layer's directory inside the
// clone. Whether that directory holds a layer is a question for
// [github.com/qoryai/qory/internal/layer].
package source

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/qoryai/qory/internal/profile"
)

// WorkingTree is the pin of a path source: the directory as it stands, pinned by nothing.
const WorkingTree = "working-tree"

// Resolved is a layer's directory and how it is pinned.
type Resolved struct {
	// Dir is the layer directory: the source path when it is absolute, else baseDir joined
	// with it; for a git source, the path inside the clone.
	Dir string
	// Pin is what the source resolved to, [WorkingTree] for a path source and the commit,
	// twelve characters of it, for a git source.
	Pin string
	// Dirty is set for a working tree with uncommitted changes under Dir.
	Dirty bool
}

// FetchError is a git source that could not be fetched: the remote is unreachable, the
// ref does not exist, or git is not installed. It carries git's own message. The command
// layer matches it with errors.As, because it is not a mistake in an input file.
type FetchError struct {
	// Source is the git URL and ref that failed, as the profile writes them.
	Source string
	// Output is what git printed.
	Output string
	// Err is the error running git returned.
	Err error
}

// Error names the source and quotes git.
func (e *FetchError) Error() string {
	msg := strings.TrimSpace(e.Output)
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("fetching %s: %s", e.Source, msg)
}

// Unwrap returns the error from running git.
func (e *FetchError) Unwrap() error { return e.Err }

// Resolve turns a source into a directory and its pin. A relative path resolves against
// baseDir, which a caller passes absolute, such as [profile.Profile.Dir]. A git source is
// read from the cache when its ref has been resolved before, and resolved again only when
// update is set, so a compose needs the network once per ref and a branch ref moves on
// request, for this compose alone: every clone is kept by commit, and a checkout composed
// from the earlier commit keeps reading it.
//
// The error for a path that is not there comes from the operating system unchanged, and a
// caller can match it with errors.Is and os.ErrNotExist. A path that is there but is not a
// directory gets an error naming the path. A git source that cannot be fetched returns a
// [*FetchError].
func Resolve(baseDir string, s profile.Source, update bool) (Resolved, error) {
	if s.Git != "" {
		return resolveGit(s, update)
	}
	dir := s.Path
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(baseDir, dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return Resolved{}, err
	}
	if !info.IsDir() {
		return Resolved{}, fmt.Errorf("%s is not a directory", dir)
	}
	return Resolved{Dir: dir, Pin: WorkingTree, Dirty: dirty(dir)}, nil
}

// CacheDir is where git sources are fetched to: qory/sources under the user's cache
// directory, ~/Library/Caches on macOS and $XDG_CACHE_HOME or ~/.cache on Linux. Each
// source has a directory named after its URL, holding one clone per commit and, under
// refs/, one file per ref naming the commit it last resolved to.
func CacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "qory", "sources"), nil
}

// resolveGit finds the commit the ref resolves to, fetches it once, and returns the layer's
// directory in the clone and the commit as the pin.
//
// Clones are kept per commit, never per ref, so a checkout composed from a branch keeps
// reading the commit it was composed from until its own compose asks for an update. The
// ref's commit is remembered in refs/<ref> beside the clones; without update, that file
// answers and no network is used.
func resolveGit(s profile.Source, update bool) (Resolved, error) {
	cache, err := CacheDir()
	if err != nil {
		return Resolved{}, err
	}
	dir := filepath.Join(cache, cacheName(s.Git))
	refFile := filepath.Join(dir, "refs", cacheName(s.Ref))
	var commit string
	if data, err := os.ReadFile(refFile); err == nil && !update {
		commit = strings.TrimSpace(string(data))
	}
	if commit == "" || !cloned(filepath.Join(dir, commit)) {
		if commit, err = fetch(dir, s); err != nil {
			return Resolved{}, err
		}
		if err := os.MkdirAll(filepath.Dir(refFile), 0o755); err != nil {
			return Resolved{}, err
		}
		if err := os.WriteFile(refFile, []byte(commit+"\n"), 0o644); err != nil {
			return Resolved{}, err
		}
	}
	clone := filepath.Join(dir, commit)
	layer := clone
	if s.Path != "" {
		layer = filepath.Join(clone, s.Path)
	}
	info, err := os.Stat(layer)
	if err != nil {
		return Resolved{}, fmt.Errorf("%s has no %s at %s", s.Git, s.Path, s.Ref)
	}
	if !info.IsDir() {
		return Resolved{}, fmt.Errorf("%s is not a directory in %s at %s", s.Path, s.Git, s.Ref)
	}
	return Resolved{Dir: layer, Pin: commit[:12]}, nil
}

// cloned reports whether a clone at dir landed whole: the fetch writes a done mark after
// the checkout, so a fetch cut short is fetched again.
func cloned(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git", "qory-done"))
	return err == nil
}

// fetch fetches the ref of the source at depth one into a fresh clone under dir, named
// after the commit it resolved to, and returns that commit. It clones into a temporary
// sibling and renames it into place once the checkout is done, so a fetch that fails or is
// cut short leaves the clones already there as they were and nothing half-made behind. A
// commit already cloned is not fetched twice. The ref may be a tag, a branch or a commit
// by its full id, when the remote allows fetching a commit by id.
func fetch(dir string, s profile.Source) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(dir, "fetch-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	steps := [][]string{
		{"init", "--quiet"},
		{"remote", "add", "origin", s.Git},
		{"fetch", "--quiet", "--depth", "1", "origin", s.Ref},
		{"checkout", "--quiet", "--detach", "FETCH_HEAD"},
	}
	for _, args := range steps {
		if out, err := git(tmp, args...); err != nil {
			return "", &FetchError{Source: s.Git + "#" + s.Ref, Output: string(out), Err: err}
		}
	}
	out, err := git(tmp, "rev-parse", "HEAD")
	if err != nil {
		return "", &FetchError{Source: s.Git + "#" + s.Ref, Output: string(out), Err: err}
	}
	commit := strings.TrimSpace(string(out))
	if err := os.WriteFile(filepath.Join(tmp, ".git", "qory-done"), nil, 0o644); err != nil {
		return "", err
	}
	clone := filepath.Join(dir, commit)
	if cloned(clone) {
		return commit, nil
	}
	if err := os.RemoveAll(clone); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, clone); err != nil {
		return "", err
	}
	return commit, nil
}

// unsafe matches every character a cache directory name may not carry.
var unsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// cacheName turns a URL or a ref into one directory name: the unsafe characters replaced
// by "_", and for a value that had any, a short hash appended so two values that clean
// to the same text do not share a directory.
func cacheName(v string) string {
	clean := strings.Trim(unsafe.ReplaceAllString(v, "_"), "_")
	if clean == v {
		return clean
	}
	sum := sha256.Sum256([]byte(v))
	return clean + "-" + hex.EncodeToString(sum[:4])
}

// git runs one git command in dir with prompts off, so a remote that wants a password or
// an unknown host key fails instead of waiting, and returns its combined output. A
// GIT_SSH_COMMAND the environment already carries is kept.
func git(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if os.Getenv("GIT_SSH_COMMAND") == "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	return cmd.CombinedOutput()
}

// dirty reports whether git sees uncommitted changes under dir, untracked files included.
// It asks git about dir alone, so changes elsewhere in the same repository do not count. A
// directory outside a git working tree, and a host without git, read as clean, because the
// dirty mark is a note in the report and not a reason to refuse a compose.
func dirty(dir string) bool {
	cmd := exec.Command("git", "status", "--porcelain", "--", ".")
	cmd.Dir = dir
	out, err := cmd.Output()
	return err == nil && len(out) > 0
}
