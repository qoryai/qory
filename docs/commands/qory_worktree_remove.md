## qory worktree remove

Remove a worktree, the one you stand in by default, and keep its branch

### Synopsis

Remove a worktree, the one you stand in by default, and keep its branch.

Every worktree.run.remove of qory.yaml runs in the worktree first. A worktree with
uncommitted changes to tracked files, or whose branch holds commits no remote branch
holds, is refused unless --force. The branch stays unless --delete-branch.

```
qory worktree remove [<branch or path>] [flags]
```

### Options

```
      --delete-branch   delete the branch after the worktree
      --force           remove a worktree with uncommitted changes or unpushed commits
  -h, --help            help for remove
      --path            print the main checkout's path alone on stdout, the rows on stderr
```

### SEE ALSO

* [qory worktree](qory_worktree.md)	 - Add, remove and list the worktrees of the repository you stand in

