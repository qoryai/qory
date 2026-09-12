## qory worktree add

Add a worktree for a branch, prepare it, and compose the harness into it

### Synopsis

Add a worktree for a branch, prepare it, and compose the harness into it.

The worktree goes where worktree.dir and worktree.name in qory.yaml say, beside the main
checkout as wt-<branch> by default. A branch that exists locally is checked out; one that
exists on the remote is tracked; a new one is cut off --base, else worktree.base, else the
remote's HEAD branch, else the branch the main checkout is on, which has to hold a commit;
there is no upstream on the base: the worktree pushes to a remote branch of its own name,
created by the first push. A worktree already there on the branch is reused, and the
rows say so.

--base on a branch that already exists moves it: a branch with no commits of its own is
reset onto the base, one with commits has them rebased onto it, either after a question
or at once with --rebase. The base a branch was cut from is recorded in its git config,
which is how add knows what the branch's own commits are; a branch made without qory
counts from the remote's HEAD branch. A no keeps the branch where it is.

Then every worktree.link is linked and every worktree.copy copied from the main checkout,
every worktree.run.add is run in the worktree with QORY_WORKTREE, QORY_MAIN and
QORY_BRANCH set, and the harness is composed into it when the repository holds a
qory-stack.yaml or a qory.yaml naming one.

```
qory worktree add <branch> [flags]
```

### Options

```
      --base string   the branch, tag or commit a new branch starts from, or an existing one is moved onto (qory.yaml: worktree.base; default: the remote's HEAD branch)
      --fetch         fetch the remote first, so the base and the branch are the remote's
  -h, --help          help for add
      --no-compose    do not compose the harness into the worktree
      --path          print the worktree's path alone on stdout, the rows on stderr
      --rebase        move or rebase a branch that already exists onto --base without asking
```

### SEE ALSO

* [qory worktree](qory_worktree.md)	 - Add, remove and list the worktrees of the repository you stand in

