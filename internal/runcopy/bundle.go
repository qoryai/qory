package runcopy

import (
	"bufio"
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
	"time"

	"github.com/qoryai/qory/internal/checkout"
)

// MaxBundle is the largest bundle qory reads back from a run.
// 1 GiB is far above an agent's change, and keeps a planted file from filling the disk.
const MaxBundle = 1 << 30

// maxBundleHeader is the largest header of a bundle, its refs and prerequisites, that
// qory reads when it checks the pack itself.
const maxBundleHeader = 1 << 20

// LeftBehind reports whether the git session of repos recorded work of the run that the bundles
// do not have. The file's content is not read: a program in the session can write it, and all
// it can do is keep the copy.
func (d Dir) LeftBehind(repos []Repository) bool {
	for i := range repos {
		if info, err := os.Lstat(filepath.Join(d.Bundle(), strconv.Itoa(i)+".left")); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// ErrNoBundle is a git session that recorded, for every repository, that the run made
// no commit past its base, so nothing comes back.
var ErrNoBundle = errors.New("no action needed: the run made no commit past its base commit")

// OpenBundle opens the bundle file at path to read: only when it is a regular file of
// at most [MaxBundle] bytes, as lstat reads it, and when the file opened, without
// following a link, is that same file by device and inode.
func OpenBundle(path string) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf(textBundleOpen, shown(path), err)
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf(textBundleKind, shown(path), kind(before.Mode()))
	}
	f, err := openNoFollow(path)
	if err != nil {
		return nil, fmt.Errorf(textBundleOpen, shown(path), err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf(textBundleOpen, shown(path), err)
	}
	if !os.SameFile(before, info) || !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf(textBundleMoved, shown(path))
	}
	if info.Size() > MaxBundle {
		f.Close()
		return nil, fmt.Errorf(textBundleSize, shown(path), int64(MaxBundle), info.Size())
	}
	return f, nil
}

// kind is the kind of file mode is, as a line prints it.
func kind(mode fs.FileMode) string {
	switch {
	case mode.IsDir():
		return "folder"
	case mode&fs.ModeSymlink != 0:
		return "link"
	case mode&fs.ModeNamedPipe != 0:
		return "named pipe"
	case mode&fs.ModeSocket != 0:
		return "socket"
	case mode&fs.ModeDevice != 0:
		return "device"
	}
	return "file of another kind"
}

// Quarantine is the bare repository that one repository's bundle is fetched into, with
// the bare source of that repository as its alternates, and the quarantines of its
// submodules.
type Quarantine struct {
	// Path is the quarantine repository, "" when the session recorded nothing past the base
	// commit there and only submodules have refs of the run.
	Path string
	// Repo is the repository of the copy, as [Dir.Clone] returned it. For a submodule, Path is
	// the submodule's folder in the copy and Base the commit the clone checked out there.
	Repo Repository
	// Branch is the run's branch, which the fetch wrote in the quarantine when Head is set.
	Branch string
	// Head is the branch's commit, "" when the bundle has no commit on the branch.
	Head string
	// Refs are the run's other refs in the quarantine, each a full name under the run's
	// [Dir.RefPrefix], sorted.
	Refs []string
	// Submodules are the quarantines of the repository's submodules that have refs of the run.
	Submodules []*Quarantine
	// prefix is the run's [Dir.RefPrefix].
	prefix string
	// commits are the commits of Refs, in the same order.
	commits []string
	// gitDir is the person's git folder of a submodule, where its refs come back; "" for a
	// repository.
	gitDir string
}

// Quarantine checks the bundle the git session wrote for each repository of repos, as
// [Dir.Clone] returned them, copies it into the run's folder, and fetches branch and the
// run's refs under [Dir.RefPrefix] from it into a new bare repository whose alternates are
// the bare source of that repository, which no run wrote. It does the same for the bundle of
// each submodule that the clone copied, with the person's git folder of that submodule as the
// alternates, and fetches only refs under the prefix from it. Call it once the run's containers
// are gone; ctx sets how long it may take. It returns a quarantine per repository, nil for one
// whose git session recorded no commit past its base there or in a submodule, and
// [ErrNoBundle] when that is true for all of them.
//
// A bundle's header may have only branch and refs under the prefix of the forms that
// [Dir.RefPrefix] lists, each once in any letter case, and a submodule's only the latter. The
// fetch checks every object it receives: by transfer.fsckObjects from git 2.48 on, and by git
// index-pack --strict on an older git, whose fetch from a bundle checks none. Each ref must
// reach a commit. A commit of a ref that adds an entry at the top of the repository named .git
// or .qory, in any letter case, and for stash the commits of the index and of the untracked
// files too, keeps the quarantine from its caller. A second call makes the quarantines again.
func (d Dir) Quarantine(ctx context.Context, branch string, repos []Repository) ([]*Quarantine, error) {
	if err := checkBranch(ctx, branch); err != nil {
		return nil, err
	}
	top := filepath.Join(d.root, quarantineName)
	if err := os.RemoveAll(top); err != nil {
		return nil, fileErr(top, err)
	}
	if err := os.Mkdir(top, 0o700); err != nil {
		return nil, fileErr(top, err)
	}
	out := make([]*Quarantine, len(repos))
	found := false
	for i, r := range repos {
		rel, err := repositoryRel(r)
		if err != nil {
			return nil, err
		}
		var subs []*Quarantine
		for k, s := range r.submodules {
			bundle := filepath.Join(d.Bundle(), strconv.Itoa(i)+"-"+strconv.Itoa(k)+".bundle")
			if _, err := os.Lstat(bundle); errors.Is(err, fs.ErrNotExist) {
				continue
			}
			q, err := d.quarantineSub(ctx, i, k, r, s, rel, bundle)
			if err != nil {
				return nil, err
			}
			subs = append(subs, q)
		}
		none := filepath.Join(d.Bundle(), strconv.Itoa(i)+".none")
		if info, err := os.Lstat(none); err == nil && info.Mode().IsRegular() {
			if len(subs) > 0 {
				out[i], found = &Quarantine{Repo: r, Branch: branch, Submodules: subs, prefix: d.RefPrefix()}, true
			}
			continue
		}
		bundle := filepath.Join(d.Bundle(), strconv.Itoa(i)+".bundle")
		if _, err := os.Lstat(bundle); errors.Is(err, fs.ErrNotExist) {
			if rel == "." {
				return nil, fmt.Errorf(textNoBundle, "the run's repository")
			}
			return nil, fmt.Errorf(textNoBundle, shown(rel))
		}
		q, err := d.quarantineOne(ctx, i, r, rel, branch, bundle)
		if err != nil {
			return nil, err
		}
		q.Submodules = subs
		out[i], found = q, true
	}
	if !found {
		return nil, ErrNoBundle
	}
	return out, nil
}

// quarantineOne is [Dir.Quarantine] for the repository r, number i, at rel in the copy.
func (d Dir) quarantineOne(ctx context.Context, i int, r Repository, rel, branch, bundle string) (*Quarantine, error) {
	q := &Quarantine{Path: d.quarantine(r.index), Repo: r, Branch: branch, prefix: d.RefPrefix()}
	checked := filepath.Join(d.root, quarantineName, strconv.Itoa(i)+".bundle")
	if err := q.fetchBundle(ctx, d.root, d.source(r.index), bundle, checked, "refs/heads/"+branch, rel); err != nil {
		return nil, err
	}
	return q, nil
}

// quarantineSub is [Dir.Quarantine] for the submodule s, number k, of the repository r, number i,
// at rel in the copy.
func (d Dir) quarantineSub(ctx context.Context, i, k int, r Repository, s submodule, rel, bundle string) (*Quarantine, error) {
	name := strconv.Itoa(i) + "-" + strconv.Itoa(k)
	at := path.Join(r.Path, s.path)
	q := &Quarantine{
		Path: filepath.Join(d.root, quarantineName, name+".git"), Repo: Repository{Path: at, Base: s.link, index: r.index},
		prefix: d.RefPrefix(), gitDir: s.gitDir,
	}
	checked := filepath.Join(d.root, quarantineName, name+".bundle")
	if err := q.fetchBundle(ctx, d.root, s.gitDir, bundle, checked, "", path.Join(rel, s.path)); err != nil {
		return nil, err
	}
	return q, nil
}

// fetchBundle copies the bundle at path to checked, makes q.Path a bare repository with the
// objects of the git folder source as its alternates, checks the bundle's header, and fetches
// its refs into q.Path: ref, the run's branch, "" for none, and the run's refs under q.prefix.
// It sets Head, Refs and their commits, and checks the top of each commit. root is the folder
// git init runs in, and rel is the repository as an error shows it.
func (q *Quarantine) fetchBundle(ctx context.Context, root, source, bundle, checked, ref, rel string) error {
	if err := copyBundle(bundle, checked); err != nil {
		return err
	}
	format, err := git(ctx, source, "--git-dir="+source, "rev-parse", "--show-object-format")
	if err != nil {
		return fmt.Errorf(textGitInRepo, shown(source), err)
	}
	if _, err := git(ctx, root, "init", "--quiet", "--bare", "--template=", "--object-format="+strings.TrimSpace(format), "--", q.Path); err != nil {
		return fmt.Errorf(textQuarantine, shown(q.Path), err)
	}
	if err := writeNew(filepath.Join(q.Path, "objects", "info", "alternates"), filepath.Join(source, "objects")+"\n"); err != nil {
		return fileErr(q.Path, err)
	}
	if err := copyShallow(source, q.Path); err != nil {
		return fileErr(q.Path, err)
	}
	// git fetch follows a file that has gitdir: <path>, so nothing but a bundle reaches it.
	refs, err := checkBundleHeader(checked, ref, q.prefix)
	if err != nil {
		return fmt.Errorf(textBundleChecks, shown(rel), err)
	}
	if bundleFetchChecks(ctx) {
		var specs strings.Builder
		for _, r := range refs {
			specs.WriteString(r.name + ":" + r.name + "\n")
		}
		_, err = gitWith(ctx, q.Path, nil, strings.NewReader(specs.String()), "-c", "transfer.fsckObjects=true", "-c", "fetch.fsckObjects=true", "-c", "fetch.fsck.hasDotgit=error",
			"fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--atomic", "--stdin", "--", checked)
	} else {
		err = indexBundle(ctx, q.Path, checked, ref, q.prefix)
	}
	if err != nil {
		return fmt.Errorf(textBundleChecks, shown(rel), err)
	}
	if err := q.readRefs(ctx, refs, ref); err != nil {
		return fmt.Errorf(textBundleChecks, shown(rel), err)
	}
	return q.checkTops(ctx, rel)
}

// readRefs checks that the quarantine has the refs of the bundle's header and no other, and sets
// Head, Refs and their commits from them. ref is the run's branch, "" for none.
func (q *Quarantine) readRefs(ctx context.Context, want []bundleRef, ref string) error {
	out, err := git(ctx, q.Path, "for-each-ref", "--format=%(objectname) %(refname)")
	if err != nil {
		return err
	}
	var have []bundleRef
	for line := range strings.Lines(out) {
		id, name, _ := strings.Cut(strings.TrimSuffix(line, "\n"), " ")
		have = append(have, bundleRef{id, name})
	}
	sorted := slices.Clone(want)
	sortRefs(sorted)
	sortRefs(have)
	if !slices.Equal(sorted, have) {
		return errQuarantineRefs
	}
	for _, r := range sorted {
		commit, err := commitOf(ctx, q.Path, r.name)
		if err != nil {
			return err
		}
		if r.name == ref {
			q.Head = commit
			continue
		}
		q.Refs = append(q.Refs, r.name)
		q.commits = append(q.commits, commit)
	}
	return nil
}

// sortRefs sorts refs by name.
func sortRefs(refs []bundleRef) {
	slices.SortFunc(refs, func(a, b bundleRef) int { return strings.Compare(a.name, b.name) })
}

// errQuarantineRefs is a quarantine whose refs differ from what the bundle's header lists.
var errQuarantineRefs = errors.New("the refs the fetch wrote differ from the refs of the bundle's header")

// copyShallow gives the quarantine q the shallow list of its bare source, when the source
// has one: a repository fetched at depth one has a base commit whose parents no alternate
// has, and git stops at the commits of the list when it walks the history.
func copyShallow(source, q string) error {
	data, err := os.ReadFile(filepath.Join(source, "shallow"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return writeNew(filepath.Join(q, "shallow"), string(data))
}

// checkTops runs [Quarantine.checkTop] on Head and on the commit of each of Refs, and for
// stash on its commits of the index and of the untracked files too, which git stash apply
// writes. rel is the repository as an error shows it.
func (q *Quarantine) checkTops(ctx context.Context, rel string) error {
	commits := slices.Clone(q.commits)
	if q.Head != "" {
		commits = append(commits, q.Head)
	}
	for _, r := range q.Refs {
		if r != q.prefix+"/stash" {
			continue
		}
		for _, parent := range []string{"^2", "^3"} {
			if c, err := commitOf(ctx, q.Path, r+parent); err == nil {
				commits = append(commits, c)
			}
		}
	}
	if len(commits) == 0 {
		return nil
	}
	base, err := q.treeEntries(ctx, q.Repo.Base+"^{tree}", "")
	if err != nil {
		return fmt.Errorf(textBundleChecks, shown(rel), err)
	}
	for _, c := range commits {
		if err := q.checkTop(ctx, rel, c, base); err != nil {
			return err
		}
	}
	return nil
}

// checkTop returns an error when the tree of commit has an entry at the top whose name is
// .git or .qory in any letter case, other than the same entry in base, the top of the base
// commit's tree.
func (q *Quarantine) checkTop(ctx context.Context, rel, commit string, base []treeEntry) error {
	head, err := q.treeEntries(ctx, commit+"^{tree}", "")
	if err != nil {
		return fmt.Errorf(textBundleChecks, shown(rel), err)
	}
	for _, e := range head {
		if !folds(e.name, ".git") && !folds(e.name, checkout.Dir) {
			continue
		}
		kept := false
		for _, b := range base {
			kept = kept || b == e
		}
		if !kept {
			return fmt.Errorf(textTopEntry, commit, shown(rel), shown(e.name))
		}
	}
	return nil
}

// indexBundle fetches the refs of the bundle at path into the bare repository q for a git
// whose fetch from a bundle checks no object: it reads the bundle's header, as
// [checkBundleHeader] checks it with ref and prefix, passes its pack to git index-pack --strict
// --fsck-objects, which checks every object and fails on a broken one, writes each ref, and checks
// with git rev-list that every object the refs reach past the bundle's prerequisites is there.
func indexBundle(ctx context.Context, q, path, ref, prefix string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	refs, prereqs, offset, err := bundleHeader(f, ref, prefix)
	if err != nil {
		return err
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	if _, err := gitWith(ctx, q, nil, f, "index-pack", "--stdin", "--fix-thin", "--strict=hasDotgit=error", "--fsck-objects"); err != nil {
		return err
	}
	var updates, walk strings.Builder
	for _, r := range refs {
		updates.WriteString("create " + r.name + " " + r.id + "\n")
		walk.WriteString(r.id + "\n")
	}
	for _, p := range prereqs {
		walk.WriteString("^" + p + "\n")
	}
	if _, err := gitWith(ctx, q, nil, strings.NewReader(updates.String()), "update-ref", "--stdin"); err != nil {
		return err
	}
	_, err = gitWith(ctx, q, nil, strings.NewReader(walk.String()), "rev-list", "--objects", "--quiet", "--stdin")
	return err
}

// bundleRef is a ref of a bundle's header: the object id and the full name.
type bundleRef struct{ id, name string }

// checkBundleHeader returns the refs of the bundle at path, or an error unless the file starts
// with the header of a version 2 or 3 git bundle whose refs [bundleHeader] takes.
func checkBundleHeader(path, ref, prefix string) ([]bundleRef, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	refs, _, _, err := bundleHeader(f, ref, prefix)
	return refs, err
}

// maxBundleRefs is the most refs qory takes from one bundle.
const maxBundleRefs = 10000

// bundleHeader reads the header of the bundle r: its refs, the prerequisite commits, and the
// offset where the pack starts. It takes ref, the run's branch, unless ref is "", and each ref
// that [runRef] takes under prefix; any other ref, a ref listed twice in any letter case, or
// more than [maxBundleRefs] refs is an error, and so is a header with none.
func bundleHeader(r io.Reader, ref, prefix string) ([]bundleRef, []string, int64, error) {
	br := bufio.NewReader(io.LimitReader(r, maxBundleHeader))
	var offset int64
	line := func() (string, error) {
		s, err := br.ReadString('\n')
		offset += int64(len(s))
		if err != nil {
			return "", errBundleHeader
		}
		return strings.TrimSuffix(s, "\n"), nil
	}
	first, err := line()
	if err != nil || first != "# v2 git bundle" && first != "# v3 git bundle" {
		return nil, nil, 0, errBundleHeader
	}
	var refs []bundleRef
	var prereqs []string
	seen := map[string]bool{}
	for {
		s, err := line()
		if err != nil {
			return nil, nil, 0, err
		}
		switch {
		case s == "":
			if len(refs) == 0 {
				return nil, nil, 0, errBundleHeader
			}
			return refs, prereqs, offset, nil
		case strings.HasPrefix(s, "@"):
			// A capability: the object format, or a filter, which a partial bundle has.
			if !strings.HasPrefix(s, "@object-format=") {
				return nil, nil, 0, errBundleHeader
			}
		case strings.HasPrefix(s, "-"):
			id, _, _ := strings.Cut(s[1:], " ")
			if !objectID.MatchString(id) {
				return nil, nil, 0, errBundleHeader
			}
			prereqs = append(prereqs, id)
		default:
			id, name, _ := strings.Cut(s, " ")
			if !objectID.MatchString(id) {
				return nil, nil, 0, errBundleHeader
			}
			if (ref == "" || name != ref) && !runRef(prefix, name) {
				return nil, nil, 0, fmt.Errorf(textBundleRef, shown(name), prefix)
			}
			if folded := strings.ToLower(name); seen[folded] {
				return nil, nil, 0, fmt.Errorf(textBundleRefTwice, shown(name))
			} else {
				seen[folded] = true
			}
			if len(refs) == maxBundleRefs {
				return nil, nil, 0, fmt.Errorf(textBundleRefs, maxBundleRefs)
			}
			refs = append(refs, bundleRef{id, name})
		}
	}
}

// runRef reports whether name is a ref of the run under prefix: prefix/HEAD, /uncommitted, /stash,
// /lost/<n>, or /heads/<name> or /tags/<name> with a name that [refPartsOK] takes.
func runRef(prefix, name string) bool {
	rest, ok := strings.CutPrefix(name, prefix+"/")
	if !ok {
		return false
	}
	switch rest {
	case "HEAD", "uncommitted", "stash":
		return true
	}
	if n, ok := strings.CutPrefix(rest, "lost/"); ok {
		return n != "" && len(n) <= 9 && n[0] != '0' && strings.Trim(n, "0123456789") == ""
	}
	for _, kind := range []string{"heads/", "tags/"} {
		if after, ok := strings.CutPrefix(rest, kind); ok {
			return refPartsOK(after)
		}
	}
	return false
}

// refPartsOK reports whether name, a part of a ref name with slashes, follows git's rules for
// a ref name, as git check-ref-format does: each part is not empty, does not start with a dot
// and does not end with .lock; no .., @{ or character below a space, DEL, space, ~, ^, :, ?, *,
// [ or a backslash; and no / or . at the end.
func refPartsOK(name string) bool {
	if name == "" || name == "@" || strings.HasSuffix(name, ".") || strings.Contains(name, "..") || strings.Contains(name, "@{") {
		return false
	}
	if strings.ContainsFunc(name, func(r rune) bool { return r < ' ' || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) }) {
		return false
	}
	for part := range strings.SplitSeq(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

// errBundleHeader is a bundle whose header is not a git bundle's.
var errBundleHeader = errors.New("the bundle's header is not a git bundle's, or it has no ref")

// copyBundle copies the bundle at src, opened by [OpenBundle], to dest, a new file in a
// folder no run binds, and reads at most [MaxBundle] bytes.
func copyBundle(src, dest string) error {
	in, err := OpenBundle(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_TRUNC, 0o600)
	if err != nil {
		return fileErr(dest, err)
	}
	n, err := io.Copy(out, io.LimitReader(in, MaxBundle+1))
	if err == nil && n > MaxBundle {
		err = fmt.Errorf(textBundleGrew, shown(src), int64(MaxBundle))
	} else if err != nil {
		err = fileErr(dest, err)
	}
	if cerr := out.Close(); err == nil && cerr != nil {
		err = fileErr(dest, cerr)
	}
	return err
}

// writeNew writes text to path, a file that must not exist.
func writeNew(path, text string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// FetchInto fetches the run's branch from the quarantine into the person's repository
// at checkout, as ref, a full ref name such as refs/heads/qory/run-<run id>, and the run's
// other refs, in the repository and in each submodule's git folder, under the first of the
// run's [Dir.RefPrefix], then the same with -2, -3 and so on, that no ref there has, lies
// under or lies above, with letter case ignored. ctx sets how long it may take. It returns that
// prefix, "" when no other ref came back. Each fetch checks every object, writes no tag and no
// submodule, writes only the refs it names, all or none of them, and moves ref only forward. git
// stops the fetch when ref is a branch checked out in a working tree of the repository: the
// person's files never change to what the run wrote. When another git process holds a lock that
// a fetch needs, the fetch runs again after each wait of [lockWaits]. When a fetch fails, the
// quarantine stays, and the error contains its path.
func (q *Quarantine) FetchInto(ctx context.Context, checkout, ref string) (string, error) {
	from := "refs/heads/" + q.Branch
	var specs []string
	if q.Head != "" {
		if err := checkRef(ctx, ref); err != nil {
			return "", err
		}
		specs = append(specs, from+":"+ref)
	}
	prefix := ""
	if q.hasRefs() {
		dirs := []string{checkout}
		for _, s := range q.Submodules {
			dirs = append(dirs, s.gitDir)
		}
		var err error
		if prefix, err = freePrefix(ctx, q.prefix, dirs); err != nil {
			return "", err
		}
	}
	if q.Path != "" && len(q.Refs)+len(specs) > 0 {
		if err := fetchRefs(ctx, checkout, q.Path, slices.Concat(specs, q.moved(prefix))); err != nil {
			if q.Head == "" {
				return "", fmt.Errorf(textFetchFailed, shown(q.Path), prefix, shown(checkout), q.prefix+"/*", err)
			}
			if at := checkedOut(ctx, checkout, ref); at != "" {
				return "", fmt.Errorf(textFetchCheckedOut, shown(ref), shown(at), from, shown(q.Path), err)
			}
			if _, verr := git(ctx, checkout, "rev-parse", "--verify", "--quiet", "--end-of-options", ref); verr == nil {
				return "", fmt.Errorf(textFetchExists, shown(q.Path), shown(ref), shown(checkout), from, err)
			}
			return "", fmt.Errorf(textFetchFailed, shown(q.Path), shown(ref), shown(checkout), from, err)
		}
	}
	for _, s := range q.Submodules {
		if err := fetchRefs(ctx, s.gitDir, s.Path, s.moved(prefix)); err != nil {
			return "", fmt.Errorf(textFetchFailed, shown(s.Path), prefix, shown(s.gitDir), q.prefix+"/*", err)
		}
	}
	return prefix, nil
}

// hasRefs reports whether q or one of its submodules has refs under the run's prefix.
func (q *Quarantine) hasRefs() bool {
	return len(q.Refs) > 0 || slices.ContainsFunc(q.Submodules, (*Quarantine).hasRefs)
}

// moved is a refspec for each of Refs, from the run's prefix to prefix.
func (q *Quarantine) moved(prefix string) []string {
	specs := make([]string, len(q.Refs))
	for i, r := range q.Refs {
		specs[i] = r + ":" + prefix + strings.TrimPrefix(r, q.prefix)
	}
	return specs
}

// fetchRefs fetches specs from the bare repository from into the git repository at dir, a
// working tree or a git folder, with its objects checked, and waits for another git process's
// lock as [Quarantine.FetchInto] says.
func fetchRefs(ctx context.Context, dir, from string, specs []string) error {
	input := strings.Join(specs, "\n") + "\n"
	fetch := func() error {
		_, err := gitWith(ctx, dir, nil, strings.NewReader(input), "-c", "transfer.fsckObjects=true", "-c", "fetch.fsckObjects=true",
			"fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--atomic", "--stdin",
			"--", from)
		return err
	}
	err := fetch()
	for _, wait := range lockWaits {
		if err == nil || !lockHeld(err) || !sleep(ctx, wait) {
			break
		}
		err = fetch()
	}
	return err
}

// prefixTries is how many prefixes [freePrefix] tries before it stops.
const prefixTries = 100

// freePrefix is the first of base, base-2, base-3 and so on that no ref of the repositories at
// dirs has, lies under or lies above, with letter case ignored, as on a disk that ignores case.
func freePrefix(ctx context.Context, base string, dirs []string) (string, error) {
	var names []string
	for _, dir := range dirs {
		out, err := git(ctx, dir, "for-each-ref", "--format=%(refname)")
		if err != nil {
			return "", fmt.Errorf(textGitInRepo, shown(dir), err)
		}
		for line := range strings.Lines(out) {
			names = append(names, strings.TrimSuffix(line, "\n"))
		}
	}
	for n := 1; n <= prefixTries; n++ {
		prefix := base
		if n > 1 {
			prefix += "-" + strconv.Itoa(n)
		}
		if !blocked(names, prefix) {
			if err := checkRef(ctx, prefix); err != nil {
				return "", err
			}
			return prefix, nil
		}
	}
	return "", fmt.Errorf(textNoFreePrefix, base, base, shown(dirs[0]), prefixTries)
}

// lockWaits are the waits of [Quarantine.FetchInto] before it fetches again while another git
// process holds a lock: 5 more fetches within 30 s. A test sets shorter ones.
var lockWaits = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second}

// sleep waits for d, and reports whether d passed before ctx ended.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// lockHeld reports whether git failed in err because another git process held a lock: a ref's
// lock or the index's.
func lockHeld(err error) bool {
	for _, ge := range gitErrors(err) {
		if strings.Contains(ge.line, "cannot lock ref") && strings.Contains(ge.line, ".lock") || strings.Contains(ge.line, "index.lock") {
			return true
		}
	}
	return false
}

// checkedOut is the working tree of the repository at checkout that has ref checked
// out, by git worktree list; "" for none or when the list fails.
func checkedOut(ctx context.Context, checkout, ref string) string {
	out, err := git(ctx, checkout, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return ""
	}
	tree := ""
	for field := range strings.SplitSeq(out, "\x00") {
		if p, ok := strings.CutPrefix(field, "worktree "); ok {
			tree = p
		}
		if field == "branch "+ref {
			return tree
		}
	}
	return ""
}
