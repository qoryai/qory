# Worktrees

A worktree is a second checkout of the same repository, on its own branch. `qory` gives
each branch its own worktree, beside the main checkout. It prepares the worktree and
composes the harness into it, so the agent can start right away.

## What a new worktree needs

The repository's `qory.yaml` says how to prepare a worktree. It is committed, beside the
`harness` section:

```yaml
# qory.yaml, committed, beside the harness section
apiVersion: qory.dev/v1alpha1
worktree:
  base: main                     # the base branch, read on the remote; default: the remote's HEAD
  link: [.env, .env.local]       # linked from the main checkout into the worktree
  copy: [config/local.json]      # copied once into the worktree
  run:
    add: [pnpm install]          # run in the new worktree
    remove: []                   # run in a worktree before it is removed
```

### Links and copies

- A path listed alone comes from the main checkout.
- `{from: <path>, to: <path>}` comes from anywhere on the machine. `from` is absolute or
  under `~`. `to` is the path in the worktree.
- A destination already in the worktree is kept. So is a dangling link.
- A source that is not there is reported.

### Commands it runs

`worktree.run.add` runs in the new worktree, after the links and copies.
`worktree.run.remove` runs in a worktree before it is removed. Both get these variables:

| Variable        | Value                                              |
| --------------- | -------------------------------------------------- |
| `QORY_WORKTREE` | the worktree's path                                |
| `QORY_MAIN`     | the main checkout's path                           |
| `QORY_BRANCH`   | the branch                                         |
| `QORY_BASE`     | the branch's base, when it is recorded             |

### The harness

The harness is composed into the new worktree when the repository contains a
`qory-stack.yaml`, or a `qory.yaml` that defines a stack.

- `-f` selects the stack to compose instead, as it does on `harness compose`. When the
  worktree's own document extends a stack, the one `-f` selects is its base. That is how
  a runner that has the stack tree supplies one.
- `--no-compose` composes nothing.

## Commands

```sh
qory worktree add feature        # ../wt-feature on branch feature, pushing to origin/feature
qory worktree add feature --base v1.2.0   # a branch that exists is moved onto the base, after a question
qory worktree add feature --offline       # without the fetch every add starts with
qory worktree add --branch feature   # attach to the remote's feature: fetched, tracked, refused when the remote lacks it
qory worktree add --pr 7             # attach to pull request 7: its branch, found among the remote's refs
qory worktree add review --pr 7      # the same, in ../wt-review
qory worktree remove             # the worktree you stand in, and its branch
qory worktree list               # every worktree and its branch, the main checkout first
qory setup shell                 # make your shell cd into a new worktree, and back on remove
```

`-v` on `add` or `remove` prints each git command as it runs. It also prints what the
configured commands print. On `add`, the compose lists its entries too. On `remove`, it
also shows how the branch's own commits were counted. Without `-v`, a command's output
shows only when the command fails.

## Adding a worktree

Every add fetches the whole remote first. So:

- A new branch starts at the remote's tip.
- A branch pushed from another machine is found and tracked, not cut anew.

`git.timeout` in `qory.yaml` bounds the fetch. A fetch that fails stops the add. With
`--offline`, the add goes on with the refs already there.

### Which branch

- A branch that exists locally is checked out.
- A branch that exists on the remote is tracked.
- A new branch is cut from the base.

The base is the first of these that is set:

1. `--base`,
2. `worktree.base`,
3. the remote's HEAD branch,
4. the branch the main checkout is on. It has to have a commit.

A base that matches a branch of the remote is read there, as `<remote>/<base>`. So a
branch you never checked out works as a base, and a local copy that fell behind is not
used. `heads/<base>` selects the local branch instead.

The base is not the worktree's upstream. The worktree pushes to a remote branch of its
own name, which the first push creates.

A worktree already on the branch is reused, and the rows say so.

### Moving a branch onto a new base

`--base` on a branch that already exists moves it:

- A branch with no commits of its own is reset onto the base.
- A branch with commits has them rebased onto the base.

`qory` asks first. `--rebase` moves it at once. A no keeps the branch where it is.

`qory` records the base a branch was cut from in its git config, as
`branch.<name>.qory-base`. That is how it knows the branch's own commits. A branch made
without `qory` counts from the remote's HEAD branch.

## Attaching to a remote branch or a pull request

`--branch` attaches the worktree to a branch of the remote, to go on with work pushed
from elsewhere:

- The branch is fetched, checked out under its own name, and set to track the remote's.
- A branch the remote does not have is refused, not cut new.
- A local branch of that name is fast-forwarded to the remote's, when that is possible.

`--pr` attaches to a pull request the same way, by number. The branch of the remote at
the pull request's head is the one checked out, so a push goes to the pull request.

A pull request from a fork has a head on no branch of the remote. It is checked out as
`pr-<n>`, pulled from its ref, and pushed nowhere.

With either flag the branch argument may be left out. When given, it names the worktree:
`qory worktree add review --pr 7` makes `../wt-review`.

### Where a pull request's head is found

`--pr` sends no request to a hosting API. A pull request's head is one of the refs the
remote publishes:

| Host             | Ref                              |
| ---------------- | -------------------------------- |
| GitHub, Forgejo  | `refs/pull/<n>/head`             |
| GitLab           | `refs/merge-requests/<n>/head`   |
| Bitbucket Server | `refs/pull-requests/<n>/from`    |

For another host, set `worktree.pr: refs/.../{n}/...` in the repository's `qory.yaml`.

Some hosts publish no such ref, such as Bitbucket Cloud. There, use `--branch` with the
pull request's branch.

## Removing a worktree

`qory worktree remove` removes the worktree you stand in. Name another by its branch, its
path, or the name it was added as beside `--branch` or `--pr`.

`worktree.run.remove` runs in the worktree first. A worktree with uncommitted changes to
tracked files is refused, unless `--force`.

### The branch

The branch goes with the worktree.

- It goes quietly when every commit of it is already somewhere else: on a remote branch,
  in the main checkout, or on the base it was cut from.
- It also goes quietly when its change landed on the base by a squash or rebase merge,
  which writes new commits. `qory` fetches the base and finds the branch's commits there
  by patch, one by one or as one whole change. `--offline` skips that fetch.
- Otherwise `qory` asks first. The answers are push and delete, keep, delete anyway, and
  stop.

`--keep-branch` keeps the branch. `worktree.branch: keep` makes that the default.
`--delete-branch` deletes it without asking. Deleted commits stay in git's reflog for 30
days.

## Listing worktrees

`qory worktree list` shows every worktree with its branch, the main checkout first. It
marks the ones with a composed harness. `-v` adds the path of each one's report.

## Following a worktree in your shell

A program cannot change the directory of the shell that ran it. `qory setup shell` adds a
shell function that does it. The function runs `worktree add` and `worktree remove` with
`--path`, and changes into the path they print: the new worktree, or the main checkout.

`--path` prints that path alone on standard output. The rows go to standard error.

## Your own settings

Where a worktree goes and what it is called is your choice, not the repository's. Set it
in your own `qory.yaml`, in `~/.config/qory`. `qory setup machine` writes that file.

- `worktree.dir`: where worktrees go, relative to the main checkout. Default: `..`.
- `worktree.name`: what a worktree is called. Default: `wt-{branch}`.
- `worktree.branch`: what happens to the branch on remove, `delete` or `keep`.

Some files live at a path on your machine that the repository's `qory.yaml` must not
mention. Link those from your own file too:

```yaml
# ~/.config/qory/qory.yaml
worktree:
  link: [{from: ~/secrets/app.env, to: .env}]   # this machine's path, linked as .env
```

See [the harness](harness.md#the-qoryyaml-files) for how the `qory.yaml` files work
together.
