## qory worktree add

Add a worktree for a branch, ready to work

### Synopsis

Add a worktree for a branch, prepare it, and compose the harness into it.

The remote is fetched first, so a new branch starts at the remote's tip. --offline skips
the fetch.

A new branch starts from --base, else worktree.base, else the remote's HEAD branch. A
branch that exists, here or on the remote, is checked out as it is. With --base, it is
moved onto the base, after a question or at once with --rebase.

--branch attaches to a branch of the remote. --pr attaches to a pull request, by number.
With either, <branch> only names the worktree.

Then qory links and copies what worktree.link and worktree.copy list, runs
worktree.run.add, and composes the harness when the repository has a stack.

--verbose prints each git command, what the configured commands print, and the composed
entries. Without it, a command's output shows only when it fails.

More: https://github.com/qoryai/qory/blob/main/docs/worktrees.md

```
qory worktree add [<branch>] [flags]
```

### Examples

```
  qory worktree add feature                # ../wt-feature, on branch feature
  qory worktree add feature --base v1.2.0  # start from a tag
  qory worktree add --branch feature       # go on with a branch pushed from elsewhere
  qory worktree add --pr 7                 # check out pull request 7
  qory worktree add review --pr 7          # the same, in ../wt-review
```

### Options

```
      --base string     the branch, tag or commit a new branch starts from, or an existing one moves onto (qory.yaml: worktree.base; default: the remote's HEAD branch)
      --branch string   a branch of the remote to attach to; <branch> then names the worktree
  -f, --file string     the stack, qory.yaml or harness.yaml to compose, instead of the one found; as on harness compose
  -h, --help            help for add
      --no-compose      do not compose the harness into the worktree
      --offline         skip the fetch; use the refs already fetched
      --path            print only the worktree's path on stdout; the rows go to stderr
      --pr int          a pull request of the remote to attach to, by number; <branch> then names the worktree
      --rebase          move a branch that already exists onto --base without asking
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory worktree](qory_worktree.md)	 - Add, remove and list worktrees

