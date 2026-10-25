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
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/places"
	"github.com/qoryai/qory/internal/proxygit"
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
	// Commit is the full commit id a git source resolved to, 40 or 64 hexadecimal
	// characters in lower case; "" for a path source.
	Commit string
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
	// Clone is the directory of a git source's clone, its links resolved, which contains
	// Dir; "" for a path source.
	Clone string
}

// LinksOut reports whether file, a file in clone, a directory with its links resolved,
// is a link that leads outside clone. A file that does not exist does not.
func LinksOut(clone, file string) (bool, error) {
	real, err := filepath.EvalSymlinks(file)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, exports.HideError(err)
	}
	return real != clone && !strings.HasPrefix(real, clone+string(filepath.Separator)), nil
}

// FetchError is a git source that could not be fetched: the remote is unreachable, the
// ref does not exist, or git is not installed. It contains git's own message. The command
// module matches it with errors.As, because it is not a mistake in an input file.
type FetchError struct {
	// Source is the git URL and ref that failed, as the stack writes them.
	Source string
	// URL and Ref are the source's URL and ref, as the stack writes them.
	URL, Ref string
	// Output is what git printed.
	Output string
	// Err is the error running git returned.
	Err error
	// Name is the source as a text of a [Named] compose shows it; "" for a source whose
	// address shows.
	Name string
}

// Error returns the source and quotes git; for a source of a [Named] compose, the
// source's name and not git's output, which can contain the URL.
func (e *FetchError) Error() string {
	if e.Name != "" {
		return "the fetch of " + e.Name + " failed"
	}
	msg := strings.TrimSpace(e.Output)
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("fetching %s: %s", exports.URL(e.Source), exports.HideValues(exports.HideURLs(msg)))
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
	// Name is the source as a text of a [Named] compose shows it; "" for a source whose
	// address shows.
	Name string
}

// Error contains the remote, the ref and how to keep what was composed; for a named
// stack's source, the source's name in place of the URL and the ref.
func (e *GoneError) Error() string {
	if e.Name != "" {
		return "the repository of " + e.Name + " has no branch or tag by the ref it is set to"
	}
	return fmt.Sprintf("%s has no branch or tag %s; to keep the old harness, set ref to a commit id", exports.URL(e.URL), exports.Value(e.Ref, false))
}

// SchemeError is a git source that is not an https URL, in a compose that fetches
// through a run's stack proxy, see [Git]: git does not run for it.
type SchemeError struct {
	// URL is the git URL as the stack writes it.
	URL string
	// File is the file that lists the source, as a line prints it; "" when the line
	// names none.
	File string
	// Name is the source as a text of a [Named] compose shows it; "" for a source whose
	// address shows.
	Name string
}

// Error is the line [HTTPSFix] makes for the URL and the file; for a source of a
// [Named] compose, the line with the source's name in place of the URL.
func (e *SchemeError) Error() string {
	if e.Name != "" {
		return "use an https URL for " + e.Name + ": a run from a control plane fetches git sources only over https"
	}
	return HTTPSFix(e.URL, e.File)
}

// HTTPSFix is "" for a git source git fetches over https, an https URL with a host, and
// otherwise the line a run from a control plane stops with for it: use an https URL for
// it, in file when file is set, such as the https URL of the same repository when one
// follows from it, and why. The source is printed with a URL's password hidden. A local
// path or a file URL is a folder of this machine; an ssh URL or the scp form host:path,
// which git fetches over ssh, would use this machine's keys; a git:// URL would reach
// the host past the gateway; any other is not an https URL.
func HTTPSFix(src, file string) string {
	shown := exports.URL(src)
	why, https := "", ""
	if i := strings.Index(src, "://"); i >= 0 {
		rest := src[i+3:]
		switch strings.ToLower(src[:i]) {
		case "https":
			if u, err := url.Parse(src); err == nil && u.Host != "" {
				return ""
			}
			why = shown + " is not an https URL with a host."
		case "file":
			why = shown + " is a folder of this machine."
		case "ssh", "git+ssh", "ssh+git":
			why = "ssh would use this machine's keys."
			https = httpsOf(rest, "/")
		case "git":
			why = "git:// would reach the host past the gateway."
			https = httpsOf(rest, "/")
		case "http":
			why = shown + " is not an https URL."
			https = httpsOf(rest, "/")
		default:
			why = shown + " is not an https URL."
		}
	} else if strings.Contains(src, "::") {
		// git reads <transport>::<address>, such as fd::3 or ext::<command>, as a remote
		// helper's address, not as ssh's host:path.
		why = shown + " is not an https URL."
	} else if i := strings.Index(src, ":"); i > 0 && !strings.Contains(src[:i], "/") {
		// git reads host:path as ssh when no slash comes before the first colon, and any
		// other source without a scheme as a path on this machine.
		why = "ssh would use this machine's keys."
		https = httpsOf(src, ":")
	} else {
		why = shown + " is a folder of this machine."
	}
	line := "use an https URL for " + shown
	if file != "" {
		line += " in " + file
	}
	if https != "" {
		line += ", such as " + https
	}
	return line + ". A run from a control plane fetches git sources only over https, through the run's gateway; " + why
}

// httpsOf is the https URL of the repository at rest, [user@]host[:port]<sep>path, with
// no user, no port and no .git at the end; "" when rest has no host or no path, or
// either contains what a URL cannot take as it is.
func httpsOf(rest, sep string) string {
	hostPart, path, ok := strings.Cut(rest, sep)
	if !ok {
		return ""
	}
	if at := strings.LastIndex(hostPart, "@"); at >= 0 {
		hostPart = hostPart[at+1:]
	}
	if sep == "/" {
		if h, _, ok := strings.Cut(hostPart, ":"); ok {
			hostPart = h
		}
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if hostPart == "" || path == "" || strings.ContainsAny(hostPart+path, " ?#%\\") || strings.Contains(hostPart, "/") {
		return ""
	}
	return exports.URL("https://" + hostPart + "/" + path)
}

// Git is how a compose of a run from a control plane fetches its git sources: on this
// machine through the run's stack proxy, over https alone, with the git [proxygit] sets
// up, which reads no configuration of the person's or the system's and no credential of
// this machine. A source that is not an https URL is a [*SchemeError], before git runs.
type Git struct {
	// Proxy is the run's stack proxy.
	Proxy proxygit.Proxy
	// Fetched, when set, is called once per URL and ref that a resolve asked the remote
	// for, with [Options.Name] of that resolve, the full commit it resolved to and how
	// long the lookup and the fetch took.
	Fetched func(url, ref, name, commit string, took time.Duration)
	// Named, when set, is the harness this compose fetches the sources of, whose
	// addresses no text shows; see [Named].
	Named *Named
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
	// remote's commit whatever the pin, and keeps the pin only when the lookup on the
	// remote fails. A pin that differs from the new commit comes back as [Resolved.Previous].
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
	// Git, when set, fetches every git source through a run's stack proxy, see [Git]; nil
	// fetches with the person's git, as [hostgit.Command] runs it.
	Git *Git
	// Name is the source as a text of a [Named] compose shows it, such as the base of the
	// workspace's harness or the module tools, read when Git has a [Named].
	Name string
}

// Resolve turns a source into a directory and its pin. A relative path resolves against
// baseDir, which a caller passes absolute, such as [stack.Stack.Dir].
//
// A git source at a full commit id is read from the cache, and fetched only when its
// clone is not there. A ref the cache knows as a tag stays on the commit cached for it,
// the pin's first, and needs no network at all. Any other ref is looked up on the remote
// with git ls-remote: a branch takes the remote's current commit, and a tag the cache did
// not know as one takes the commit the remote lists for it, each fetched when its clone
// is not cached. When the lookup fails, because the remote cannot be reached or git runs
// past the timeout and is killed, the cached commit is kept with [Resolved.Warning] set.
// A fetch after a lookup that answered is not covered by that: when it fails or runs past
// the timeout, the resolve fails, whatever the cache holds. An update fetches every ref
// again, and fails when the fetch does.
//
// The error for a path that is not there comes from the operating system unchanged, and a
// caller can match it with errors.Is and os.ErrNotExist. A path that is there but is not
// a directory gets an error that contains the path. A git source whose lookup fails with
// nothing cached, or whose fetch fails, returns a [*FetchError]. A remote that answers
// without the branch or tag returns a [*GoneError]. A source that selects an export the repository
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
	if err := LeadsOut(dir); err != nil {
		return Resolved{}, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return Resolved{}, exports.HideError(err)
	}
	if !info.IsDir() {
		return Resolved{}, fmt.Errorf("%s is not a directory", exports.Value(dir, false))
	}
	if s.Module != "" || s.Stack != "" {
		rel, err := exported(dir, exports.Value(dir, false), s)
		if err != nil {
			return Resolved{}, err
		}
		repo := dir
		dir = filepath.Join(repo, rel)
		if err := LeadsOut(dir); err != nil {
			return Resolved{}, err
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return Resolved{}, fmt.Errorf("%s exports %s at %s, which is not a directory there", exports.Value(repo, false), exportName(s), exports.Value(rel, false))
		}
	}
	d, err := dirty(dir)
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{Dir: dir, Pin: WorkingTree, Dirty: d}, nil
}

// LinkOutError is a folder qory reads a module or a stack from that passes through a
// place a walled run has used, and leads out of it through a link.
type LinkOutError struct {
	// Folder is the folder, absolute and clean as written; Place is the place.
	Folder, Place string
}

func (e *LinkOutError) Error() string {
	return fmt.Sprintf("%s leads through a link out of %s, a place a walled run has used; qory does not read a module or a stack from it, since the run could have pointed the link elsewhere. Remove the link to go on", shownPath(e.Folder), shownPath(e.Place))
}

// LeadsOut refuses folder, a folder qory reads a module or a stack from, with a
// [*LinkOutError] when it passes through a recorded place but leads out of it through a
// link: a walled run there could have pointed the link at any folder of this machine,
// whichever stack file names or derives the folder, one in no place included. folder
// passes through a place when a folder its resolution visits, see [visited], lies in
// the place, so a path that reaches the place through a link of its own, such as /tmp on
// macOS, or through a link elsewhere that leads into it, counts.
func LeadsOut(folder string) error {
	recs, err := places.Cached()
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(folder)
	if err != nil {
		return err
	}
	seen := visited(abs)
	real := places.Real(abs)
	for _, r := range recs {
		if places.Inside(r.Path, real) {
			continue
		}
		for _, v := range seen {
			if places.Inside(r.Path, v) {
				return &LinkOutError{Folder: abs, Place: r.Path}
			}
		}
	}
	return nil
}

// maxLinks is how many links [visited] follows before it stops, as the kernel does.
const maxLinks = 255

// visited returns each folder that resolving path, absolute and clean, passes through,
// with its links resolved, in order: each folder above it as the resolution reaches it,
// and each folder a link on the way leads through. A name that is not there is taken as
// written; a loop of links, or a link qory cannot read, ends the list.
func visited(path string) []string {
	sep := string(filepath.Separator)
	var out []string
	cur := sep
	rest := strings.Split(path, sep)
	links := 0
	for len(rest) > 0 {
		c := rest[0]
		rest = rest[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
			out = append(out, cur)
			continue
		}
		next := filepath.Join(cur, c)
		info, err := os.Lstat(next)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			cur = next
			out = append(out, cur)
			continue
		}
		links++
		target, err := os.Readlink(next)
		if err != nil || links > maxLinks {
			return out
		}
		if filepath.IsAbs(target) {
			cur = sep
		}
		rest = append(strings.Split(target, sep), rest...)
	}
	return out
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

// configInClone stops a clone, base with its links resolved, whose qory.yaml, under
// either of its names, is a link that leads outside it. where is the source as a message
// prints it; hidden is set for a source of a [Named] compose, whose message shows no
// file name of the repository's folders.
func configInClone(base, where string, hidden bool) error {
	file, err := exports.File(base)
	if err != nil || file == "" {
		// A clone with both names is an error the reader of the file reports.
		return nil
	}
	out, err := LinksOut(base, file)
	switch {
	case err != nil:
		return err
	case out && hidden:
		return fmt.Errorf("the qory.yaml of the repository of %s links outside its repository", where)
	case out:
		return fmt.Errorf("%s in %s links outside the repository", filepath.Base(file), where)
	}
	return nil
}

// exportName is the export a source selects, for a message: "module core" or "stack
// nextjs".
func exportName(s stack.Source) string {
	if s.Stack != "" {
		return "stack " + accesskey.ShownName(s.Stack, false)
	}
	return "module " + accesskey.ShownName(s.Module, false)
}

// shownPath is a path as [LinkOutError] prints it: as [hostgit.Shown] prints it, or as
// [exports.Value] prints it when it contains an access key secret or a run key.
func shownPath(p string) string {
	if accesskey.ContainsSecret(p) {
		return exports.Value(p, false)
	}
	return hostgit.Shown(p)
}

// at is a git source's URL and ref as a message prints them, <url> at <ref>, each
// with a secret or a URL's password hidden.
func at(s stack.Source) string { return exports.URL(s.Git) + " at " + exports.Value(s.Ref, false) }

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
	n := named(opts)
	if n != nil {
		n.Add(s, shownAs(s, opts))
	}
	if opts.Git != nil {
		if HTTPSFix(s.Git, "") != "" {
			e := &SchemeError{URL: s.Git}
			if n != nil {
				e.Name = shownAs(s, opts)
			}
			return Resolved{}, e
		}
	}
	cache := opts.Cache
	if cache == "" {
		var err error
		if cache, err = CacheDir(); err != nil {
			return Resolved{}, err
		}
	}
	dir := filepath.Join(cache, folderName(s.Git, opts))
	got, ok := opts.Memo.get(s.Git, s.Ref)
	if !ok || !cloned(filepath.Join(dir, got.commit)) {
		var err error
		start := time.Now()
		if got, err = commitOf(dir, s, opts); err != nil {
			// The cache directory is named after the URL, so an error of a file
			// under it prints the URL's secret unless it is hidden.
			return Resolved{}, exports.HideError(namedFetch(err, s, opts))
		}
		opts.Memo.put(s.Git, s.Ref, got)
		if opts.Git != nil && opts.Git.Fetched != nil {
			opts.Git.Fetched(s.Git, s.Ref, opts.Name, got.commit, time.Since(start))
		}
	}
	commit := got.commit
	var err error
	clone := filepath.Join(dir, commit)
	module, sub := clone, s.Path
	where := at(s)
	if n != nil {
		where = shownAs(s, opts)
	}
	base, err := filepath.EvalSymlinks(clone)
	if err != nil {
		return Resolved{}, exports.HideError(err)
	}
	if s.Module != "" || s.Stack != "" {
		// The clone's qory.yaml, which lists the exports, may be a link the repository
		// contains; the file it leads to must still be inside the clone.
		if err := configInClone(base, where, n != nil); err != nil {
			return Resolved{}, err
		}
		if sub, err = exported(clone, where, s); err != nil {
			return Resolved{}, exports.HideError(err)
		}
		if n != nil {
			// The export's folder is part of the source's address: a path in it shows
			// from that folder, in the source's phrase.
			n.Add(stack.Source{Git: s.Git, Ref: s.Ref, Path: filepath.ToSlash(sub)}, where)
		}
	}
	if sub != "" {
		module = filepath.Join(clone, sub)
	}
	info, err := os.Stat(module)
	if errors.Is(err, os.ErrNotExist) && s.Path == "" && sub != "" && n != nil {
		return Resolved{}, fmt.Errorf("%s exports %s at a folder that is not there", where, exportName(s))
	}
	if errors.Is(err, os.ErrNotExist) && s.Path == "" && sub != "" {
		return Resolved{}, fmt.Errorf("%s exports %s at %s, which is not there", where, exportName(s), exports.Value(sub, false))
	}
	// The path of a source of a [Named] compose is not shown: the folder is part of the
	// source's address.
	if errors.Is(err, os.ErrNotExist) && n != nil {
		return Resolved{}, fmt.Errorf("the repository of %s has no folder at the path it is set to", where)
	}
	if errors.Is(err, os.ErrNotExist) {
		return Resolved{}, fmt.Errorf("%s has no %s at %s", exports.URL(s.Git), exports.Value(sub, false), exports.Value(s.Ref, false))
	}
	if err != nil {
		return Resolved{}, exports.HideError(err)
	}
	if !info.IsDir() && n != nil {
		return Resolved{}, fmt.Errorf("the path of %s is not a directory in its repository", where)
	}
	if !info.IsDir() {
		return Resolved{}, fmt.Errorf("%s is not a directory in %s", exports.Value(sub, false), where)
	}
	// A path inside the clone may be a symlink the repository contains; the module it
	// points to must still be inside the clone.
	real, err := filepath.EvalSymlinks(module)
	if err != nil {
		return Resolved{}, exports.HideError(err)
	}
	if real != base && !strings.HasPrefix(real, base+string(filepath.Separator)) && n != nil {
		return Resolved{}, fmt.Errorf("the path of %s links outside its repository", where)
	}
	if real != base && !strings.HasPrefix(real, base+string(filepath.Separator)) {
		return Resolved{}, fmt.Errorf("%s in %s links outside the repository", exports.Value(sub, false), where)
	}
	r := Resolved{Dir: module, Pin: commit[:12], Commit: strings.ToLower(commit), Warning: got.warning, Clone: base}
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
// A lookup that fails, the remote not reached or git past the timeout, leaves the cached
// commit with a warning; with nothing cached the error is a [*FetchError]. A fetch that
// fails after the lookup answered is a [*FetchError] too, whatever the cache holds. A
// remote that answers without the ref is a [*GoneError], whatever the cache holds,
// except for a ref of unknown kind that may be an abbreviated commit id, which is read
// from the cache or fetched as it is.
func commitOf(dir string, s stack.Source, opts Options) (memoEntry, error) {
	refFile := filepath.Join(dir, "refs", folderName(s.Ref, opts))
	if commitID.MatchString(s.Ref) && !opts.Update {
		if id := strings.ToLower(s.Ref); cloned(filepath.Join(dir, id)) {
			return memoEntry{commit: id}, nil
		}
		return fetched(dir, refFile, s, s.Ref, kindCommit, opts)
	}
	kind, recorded := readRef(refFile)
	if opts.Update {
		return fetched(dir, refFile, s, s.Ref, kind, opts)
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
	found, err := lsRemote(dir, s, opts, names...)
	if err != nil {
		if cached != "" {
			warning := fmt.Sprintf("%s#%s: could not reach the remote; kept %s", exports.URL(s.Git), exports.Value(s.Ref, false), cached[:12])
			if named(opts) != nil {
				warning = fmt.Sprintf("%s: could not reach its repository; kept %s", shownAs(s, opts), cached[:12])
			}
			return memoEntry{commit: cached, warning: warning}, nil
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
			return fetched(dir, refFile, s, tag, kindTag, opts)
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
			return fetched(dir, refFile, s, branch, kindBranch, opts)
		}
		if kind != kindBranch || recorded != remote {
			if err := writeRef(refFile, kindBranch, remote); err != nil {
				return memoEntry{}, err
			}
		}
		return memoEntry{commit: remote}, nil
	}
	// A ref the refs file knows as a branch or a tag was one, so the remote no longer
	// having it is gone, whatever the name looks like. Only a ref of unknown kind that
	// looks like hex may be an abbreviated commit.
	if !abbreviated.MatchString(s.Ref) || kind == kindBranch || kind == kindTag {
		return memoEntry{}, &GoneError{URL: s.Git, Ref: s.Ref}
	}
	if cached != "" {
		return memoEntry{commit: cached}, nil
	}
	return fetched(dir, refFile, s, s.Ref, kindUnknown, opts)
}

// fetched fetches ref, the source's ref or its full name, records the commit it landed on
// in the refs file with kind, and returns it.
func fetched(dir, refFile string, s stack.Source, ref, kind string, opts Options) (memoEntry, error) {
	commit, err := fetch(dir, s, ref, opts)
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
func lsRemote(dir string, s stack.Source, opts Options, names ...string) (map[string]string, error) {
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
	in := dir
	if opts.Git != nil {
		// Through a run's proxy, git runs in a new empty folder of dir, and
		// [git] sets dir as the ceiling, so no repository's configuration applies to the
		// lookup, not even one at dir/.git.
		tmp, err := os.MkdirTemp(dir, "ls-remote-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(tmp)
		in = tmp
	}
	out, err := git(in, opts, args...)
	if hostgit.Refused(err) {
		return nil, err
	}
	if err != nil {
		return nil, &FetchError{Source: s.Git + "#" + s.Ref, URL: s.Git, Ref: s.Ref, Output: string(out), Err: err}
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
func fetch(dir string, s stack.Source, ref string, opts Options) (string, error) {
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
		if out, err := git(tmp, opts, args...); hostgit.Refused(err) {
			return "", err
		} else if err != nil {
			return "", &FetchError{Source: s.Git + "#" + s.Ref, URL: s.Git, Ref: s.Ref, Output: string(out), Err: err}
		}
	}
	out, err := git(tmp, opts, "rev-parse", "HEAD")
	if hostgit.Refused(err) {
		return "", err
	}
	if err != nil {
		return "", &FetchError{Source: s.Git + "#" + s.Ref, URL: s.Git, Ref: s.Ref, Output: string(out), Err: err}
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
		return "", &FetchError{Source: s.Git + "#" + s.Ref, URL: s.Git, Ref: s.Ref, Output: err.Error(), Err: err}
	}
	return commit, nil
}

// namedFetch is err, an error of the resolve of s with opts, with the source's name
// set on a [*FetchError] or a [*GoneError] of a source of a [Named] compose, so its text
// shows neither the URL nor the ref nor git's output.
func namedFetch(err error, s stack.Source, opts Options) error {
	if named(opts) == nil {
		return err
	}
	var fetch *FetchError
	var gone *GoneError
	switch {
	case errors.As(err, &fetch):
		fetch.Name = shownAs(s, opts)
	case errors.As(err, &gone):
		gone.Name = shownAs(s, opts)
	}
	return err
}

// folderName is the name of the cache folder of v, a source's URL, or of its refs file,
// v its ref: [hashedName] for a source of a [Named] compose, and [cacheName]
// otherwise.
func folderName(v string, opts Options) string {
	if named(opts) != nil {
		return hashedName(v)
	}
	return cacheName(v)
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

// git runs one git command in dir and returns its combined output. A command still
// running after opts.Timeout is killed and the error says so; a timeout of zero is no
// limit.
//
// With opts.Git nil, git runs through [hostgit.Command], with every prompt off, the
// terminal's, an askpass program's and ssh's, so a remote that wants a password or an
// unknown host key fails instead of waiting. A GIT_SSH_COMMAND the environment already
// carries is kept. The settings check's refusal is a [*hostgit.RefusedError] and no
// output.
//
// With opts.Git set, git is the one [hostgit.Unchecked] finds, with the options of
// [proxygit.Hardened] and [proxygit.HTTPSOnly] and the environment [proxygit.Env]
// makes for the run's stack proxy, and with GIT_CEILING_DIRECTORIES set to dir's
// parent; its folder is one of the run's cache, which nothing but this compose writes. The output has the proxy's password hidden.
func git(dir string, opts Options, args ...string) ([]byte, error) {
	timeout := opts.Timeout
	ctx, cancel := context.Background(), func() {}
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	if opts.Git != nil {
		p := &opts.Git.Proxy
		full := append(append(append([]string{}, proxygit.Hardened...), proxygit.HTTPSOnly...), args...)
		cmd, err := hostgit.Unchecked(ctx, dir, os.Environ(), full...)
		if err != nil {
			return nil, err
		}
		// dir is a clone being fetched or, for ls-remote, a new empty folder: git looks
		// for a repository no higher than dir, so the configuration of a repository
		// above it sets no proxy, URL or header.
		cmd.Env = append(proxygit.Env(cmd.Env, p), "GIT_CEILING_DIRECTORIES="+filepath.Dir(dir))
		out, err := cmd.CombinedOutput()
		out = []byte(proxygit.Hide(string(out), p))
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return out, fmt.Errorf("git %s ran past %s and was stopped", args[0], timeout)
		}
		if err != nil {
			err = errors.New(proxygit.Hide(err.Error(), p))
		}
		return out, err
	}
	cmd, err := hostgit.Command(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=")
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
// the dirty mark is a note in the report and not a reason to refuse a compose. The error
// is the settings check's refusal of the repository, which is one.
func dirty(dir string) (bool, error) {
	cmd, err := hostgit.Command(context.Background(), dir, "status", "--porcelain", "--", ".")
	if err != nil {
		return false, err
	}
	out, err := cmd.Output()
	return err == nil && len(out) > 0, nil
}
