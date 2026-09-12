## qory worktree

Add, remove and list the worktrees of the repository you stand in

### Synopsis

Add, remove and list the worktrees of the repository you stand in.

A worktree is one branch checked out beside the main checkout, prepared the way the
repository's qory.yaml says under worktree: files linked or copied from the main checkout,
commands run in the new worktree, and the harness composed into it when the repository
holds a stack. Where a worktree goes and what it is called is the machine's choice, in
the same section of the user's qory.yaml.

Shortcuts:
  wa  worktree add
  wr  worktree remove
  wl  worktree list

### Options

```
  -h, --help   help for worktree
```

### SEE ALSO

* [qory](qory.md)	 - Compose the harness a runtime loads from modules
* [qory worktree add](qory_worktree_add.md)	 - Add a worktree for a branch, prepare it, and compose the harness into it
* [qory worktree list](qory_worktree_list.md)	 - List the repository's worktrees with their branches
* [qory worktree remove](qory_worktree_remove.md)	 - Remove a worktree, the one you stand in by default, and its branch

