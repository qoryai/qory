## qory worktree add

Add a worktree for a branch, prepare it, and compose the harness into it

### Synopsis

Add a worktree for a branch, prepare it, and compose the harness into it.

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
when the command fails.

```
qory worktree add [<branch>] [flags]
```

### Options

```
      --base string     the branch, tag or commit a new branch starts from, or an existing one is moved onto (qory.yaml: worktree.base; default: the remote's HEAD branch)
      --branch string   a branch of the remote to attach to: fetched, checked out and tracked; <branch> then names the worktree
  -f, --file string     the qory-stack.yaml to compose into the worktree, or the qory.yaml or harness.yaml whose harness section to compose, instead of discovering one; a stack named here is the base of the worktree's own document, as on harness compose
  -h, --help            help for add
      --no-compose      do not compose the harness into the worktree
      --offline         do not fetch the remote first; use the refs already fetched
      --path            print the worktree's path alone on stdout, the rows on stderr
      --pr int          a pull request of the remote to attach to, by number: its branch fetched, checked out and tracked; <branch> then names the worktree
      --rebase          move or rebase a branch that already exists onto --base without asking
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help says what
```

### SEE ALSO

* [qory worktree](qory_worktree.md)	 - Add, remove and list the worktrees of the repository you stand in

