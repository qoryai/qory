## qory worktree remove

Remove a worktree, the one you stand in by default, and its branch

### Synopsis

Remove a worktree, the one you stand in by default, and its branch.

Every worktree.run.remove of qory.yaml runs in the worktree first. A worktree with
uncommitted changes to tracked files is refused unless --force.

The branch goes with the worktree, unless --keep-branch or worktree.branch: keep in
qory.yaml. It goes quietly when every commit of it is on a remote branch, in the main
checkout or on the base it was cut from. A branch holding commits nothing else does is
asked about: push it and delete, keep it, delete it anyway, or stop; --delete-branch
answers delete, and the deleted commits stay in git's reflog for 30 days.

```
qory worktree remove [<branch or path>] [flags]
```

### Options

```
      --delete-branch   delete the branch even when it holds commits nothing else does, without asking
      --force           remove a worktree with uncommitted changes
  -h, --help            help for remove
      --keep-branch     keep the branch after the worktree (qory.yaml: worktree.branch)
      --path            print the main checkout's path alone on stdout, the rows on stderr
```

### SEE ALSO

* [qory worktree](qory_worktree.md)	 - Add, remove and list the worktrees of the repository you stand in

