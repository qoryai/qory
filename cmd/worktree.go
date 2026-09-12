package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/ui"
	"github.com/qoryai/qory/internal/worktree"
)

// newWorktree builds the worktree verb and its subverbs: add, remove, list.
func newWorktree() *cobra.Command {
	c := &cobra.Command{
		Use:   "worktree",
		Short: "Add, remove and list the worktrees of the repository you stand in",
		Long: `Add, remove and list the worktrees of the repository you stand in.

A worktree is one branch checked out beside the main checkout, prepared the way the
repository's qory.yaml says under worktree: files linked or copied from the main checkout
or from elsewhere on the machine, commands run in the new worktree, and the harness
composed into it when the repository holds a stack. Where a worktree goes and what it is
called is the machine's choice, in the same section of the user's qory.yaml.

Shortcuts:
  wa  worktree add
  wr  worktree remove
  wl  worktree list`,
	}
	c.AddCommand(newWorktreeAdd("add"), newWorktreeRemove("remove"), newWorktreeList("list"))
	return c
}

// worktreeShortcuts are the hidden top-level shortcuts of the worktree verbs.
func worktreeShortcuts() []*cobra.Command {
	var cmds []*cobra.Command
	for _, c := range []*cobra.Command{newWorktreeAdd("wa"), newWorktreeRemove("wr"), newWorktreeList("wl")} {
		c.Hidden = true
		cmds = append(cmds, c)
	}
	return cmds
}

// worktreeOptions reads the configuration's worktree section for the checkout at root
// into the package's options, with git.timeout as the bound on the fetch. own is false
// under extends, and the worktree section is read either way.
func worktreeOptions(root string) (worktree.Options, error) {
	conf, err := config.Load(root, true)
	if err != nil {
		return worktree.Options{}, input(err)
	}
	// A file whose qory key excludes this qory is refused before a worktree is made,
	// since the compose after it would refuse the same file.
	checks := newQoryChecks(root)
	for _, r := range conf.Qory {
		if err := checks.check(r.File, "the file", r.Qory); err != nil {
			return worktree.Options{}, err
		}
	}
	w := conf.Worktree
	return worktree.Options{Dir: w.Dir, Name: w.Name, Base: w.Base, KeepBranch: w.Branch == "keep", PullRef: w.PR, Link: paths(w.Link), Copy: paths(w.Copy), Add: w.Add, Remove: w.Remove, Timeout: conf.Git.Timeout}, nil
}

// paths is the configuration's link or copy list as the worktree package takes it.
func paths(in []config.Path) []worktree.Path {
	var out []worktree.Path
	for _, p := range in {
		out = append(out, worktree.Path{From: p.From, To: p.To})
	}
	return out
}

// asker returns what answers a question the worktree package asks: the question written
// to rows, one line read from in. It returns nil when in is a file that is not a
// terminal, so a script gets an error naming the flag that answers instead of a hang;
// any other reader, a test's, is read. An end of input is an empty answer.
func asker(in io.Reader, rows io.Writer) func(string) (string, error) {
	if f, ok := in.(*os.File); ok && !term.IsTerminal(f.Fd()) {
		return nil
	}
	r := bufio.NewReader(in)
	return func(question string) (string, error) {
		fmt.Fprint(rows, "  "+question)
		line, err := r.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		if !strings.HasSuffix(line, "\n") {
			fmt.Fprintln(rows)
		}
		return strings.ToLower(strings.TrimSpace(line)), nil
	}
}

// tracing sets what --verbose adds on an add or a remove: each git command and the
// reasoning go to the rows as they happen, and so does what every configured command
// prints. Without it the commands' output is kept, and a failing command shows its own.
func tracing(cmd *cobra.Command, o *worktree.Options, u *ui.UI, rows io.Writer) {
	if !verbose(cmd) {
		return
	}
	o.Trace = func(line string) { u.Text(line) }
	o.Output = rows
}

// inside reports whether the process stands in dir or below it.
func inside(dir string) bool {
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = real
	}
	rel, err := filepath.Rel(dir, cwd)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// mainCheckout finds the main checkout of the repository the process stands in.
func mainCheckout() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	main, err := worktree.Main(cwd)
	if err != nil {
		return "", input(err)
	}
	return main, nil
}

// newWorktreeAdd builds the add verb: git makes the worktree, the configuration prepares
// it, and the harness is composed into it when the repository holds a stack. With --path,
// the rows go to stderr and stdout carries the worktree's path alone, for a shell to cd
// into; the shell function qory shell init writes does that.
func newWorktreeAdd(use string) *cobra.Command {
	var base, file, remoteBranch string
	var pr int
	var offline, rebase, pathOnly, noCompose bool
	c := &cobra.Command{
		Use:   use + " [<branch>]",
		Short: "Add a worktree for a branch, prepare it, and compose the harness into it",
		Long: `Add a worktree for a branch, prepare it, and compose the harness into it.

The remote is fetched first, whole, so the base is the remote's tip and a branch pushed
from another machine is found; git.timeout in qory.yaml bounds the fetch. A fetch that
fails stops the add, and --offline skips it to go on with the refs already fetched.

The worktree goes where worktree.dir and worktree.name in qory.yaml say, beside the main
checkout as wt-<branch> by default. A branch that exists locally is checked out; one that
exists on the remote is tracked; a new one is cut off --base, else worktree.base, else the
remote's HEAD branch, else the branch the main checkout is on, which has to hold a commit;
there is no upstream on the base: the worktree pushes to a remote branch of its own name,
created by the first push. A worktree already there on the branch is reused, and the
rows say so.

--branch attaches the worktree to a branch of the remote, to go on with work pushed from
elsewhere: the branch is fetched, checked out under its own name and set to track the
remote's, and one the remote does not have is refused instead of cut new. A local branch
of that name is fast-forwarded to the remote's when that is possible. --pr attaches it to
a pull request the same way, by number: the pull request's head is found among the refs
the remote publishes, refs/pull/<n>/head on GitHub and Forgejo, refs/merge-requests/<n>/head
on GitLab, refs/pull-requests/<n>/from on Bitbucket Server, or the ref worktree.pr in
qory.yaml names with {n} for the number; no hosting API is asked. The branch of the
remote at that head is the one checked out, so a push goes to the pull request; a head
that no branch of the remote holds, a fork's, is checked out as pr-<n>, pulled from its
ref and pushed nowhere. With either flag the branch may be left out, and when given it
names the worktree instead: qory worktree add review --pr 7 makes ../wt-review.

--base on a branch that already exists moves it: a branch with no commits of its own is
reset onto the base, one with commits has them rebased onto it, either after a question
or at once with --rebase. The base a branch was cut from is recorded in its git config,
which is how add knows what the branch's own commits are; a branch made without qory
counts from the remote's HEAD branch. A no keeps the branch where it is.

Then every worktree.link is linked and every worktree.copy copied into the worktree: a
path named alone comes from the main checkout, {from: <path>, to: <path>} from anywhere
on the machine, from absolute or under ~, to the path in the worktree. A destination
already there is kept, a dangling link too, and a source that is not there is reported.
Every worktree.run.add is run in the worktree with QORY_WORKTREE, QORY_MAIN, QORY_BRANCH
and, when the branch's base is recorded, QORY_BASE set, and the harness is composed into
it when the repository holds a qory-stack.yaml or a qory.yaml naming one. -f names the
stack to compose instead, as it does on harness compose: a stack the worktree's own
document extends composes on it as its base, which is how a runner holding the stack
tree supplies one.

--verbose prints each git command as it runs and what every worktree.run.add command
prints, and the compose lists its entries. Without it a command's output is shown only
when the command fails.`,
		Args: maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("pr") && pr < 1 {
				return input(fmt.Errorf("--pr takes a pull request number, got %d", pr))
			}
			if len(args) == 0 && remoteBranch == "" && pr == 0 {
				return input(fmt.Errorf("%s takes a branch name, or --branch or --pr to say which branch of the remote to attach to", cmd.CommandPath()))
			}
			main, err := mainCheckout()
			if err != nil {
				return err
			}
			o, err := worktreeOptions(main)
			if err != nil {
				return err
			}
			if base != "" {
				o.Base, o.Onto = base, true
			}
			o.Offline, o.Rebase = offline, rebase
			var branch string
			if len(args) == 1 {
				branch = args[0]
			}
			out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
			rows := out
			if pathOnly {
				rows = errOut
			}
			o.Ask = asker(cmd.InOrStdin(), rows)
			u := ui.New(rows)
			tracing(cmd, &o, u, rows)
			title := branch
			if remoteBranch != "" || pr != 0 {
				var r worktree.Remote
				if pr != 0 {
					r, err = worktree.PullRequest(main, pr, o)
				} else {
					r, err = worktree.RemoteBranch(main, remoteBranch, o)
				}
				if err != nil {
					return input(err)
				}
				o.Attach, o.As = r, branch
				branch = r.Name()
				if title == "" {
					title = branch
				}
			}
			u.Title(filepath.Base(main), "worktree "+title)
			a, err := worktree.Add(main, branch, o)
			var runErr *worktree.RunError
			if errors.As(err, &runErr) {
				printAdded(u, main, a)
				return input(err)
			}
			if err != nil {
				return input(err)
			}
			printAdded(u, main, a)
			if !noCompose {
				if _, err := config.DiscoverStack(a.Path); err == nil || file != "" {
					u.Blank()
					if err := runCompose(rows, errOut, composeOptions{dir: a.Path, file: file, verbose: verbose(cmd)}); err != nil {
						return err
					}
				} else {
					u.Fields([][2]string{{"compose", "skipped  (no stack to compose)"}})
				}
			}
			if pathOnly {
				fmt.Fprintln(out, a.Path)
			}
			return nil
		},
	}
	c.Flags().StringVar(&base, "base", "", "the branch, tag or commit a new branch starts from, or an existing one is moved onto (qory.yaml: worktree.base; default: the remote's HEAD branch)")
	c.Flags().StringVar(&remoteBranch, "branch", "", "a branch of the remote to attach to: fetched, checked out and tracked; <branch> then names the worktree")
	c.Flags().IntVar(&pr, "pr", 0, "a pull request of the remote to attach to, by number: its branch fetched, checked out and tracked; <branch> then names the worktree")
	c.Flags().BoolVar(&rebase, "rebase", false, "move or rebase a branch that already exists onto --base without asking")
	c.MarkFlagsMutuallyExclusive("branch", "pr", "base")
	c.Flags().BoolVar(&offline, "offline", false, "do not fetch the remote first; use the refs already fetched")
	c.MarkFlagsMutuallyExclusive("offline", "branch")
	c.MarkFlagsMutuallyExclusive("offline", "pr")
	c.Flags().BoolVar(&pathOnly, "path", false, "print the worktree's path alone on stdout, the rows on stderr")
	c.Flags().BoolVar(&noCompose, "no-compose", false, "do not compose the harness into the worktree")
	c.Flags().StringVarP(&file, "file", "f", "", "the qory-stack.yaml to compose into the worktree, or the qory.yaml or harness.yaml whose harness section to compose, instead of discovering one; a stack named here is the base of the worktree's own document, as on harness compose")
	c.MarkFlagsMutuallyExclusive("file", "no-compose")
	return c
}

// shortPath is path relative to the main checkout when that reads shorter, as
// ../wt-feature for a sibling, and "." for the main checkout itself.
func shortPath(main, path string) string {
	rel, err := filepath.Rel(main, path)
	if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)+"..") {
		return path
	}
	return rel
}

// printAdded prints the rows of an add: the path, the branch and where it came from, the
// upstream, and what was linked, copied, kept, missing and run.
func printAdded(u *ui.UI, main string, a worktree.Added) {
	path := shortPath(main, a.Path)
	if inside(a.Path) {
		path += "  (you are in it)"
	}
	rows := [][2]string{{"path", path}}
	if a.How != "" {
		rows = append(rows, [2]string{"branch", a.Branch + "  (" + a.How + ")"})
	}
	if a.Upstream != "" {
		rows = append(rows, [2]string{"pushes to", a.Upstream})
	}
	if a.Head != "" {
		rows = append(rows, [2]string{"pulls from", a.Head + "  (no branch of the remote holds it, a fork's or a deleted one; a push from here goes nowhere)"})
	}
	for _, p := range a.Linked {
		rows = append(rows, [2]string{"linked", p.To + "  (" + from(p) + ")"})
	}
	for _, p := range a.Copied {
		rows = append(rows, [2]string{"copied", p.To + "  (" + from(p) + ")"})
	}
	for _, p := range a.Kept {
		rows = append(rows, [2]string{"kept", p.To + "  (already in the worktree)"})
	}
	for _, p := range a.Missing {
		rows = append(rows, [2]string{"missing", p.To + "  (" + notAt(p) + "; nothing linked)"})
	}
	for _, c := range a.Ran {
		rows = append(rows, [2]string{"ran", c})
	}
	u.Fields(rows)
}

// from says where a brought path came from: the main checkout, a path in it, or a path
// outside it with the home directory as ~.
func from(p worktree.Path) string {
	switch {
	case p.Outside():
		return "from " + ui.Short(p.From, "")
	case p.From == p.To:
		return "from the main checkout"
	default:
		return "from " + p.From + " in the main checkout"
	}
}

// notAt says where a missing path was looked for.
func notAt(p worktree.Path) string {
	switch {
	case p.Outside():
		return "not at " + ui.Short(p.From, "")
	case p.From == p.To:
		return "not in the main checkout"
	default:
		return p.From + " is not in the main checkout"
	}
}

// newWorktreeRemove builds the remove verb. With --path, stdout carries the main
// checkout's path alone, for a shell to go back to.
func newWorktreeRemove(use string) *cobra.Command {
	var force, keepBranch, deleteBranch, pathOnly bool
	c := &cobra.Command{
		Use:   use + " [<branch, name or path>]",
		Short: "Remove a worktree, the one you stand in by default, and its branch",
		Long: `Remove a worktree, the one you stand in by default, and its branch. A worktree is named
by its branch, its path, or the name it was added as beside --branch or --pr.

Every worktree.run.remove of qory.yaml runs in the worktree first, with QORY_WORKTREE,
QORY_MAIN, QORY_BRANCH and, when the branch's base is recorded, QORY_BASE set. A worktree
with uncommitted changes to tracked files is refused unless --force.

The branch goes with the worktree, unless --keep-branch or worktree.branch: keep in
qory.yaml. It goes quietly when every commit of it is on a remote branch, in the main
checkout or on the base it was cut from. A branch holding commits nothing else does is
asked about: push it and delete, keep it, delete it anyway, or stop; --delete-branch
answers delete, and the deleted commits stay in git's reflog for 30 days.

--verbose prints how the branch's own commits were counted, each git command as it
runs, and what every worktree.run.remove command prints. Without it a command's output
is shown only when the command fails.`,
		Args: maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			main, err := mainCheckout()
			if err != nil {
				return err
			}
			o, err := worktreeOptions(main)
			if err != nil {
				return err
			}
			o.Force, o.DeleteBranch = force, deleteBranch
			if keepBranch {
				o.KeepBranch = true
			}
			if deleteBranch {
				o.KeepBranch = false
			}
			var target worktree.Entry
			if len(args) == 1 {
				// The name is a path, a branch, or what the worktree was added as, which
				// is its branch unless a name was given beside --branch or --pr.
				if target, err = worktree.Find(main, args[0]); err != nil {
					if target, err = worktree.Find(main, worktree.PathFor(main, args[0], o)); err != nil {
						return input(fmt.Errorf("no worktree is at %s, on a branch of that name or added as it; qory worktree list shows them", args[0]))
					}
				}
			} else {
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				root, err := checkout.Root(cwd)
				if err != nil {
					return err
				}
				if target, err = worktree.Find(main, root); err != nil {
					return input(err)
				}
			}
			out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
			rows := out
			if pathOnly {
				rows = errOut
			}
			o.Ask = asker(cmd.InOrStdin(), rows)
			u := ui.New(rows)
			tracing(cmd, &o, u, rows)
			u.Title(filepath.Base(main), "worktree remove "+shortPath(main, target.Path))
			r, err := worktree.Remove(main, target.Path, o)
			if err != nil {
				return input(err)
			}
			fields := [][2]string{}
			for _, c := range r.Ran {
				fields = append(fields, [2]string{"ran", c})
			}
			fields = append(fields, [2]string{"removed", shortPath(main, r.Path)}, [2]string{"branch", branchRow(r)}, [2]string{"main", r.Main})
			u.Fields(fields)
			if pathOnly {
				fmt.Fprintln(out, r.Main)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&force, "force", false, "remove a worktree with uncommitted changes")
	c.Flags().BoolVar(&keepBranch, "keep-branch", false, "keep the branch after the worktree (qory.yaml: worktree.branch)")
	c.Flags().BoolVar(&deleteBranch, "delete-branch", false, "delete the branch even when it holds commits nothing else does, without asking")
	c.Flags().BoolVar(&pathOnly, "path", false, "print the main checkout's path alone on stdout, the rows on stderr")
	c.MarkFlagsMutuallyExclusive("keep-branch", "delete-branch")
	return c
}

// branchRow is the branch row of a remove: the branch and, in parentheses, what became
// of it and why.
func branchRow(r worktree.Removed) string {
	own := fmt.Sprintf("%d commit%s nothing else holds", r.Own, map[bool]string{true: "", false: "s"}[r.Own == 1])
	switch {
	case r.BranchDeleted && r.Pushed:
		return r.Branch + "  (pushed to " + r.Upstream + ", then deleted)"
	case r.BranchDeleted && r.Own > 0:
		return r.Branch + "  (deleted with " + own + "; git reflog finds " + map[bool]string{true: "it", false: "them"}[r.Own == 1] + " for 30 days)"
	case r.BranchDeleted:
		return r.Branch + "  (deleted; every commit of it is on the remote, in the main checkout or on its base)"
	case r.Own > 0:
		return r.Branch + "  (kept with " + own + "; git branch -D " + r.Branch + " deletes it)"
	default:
		return r.Branch + "  (kept; git branch -d " + r.Branch + " deletes it)"
	}
}

// newWorktreeList builds the list verb.
func newWorktreeList(use string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: "List the repository's worktrees with their branches",
		Long: `List the repository's worktrees with their branches, the main checkout first, and
which of them hold a composed harness.

--verbose adds the path of each composed worktree's report.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			main, err := mainCheckout()
			if err != nil {
				return err
			}
			entries, err := worktree.List(main)
			if err != nil {
				return input(err)
			}
			u := ui.New(cmd.OutOrStdout())
			u.Title(filepath.Base(main), "worktrees")
			var rows [][]string
			for _, e := range entries {
				branch := e.Branch
				if branch == "" {
					branch = "(detached)"
				}
				var notes []string
				if e.Main {
					notes = append(notes, "main")
				}
				if e.Composed {
					notes = append(notes, "composed")
				}
				row := []string{shortPath(main, e.Path), branch, strings.Join(notes, ", ")}
				if verbose(cmd) && e.Report != "" {
					row = append(row, shortPath(main, e.Report))
				}
				rows = append(rows, row)
			}
			u.Table(rows)
			return nil
		},
	}
}

// exactArgs is [cobra.ExactArgs] returning an input error that names what is expected.
func exactArgs(n int, what string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return input(fmt.Errorf("%s takes %s, got %d argument%s", cmd.CommandPath(), what, len(args), map[bool]string{true: "", false: "s"}[len(args) == 1]))
		}
		return nil
	}
}

// maxArgs is [cobra.MaximumNArgs] returning an input error.
func maxArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		return input(cobra.MaximumNArgs(n)(cmd, args))
	}
}
