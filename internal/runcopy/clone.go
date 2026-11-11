package runcopy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/preparation"
)

// repositories is the folder, relative to the checkout, that the further repositories
// of a run are cloned under, as preparation.Repositories sets it.
const repositories = checkout.Dir + "/repositories"

// submoduleClockMargin is how far before the end of the clone [Dir.SubmoduleSetup] starts to read
// a file as the setup's: a file system's times and a container's clock can lag by that.
const submoduleClockMargin = 2 * time.Second

// checkoutLF are the options of a checkout of a copy: the person's core.autocrlf would write
// CRLF into the copy, and the git session would then commit it as a change on every line.
var checkoutLF = []string{"-c", "core.autocrlf=false", "-c", "core.eol=lf"}

// Repository is one repository of the copy and the commit it starts at.
type Repository struct {
	// Path is the repository's folder relative to the copy, with forward slashes: "" for
	// the person's repository, and .qory/repositories/<path> for a further one.
	Path string
	// Base is the commit the copy checks out: the one the person's checkout has at HEAD.
	Base string
	// index is the repository's number: 0 for the person's, and from 1 for the further
	// ones, which is in the name of its bare source and of its quarantine.
	index int
	// excludes are the person's personal ignore rules for this repository: its
	// .git/info/exclude and the global excludes file. The git session's git folder has them,
	// so those files stay out of the commit, as they do in the person's repository.
	excludes []byte
	// filtered are the files a filter changed at the checkout, such as a Git LFS file,
	// with what the checkout wrote: the git session runs no filter, so it leaves each
	// out while the copy still has what the checkout wrote.
	filtered []SetupPath
	// refs is every ref of the copy when the clone ended, as for-each-ref prints the object and the
	// name. The git session compares the refs at its start with it, for what the run made or moved.
	refs string
	// cloned is when the clone ended, less [submoduleClockMargin]: [Dir.SubmoduleSetup] reads each
	// file in a submodule with a later time as the setup's.
	cloned time.Time
	// ignorecase is whether git, in the clone, set core.ignorecase for the disk of the copy. The
	// git session's configuration has the same setting.
	ignorecase bool
}

// Clone copies the person's repository at checkout, the top of its working tree, and
// each repository under .qory/repositories in it, for a run that works on branch. It
// returns the repositories, the person's first.
//
// For each, git clone --no-local first clones the repository into a bare repository
// under the run's folder that no run binds, and fetches HEAD into it as branch. git
// clone --no-local then clones that bare repository into the copy, [Dir.Work] or the
// same path under it, and checks out branch with the person's git environment, so a
// filter such as Git LFS applies; no run has written the copy yet. --no-local copies
// the objects and links none of them, and no clone uses --shared or --reference. Each
// submodule of the person's repository whose repository is under its .git/modules is
// cloned the same way at the commit its gitlink records. The copy shows the person's origin,
// see [Dir.copySettings]. The copy has commits alone: no uncommitted, untracked or ignored
// file of the checkout. A partial clone stops here, since its missing objects have no way into
// the copy. A repository with [preparation.TargetRef] has it in the copy too, at the same commit.
func (d Dir) Clone(ctx context.Context, checkout, branch string) ([]Repository, error) {
	if err := checkBranch(ctx, branch); err != nil {
		return nil, err
	}
	checkout, err := filepath.Abs(checkout)
	if err != nil {
		return nil, fileErr(checkout, err)
	}
	if err := InRunCopies(checkout); err != nil {
		return nil, err
	}
	for _, p := range []string{d.Work(), filepath.Join(d.root, sourcesName)} {
		if _, err := os.Lstat(p); err == nil {
			return nil, fmt.Errorf(textExists, shown(p))
		}
	}
	if err := os.Mkdir(filepath.Join(d.root, sourcesName), 0o700); err != nil {
		return nil, fileErr(d.root, err)
	}
	first, err := d.cloneOne(ctx, 0, checkout, d.Work(), branch)
	if err != nil {
		return nil, err
	}
	repos := []Repository{first}
	further, err := furtherRepositories(checkout)
	if err != nil {
		return nil, err
	}
	if len(further) > 0 {
		if err := d.Exclude(preparation.ExcludeLine); err != nil {
			return nil, err
		}
	}
	for i, rel := range further {
		src := filepath.Join(checkout, filepath.FromSlash(rel))
		dest := filepath.Join(d.Work(), filepath.FromSlash(rel))
		// The commit can have a link at .qory or below it, which a clone would write through.
		if through, err := linkOnPath(d.Work(), rel); err != nil {
			return nil, fileErr(dest, err)
		} else if through {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return nil, fileErr(filepath.Dir(dest), err)
		}
		r, err := d.cloneOne(ctx, i+1, src, dest, branch)
		if err != nil {
			return nil, err
		}
		r.Path = rel
		repos = append(repos, r)
	}
	return repos, nil
}

// Exclude adds line to .git/info/exclude of the copy of a repository, so that an agent's git add
// -A in the copy leaves those paths out. Call it before any run writes the copy.
func (d Dir) Exclude(line string) error {
	file := filepath.Join(d.Work(), ".git", "info", "exclude")
	data, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fileErr(file, err)
	}
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		data = append(data, '\n')
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return fileErr(file, err)
	}
	if err := os.WriteFile(file, append(data, line+"\n"...), 0o644); err != nil {
		return fileErr(file, err)
	}
	return nil
}

// cloneOne copies the repository at src, as [Dir.Clone] describes, through the bare
// repository number i, into dest, a path that does not exist, and returns it with the
// commit it checks out, the one src has at HEAD, and its filtered files.
func (d Dir) cloneOne(ctx context.Context, i int, src, dest, branch string) (Repository, error) {
	base, err := d.cloneAt(ctx, i, src, dest, branch)
	if err != nil {
		return Repository{}, err
	}
	if err := d.cloneSubmodules(ctx, src, dest, base); err != nil {
		return Repository{}, err
	}
	cloned := time.Now().Add(-submoduleClockMargin)
	filtered, err := d.filteredFiles(ctx, i, dest)
	if err != nil {
		return Repository{}, fmt.Errorf(textNoCheckout, base, shown(src), err)
	}
	rewritten, err := d.lineEndFiles(ctx, i, dest, base)
	if err != nil {
		return Repository{}, fmt.Errorf(textNoCheckout, base, shown(src), err)
	}
	refs, err := git(ctx, dest, "for-each-ref", "--format=%(objectname) %(refname)")
	if err != nil {
		return Repository{}, fmt.Errorf(textNoCheckout, base, shown(src), err)
	}
	return Repository{
		Base: base, index: i, filtered: slices.Concat(filtered, rewritten),
		excludes: personalExcludes(ctx, src), refs: refs, cloned: cloned, ignorecase: ignoreCase(ctx, dest),
	}, nil
}

// cloneAt is [Dir.cloneOne] without the filtered files.
func (d Dir) cloneAt(ctx context.Context, i int, src, dest, branch string) (string, error) {
	// A partial clone has no objects to copy for what it left out, and no run has its promisor.
	if partialClone(ctx, src) {
		return "", fmt.Errorf(textPartialClone, shown(src))
	}
	bare := d.source(i)
	if _, err := git(ctx, d.root, "clone", "--quiet", "--bare", "--no-local", "--no-hardlinks", "--", src, bare); err != nil {
		return "", fmt.Errorf(textNoClone, shown(src), err)
	}
	// HEAD by its name, as the branch of the run: the clone copies the branches alone,
	// and a HEAD on no branch would not be among them.
	ref := "refs/heads/" + branch
	if _, err := git(ctx, bare, "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--", src, "+HEAD:"+ref); err != nil {
		return "", fmt.Errorf(textNoHead, shown(src), err)
	}
	base, err := commitOf(ctx, bare, ref)
	if err != nil {
		return "", fmt.Errorf(textNoHead, shown(src), err)
	}
	if _, err := git(ctx, d.root, "clone", "--quiet", "--no-local", "--no-hardlinks", "--no-checkout", "--no-recurse-submodules", "--branch", branch, "--", bare, dest); err != nil {
		return "", fmt.Errorf(textNoClone, shown(src), err)
	}
	if err := copyTargetVersion(ctx, src, bare, dest); err != nil {
		return "", fmt.Errorf(textNoClone, shown(src), err)
	}
	if err := d.copySettings(ctx, src, bare, dest); err != nil {
		return "", fmt.Errorf(textNoClone, shown(src), err)
	}
	if _, err := personGit(ctx, dest, lfsConfig(ctx, src), slices.Concat(checkoutLF, []string{"checkout", "--quiet", "--force", "--no-recurse-submodules", "-B", branch, base})...); err != nil {
		return "", fmt.Errorf(textNoCheckout, base, shown(src), err)
	}
	return base, nil
}

// copyTargetVersion writes [preparation.TargetRef] in dest at the commit src has there, through
// the bare repository: no branch or tag may reach that commit, so the clone leaves it out. A
// src without the ref writes nothing. --update-shallow takes the commit when a run's fetch left
// it at depth 1.
func copyTargetVersion(ctx context.Context, src, bare, dest string) error {
	want, err := git(ctx, src, "rev-parse", "--verify", "--quiet", preparation.TargetRef)
	if err != nil {
		var ge *gitError
		if errors.As(err, &ge) && ge.exit == 1 {
			return nil
		}
		return err
	}
	ref := "+" + preparation.TargetRef + ":" + preparation.TargetRef
	fetch := []string{"fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--update-shallow", "--"}
	if _, err := git(ctx, bare, slices.Concat(fetch, []string{src, ref})...); err != nil {
		return err
	}
	if _, err := git(ctx, dest, slices.Concat(fetch, []string{bare, ref})...); err != nil {
		return err
	}
	got, err := commitOf(ctx, dest, preparation.TargetRef)
	if err != nil {
		return err
	}
	if got != strings.TrimSpace(want) {
		return fmt.Errorf(textTargetVersion, preparation.TargetRef, got, strings.TrimSpace(want))
	}
	return nil
}

// partialClone reports whether the repository at src is a partial clone: git writes
// remote.<name>.promisor, and an older git extensions.partialClone.
func partialClone(ctx context.Context, src string) bool {
	if out, err := git(ctx, src, "config", "--get", "extensions.partialClone"); err == nil && strings.TrimSpace(out) != "" {
		return true
	}
	out, err := git(ctx, src, "config", "--type=bool", "--get-regexp", `^remote\..*\.promisor$`)
	if err != nil {
		return false
	}
	for line := range strings.Lines(out) {
		if strings.HasSuffix(strings.TrimSpace(line), " true") {
			return true
		}
	}
	return false
}

// copySettings makes the copy at dest show what the person's repository at src shows:
// remote.origin.url with no credentials, refs/remotes/origin/* of the person's origin in place
// of the person's local branches, and user.name and user.email of the repository's
// configuration. The objects come through the bare repository, so nothing in dest links to
// src.
func (d Dir) copySettings(ctx context.Context, src, bare, dest string) error {
	if _, err := git(ctx, bare, "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--", src, "+refs/remotes/origin/*:refs/remotes/origin/*"); err != nil {
		return err
	}
	// git clone wrote the bare repository's branches as these refs.
	refs, err := git(ctx, dest, "for-each-ref", "--format=%(refname)", "refs/remotes/origin/")
	if err != nil {
		return err
	}
	_, _ = git(ctx, dest, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	for ref := range strings.Lines(refs) {
		if _, err := git(ctx, dest, "update-ref", "--no-deref", "-d", strings.TrimSpace(ref)); err != nil {
			return err
		}
	}
	if _, err := git(ctx, dest, "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--", bare, "+refs/remotes/origin/*:refs/remotes/origin/*"); err != nil {
		return err
	}
	if head, err := git(ctx, src, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil {
		if _, err := git(ctx, dest, "symbolic-ref", "refs/remotes/origin/HEAD", strings.TrimSpace(head)); err != nil {
			return err
		}
	}
	origin, err := git(ctx, src, "config", "--local", "--get", "remote.origin.url")
	switch {
	case err == nil:
		if address := withoutCredentials(strings.TrimSpace(origin)); address != "" {
			if _, err := git(ctx, dest, "config", "remote.origin.url", address); err != nil {
				return err
			}
		} else if _, err := git(ctx, dest, "config", "--remove-section", "remote.origin"); err != nil {
			return err
		}
	case exitOf(err) == 1:
		if _, err := git(ctx, dest, "config", "--remove-section", "remote.origin"); err != nil {
			return err
		}
	default:
		return err
	}
	for _, key := range []string{"user.name", "user.email"} {
		value, err := git(ctx, src, "config", "--local", "--get", key)
		if err != nil {
			continue
		}
		if _, err := git(ctx, dest, "config", key, strings.TrimSpace(value)); err != nil {
			return err
		}
	}
	return nil
}

// withoutCredentials is the remote address raw with no password and no query, and with no
// user name for an address other than ssh. An ssh user name, as in git@host:path, is part of the
// address. It cuts the text by hand, as git reads an address: a password can have characters
// that a URL parser stops on. It is "" when the address could still have a credential in it.
func withoutCredentials(raw string) string {
	if strings.ContainsFunc(raw, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return ""
	}
	scheme, rest, isURL := strings.Cut(raw, "://")
	if !isURL {
		return localOrSCP(raw)
	}
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	authority, path := rest[:end], rest[end:]
	// An @ after the end of the authority, or a ? or # before an @, can be a password with one of
	// those characters in it.
	if strings.Contains(path, "@") {
		return ""
	}
	if cut := strings.IndexAny(path, "?#"); cut >= 0 {
		path = path[:cut]
	}
	userinfo, host := "", authority
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		userinfo, host = authority[:at], authority[at+1:]
	}
	if !plainHost(host) {
		return ""
	}
	if user, _, _ := strings.Cut(userinfo, ":"); user != "" && sshScheme(scheme) {
		host = user + "@" + host
	}
	return scheme + "://" + host + path
}

// localOrSCP is raw, an address with no ://, when it is a path, an alias, or user@host:path with
// nothing before the @ but a user name; "" for anything else with an @ in it.
func localOrSCP(raw string) string {
	user, _, hasAt := strings.Cut(raw, "@")
	if !hasAt || strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, ".") || user != "" && !strings.ContainsAny(user, ":/ ") {
		return raw
	}
	return ""
}

// sshScheme reports whether scheme, in any case, is a form of ssh that git takes.
func sshScheme(scheme string) bool {
	switch strings.ToLower(scheme) {
	case "ssh", "git+ssh", "ssh+git":
		return true
	}
	return false
}

// plainHost reports whether host is a host name or a bracketed address, with an optional port
// of digits: anything else can be the end of a password.
func plainHost(host string) bool {
	name := host
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.HasSuffix(host, "]") {
		port := host[i+1:]
		if port == "" || strings.Trim(port, "0123456789") != "" {
			return false
		}
		name = host[:i]
	}
	if strings.HasPrefix(name, "[") && strings.HasSuffix(name, "]") {
		name = strings.Trim(name, "[]")
		return name != "" && strings.Trim(name, "0123456789abcdefABCDEF:.") == ""
	}
	return !strings.ContainsAny(name, " :[]\\")
}

// lfsConfig is the configuration of a checkout of the repository at src, for Git LFS, as
// variables of git's environment: lfs.storage at its folder of LFS objects, so the checkout
// finds what the person has, and lfs.url at its endpoint. The address can have a credential in
// it, and git's command line is visible in a process list. nil for a repository with neither.
// Nothing here runs git-lfs.
func lfsConfig(ctx context.Context, src string) []string {
	var keys, values []string
	dir, err := git(ctx, src, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err == nil {
		storage := filepath.Join(strings.TrimSpace(dir), "lfs")
		if info, err := os.Lstat(storage); err == nil && info.IsDir() {
			keys, values = append(keys, "lfs.storage"), append(values, storage)
		}
	}
	if endpoint, err := personGit(ctx, src, nil, "config", "--get", "lfs.url"); err == nil && strings.TrimSpace(endpoint) != "" {
		keys, values = append(keys, "lfs.url"), append(values, strings.TrimSpace(endpoint))
	}
	if len(keys) == 0 {
		return nil
	}
	env := []string{"GIT_CONFIG_COUNT=" + strconv.Itoa(len(keys))}
	for i := range keys {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, keys[i]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, values[i]))
	}
	return env
}

// maxExcludeFile is the most bytes [personalExcludes] reads of one file.
const maxExcludeFile = 1 << 20

// personalExcludes is what the person's .git/info/exclude of the repository at src and the
// global excludes file have, nil when the lookup fails or both have nothing. A rule of the
// person's stays in effect; a file that cannot be read has no rules.
func personalExcludes(ctx context.Context, src string) []byte {
	var files []string
	if p, err := git(ctx, src, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude"); err == nil {
		files = append(files, strings.TrimSpace(p))
	}
	global, err := personGit(ctx, src, nil, "config", "--path", "--get", "core.excludesFile")
	switch {
	case err == nil:
		files = append(files, strings.TrimSpace(global))
	default:
		if home := os.Getenv("XDG_CONFIG_HOME"); home != "" {
			files = append(files, filepath.Join(home, "git", "ignore"))
		} else if home, err := os.UserHomeDir(); err == nil {
			files = append(files, filepath.Join(home, ".config", "git", "ignore"))
		}
	}
	var out []byte
	for _, p := range files {
		// O_NONBLOCK: the open of a named pipe with no writer does not wait.
		f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			continue
		}
		if info, err := f.Stat(); err == nil && info.Mode().IsRegular() {
			data, _ := io.ReadAll(io.LimitReader(f, maxExcludeFile))
			out = append(append(out, data...), '\n')
		}
		f.Close()
	}
	return out
}

// filteredFiles is each file of the copy at dest, the repository number i just checked
// out, that .gitattributes sets a filter for, with what the checkout wrote there. git
// ls-files and git check-attr read the copy before any run writes it.
func (d Dir) filteredFiles(ctx context.Context, i int, dest string) ([]SetupPath, error) {
	files, err := git(ctx, dest, "ls-files", "-z")
	if err != nil || files == "" {
		return nil, err
	}
	out, err := gitWith(ctx, dest, nil, strings.NewReader(files), "check-attr", "-z", "--stdin", "filter")
	if err != nil {
		return nil, err
	}
	var paths []string
	fields := strings.Split(out, "\x00")
	for j := 0; j+2 < len(fields); j += 3 {
		if v := fields[j+2]; v != "unspecified" && v != "unset" && cleanRel(fields[j]) {
			paths = append(paths, fields[j])
		}
	}
	if len(paths) == 0 {
		return nil, nil
	}
	return d.setupStatesAt(ctx, dest, i, paths)
}

// ignoreCase reports whether the clone at dest has core.ignorecase set to true.
func ignoreCase(ctx context.Context, dest string) bool {
	out, err := git(ctx, dest, "config", "--type=bool", "--get", "core.ignorecase")
	return err == nil && strings.TrimSpace(out) == "true"
}

// lineEndFiles is each file of the copy at dest, the repository number i just checked out at
// base, that a git add would change with no edit, with what the checkout wrote there: a CRLF blob
// with a line-end rule in .gitattributes. The git session leaves each out while the copy has
// that. git runs on a temporary index, read from the tree, before any run writes the copy.
func (d Dir) lineEndFiles(ctx context.Context, i int, dest, base string) ([]SetupPath, error) {
	temp, err := os.MkdirTemp(d.root, "index-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temp)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(temp, "index")}
	if _, err := gitWith(ctx, dest, env, nil, "read-tree", base); err != nil {
		return nil, err
	}
	options := []string{"-c", "core.autocrlf=false", "-c", "core.eol=lf", "-c", "core.safecrlf=false"}
	// update-index exits 1 when a file differs, which the next command lists.
	if _, err := gitWith(ctx, dest, env, nil, slices.Concat(options, []string{"update-index", "-q", "--refresh"})...); err != nil && exitOf(err) != 1 {
		return nil, err
	}
	out, err := gitWith(ctx, dest, env, nil, slices.Concat(options, []string{"diff-files", "--name-only", "-z", "--ignore-submodules=all"})...)
	if err != nil || out == "" {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(strings.TrimSuffix(out, "\x00"), "\x00") {
		if cleanRel(p) {
			paths = append(paths, p)
		}
	}
	return d.setupStatesAt(ctx, dest, i, paths)
}

// cloneSubmodules clones, into the copy at dest, each submodule that .gitmodules at
// commit base lists, whose repository the person's checkout at src has under its git
// folder's modules, at the commit its gitlink in base records. A submodule with no
// such repository stays an empty folder, as git leaves one that is not set up.
func (d Dir) cloneSubmodules(ctx context.Context, src, dest, base string) error {
	listed, err := git(ctx, dest, "ls-tree", "-z", "--full-tree", base, "--", ".gitmodules")
	if err != nil {
		return fmt.Errorf(textSubmodules, base, shown(src), err)
	}
	if listed == "" {
		return nil
	}
	out, err := git(ctx, dest, "config", "--blob", base+":.gitmodules", "-z", "--get-regexp", `^submodule\..*\.path$`)
	if err != nil {
		if exitOf(err) == 1 {
			return nil
		}
		return fmt.Errorf(textSubmodules, base, shown(src), err)
	}
	gitDir, err := git(ctx, src, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return fmt.Errorf(textGitInRepo, shown(src), err)
	}
	modules := filepath.Join(strings.TrimSpace(gitDir), "modules")
	for entry := range strings.SplitSeq(out, "\x00") {
		key, sub, ok := strings.Cut(entry, "\n")
		if !ok {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(key, "submodule."), ".path")
		if !cleanRel(name) || !cleanRel(sub) {
			continue
		}
		commit, ok, err := gitlinkAt(ctx, dest, base, sub)
		if err != nil {
			return fmt.Errorf(textSubmodules, base, shown(src), err)
		}
		from := filepath.Join(modules, filepath.FromSlash(name))
		if info, err := os.Lstat(from); !ok || err != nil || !info.IsDir() {
			continue
		}
		// The commit can have a link on the way, which a clone would write through.
		if through, err := linkOnPath(dest, sub); err != nil {
			return fmt.Errorf(textNoSubmodule, shown(sub), shown(src), shown(from), err)
		} else if through {
			continue
		}
		to := filepath.Join(dest, filepath.FromSlash(sub))
		if _, err := git(ctx, d.root, "clone", "--quiet", "--no-local", "--no-hardlinks", "--no-checkout", "--no-recurse-submodules", "--", from, to); err != nil {
			return fmt.Errorf(textNoSubmodule, shown(sub), shown(src), shown(from), err)
		}
		if _, err := git(ctx, to, "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--", from, commit); err != nil {
			return fmt.Errorf(textNoSubmodule, shown(sub), shown(src), shown(from), err)
		}
		args := slices.Concat(checkoutLF, []string{"checkout", "--quiet", "--force", "--detach", commit})
		if _, err := personGit(ctx, to, lfsConfig(ctx, from), args...); err != nil {
			return fmt.Errorf(textNoSubmodule, shown(sub), shown(src), shown(from), err)
		}
	}
	return nil
}

// linkOnPath reports whether a part of rel, with forward slashes, below the folder top is a
// link, part by part with lstat. It stops at the first part that does not exist.
func linkOnPath(top, rel string) (bool, error) {
	at := top
	for part := range strings.SplitSeq(rel, "/") {
		at = filepath.Join(at, part)
		info, err := os.Lstat(at)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return false, nil
		case err != nil:
			return false, err
		case info.Mode()&fs.ModeSymlink != 0:
			return true, nil
		}
	}
	return false, nil
}

// gitlinkAt is the commit the gitlink at rel in commit records, and whether there is
// one.
func gitlinkAt(ctx context.Context, dir, commit, rel string) (string, bool, error) {
	out, err := gitWith(ctx, dir, []string{"GIT_LITERAL_PATHSPECS=1"}, nil, "ls-tree", "-z", "--full-tree", commit, "--", rel)
	if err != nil {
		return "", false, err
	}
	about, name, _ := strings.Cut(strings.TrimSuffix(out, "\x00"), "\t")
	fields := strings.Fields(about)
	if len(fields) != 3 || fields[0] != "160000" || name != rel || !objectID.MatchString(fields[2]) {
		return "", false, nil
	}
	return fields[2], true, nil
}

// furtherRepositories is the path of each repository under .qory/repositories in the
// checkout, relative to the checkout with forward slashes, in order: each folder with a
// .git, below which no other is looked for. A link is not followed, and a .qory or a
// .qory/repositories that is no folder has none.
func furtherRepositories(checkout string) ([]string, error) {
	top := filepath.Join(checkout, filepath.FromSlash(repositories))
	for _, p := range []string{filepath.Dir(top), top} {
		info, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) || err == nil && !info.IsDir() {
			return nil, nil
		}
		if err != nil {
			return nil, fileErr(p, err)
		}
	}
	var out []string
	err := filepath.WalkDir(top, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() || p == top {
			return nil
		}
		if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
			rel, err := filepath.Rel(checkout, p)
			if err != nil {
				return err
			}
			out = append(out, filepath.ToSlash(rel))
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, fileErr(top, err)
	}
	return out, nil
}

// cleanRel reports whether p is a clean relative path with forward slashes that stays
// in the folder it is relative to, with no . or .. part.
func cleanRel(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || path.Clean(p) != p || strings.ContainsRune(p, 0) {
		return false
	}
	for part := range strings.SplitSeq(p, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	return true
}
