# 🐝 qory

A harness is what your coding agent reads before it works: instructions, skills, agents,
commands and settings. `qory` builds the harness of a repository from modules. Each module
is one piece of harness: the piece every repository of your team shares, the piece for
apps built on your framework, the piece this repository alone needs. The repository
commits the list of modules, not a copy of their files.

## The problem

Your coding agent reads its harness from the repository. You have many repositories, and
more than one agent. Claude Code reads `.claude`, Codex reads `.codex`, Gemini CLI reads
`.gemini`. So every repository holds one copy per tool. When the shared part improves, the
copies drift, and nothing tells you which version a checkout runs with.

<p align="center">
  <img src="docs/assets/slogan.png" alt="Don't worry, use Qory" width="720">
</p>

## What qory does

A stack lists modules in order: the harness your team shares, the harness for apps built
on the framework, the harness this repository alone needs. `qory` composes them into one
tree and writes that tree the way each tool reads it. The tree is linked into the checkout
and kept out of git. A report says which module every entry came from.

A module is written once and serves Claude Code, Codex CLI, Gemini CLI, OpenCode, Cursor,
GitHub Copilot CLI, Amp, Goose, and every tool that reads `AGENTS.md` and `.agents/skills`.
A checkout can serve two tools at once, and both read the same harness.

The model is Docker's: a module is an image, the stack is the Compose file, and the
composed tree is what runs. One rule differs on purpose. When two modules provide the same
entry, `qory` refuses to compose until the stack says which one to keep. Nothing wins by
coming last.

## Three steps

1. Write `qory.yaml` in the repository, or let `qory setup repo` write it:

   ```yaml
   apiVersion: qory.dev/v1alpha1
   harness:
     target:
       runtime: claude          # or both at once: [claude, codex]
       model: opus
     modules:
       - name: core             # what every repository of yours gets, pinned to a tag
         source: {git: https://github.com/acme/harness, ref: v2.4.0, path: core}
       - name: nextjs           # the harness for Next.js apps
         source: {path: ../harness/nextjs}
       - name: app              # this repository's own, committed with it
         source: {path: ./harness}
   ```

   Every module has a `qory-module.yaml` that names it. A repository can also take a
   stack someone else delivers, a `qory-stack.yaml`: it names that stack under `extends`
   instead of `target`, and adds its own modules. The delivered modules cannot be changed.
   A runner that holds the stack tree names the base with `qory harness compose -f
   <stack>` instead, and the repository's file then names no version, ref or URL of it.
   A delivered stack states the qory it needs, `qory: ">=0.4.0"`, and every repository
   extending it inherits the range.

   ```yaml
   apiVersion: qory.dev/v1alpha1
   harness:
     extends: {git: https://github.com/acme/harness, ref: v2.4.0, stack: nextjs}
     modules:
       - name: marketing          # the harness repository exports it too
         source: {git: https://github.com/acme/harness, ref: v2.4.0, module: marketing}
       - name: app
         source: {path: ./harness}
   ```

   `stack` and `module` name what the harness repository publishes in the `exports`
   section of its own `qory.yaml`, so nobody outside it depends on its directories:

   ```yaml
   exports:
     dir: ./harness               # where stacks/ and modules/ are; default: the root
     stacks: [nextjs]
     modules: [core, nextjs, marketing]
   ```

2. Compose it:

   ```sh
   qory harness compose
   ```

3. Start your agent in the checkout. It reads the composed tree through the files it
   already looks for.

## Install

With Homebrew:

```sh
brew install qoryai/tap/qory
```

With the install script, which puts the release binary in `~/.local/bin`:

```sh
curl -fsSL https://raw.githubusercontent.com/qoryai/qory/main/install.sh | sh
```

From source, with Go 1.27 or later:

```sh
go install github.com/qoryai/qory@latest
```

## Try it

```sh
mkdir hello && cd hello
qory setup example       # writes a stack and two modules
qory harness compose     # or: qory hc; for Codex: qory hc --runtime codex
claude                   # type /hello
qory harness remove      # or: qory hr
```

Both modules ship a greet skill. The README that `qory setup example` writes says which
line to delete to watch `qory` refuse the collision. For a real repository, run
`qory setup repo` instead.

## Worktrees

One branch per worktree, beside the main checkout. `qory` prepares the worktree the way
the repository's `qory.yaml` says and composes the harness into it:

```yaml
# qory.yaml, committed, beside the harness section
apiVersion: qory.dev/v1alpha1
worktree:
  base: main                     # a new branch starts here; default: the remote's HEAD
  link: [.env, .env.local]       # linked from the main checkout into the worktree
  run:
    add: [pnpm install]          # run in the new worktree
```

```sh
qory worktree add feature        # ../wt-feature on branch feature, pushing to origin/feature
qory worktree add feature --base v1.2.0   # a branch that exists is moved onto the base, after a question
qory worktree add feature --offline       # without the fetch every add starts with
qory worktree add --branch feature   # attach to the remote's feature: fetched, tracked, refused when the remote lacks it
qory worktree add --pr 7             # attach to pull request 7: its branch, found among the remote's refs
qory worktree add review --pr 7      # the same, in ../wt-review
qory worktree remove             # the worktree you stand in, and its branch
qory setup shell                 # make your shell cd into a new worktree, and back on remove
```

Every add fetches the remote first, so a new branch starts at the remote's tip and a
branch pushed from another machine is tracked instead of cut anew; a fetch that fails
stops the add, and `--offline` goes on with the refs already there. The branch goes with
the worktree on remove: quietly when every commit of it is on the remote, in the main
checkout or on the base it was cut from, and after a question otherwise, with push,
keep, delete anyway and stop as the answers. `--keep-branch` keeps it, and
`worktree.branch: keep` makes that the default. `-v` on either verb prints each git
command as it runs, and what the configured commands print.

`--pr` asks no hosting API: the pull request's head is one of the refs the remote
publishes, `refs/pull/<n>/head` on GitHub and Forgejo, `refs/merge-requests/<n>/head` on
GitLab, `refs/pull-requests/<n>/from` on Bitbucket Server. A host with another layout
names it as `worktree.pr: refs/.../{n}/...` in the repository's `qory.yaml`; one that
publishes none, Bitbucket Cloud, takes `--branch` with the pull request's branch.

Where a worktree goes and what it is called is your choice, not the repository's:
`worktree.dir`, `worktree.name` and `worktree.branch` in your own `qory.yaml`, which
`qory setup machine` writes. A file the repository must not name goes there too:

```yaml
# ~/.config/qory/qory.yaml
worktree:
  link: [{from: ~/secrets/app.env, to: .env}]   # this machine's path, linked as .env
```

## Commands

```sh
qory setup repo          # write the repository's qory.yaml: its stack, module and worktree settings
qory setup example       # write the hello example into the current directory
qory setup machine       # write your qory.yaml in ~/.config/qory: how qory runs here
qory setup shell         # completions, and a shell that follows worktree add and remove
qory harness compose     # compose the stack into the checkout you stand in   (qory hc)
qory harness inspect     # the report: every entry and the module it came from (qory hi)
qory harness remove      # remove the composed tree and its links               (qory hr)
qory worktree add        # add a worktree for a branch and prepare it            (qory wa)
qory worktree remove     # remove a worktree and its branch                      (qory wr)
qory worktree list       # every worktree with its branch                        (qory wl)
qory config              # every setting, its value and the file it came from
```

Flags worth knowing on `compose`:

```
-f <stack>             compose this stack; as the base of the repository's document when that extends one
--dry-run              print the report and write nothing
--runtime claude,codex render for these runtimes instead of the stack's
--model opus           write this model instead of the stack's
--force                replace a tracked, unmodified file where a link goes
--update               fetch every git source again
--check                exit 6 when the composed tree is behind the stack and modules
```

Two `qory.yaml` files are read. Every setting has a default, so both are optional. The
repository's file is committed and holds what the repository decides: its stack, and what
a worktree needs; it may be named `harness.yaml` instead, for a file that says nothing of
the tool that reads it. Your file, in `~/.config/qory`, holds how `qory` runs on this machine
for every repository: the runtime to compose for, where worktrees go, the git timeout.
The repository's file is read on top of yours. `qory config` shows every setting and the
file it came from. The reference, one page per command, is under
[docs/commands](docs/commands/qory.md).

## What you get

- The files your tool reads, linked to a composed tree under `.qory` and kept out of git.
  Your own `settings.local.json` stays yours.
- Settings merged from every module, in the tool's own format. Permission lists and hooks
  join. A value set twice to different things is a collision, never a silent override.
- MCP servers, one file each in a module, written where every tool reads them.
- Modules from a directory beside the repository, or from a git repository at a tag,
  pinned by commit in the report. A repository that publishes stacks and modules lists
  them under `exports`, and a consumer names them, never their directories.
- One instruction file, joined from the modules in order, under the name each tool wants.
- A report that names the module of every entry. When two modules provide the same entry,
  a refusal with the lines that resolve it.
- Part of a module, when that is all you want. `exclude` leaves entries, the instruction
  section, settings fragments or variables out. `only` takes the named things, plus what
  they need as the module's manifest declares it, and nothing else: `only: {skills:
  [deploy]}` is the deploy skill, the command it runs and the agent it calls, from a
  module full of other things. A required entry you take from another module instead is
  one `exclude` line beside the `only`.

## The composed tree

The home, `.qory/harness`, holds one directory per runtime. Most of what is in it is a
symlink to a file in a module: every skill, agent, command and hook. Beside the links
are the generated copies: `AGENTS.md` at the root of the home, the instruction file each
runtime wants, its settings and its MCP file, each joined from the modules' fragments.

Editing a module's file is live through the link. Editing a module's instruction section
or a settings fragment is not: the merged copy is generated, and it stays as it was until
the next compose. `qory harness compose --check` compares the home with what the stack
and modules say now and exits 6 when a merged copy is behind, which is the check a CI
job runs.

What the checkout holds is links too. `.claude` is a real directory with one symlink per
entry, so Claude Code's own `settings.local.json` stays beside them; `.mcp.json` and a
root `AGENTS.md` are symlinks. A tool that walks the tree has to follow them:

- `find` needs `-L`.
- `grep -R` given a symlink as its operand without a trailing slash reads nothing with
  BSD grep on macOS and follows the link with GNU grep on Linux, and neither reports an
  error. Give it the directory with the slash, `grep -R pattern .claude/`, or use `-R -L`.
- `rg` needs `--follow`.

`git status` does not show the tree. Every path `qory` writes is listed in
`.git/info/exclude`, which every worktree of a repository shares.

## The format

The stack, the module manifest, `qory.yaml`, the composition rules and the runtimes are
specified in [contracts/harness/v1](contracts/harness/v1/README.md), with JSON schemas and
the fixtures the test suite runs.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Contributions are made under the agreement in
[CLA.md](CLA.md).

## Licence

Apache License 2.0. See `LICENSE`. Qory™ is a trademark of 8wonders GmbH;
`TRADEMARKS.md` says what you may do with the name.
