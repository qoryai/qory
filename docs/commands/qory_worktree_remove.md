## qory worktree remove

Remove a worktree and its branch

### Synopsis

Remove a worktree and its branch. By default, the worktree you stand in. Select
another by its branch, its path, or the name it was added as.

worktree.run.remove runs in the worktree first. A worktree with uncommitted changes is
refused unless --force.

The branch goes quietly when its work is already elsewhere: on the remote, in the main
checkout, or on its base, even when merged there by squash or rebase. Otherwise qory
asks: push, keep, delete anyway, or stop. --keep-branch keeps it. --delete-branch deletes
it without asking.

--verbose prints how the branch's own commits were counted, each git command, and what
the configured commands print.

More: https://github.com/qoryai/qory/blob/main/docs/worktrees.md

```
qory worktree remove [<branch, name or path>] [flags]
```

### Examples

```
  qory worktree remove                 # the worktree you stand in
  qory worktree remove feature         # by branch, path or name
  qory worktree remove --keep-branch   # keep the branch
```

### Options

```
      --delete-branch   delete the branch without asking, even with commits nothing else has
      --force           remove a worktree with uncommitted changes
  -h, --help            help for remove
      --keep-branch     keep the branch after the worktree (qory.yaml: worktree.branch)
      --offline         skip the fetch that checks whether the branch landed on its base
      --path            print only the main checkout's path on stdout; the rows go to stderr
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory worktree](qory_worktree.md)	 - Add, remove and list worktrees

