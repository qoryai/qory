## qory worktree remove

Remove a worktree, the one you stand in by default, and its branch

### Synopsis

Remove a worktree, the one you stand in by default, and its branch. A worktree is
selected by its branch, its path, or the name it was added as beside --branch or --pr.

Every worktree.run.remove of qory.yaml runs in the worktree first, with QORY_WORKTREE,
QORY_MAIN, QORY_BRANCH and, when the branch's base is recorded, QORY_BASE set. A worktree
with uncommitted changes to tracked files is refused unless --force.

The branch goes with the worktree, unless --keep-branch or worktree.branch: keep in
qory.yaml. It goes quietly when every commit of it is on a remote branch, in the main
checkout or on the base it was cut from. It goes quietly too when its change landed on
the base by a squash or rebase merge, which writes new commits: the base is fetched, and
the branch's commits, or its whole change as one, are found there by patch; --offline
skips the fetch. For a branch with commits nothing else has, a question offers: push it
and delete, keep it, delete it anyway, or stop; --delete-branch answers delete, and the
deleted commits stay in git's reflog for 30 days.

--verbose prints how the branch's own commits were counted, each git command as it
runs, and what every worktree.run.remove command prints. Without it a command's output
is shown only when the command fails.

```
qory worktree remove [<branch, name or path>] [flags]
```

### Options

```
      --delete-branch   delete the branch even when it has commits nothing else has, without a question
      --force           remove a worktree with uncommitted changes
  -h, --help            help for remove
      --keep-branch     keep the branch after the worktree (qory.yaml: worktree.branch)
      --offline         do not fetch the base to see whether the branch landed on it; use the refs already fetched
      --path            print the main checkout's path alone on stdout, the rows on stderr
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory worktree](qory_worktree.md)	 - Add, remove and list the worktrees of the repository you stand in

