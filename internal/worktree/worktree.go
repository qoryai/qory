// Package worktree adds, removes and lists the linked worktrees of a repository, and
// prepares a new one the way the repository's qory.yaml says: files linked or copied from
// the main checkout and commands run in the new worktree. What it does with git is
// plain: a worktree per branch, beside the main checkout unless the configuration says
// where else, a new branch cut off a base with no upstream on it, and its upstream set
// to a remote branch of its own name, whether that branch exists yet or not, so a push
// from the worktree creates or updates that branch and never touches the base. The base
// is recorded in the branch's git config, so a later add can move the branch onto
// another one and a remove can tell whether the branch holds anything of its own, which
// decides whether it goes with the worktree quietly or after a question.
package worktree

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Options is what an add or a remove is told, from the configuration and the flags.
type Options struct {
	// Dir is where worktrees go, relative to the main checkout unless absolute.
	Dir string
	// Name is the directory name template: {branch} with each slash made a dash, {repo}
	// the main checkout's directory name.
	Name string
	// Base is the ref a new branch starts from, "" for the remote's HEAD branch, else the
	// main checkout's current branch.
	Base string
	// Onto says Base was asked for on the command line, so a branch that already exists
	// is moved or rebased onto it after Ask, instead of left where it is.
	Onto bool
	// Rebase answers the question Onto asks with yes.
	Rebase bool
	// Fetch fetches the remote before the branch is cut, so the base is the remote's.
	Fetch bool
	// Attach is a branch of the remote the worktree attaches to, from [RemoteBranch] or
	// [PullRequest]; its zero value attaches to nothing. The branch is fetched, checked
	// out under the name Attach.Name gives and set to track it.
	Attach Remote
	// As names the worktree instead of its branch: it is what {branch} in Name becomes,
	// "" for the branch.
	As string
	// PullRef is the ref the remote publishes a pull request's head under, {n} for its
	// number, "" to probe [PullRefs].
	PullRef string
	// Link are paths linked from the main checkout into the new worktree.
	Link []string
	// Copy are paths copied once from the main checkout into the new worktree.
	Copy []string
	// Add are commands run in the new worktree after the links and copies, in order.
	Add []string
	// Remove are commands run in a worktree before it is removed, in order.
	Remove []string
	// Force removes a worktree with uncommitted changes.
	Force bool
	// KeepBranch keeps the branch after the worktree is removed; the default deletes it.
	KeepBranch bool
	// DeleteBranch deletes the branch even when it holds commits nothing else does,
	// without asking.
	DeleteBranch bool
	// Ask puts a question to the user and returns the answer, trimmed and lower-cased,
	// "" for none. It is nil when nobody is there to answer, and a question is then an
	// error naming the flag that answers it.
	Ask func(question string) (string, error)
	// Output is where the commands' output goes, nil for none.
	Output io.Writer
}

// Added is what [Add] did.
type Added struct {
	// Path is the worktree's absolute path.
	Path string
	// Branch is the branch checked out in it.
	Branch string
	// How says where the branch came from: "new off <base>", "local", "remote", "fetched
	// from <remote>" for Options.Attach, or "already there" for a worktree that was
	// already there on that branch, and what Options.Onto did with it after a semicolon.
	How string
	// Upstream is the remote branch the worktree pushes to, "" without a remote or when
	// Head is set.
	Upstream string
	// Head is the remote's ref the branch pulls from when it is a pull request's head that
	// no branch of the remote holds, a fork's or a deleted one, as "<remote> <ref>"; a
	// push from the worktree goes nowhere then. It is "" otherwise.
	Head string
	// Linked and Copied are the paths brought in, relative to the worktree.
	Linked, Copied []string
	// Kept are the paths of Link and Copy already present in the worktree, left as they were.
	Kept []string
	// Missing are the paths of Link and Copy absent in the main checkout.
	Missing []string
	// Ran are the commands run, in order.
	Ran []string
}

// Removed is what [Remove] did.
type Removed struct {
	// Path is the removed worktree's absolute path, Branch its branch.
	Path, Branch string
	// Main is the main checkout, where a shell goes next.
	Main string
	// Ran are the commands run before the removal, in order.
	Ran []string
	// Own is how many commits the branch held that nothing else did: no remote branch,
	// not the main checkout, not the base it was cut from.
	Own int
	// Pushed says the branch was pushed to Upstream before it was deleted.
	Pushed bool
	// Upstream is the remote branch of the branch's name, "" without a remote.
	Upstream string
	// BranchDeleted says whether the branch went with the worktree.
	BranchDeleted bool
}

// Entry is one worktree of the repository.
type Entry struct {
	// Path is absolute; Branch is "" for a detached head.
	Path, Branch string
	// Main marks the main checkout.
	Main bool
	// Composed says whether a qory compose report is in it.
	Composed bool
}

// RunError is a command of Options.Add or Options.Remove that failed. The worktree is
// kept as it is, so the command can be repaired and the add run again.
type RunError struct {
	// Command is the command as the configuration wrote it, Path the worktree it ran in.
	Command, Path string
	// Err is the error the command ended with.
	Err error
}

func (e *RunError) Error() string {
	return fmt.Sprintf("%s in %s: %v; the worktree is kept, repair the command and add again", e.Command, e.Path, e.Err)
}

func (e *RunError) Unwrap() error { return e.Err }

// Remote is a branch of the remote an add attaches a worktree to, instead of a branch of
// the worktree's own: found by [RemoteBranch] or [PullRequest], fetched, checked out and
// tracked by [Add].
type Remote struct {
	// Branch is the branch on the remote, "" for a pull request whose head no branch of
	// the remote holds: one from a fork, or one whose branch is deleted.
	Branch string
	// PR is the pull request's number, 0 for a plain branch.
	PR int
	// Head is the ref the remote publishes the pull request's head under, "" for a
	// plain branch.
	Head string
}

// PullRefs are the refs a remote publishes a pull request's head under, {n} for its
// number: GitHub's and Forgejo's, GitLab's, Bitbucket Server's. [PullRequest] probes
// them in order when Options.PullRef names none.
var PullRefs = []string{"refs/pull/{n}/head", "refs/merge-requests/{n}/head", "refs/pull-requests/{n}/from"}

// attached reports whether r names anything.
func (r Remote) attached() bool { return r.Branch != "" || r.PR != 0 }

// Name is the local branch the remote's is checked out as: its own name, or pr-<n> for
// a pull request whose head no branch of the remote holds.
func (r Remote) Name() string {
	if r.Branch != "" {
		return r.Branch
	}
	return fmt.Sprintf("pr-%d", r.PR)
}

// ref is the ref fetched from the remote, and what the local branch merges from.
func (r Remote) ref() string {
	if r.Branch != "" {
		return "refs/heads/" + r.Branch
	}
	return r.Head
}

// tracking is the remote-tracking ref the fetch lands in: <remote>/<branch> for a branch,
// the pull request's ref under <remote> otherwise, as refs/remotes/origin/pull/7/head.
func (r Remote) tracking(remote string) string {
	if r.Branch != "" {
		return "refs/remotes/" + remote + "/" + r.Branch
	}
	return "refs/remotes/" + remote + "/" + strings.TrimPrefix(r.Head, "refs/")
}

// how is what the branch row says of an attach: where it was fetched from, and the pull
// request it is.
func (r Remote) how(remote string) string {
	if r.PR != 0 {
		return fmt.Sprintf("pull request #%d, fetched from %s", r.PR, remote)
	}
	return "fetched from " + remote
}

// RemoteBranch finds branch on the repository's remote, for [Add] to attach a worktree
// to. It asks the remote, so a branch pushed from elsewhere is found without a fetch, and
// one the remote does not have is an error saying so.
func RemoteBranch(main, branch string) (Remote, error) {
	if err := checkName(branch); err != nil {
		return Remote{}, err
	}
	remote, refs, err := lsRemote(main, "refs/heads/"+branch)
	if err != nil {
		return Remote{}, err
	}
	if _, ok := refs["refs/heads/"+branch]; !ok {
		return Remote{}, fmt.Errorf("%s has no branch %s; qory worktree add %s cuts a new one", remote, branch, branch)
	}
	return Remote{Branch: branch}, nil
}

// PullRequest finds pull request n on the repository's remote, for [Add] to attach a
// worktree to: the ref the remote publishes its head under, ref with {n} for the number
// or each of [PullRefs] in turn when ref is "", and the branch of the remote at the
// same commit, which is the pull request's branch when the remote holds it. No hosting
// API is asked, so any host that publishes the head as a ref serves; one that does not
// is an error naming what was looked for. A head that two branches hold is an error
// too, since which one to track is a guess.
func PullRequest(main string, n int, ref string) (Remote, error) {
	if n < 1 {
		return Remote{}, fmt.Errorf("%d is not a pull request number", n)
	}
	patterns := PullRefs
	if ref != "" {
		patterns = []string{ref}
	}
	heads := make([]string, len(patterns))
	for i, p := range patterns {
		heads[i] = strings.ReplaceAll(p, "{n}", strconv.Itoa(n))
	}
	remote, refs, err := lsRemote(main, append(heads, "refs/heads/*")...)
	if err != nil {
		return Remote{}, err
	}
	r := Remote{PR: n}
	var sha string
	for _, head := range heads {
		if s, ok := refs[head]; ok {
			r.Head, sha = head, s
			break
		}
	}
	if r.Head == "" {
		return r, fmt.Errorf("pull request %d not found on %s; looked for %s. worktree.pr in qory.yaml names the ref your host uses, with {n} for the number; a host that publishes none takes --branch with the pull request's branch", n, remote, strings.Join(heads, ", "))
	}
	var branches []string
	for name, s := range refs {
		if b, ok := strings.CutPrefix(name, "refs/heads/"); ok && s == sha {
			branches = append(branches, b)
		}
	}
	sort.Strings(branches)
	switch len(branches) {
	case 0:
	case 1:
		r.Branch = branches[0]
	default:
		return r, fmt.Errorf("pull request %d is at the tip of %d branches of %s, %s; --branch says which to attach to", n, len(branches), remote, strings.Join(branches, ", "))
	}
	return r, nil
}

// lsRemote asks the repository's remote for the refs matching the patterns and returns
// the remote's name and the refs it has, ref to commit.
func lsRemote(main string, patterns ...string) (string, map[string]string, error) {
	remote := remoteOf(main)
	if remote == "" {
		return "", nil, errors.New("the repository has no remote to attach to")
	}
	out, err := git(main, append([]string{"ls-remote", "--refs", remote}, patterns...)...)
	if err != nil {
		return remote, nil, fmt.Errorf("git ls-remote %s: %w", remote, err)
	}
	refs := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if sha, ref, ok := strings.Cut(line, "\t"); ok {
			refs[ref] = sha
		}
	}
	return remote, refs, nil
}

// checkName refuses what git would not take as a branch name.
func checkName(branch string) error {
	if branch == "" || strings.HasPrefix(branch, "-") || strings.ContainsAny(branch, " ~^:?*[\\") || strings.HasSuffix(branch, "/") || strings.Contains(branch, "..") {
		return fmt.Errorf("%q is not a branch name", branch)
	}
	return nil
}

// Main returns the main checkout of the repository holding dir: the first worktree git
// lists, which is the one with the repository's own git directory.
func Main(dir string) (string, error) {
	out, err := git(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("%s is not inside a git working tree", dir)
	}
	for _, line := range strings.Split(out, "\n") {
		if path, ok := strings.CutPrefix(line, "worktree "); ok {
			return path, nil
		}
	}
	return "", fmt.Errorf("git lists no worktree for %s", dir)
}

// List returns the repository's worktrees, the main checkout first.
func List(main string) ([]Entry, error) {
	out, err := git(main, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var entries []Entry
	var cur *Entry
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			entries = append(entries, Entry{Path: strings.TrimPrefix(line, "worktree ")})
			cur = &entries[len(entries)-1]
		case strings.HasPrefix(line, "branch ") && cur != nil:
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		}
	}
	for i := range entries {
		entries[i].Main = i == 0
		_, err := os.Stat(filepath.Join(entries[i].Path, ".qory", "harness-report.json"))
		entries[i].Composed = err == nil
	}
	return entries, nil
}

// PathFor is the directory the worktree called name gets, from Options.Dir and
// Options.Name, absolute. The name is the branch, or Options.As when that is set.
func PathFor(main, name string, o Options) string {
	dir := o.Dir
	if dir == "" {
		dir = ".."
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(main, dir)
	}
	tmpl := o.Name
	if tmpl == "" {
		tmpl = "wt-{branch}"
	}
	tmpl = strings.ReplaceAll(tmpl, "{branch}", strings.ReplaceAll(name, "/", "-"))
	tmpl = strings.ReplaceAll(tmpl, "{repo}", filepath.Base(main))
	return filepath.Clean(filepath.Join(dir, tmpl))
}

// Add makes the worktree of branch for the repository whose main checkout is main, and
// prepares it. A worktree already on the branch is reused, wherever it is, and so is a
// directory already at the path when it is a worktree on that branch; one on another
// branch is refused. The branch is checked out when it exists locally, tracked when it
// exists on the remote, and cut off the base otherwise, with its upstream set to the
// remote branch of its own name and the base it was cut from recorded in its git
// config. With Options.Attach, the remote's branch is fetched first and the worktree
// tracks it: a new local branch starts at it, one that exists is fast-forwarded to it
// when that is possible, and a pull request's head that no branch of the remote holds
// is pulled from its ref and pushed nowhere. With Options.Onto, a branch that existed is
// moved or rebased onto Options.Base, see [onto]. Then every Options.Link is linked and
// every Options.Copy copied from the main checkout, each skipped with a note when it is
// already in the worktree or absent in the main checkout, and every Options.Add is run
// in the worktree with QORY_WORKTREE, QORY_MAIN and QORY_BRANCH set; the first failing
// command is a [*RunError].
func Add(main, branch string, o Options) (Added, error) {
	name := branch
	if o.As != "" {
		name = o.As
	}
	a := Added{Path: PathFor(main, name, o), Branch: branch}
	if err := checkName(branch); err != nil {
		return a, err
	}
	if err := checkName(name); err != nil {
		return a, fmt.Errorf("%q is not a worktree name", name)
	}
	remote := remoteOf(main)
	attach := o.Attach.attached()
	if attach && remote == "" {
		return a, errors.New("the repository has no remote to attach to")
	}
	if o.Fetch && remote != "" {
		if _, err := git(main, "fetch", "--quiet", remote); err != nil {
			return a, fmt.Errorf("git fetch %s: %w", remote, err)
		}
	}
	if attach {
		if _, err := git(main, "fetch", "--quiet", remote, "+"+o.Attach.ref()+":"+o.Attach.tracking(remote)); err != nil {
			return a, fmt.Errorf("git fetch %s %s: %w", remote, o.Attach.ref(), err)
		}
	}
	// A base that resolves to nothing is a mistake on every path, not only the one
	// that cuts a new branch off it.
	if o.Base != "" && !resolves(main, o.Base) {
		return a, fmt.Errorf("base %s is not a branch, tag or commit of this repository", o.Base)
	}
	// A branch checked out somewhere is that worktree, whatever the name says; git
	// refuses a second checkout of one branch, and the main checkout is no worktree.
	if entries, err := List(main); err == nil {
		for _, e := range entries {
			if e.Branch != branch {
				continue
			}
			if e.Main {
				return a, fmt.Errorf("%s is checked out in the main checkout, %s; a worktree is for another branch", branch, main)
			}
			a.Path = e.Path
		}
	}
	existed := true
	if info, err := os.Stat(a.Path); err == nil {
		if !info.IsDir() {
			return a, fmt.Errorf("%s is a file, where the worktree of %s would go", a.Path, branch)
		}
		on, err := git(a.Path, "branch", "--show-current")
		if err != nil {
			return a, fmt.Errorf("%s exists and is not a worktree of this repository", a.Path)
		}
		if on != branch {
			return a, fmt.Errorf("%s exists on branch %s, not %s; remove it or pick another branch", a.Path, on, branch)
		}
		a.How = "already there"
	} else {
		if err := os.MkdirAll(filepath.Dir(a.Path), 0o755); err != nil {
			return a, err
		}
		local := refExists(main, "refs/heads/"+branch)
		onRemote := remote != "" && refExists(main, "refs/remotes/"+remote+"/"+branch)
		switch {
		case local:
			if _, err := git(main, "worktree", "add", a.Path, branch); err != nil {
				return a, fmt.Errorf("git worktree add: %w", err)
			}
			a.How = "local"
		case attach:
			if _, err := git(main, "worktree", "add", "--no-track", "-b", branch, a.Path, o.Attach.tracking(remote)); err != nil {
				return a, fmt.Errorf("git worktree add -b %s at %s: %w", branch, o.Attach.tracking(remote), err)
			}
			a.How = o.Attach.how(remote)
		case onRemote:
			if _, err := git(main, "worktree", "add", "--track", "-b", branch, a.Path, remote+"/"+branch); err != nil {
				return a, fmt.Errorf("git worktree add: %w", err)
			}
			a.How = "remote"
		default:
			base, err := baseRef(main, remote, o.Base)
			if err != nil {
				return a, err
			}
			if _, err := git(main, "worktree", "add", "--no-track", "-b", branch, a.Path, base); err != nil {
				return a, fmt.Errorf("git worktree add -b %s off %s: %w", branch, base, err)
			}
			if err := recordBase(a.Path, branch, base); err != nil {
				return a, err
			}
			a.How = "new off " + base
			existed = false
		}
	}
	if remote != "" {
		// The upstream is the branch's own name on the remote, set before that branch
		// exists there, so a push creates it and a pull, once it does, reads it. An
		// attached pull request that no branch of the remote holds pulls from the
		// pull request's ref instead, which nothing pushes to.
		merge := "refs/heads/" + branch
		if attach {
			merge = o.Attach.ref()
		}
		if _, err := git(a.Path, "config", "branch."+branch+".remote", remote); err != nil {
			return a, err
		}
		if _, err := git(a.Path, "config", "branch."+branch+".merge", merge); err != nil {
			return a, err
		}
		if attach && o.Attach.Branch == "" {
			a.Head = remote + " " + o.Attach.Head
		} else {
			a.Upstream = remote + "/" + branch
		}
	}
	if attach && a.How == "local" {
		a.How = o.Attach.how(remote) + "; " + fastForward(a.Path, o.Attach.tracking(remote), remote)
	}
	if existed && o.Onto && o.Base != "" {
		if err := onto(main, remote, &a, o); err != nil {
			return a, err
		}
	}
	for _, rel := range o.Link {
		switch state := bring(main, a.Path, rel, true); state {
		case brought:
			a.Linked = append(a.Linked, rel)
		case kept:
			a.Kept = append(a.Kept, rel)
		case missing:
			a.Missing = append(a.Missing, rel)
		default:
			return a, state.err
		}
	}
	for _, rel := range o.Copy {
		switch state := bring(main, a.Path, rel, false); state {
		case brought:
			a.Copied = append(a.Copied, rel)
		case kept:
			a.Kept = append(a.Kept, rel)
		case missing:
			a.Missing = append(a.Missing, rel)
		default:
			return a, state.err
		}
	}
	for _, command := range o.Add {
		if err := run(command, a.Path, main, branch, o.Output); err != nil {
			return a, &RunError{Command: command, Path: a.Path, Err: err}
		}
		a.Ran = append(a.Ran, command)
	}
	return a, nil
}

// onto puts the branch of a, which existed before this add, onto o.Base. The branch's
// own commits are the ones past its recorded base, or past the default base when none
// was recorded: with none, the branch is reset to o.Base; with some, they are rebased
// onto it. Either way the user is asked first, unless o.Rebase answers yes, and a no
// keeps the branch where it is, which a.How says. A worktree with uncommitted changes is
// refused, a rebase that stops at a conflict is aborted and the branch left as it was,
// and a branch that already holds o.Base is left alone. The recorded base may be a
// branch that moved on, so --base with the same name rebases onto where it is now.
func onto(main, remote string, a *Added, o Options) error {
	if status, _ := git(a.Path, "status", "--porcelain", "--untracked-files=no"); status != "" {
		return fmt.Errorf("%s has uncommitted changes, so %s stays where it is; commit or stash them to move it onto %s", a.Path, a.Branch, o.Base)
	}
	old := recordedBase(main, a.Branch)
	if old == "" {
		var err error
		if old, err = baseRef(main, remote, ""); err != nil {
			return err
		}
	}
	// A branch that holds the base already sits on it, or on top of it; there is
	// nothing to move. The recorded base may be a branch that moved on since, so the
	// question is asked of the branch's own history, not of the two bases' tips.
	if _, err := git(a.Path, "merge-base", "--is-ancestor", o.Base, "HEAD"); err == nil {
		a.How += "; on " + o.Base + " already"
		return recordBase(a.Path, a.Branch, o.Base)
	}
	out, err := git(a.Path, "rev-list", "--count", old+"..HEAD")
	if err != nil {
		return err
	}
	n, _ := strconv.Atoi(out)
	yes := o.Rebase
	if !yes {
		if o.Ask == nil {
			return fmt.Errorf("%s exists off %s, so --base %s needs an answer: add --rebase to move it onto %s, or leave --base out to keep it", a.Branch, old, o.Base, o.Base)
		}
		question := fmt.Sprintf("%s sits on %s with no commits of its own. Move it onto %s? [Y/n] ", a.Branch, old, o.Base)
		if n > 0 {
			question = fmt.Sprintf("%s holds %d commit%s off %s. Rebase %s onto %s? [y/N] ", a.Branch, n, plural(n), old, map[bool]string{true: "them", false: "it"}[n > 1], o.Base)
		}
		answer, err := o.Ask(question)
		if err != nil {
			return err
		}
		yes = answer == "y" || answer == "yes" || (n == 0 && answer == "")
	}
	if !yes {
		a.How += "; kept on " + old + ", not moved onto " + o.Base
		return nil
	}
	if n == 0 {
		if _, err := git(a.Path, "reset", "--hard", "--quiet", o.Base); err != nil {
			return fmt.Errorf("git reset --hard %s: %w", o.Base, err)
		}
		a.How = "moved onto " + o.Base + "; was on " + old
	} else {
		if _, err := git(a.Path, "rebase", "--quiet", "--onto", o.Base, old, a.Branch); err != nil {
			git(a.Path, "rebase", "--abort")
			return fmt.Errorf("rebasing %s onto %s stops at a conflict, so it stays on %s; run git rebase --onto %s %s in %s and resolve it there", a.Branch, o.Base, old, o.Base, old, a.Path)
		}
		a.How = fmt.Sprintf("rebased onto %s; %d commit%s, was on %s", o.Base, n, plural(n), old)
	}
	return recordBase(a.Path, a.Branch, o.Base)
}

// fastForward brings the local branch checked out at path up to the remote-tracking ref
// when that is a fast-forward, and says what it did: the branch was moved, was at the
// ref already, is ahead of it, or has diverged from it and is left as it is. The ref
// is shown as the remote shows it, origin/feature.
func fastForward(path, tracking, remote string) string {
	shown := strings.TrimPrefix(tracking, "refs/remotes/")
	before, _ := git(path, "rev-parse", "HEAD")
	if _, err := git(path, "merge", "--ff-only", "--quiet", tracking); err != nil {
		return "local branch diverged from " + shown + "; git pull merges"
	}
	after, _ := git(path, "rev-parse", "HEAD")
	switch {
	case before != after:
		return "local branch fast-forwarded to " + shown
	case before == mustRev(path, tracking):
		return "local branch at " + shown
	default:
		return "local branch ahead of " + shown
	}
}

// mustRev is the commit ref names, "" when it names none.
func mustRev(dir, ref string) string {
	sha, _ := git(dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return sha
}

// Remove takes the worktree at path away, after Options.Remove ran in it. It refuses the
// main checkout and a worktree with uncommitted changes to tracked files, the latter
// unless Options.Force. The branch goes with the worktree unless Options.KeepBranch:
// quietly when every commit of it is on a remote branch, in the main checkout or on the
// base it was cut from, and after a question otherwise, whose answers are to push the
// branch first, keep it, delete it anyway, or stop; Options.DeleteBranch answers delete.
func Remove(main, path string, o Options) (Removed, error) {
	r := Removed{Path: path, Main: main}
	if path == main {
		return r, fmt.Errorf("%s is the main checkout, not a worktree", path)
	}
	branch, err := git(path, "branch", "--show-current")
	if err != nil {
		return r, fmt.Errorf("%s is not a worktree of this repository", path)
	}
	r.Branch = branch
	if remote := remoteOf(main); remote != "" {
		r.Upstream = remote + "/" + branch
	}
	if !o.Force {
		if status, _ := git(path, "status", "--porcelain", "--untracked-files=no"); status != "" {
			return r, fmt.Errorf("%s has uncommitted changes; commit or stash them, or remove with --force", path)
		}
	}
	var last string
	if branch != "" {
		r.Own, last = own(main, path, branch)
	}
	deleteBranch := branch != "" && !o.KeepBranch
	if deleteBranch {
		if r.Own > 0 && !o.DeleteBranch {
			held := fmt.Sprintf("branch %s holds %d commit%s no remote branch, the main checkout or its base holds", branch, r.Own, plural(r.Own))
			if o.Ask == nil {
				return r, fmt.Errorf("%s; push them, or remove with --keep-branch or --delete-branch", held)
			}
			answer, err := o.Ask(fmt.Sprintf("%s (last: %q).\n  [p]ush it and delete, [k]eep it, [d]elete it anyway (git reflog finds the commits for 30 days), or [a]bort? [p/k/d/A] ", held, last))
			if err != nil {
				return r, err
			}
			switch answer {
			case "p", "push":
				if r.Upstream == "" {
					return r, errors.New("no remote to push to")
				}
				if _, err := git(path, "push", "--quiet", "--set-upstream", strings.SplitN(r.Upstream, "/", 2)[0], branch); err != nil {
					return r, fmt.Errorf("git push: %w", err)
				}
				r.Pushed = true
			case "k", "keep":
				deleteBranch = false
			case "d", "delete":
			default:
				return r, errors.New("nothing removed")
			}
		}
	}
	for _, command := range o.Remove {
		if err := run(command, path, main, branch, o.Output); err != nil {
			return r, &RunError{Command: command, Path: path, Err: err}
		}
		r.Ran = append(r.Ran, command)
	}
	// git's own remove refuses untracked files, which a prepared worktree always has,
	// the links and the composed tree say; the checks above are the guard, so git is
	// told to go ahead.
	if _, err := git(main, "worktree", "remove", "--force", path); err != nil {
		return r, fmt.Errorf("git worktree remove: %w", err)
	}
	if deleteBranch {
		// -D, since the check above is stricter than git's own, which wants the branch
		// merged into its upstream or HEAD and knows nothing of the base or other remotes.
		if _, err := git(main, "branch", "-D", branch); err != nil {
			return r, fmt.Errorf("the worktree is removed, and git branch -D %s failed: %w", branch, err)
		}
		r.BranchDeleted = true
	}
	return r, nil
}

// Find returns the worktree the name means: a path, or a branch checked out in one.
func Find(main, name string) (Entry, error) {
	entries, err := List(main)
	if err != nil {
		return Entry{}, err
	}
	if abs, err := filepath.Abs(name); err == nil {
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			abs = real
		}
		for _, e := range entries {
			if e.Path == abs {
				return e, nil
			}
		}
	}
	for _, e := range entries {
		if e.Branch == name && e.Branch != "" {
			return e, nil
		}
	}
	return Entry{}, fmt.Errorf("no worktree is at %s or on a branch of that name; qory worktree list shows them", name)
}

// own counts the commits of the worktree's branch that nothing else holds: no remote
// ref, not the main checkout's HEAD, not the base the branch was cut from. It returns the
// count and the subject of the newest one. A branch whose every commit is elsewhere is
// safe to delete, and this is the count that says so.
func own(main, path, branch string) (int, string) {
	args := []string{"rev-list", "HEAD", "--not", "--remotes"}
	if head, err := git(main, "rev-parse", "HEAD"); err == nil {
		args = append(args, head)
	}
	if base := recordedBase(main, branch); base != "" {
		args = append(args, base)
	}
	out, err := git(path, args...)
	if err != nil || out == "" {
		return 0, ""
	}
	commits := strings.Split(out, "\n")
	subject, _ := git(path, "log", "-1", "--format=%s", commits[0])
	return len(commits), subject
}

// baseKey is the git config key, under branch.<name>, that holds the base a branch was
// cut from, so a later add knows what to rebase and a remove what the branch added.
const baseKey = "qory-base"

// recordBase writes base as the branch's recorded base.
func recordBase(dir, branch, base string) error {
	_, err := git(dir, "config", "branch."+branch+"."+baseKey, base)
	return err
}

// recordedBase is the base recorded for the branch, "" when none was or it no longer
// resolves.
func recordedBase(main, branch string) string {
	base, err := git(main, "config", "--get", "branch."+branch+"."+baseKey)
	if err != nil || base == "" || !resolves(main, base) {
		return ""
	}
	return base
}

// resolves reports whether ref names a commit of the repository.
func resolves(main, ref string) bool {
	_, err := git(main, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil
}

// baseRef resolves the ref a new branch starts from: the one given, else the remote's
// HEAD branch as <remote>/<branch>, else the main checkout's current branch. A given ref
// that does not resolve is an error naming it.
func baseRef(main, remote, base string) (string, error) {
	if base != "" {
		if !resolves(main, base) {
			return "", fmt.Errorf("base %s is not a branch, tag or commit of this repository", base)
		}
		return base, nil
	}
	if remote != "" {
		if head, err := git(main, "symbolic-ref", "--quiet", "--short", "refs/remotes/"+remote+"/HEAD"); err == nil && head != "" {
			return head, nil
		}
	}
	current, err := git(main, "branch", "--show-current")
	if err != nil || current == "" {
		return "", errors.New("no base: the remote has no HEAD branch and the main checkout is on no branch; give --base")
	}
	if _, err := git(main, "rev-parse", "--verify", "--quiet", current+"^{commit}"); err != nil {
		return "", fmt.Errorf("%s has no commit yet; a worktree branch starts from a commit, so commit once and add again", current)
	}
	return current, nil
}

// remoteOf is the repository's remote, origin when it has one, else the first, "" for none.
func remoteOf(main string) string {
	out, err := git(main, "remote")
	if err != nil || out == "" {
		return ""
	}
	remotes := strings.Split(out, "\n")
	for _, r := range remotes {
		if r == "origin" {
			return r
		}
	}
	return remotes[0]
}

// refExists reports whether the fully qualified ref exists.
func refExists(dir, ref string) bool {
	_, err := git(dir, "rev-parse", "--verify", "--quiet", ref)
	return err == nil
}

// bringState is what [bring] did with one path.
type bringState struct {
	kind int
	err  error
}

var (
	brought = bringState{kind: 1}
	kept    = bringState{kind: 2}
	missing = bringState{kind: 3}
)

// bring links, or copies, the path rel from the main checkout into the worktree: a
// relative symlink for a link, a file or directory copy otherwise. A path already in the
// worktree is kept; one absent in the main checkout is missing.
func bring(main, wt, rel string, link bool) bringState {
	src := filepath.Join(main, rel)
	dst := filepath.Join(wt, rel)
	if _, err := os.Lstat(src); err != nil {
		return missing
	}
	if _, err := os.Lstat(dst); err == nil {
		return kept
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return bringState{err: err}
	}
	if link {
		target, err := filepath.Rel(filepath.Dir(dst), src)
		if err != nil {
			target = src
		}
		if err := os.Symlink(target, dst); err != nil {
			return bringState{err: err}
		}
		return brought
	}
	if err := copyPath(src, dst); err != nil {
		return bringState{err: err}
	}
	return brought
}

// copyPath copies a file, or a directory with everything below it, keeping modes.
func copyPath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, info.Mode().Perm())
	}
	if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
		return err
	}
	items, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, it := range items {
		if err := copyPath(filepath.Join(src, it.Name()), filepath.Join(dst, it.Name())); err != nil {
			return err
		}
	}
	return nil
}

// run executes one configured command through the shell in dir, with the worktree, the
// main checkout and the branch in the environment, its output going to out.
func run(command, dir, main, branch string, out io.Writer) error {
	c := exec.Command("sh", "-c", command)
	c.Dir = dir
	c.Env = append(os.Environ(), "QORY_WORKTREE="+dir, "QORY_MAIN="+main, "QORY_BRANCH="+branch)
	if out != nil {
		c.Stdout, c.Stderr = out, out
	}
	return c.Run()
}

// git runs one git command in dir and returns its trimmed output; a failure carries the
// command's own message.
func git(dir string, args ...string) (string, error) {
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return "", errors.New(strings.TrimSpace(string(exit.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
