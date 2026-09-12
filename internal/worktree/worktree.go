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
	// How says where the branch came from: "new off <base>", "local", "remote", or
	// "already there" for a worktree that was already there on that branch, and what
	// Options.Onto did with it after a semicolon.
	How string
	// Upstream is the remote branch the worktree pushes to, "" without a remote.
	Upstream string
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

// PathFor is the directory the worktree of branch gets, from Options.Dir and
// Options.Name, absolute.
func PathFor(main, branch string, o Options) string {
	dir := o.Dir
	if dir == "" {
		dir = ".."
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(main, dir)
	}
	name := o.Name
	if name == "" {
		name = "wt-{branch}"
	}
	name = strings.ReplaceAll(name, "{branch}", strings.ReplaceAll(branch, "/", "-"))
	name = strings.ReplaceAll(name, "{repo}", filepath.Base(main))
	return filepath.Clean(filepath.Join(dir, name))
}

// Add makes the worktree of branch for the repository whose main checkout is main, and
// prepares it. A directory already at the path is reused when it is a worktree on that
// branch and refused otherwise. The branch is checked out when it exists locally, tracked
// when it exists on the remote, and cut off the base otherwise, with its upstream set to
// the remote branch of its own name and the base it was cut from recorded in its git
// config. With Options.Onto, a branch that existed is moved or rebased onto Options.Base,
// see [onto]. Then every Options.Link is linked and every Options.Copy copied from the
// main checkout, each skipped with a note when it is already in the worktree or absent
// in the main checkout, and every Options.Add is run in the worktree with QORY_WORKTREE,
// QORY_MAIN and QORY_BRANCH set; the first failing command is a [*RunError].
func Add(main, branch string, o Options) (Added, error) {
	a := Added{Path: PathFor(main, branch, o), Branch: branch}
	if branch == "" || strings.HasPrefix(branch, "-") || strings.ContainsAny(branch, " ~^:?*[\\") || strings.HasSuffix(branch, "/") || strings.Contains(branch, "..") {
		return a, fmt.Errorf("%q is not a branch name", branch)
	}
	remote := remoteOf(main)
	if o.Fetch && remote != "" {
		if _, err := git(main, "fetch", "--quiet", remote); err != nil {
			return a, fmt.Errorf("git fetch %s: %w", remote, err)
		}
	}
	// A base that resolves to nothing is a mistake on every path, not only the one
	// that cuts a new branch off it.
	if o.Base != "" && !resolves(main, o.Base) {
		return a, fmt.Errorf("base %s is not a branch, tag or commit of this repository", o.Base)
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
	if remote != "" && a.How != "remote" {
		// The upstream is the branch's own name on the remote, set before that branch
		// exists there, so a push creates it and a pull, once it does, reads it.
		if _, err := git(a.Path, "config", "branch."+branch+".remote", remote); err != nil {
			return a, err
		}
		if _, err := git(a.Path, "config", "branch."+branch+".merge", "refs/heads/"+branch); err != nil {
			return a, err
		}
	}
	if remote != "" {
		a.Upstream = remote + "/" + branch
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
