// Package source resolves a module's source to a directory on disk and a pin.
//
// A path source is a directory as it stands, pinned by nothing: its pin is [WorkingTree],
// and [Resolved.Dirty] reports whether git sees uncommitted changes under it. A git
// source is a repository at a ref, fetched into the cache under [CacheDir], one clone per
// commit, and pinned by the commit the ref resolved to. A branch follows the remote: each
// compose asks the remote for its commit and takes it. A tag and a full commit id stay on
// the commit they resolved to. The report contains pin and dirty mark, so a reader of one
// compose sees what it ran on.
//
// [Resolve] joins a relative path onto the stack's directory and checks that the result
// is a directory, or fetches the git source and returns the module's directory inside the
// clone. A source that selects an export, a module or a stack the repository lists in the
// exports section of its qory.yaml, resolves the repository the same way and then reads
// the export's directory from that section, so the publisher's layout is the publisher's
// alone. Whether that directory contains a module is a question for
// [github.com/qoryai/qory/internal/module].
package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/stack"
)

// WorkingTree is the pin of a path source: the directory as it stands, pinned by nothing.
const WorkingTree = "working-tree"

// Resolved is a module's directory and how it is pinned.
type Resolved struct {
	// Dir is the module directory: the source path when it is absolute, else baseDir
	// joined with it; for a git source, the path inside the clone.
	Dir string
	// Pin is what the source resolved to, [WorkingTree] for a path source and the commit,
	// twelve characters of it, for a git source.
	Pin string
	// Dirty is set for a working tree with uncommitted changes under Dir.
	Dirty bool
	// Warning is set when the remote could not be reached and the source kept the commit
	// the cache holds: "<url>#<ref>: could not reach the remote; kept <pin>". It is ""
	// otherwise.
	Warning string
	// Previous is the pin the caller passed in [Options.Pin] when the source now resolves
	// to another commit, so a caller can show that a branch moved; "" when the commit is
	// the same or there was no pin.
	Previous string
}

// FetchError is a git source that could not be fetched: the remote is unreachable, the
// ref does not exist, or git is not installed. It contains git's own message. The command
// module matches it with errors.As, because it is not a mistake in an input file.
type FetchError struct {
	// Source is the git URL and ref that failed, as the stack writes them.
	Source string
	// Output is what git printed.
	Output string
	// Err is the error running git returned.
	Err error
}

// Error returns the source and quotes git.
func (e *FetchError) Error() string {
	msg := strings.TrimSpace(e.Output)
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("fetching %s: %s", e.Source, msg)
}

// Unwrap returns the error from running git.
func (e *FetchError) Unwrap() error { return e.Err }

// GoneError is a git source whose remote answered but has neither a branch nor a tag by
// the ref's name: the branch was deleted or renamed, or the tag never existed. A cached
// commit does not stand in for it, because the stack names something the remote no
// longer has.
type GoneError struct {
	// URL is the git URL as the stack writes it.
	URL string
	// Ref is the ref as the stack writes it.
	Ref string
}

// Error names the remote and the ref and says how to keep what was composed.
func (e *GoneError) Error() string {
	return fmt.Sprintf("%s has no branch or tag %s; to keep the old harness, set ref to a commit id", e.URL, e.Ref)
}

// Memo remembers what each git source resolved to during one compose, by URL and ref, so
// every module from one repository at one ref, and the base stack, lands on one commit
// and the remote is asked once. A caller makes one with [NewMemo] per compose and passes
// it in [Options.Memo]. It is safe for use by several goroutines.
type Memo struct {
	mu   sync.Mutex
	seen map[[2]string]memoEntry
}

// memoEntry is one resolution a [Memo] holds: the full commit and the warning it came
// with.
type memoEntry struct {
	commit, warning string
}

// NewMemo returns an empty [Memo].
func NewMemo() *Memo { return &Memo{seen: map[[2]string]memoEntry{}} }

// get returns what url at ref resolved to earlier in this compose. A nil memo holds
// nothing.
func (m *Memo) get(url, ref string) (memoEntry, bool) {
	if m == nil {
		return memoEntry{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.seen[[2]string{url, ref}]
	return e, ok
}

// put records what url at ref resolved to. A nil memo records nothing.
func (m *Memo) put(url, ref string, e memoEntry) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen[[2]string{url, ref}] = e
}

// Options are the choices a caller makes for resolving a git source. The zero value uses
// the cache under [CacheDir] with no pin, no update, no timeout and no memo.
type Options struct {
	// Pin is the commit this checkout was composed from last time, as the report recorded
	// it, "" for none. A tag stays on it while its clone is cached; a branch takes the
	// remote's commit whatever the pin, and keeps the pin only when the remote cannot be
	// reached. A pin that differs from the new commit comes back as [Resolved.Previous].
	Pin string
	// Update fetches the ref again, a tag and a commit id included, instead of reading
	// the pin or the cached resolution.
	Update bool
	// Cache is the directory sources are fetched to, "" for [CacheDir].
	Cache string
	// Timeout is the longest one git command may run, 0 for no limit.
	Timeout time.Duration
	// Memo is shared by every source of one compose, so one URL at one ref resolves once,
	// to one commit; nil resolves each source on its own.
	Memo *Memo
}

// Resolve turns a source into a directory and its pin. A relative path resolves against
// baseDir, which a caller passes absolute, such as [stack.Stack.Dir].
//
// A git source at a full commit id is read from the cache, and fetched only when its
// clone is not there. Any other ref is looked up on the remote: a branch takes the
// remote's current commit, fetched when its clone is not cached, and a tag stays on the
// commit cached for it, the pin's first, fetched only when nothing is cached. A ref the
// cache knows as a tag needs no network at all. A remote that cannot be reached leaves
// the cached commit in place with [Resolved.Warning] set. An update fetches every ref
// again. A git command that runs past the timeout is killed and counts as a remote that
// cannot be reached.
//
// The error for a path that is not there comes from the operating system unchanged, and a
// caller can match it with errors.Is and os.ErrNotExist. A path that is there but is not
// a directory gets an error that contains the path. A git source that cannot be fetched,
// with nothing cached to keep, returns a [*FetchError]. A remote that answers without the
// branch or tag returns a [*GoneError]. A source that selects an export the repository
// does not list, or whose repository has no qory.yaml with an exports section, gets an
// error that contains the repository and what it exports.
func Resolve(baseDir string, s stack.Source, opts Options) (Resolved, error) {
	if s.Git != "" {
		return resolveGit(s, opts)
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
	if s.Module != "" || s.Stack != "" {
		rel, err := exported(dir, dir, s)
		if err != nil {
			return Resolved{}, err
		}
		repo := dir
		dir = filepath.Join(repo, rel)
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return Resolved{}, fmt.Errorf("%s exports %s at %s, which is not a directory there", repo, exportName(s), rel)
		}
	}
	return Resolved{Dir: dir, Pin: WorkingTree, Dirty: dirty(dir)}, nil
}

// exported returns the directory of the export s names, relative to the repository at
// repo, as the repository's qory.yaml lists it. where names the repository in a message:
// the directory for a path source, <url> at <ref> for a git one.
func exported(repo, where string, s stack.Source) (string, error) {
	e, err := exports.Read(repo)
	if err != nil {
		return "", err
	}
	if e == nil {
		return "", fmt.Errorf("%s exports nothing: it has no %s with an exports section, so select its directories with path", where, exports.FileName)
	}
	var rel string
	if s.Stack != "" {
		rel, err = e.Stack(s.Stack)
	} else {
		rel, err = e.Module(s.Module)
	}
	if err != nil {
		return "", fmt.Errorf("%s %w", where, err)
	}
	return rel, nil
}

// exportName is the export a source selects, for a message: "module core" or "stack
// nextjs".
func exportName(s stack.Source) string {
	if s.Stack != "" {
		return "stack " + s.Stack
	}
	return "module " + s.Module
}

// CacheDir is where git sources are fetched to: qory/sources under the user's cache
// directory, ~/Library/Caches on macOS and $XDG_CACHE_HOME or ~/.cache on Linux. Each
// source has a directory named after its URL, which contains one clone per commit and,
// under refs/, one file per ref that contains the commit it last resolved to and whether
// the ref is a branch or a tag.
func CacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "qory", "sources"), nil
}

// resolveGit finds the commit the ref resolves to, fetches it when its clone is not
// cached, and returns the module's directory in the clone and the commit as the pin.
//
// Clones are kept per commit, never per ref, so a clone another checkout composed from
// stays as it was when the branch moves on. [commitOf] says which commit the ref is now;
// the memo answers for a URL and ref resolved earlier in the same compose.
func resolveGit(s stack.Source, opts Options) (Resolved, error) {
	cache := opts.Cache
	if cache == "" {
		var err error
		if cache, err = CacheDir(); err != nil {
			return Resolved{}, err
		}
	}
	dir := filepath.Join(cache, cacheName(s.Git))
	got, ok := opts.Memo.get(s.Git, s.Ref)
	if !ok || !cloned(filepath.Join(dir, got.commit)) {
		var err error
		if got, err = commitOf(dir, s, opts); err != nil {
			return Resolved{}, err
		}
		opts.Memo.put(s.Git, s.Ref, got)
	}
	commit := got.commit
	var err error
	clone := filepath.Join(dir, commit)
	module, sub := clone, s.Path
	if s.Module != "" || s.Stack != "" {
		if sub, err = exported(clone, s.Git+" at "+s.Ref, s); err != nil {
			return Resolved{}, err
		}
	}
	if sub != "" {
		module = filepath.Join(clone, sub)
	}
	info, err := os.Stat(module)
	if errors.Is(err, os.ErrNotExist) && s.Path == "" && sub != "" {
		return Resolved{}, fmt.Errorf("%s at %s exports %s at %s, which is not there", s.Git, s.Ref, exportName(s), sub)
	}
	if errors.Is(err, os.ErrNotExist) {
		return Resolved{}, fmt.Errorf("%s has no %s at %s", s.Git, sub, s.Ref)
	}
	if err != nil {
		return Resolved{}, err
	}
	if !info.IsDir() {
		return Resolved{}, fmt.Errorf("%s is not a directory in %s at %s", sub, s.Git, s.Ref)
	}
	// A path inside the clone may be a symlink the repository contains; the module it
	// points to must still be inside the clone.
	real, err := filepath.EvalSymlinks(module)
	if err != nil {
		return Resolved{}, err
	}
	base, err := filepath.EvalSymlinks(clone)
	if err != nil {
		return Resolved{}, err
	}
	if real != base && !strings.HasPrefix(real, base+string(filepath.Separator)) {
		return Resolved{}, fmt.Errorf("%s in %s at %s links outside the repository", sub, s.Git, s.Ref)
	}
	r := Resolved{Dir: module, Pin: commit[:12], Warning: got.warning}
	if opts.Pin != "" && !strings.HasPrefix(commit, opts.Pin) {
		r.Previous = opts.Pin
	}
	return r, nil
}

// Kinds of ref a refs file records. A refs file written before the kind was recorded
// holds the commit alone, and reads as kindUnknown.
const (
	kindUnknown = ""
	kindBranch  = "branch"
	kindTag     = "tag"
	kindCommit  = "commit"
)

// commitID matches a full commit id: forty hex characters for SHA-1, sixty-four for
// SHA-256.
var commitID = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)

// abbreviated matches what may be an abbreviated commit id: four hex characters or more,
// the fewest git accepts.
var abbreviated = regexp.MustCompile(`^[0-9a-fA-F]{4,}$`)

// commitOf returns the full commit the source's ref stands for now, with its clone in the
// cache under dir, and the warning when the remote could not be reached.
//
// A full commit id is its own answer and is fetched only when its clone is not there. An
// update fetches the ref again, whatever it is. Any other ref is looked up on the remote
// by its full name, refs/tags/<ref> and refs/heads/<ref>, or as written when it starts
// with refs/, so refs/heads/feature/main is never main; a name that is both is the tag,
// as git fetch takes it. A branch takes the remote's commit, fetched when its clone is
// not cached; when the fetch lands on another commit, someone pushed in between and the
// fetch's commit is taken. A ref the refs file records as a tag keeps the commit cached
// for it, the pin's first, then the refs file's, and is not looked up at all. A tag the
// cache did not know as one, such as a name cached as a branch before a tag of that name
// was pushed, takes the tag's commit as the remote lists it, fetched when its clone is not
// cached, and is recorded as a tag from then on.
//
// A remote that cannot be reached, or does not answer within the timeout, leaves the
// cached commit with a warning; with nothing cached the error is a [*FetchError]. A
// remote that answers without the ref is a [*GoneError], whatever the cache holds,
// except for a ref that may be an abbreviated commit id, which is read from the cache
// or fetched as it is.
func commitOf(dir string, s stack.Source, opts Options) (memoEntry, error) {
	refFile := filepath.Join(dir, "refs", cacheName(s.Ref))
	if commitID.MatchString(s.Ref) && !opts.Update {
		if id := strings.ToLower(s.Ref); cloned(filepath.Join(dir, id)) {
			return memoEntry{commit: id}, nil
		}
		return fetched(dir, refFile, s, s.Ref, kindCommit, opts.Timeout)
	}
	kind, recorded := readRef(refFile)
	if opts.Update {
		return fetched(dir, refFile, s, s.Ref, kind, opts.Timeout)
	}
	cached := pinned(dir, opts.Pin)
	if cached == "" && recorded != "" && cloned(filepath.Join(dir, recorded)) {
		cached = recorded
	}
	if kind == kindTag && cached != "" {
		return memoEntry{commit: cached}, nil
	}
	tag, branch := "refs/tags/"+s.Ref, "refs/heads/"+s.Ref
	if strings.HasPrefix(s.Ref, "refs/") {
		tag, branch = s.Ref, s.Ref
		if !strings.HasPrefix(s.Ref, "refs/tags/") {
			tag = ""
		}
	}
	names := []string{branch}
	if tag != "" {
		// An annotated tag's own line names the tag object; the line ending in ^{} names
		// the commit it points at.
		names = append(names, tag, tag+"^{}")
	}
	found, err := lsRemote(dir, s, opts.Timeout, names...)
	if err != nil {
		if cached != "" {
			return memoEntry{commit: cached, warning: fmt.Sprintf("%s#%s: could not reach the remote; kept %s", s.Git, s.Ref, cached[:12])}, nil
		}
		return memoEntry{}, err
	}
	if commit, ok := found[tag]; ok && tag != "" {
		// A tag known as one, with a clone cached, returned above. What is cached now was
		// resolved as something else, a branch of the same name say, or nothing is; the
		// tag's own commit is taken, from the cache when its clone is there.
		if peeled, ok := found[tag+"^{}"]; ok {
			commit = peeled
		}
		if !cloned(filepath.Join(dir, commit)) {
			return fetched(dir, refFile, s, tag, kindTag, opts.Timeout)
		}
		if kind != kindTag || recorded != commit {
			if err := writeRef(refFile, kindTag, commit); err != nil {
				return memoEntry{}, err
			}
		}
		return memoEntry{commit: commit}, nil
	}
	if remote, ok := found[branch]; ok {
		if !cloned(filepath.Join(dir, remote)) {
			return fetched(dir, refFile, s, branch, kindBranch, opts.Timeout)
		}
		if kind != kindBranch || recorded != remote {
			if err := writeRef(refFile, kindBranch, remote); err != nil {
				return memoEntry{}, err
			}
		}
		return memoEntry{commit: remote}, nil
	}
	if !abbreviated.MatchString(s.Ref) {
		return memoEntry{}, &GoneError{URL: s.Git, Ref: s.Ref}
	}
	if cached != "" {
		return memoEntry{commit: cached}, nil
	}
	return fetched(dir, refFile, s, s.Ref, kindUnknown, opts.Timeout)
}

// fetched fetches ref, the source's ref or its full name, records the commit it landed on
// in the refs file with kind, and returns it.
func fetched(dir, refFile string, s stack.Source, ref, kind string, timeout time.Duration) (memoEntry, error) {
	commit, err := fetch(dir, s, ref, timeout)
	if err != nil {
		return memoEntry{}, err
	}
	if err := writeRef(refFile, kind, commit); err != nil {
		return memoEntry{}, err
	}
	return memoEntry{commit: commit}, nil
}

// lsRemote asks the source's remote for the refs named, by their full names, and returns
// the ones it has, name to commit. A name the remote does not have is left out; "" is
// skipped. A remote that cannot be reached, or a git that runs past the timeout, is a
// [*FetchError].
func lsRemote(dir string, s stack.Source, timeout time.Duration, names ...string) (map[string]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	// The URL and the refs come from the stack, so they go after "--", as in [fetch].
	args := []string{"ls-remote", "--", s.Git}
	for _, n := range names {
		if n != "" && !slices.Contains(args[3:], n) {
			args = append(args, n)
		}
	}
	out, err := git(dir, timeout, args...)
	if err != nil {
		return nil, &FetchError{Source: s.Git + "#" + s.Ref, Output: string(out), Err: err}
	}
	// git matches a pattern against the end of a ref's name, so refs/heads/x/refs/heads/main
	// would answer for refs/heads/main; only an exact name counts.
	found := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		commit, name, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok && slices.Contains(names, name) {
			found[name] = commit
		}
	}
	return found, nil
}

// readRef reads a refs file: the kind of ref and the commit it last resolved to, both ""
// when there is no file. A file of one line holding the commit alone, as written before
// the kind was recorded, is kind unknown.
func readRef(path string) (kind, commit string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return kindUnknown, ""
	}
	fields := strings.Fields(string(data))
	switch len(fields) {
	case 1:
		return kindUnknown, fields[0]
	case 2:
		return fields[0], fields[1]
	}
	return kindUnknown, ""
}

// writeRef records the kind of ref and its commit in a refs file, "<kind> <commit>", or
// the commit alone for a kind unknown. It writes a temporary file beside it and renames
// it into place, so two composes writing at once leave one whole file and a reader never
// sees half of one.
func writeRef(path, kind, commit string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	line := commit + "\n"
	if kind != kindUnknown {
		line = kind + " " + line
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ref-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(line); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// pinned returns the commit of the one complete clone under dir whose id starts with pin,
// and "" for no pin, no such clone, or more than one.
func pinned(dir, pin string) string {
	if pin == "" {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var found string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), pin) && cloned(filepath.Join(dir, e.Name())) {
			if found != "" {
				return ""
			}
			found = e.Name()
		}
	}
	return found
}

// cloned reports whether a clone at dir landed whole: the fetch writes a done mark after
// the checkout, so a fetch cut short is fetched again.
func cloned(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git", "qory-done"))
	return err == nil
}

// fetch fetches ref, the source's ref or its full name, at depth one into a fresh clone
// under dir, named after the commit it resolved to, and returns that commit. It clones into a temporary
// sibling and renames it into place once the checkout is done, so a fetch that fails or
// is cut short leaves the clones already there as they were and nothing half-made behind.
// A ref that resolves to a commit already cloned is fetched, since the commit is known
// only afterwards, and the clone there is kept. The ref may be a tag, a branch or a
// commit by its full id, when the remote allows fetching a commit by id.
func fetch(dir string, s stack.Source, ref string, timeout time.Duration) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(dir, "fetch-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	// The URL and the ref come from the stack, which a repository carries, so both go
	// after "--": a ref written as an option, --upload-pack=<command> say, is then a ref
	// git cannot find and not a command git runs.
	steps := [][]string{
		{"init", "--quiet"},
		{"remote", "add", "--", "origin", s.Git},
		{"fetch", "--quiet", "--depth", "1", "origin", "--", ref},
		{"checkout", "--quiet", "--detach", "FETCH_HEAD"},
	}
	for _, args := range steps {
		if out, err := git(tmp, timeout, args...); err != nil {
			return "", &FetchError{Source: s.Git + "#" + s.Ref, Output: string(out), Err: err}
		}
	}
	out, err := git(tmp, timeout, "rev-parse", "HEAD")
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
	// Two composes may fetch the same source at once. Each renames its own temporary
	// clone into place; the second rename fails because the first landed, and that is the
	// clone to use. A clone left there without its done mark is one cut short, and it is
	// moved aside so this fetch can land.
	if _, err := os.Stat(clone); err == nil {
		if err := os.Rename(clone, tmp+"-old"); err != nil {
			return "", err
		}
		defer os.RemoveAll(tmp + "-old")
	}
	if err := os.Rename(tmp, clone); err != nil {
		if cloned(clone) {
			return commit, nil
		}
		return "", &FetchError{Source: s.Git + "#" + s.Ref, Output: err.Error(), Err: err}
	}
	return commit, nil
}

// unsafe matches every character a cache directory name may not carry.
var unsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// cacheName turns a URL or a ref into one directory name: the unsafe characters replaced
// by "_" and a short hash of the value appended, so two values that clean to the same
// text, or differ only in case on a file system that does not, keep their own directory.
func cacheName(v string) string {
	clean := strings.Trim(unsafe.ReplaceAllString(v, "_"), "_")
	sum := sha256.Sum256([]byte(v))
	return clean + "-" + hex.EncodeToString(sum[:4])
}

// git runs one git command in dir with every prompt off, the terminal's, an askpass
// program's and ssh's, so a remote that wants a password or an unknown host key fails
// instead of waiting, and returns its combined output. A command still running after the
// timeout is killed and the error says so; a timeout of zero is no limit. A
// GIT_SSH_COMMAND the environment already carries is kept.
func git(dir string, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.Background(), func() {}
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=")
	if os.Getenv("GIT_SSH_COMMAND") == "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	out, err := cmd.CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return out, fmt.Errorf("git %s ran past %s and was stopped", args[0], timeout)
	}
	return out, err
}

// dirty reports whether git sees uncommitted changes under dir, untracked files included.
// It runs git on dir alone, so changes elsewhere in the same repository do not count.
// A directory outside a git working tree, and a host without git, read as clean, because
// the dirty mark is a note in the report and not a reason to refuse a compose.
func dirty(dir string) bool {
	cmd := exec.Command("git", "status", "--porcelain", "--", ".")
	cmd.Dir = dir
	out, err := cmd.Output()
	return err == nil && len(out) > 0
}
