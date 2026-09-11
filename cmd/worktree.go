package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
repository's qory.yaml says under worktree: files linked or copied from the main checkout,
commands run in the new worktree, and the harness composed into it when the repository
holds a stack. Where a worktree goes and what it is called is the machine's choice, in
the same section of the user's qory.yaml.

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
// into the package's options. own is false under extends, and the worktree section is
// read either way.
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
	return worktree.Options{Dir: w.Dir, Name: w.Name, Base: w.Base, Link: w.Link, Copy: w.Copy, Add: w.Add, Remove: w.Remove}, nil
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
	var base string
	var fetch, pathOnly, noCompose bool
	c := &cobra.Command{
		Use:   use + " <branch>",
		Short: "Add a worktree for a branch, prepare it, and compose the harness into it",
		Long: `Add a worktree for a branch, prepare it, and compose the harness into it.

The worktree goes where worktree.dir and worktree.name in qory.yaml say, beside the main
checkout as wt-<branch> by default. A branch that exists locally is checked out; one that
exists on the remote is tracked; a new one is cut off --base, else worktree.base, else the
remote's HEAD branch, else the branch the main checkout is on, which has to hold a commit;
there is no upstream on the base: the worktree pushes to a remote branch of its own name,
created by the first push. Then every worktree.link is linked and
every worktree.copy copied from the main checkout, every worktree.run.add is run in the
worktree with QORY_WORKTREE, QORY_MAIN and QORY_BRANCH set, and the harness is composed
into it when the repository holds a qory-stack.yaml or a qory.yaml naming one.`,
		Args: exactArgs(1, "a branch name"),
		RunE: func(cmd *cobra.Command, args []string) error {
			main, err := mainCheckout()
			if err != nil {
				return err
			}
			o, err := worktreeOptions(main)
			if err != nil {
				return err
			}
			if base != "" {
				o.Base = base
			}
			o.Fetch = fetch
			out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
			rows := out
			if pathOnly {
				rows = errOut
			}
			o.Output = rows
			u := ui.New(rows)
			u.Title(filepath.Base(main), "worktree "+args[0])
			a, err := worktree.Add(main, args[0], o)
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
				if _, err := config.DiscoverStack(a.Path); err == nil {
					u.Blank()
					if err := runCompose(rows, errOut, composeOptions{dir: a.Path}); err != nil {
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
	c.Flags().StringVar(&base, "base", "", "the branch, tag or commit a new branch starts from (qory.yaml: worktree.base; default: the remote's HEAD branch)")
	c.Flags().BoolVar(&fetch, "fetch", false, "fetch the remote first, so the base and the branch are the remote's")
	c.Flags().BoolVar(&pathOnly, "path", false, "print the worktree's path alone on stdout, the rows on stderr")
	c.Flags().BoolVar(&noCompose, "no-compose", false, "do not compose the harness into the worktree")
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
	rows := [][2]string{{"path", shortPath(main, a.Path)}}
	if a.How != "" {
		rows = append(rows, [2]string{"branch", a.Branch + "  (" + a.How + ")"})
	}
	if a.Upstream != "" {
		rows = append(rows, [2]string{"pushes to", a.Upstream})
	}
	for _, p := range a.Linked {
		rows = append(rows, [2]string{"linked", p + "  (from the main checkout)"})
	}
	for _, p := range a.Copied {
		rows = append(rows, [2]string{"copied", p + "  (from the main checkout)"})
	}
	for _, p := range a.Kept {
		rows = append(rows, [2]string{"kept", p + "  (already in the worktree)"})
	}
	for _, p := range a.Missing {
		rows = append(rows, [2]string{"missing", p + "  (not in the main checkout; nothing linked)"})
	}
	for _, c := range a.Ran {
		rows = append(rows, [2]string{"ran", c})
	}
	u.Fields(rows)
}

// newWorktreeRemove builds the remove verb. With --path, stdout carries the main
// checkout's path alone, for a shell to go back to.
func newWorktreeRemove(use string) *cobra.Command {
	var force, deleteBranch, pathOnly bool
	c := &cobra.Command{
		Use:   use + " [<branch or path>]",
		Short: "Remove a worktree, the one you stand in by default, and keep its branch",
		Long: `Remove a worktree, the one you stand in by default, and keep its branch.

Every worktree.run.remove of qory.yaml runs in the worktree first. A worktree with
uncommitted changes to tracked files, or whose branch holds commits no remote branch
holds, is refused unless --force. The branch stays unless --delete-branch.`,
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
			var target worktree.Entry
			if len(args) == 1 {
				if target, err = worktree.Find(main, args[0]); err != nil {
					return input(err)
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
			o.Output = rows
			u := ui.New(rows)
			u.Title(filepath.Base(main), "worktree remove "+shortPath(main, target.Path))
			r, err := worktree.Remove(main, target.Path, o)
			if err != nil {
				return input(err)
			}
			fields := [][2]string{}
			for _, c := range r.Ran {
				fields = append(fields, [2]string{"ran", c})
			}
			branch := r.Branch + "  (kept; git branch -d " + r.Branch + " deletes it)"
			if r.BranchDeleted {
				branch = r.Branch + "  (deleted)"
			}
			fields = append(fields, [2]string{"removed", shortPath(main, r.Path)}, [2]string{"branch", branch}, [2]string{"main", r.Main})
			u.Fields(fields)
			if pathOnly {
				fmt.Fprintln(out, r.Main)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&force, "force", false, "remove a worktree with uncommitted changes or unpushed commits")
	c.Flags().BoolVar(&deleteBranch, "delete-branch", false, "delete the branch after the worktree")
	c.Flags().BoolVar(&pathOnly, "path", false, "print the main checkout's path alone on stdout, the rows on stderr")
	return c
}

// newWorktreeList builds the list verb.
func newWorktreeList(use string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: "List the repository's worktrees with their branches",
		Args:  noArgs,
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
				rows = append(rows, []string{shortPath(main, e.Path), branch, strings.Join(notes, ", ")})
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
