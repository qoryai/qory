# The harness

A harness is what your coding agent reads before it works: instructions, skills, agents,
commands and settings. `qory` builds the harness of a repository from modules. The
repository commits the list of modules, not a copy of their files.

## The problem

Your coding agent reads its harness from the repository. You have many repositories. You
use more than one agent. Each agent reads its own folder:

- Claude Code reads `.claude`.
- Codex reads `.codex`.
- Gemini CLI reads `.gemini`.

So every repository contains one copy per tool. The shared part improves, and the copies
drift. Nothing shows which version a checkout runs with.

## Modules and stacks

A **module** is one piece of harness. For example:

- the piece every repository of your team shares,
- the piece for apps built on your framework,
- the piece this repository alone needs.

A **stack** lists modules in order. `qory` composes them into one tree. It writes that
tree the way each tool reads it. The tree is linked into the checkout and kept out of
git. A report lists the module every entry came from.

A module is written once. It serves Claude Code, Codex CLI, Gemini CLI, OpenCode, Cursor,
GitHub Copilot CLI, Amp, Goose, and every tool that reads `AGENTS.md` and
`.agents/skills`. A checkout can serve two tools at once. Both read the same harness.

### Like Docker, with one difference

The model is Docker's:

- a module is an image,
- the stack is the Compose file,
- the composed tree is what runs.

One rule differs on purpose. When two modules provide the same entry, `qory` refuses to
compose. The stack selects which one to keep. Nothing wins by coming last.

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

   Every module has a `qory-module.yaml`. It defines the module's name.

2. Compose it:

   ```sh
   qory harness compose
   ```

3. Start your agent in the checkout. It reads the composed tree through the files it
   already looks for.

## Take a stack someone else delivers

A repository can also take a stack someone else delivers: a `qory-stack.yaml`. In its own
`qory.yaml`, the repository:

- selects that stack under `extends`,
- defines what it composes it for under `target`,
- adds its own modules.

```yaml
apiVersion: qory.dev/v1alpha1
harness:
  extends: {git: https://github.com/acme/harness, ref: v2.4.0, stack: nextjs}
  target: {runtime: claude, model: opus}
  modules:
    - name: marketing          # the harness repository exports it too
      source: {git: https://github.com/acme/harness, ref: v2.4.0, module: marketing}
    - name: app
      source: {path: ./harness}
```

The delivered modules cannot be changed.

A runner may already have the stack tree. It selects the base with
`qory harness compose -f <stack>` instead. The repository's file then contains no
version, ref or URL of it.

Under `extends`, the compose reads the repository's file for its document and its
`worktree` keys. Its `git` and `env` keys and its machine keys under `harness`, such as
`model` or `force`, are left out: your own `qory.yaml` and the files above the checkout
set those. The compose prints a `read` row with what it took from the file, and an
`ignored` row with the keys it left out, when the file sets any:

```text
read     qory.yaml  (target claude opus, 1 module, 12 extensions, worktree.base main)
ignored  qory.yaml: harness.model, env  (under extends, only ~/.config/qory/qory.yaml and the files above the checkout set these)
```

## Publish stacks and modules

`stack` and `module` select what a harness repository publishes. It lists them in the
`exports` section of its own `qory.yaml`. So nobody outside it depends on its
directories:

```yaml
exports:
  dir: ./harness               # where stacks/ and modules/ are; default: the root
  stacks: [nextjs]
  modules: [core, nextjs, marketing]
```

The harness repository states the qory it needs once, in its own `qory.yaml`:
`qory: ">=0.5.0"`. That requirement applies to every stack it delivers.

## See a collision

`qory setup example` writes a stack and two modules. Both modules ship a greet skill. The
README it writes lists the line to delete. Delete it, and watch `qory` refuse the
collision.

For a real repository, run `qory setup repo` instead.

## What you get

- **The files your tool reads.** They are linked to a composed tree under `.qory` and
  kept out of git. Your own `settings.local.json` stays yours.
- **Settings merged from every module**, in the tool's own format. Permission lists join.
  Hooks join. A value set twice to different things is a collision. It is never a silent
  override.
- **MCP servers.** Each is one file in a module. `qory` writes them where every tool
  reads them.
- **Modules from two kinds of source:** a directory beside the repository, or a git
  repository at a tag. The report pins a git module by its commit.
- **Stacks and modules by name.** A repository that publishes stacks and modules lists
  them under `exports`. A consumer selects them by name, never by directory.
- **One instruction file**, joined from the modules in order, under the name each tool
  wants.
- **A report** that lists the module of every entry. Two modules provide the same entry?
  You get a refusal, with the lines that resolve it.
- **Part of a module**, when that is all you want. See below.

### Branches, tags and commits

A git source's `ref` is a branch, a tag or a full commit id. A branch follows the remote:
each compose takes its current commit, and the module's row shows when it moved, such as
`core  main c522a5b0b1c2 → 9f1e2d3a4b5c`. A tag or a full commit id stays pinned to its
commit. Offline, compose keeps the cached commit and warns. A branch that is gone from the
remote fails; to keep the old harness, set `ref` to a commit id.

### Take part of a module

- `exclude` leaves things out: entries, the instruction section, settings fragments or
  variables.
- `only` takes the listed things, plus what they need. The module's manifest declares
  what they need. Nothing else comes in.

For example, `only: {skills: [deploy]}` takes the deploy skill, the command it runs and
the agent it calls. The rest of the module stays out.

Sometimes `only` pulls in a required entry that you take from another module instead.
Leave it out with one `exclude` line beside the `only`.

## The composed tree

The composed tree lives in the **home**, `.qory/harness`. The home has one directory per
runtime.

Most of the home is symlinks to files in modules: every skill, agent, command and hook.

Beside the links are generated copies. Each is joined from the modules' fragments:

- `AGENTS.md`, at the root of the home,
- the instruction file each runtime wants,
- its settings,
- its MCP file.

### Editing a module

An edit to a module's file is live, through the link.

An edit to a module's instruction section or a settings fragment is not. The merged copy
is generated. It stays as it was until the next compose.

`qory harness compose --check` compares the home with what the stack and modules define.
It exits 6 when a merged copy is behind. That is the check a CI job runs.

### Links in the checkout

The checkout contains links too:

- `.claude` is a real directory with one symlink per entry. So Claude Code's own
  `settings.local.json` stays beside them.
- `.mcp.json` and a root `AGENTS.md` are symlinks.

A tool that walks the tree has to follow the links:

- `find` needs `-L`.
- `grep -R` needs care when its operand is a symlink without a trailing slash. BSD grep
  on macOS reads nothing. GNU grep on Linux follows the link. Neither reports an error.
  Pass the directory with the slash, `grep -R pattern .claude/`, or use `-R -L`.
- `rg` needs `--follow`.

### Out of git

`git status` does not show the tree. Every path `qory` writes is listed in
`.git/info/exclude`. Every worktree of a repository shares that file.

## A home outside the checkout

The tree can also live outside the checkout. Whatever composes it owns it: a workflow, or
a launcher. The checkout then contains nothing of it:

- no link,
- no `.qory`,
- no exclude line.

So a tracked `harness/` directory is no collision. A pull is an ordinary pull.

`--home <dir>` defines the directory. So does `harness.home` in your own `qory.yaml`.
Every checkout and worktree gets its own home under it.

Each tool takes such a home from its own command line or environment.
`qory harness launch` prints the command:

```sh
qory harness compose --home ~/.cache/qory/homes
eval "$(qory harness launch --runtime claude --home ~/.cache/qory/homes)"
```

For Claude Code, the command passes:

| Flag                          | What it carries                                                   |
| ----------------------------- | ----------------------------------------------------------------- |
| `--plugin-dir`                | a plugin the compose renders                                      |
| `--settings`                  | the permissions, hooks and model                                  |
| `--mcp-config`                | the servers                                                       |
| `--append-system-prompt-file` | the instructions                                                  |
| `--setting-sources user`      | no `.claude` of the checkout, or of a directory above it, is read |

The other tools:

| Tool     | Takes                                      |
| -------- | ------------------------------------------ |
| Cursor   | the same plugin as Claude Code             |
| Codex    | the tree, as its `CODEX_HOME`              |
| OpenCode | the tree, as its `OPENCODE_CONFIG_DIR`     |
| Copilot  | the skills and agents, through `--add-dir` |
| Amp      | its settings file                          |
| Gemini   | its settings file                          |

The [contract](../contracts/harness/v1/README.md) lists what each tool takes from outside,
and what it reads from the checkout.

### What `qory harness launch` prints

The command is one line, quoted for a POSIX shell. A launcher runs it as it is, with
arguments of its own after it:

```sh
cd <checkout> && eval "$(qory harness launch --runtime claude)"
```

- The line is the tool's own launch template: the program, the arguments that hand it the
  home's files, and the variables it takes them from. `${dir}` in a template is the
  tool's directory in the home.
- The line also sets the harness's variables, through `env`. No settings file holds them:
  - `QORY_HARNESS_HOME`, the home, comes first.
  - Then the fixed ones: qory's own. These are the template's variables.
  - Then the defaults: what an author wrote. These are `env` in `qory.yaml`, the `env` of
    `harness.launch.<runtime>`, the `env` a settings fragment sets, Claude Code's
    `settings.json` `env` and Codex's `shell_environment_policy.set`, and what the modules
    export, each a path in the home.

  `env` in `qory.yaml` over a module's export replaces its value.
  `qory harness inspect` lists each variable with where it comes from.
- A tool's flags may move. Then `harness.launch.<runtime>` in your `qory.yaml` changes
  the command, the arguments or the variables.
- A group of arguments for a file the compose did not write is left out, such as
  `mcp.json` without a server.
- The home is found the way compose finds it: from `--home`, `harness.home`, or the
  checkout you stand in. The paths are absolute, so the line works wherever the home is.
- A tool that reads its harness from the checkout alone has no launch template.
  `qory harness launch` says so.

`--json` prints the command, the arguments and all those variables as one JSON object. It is
for a launcher that starts the program without a shell. Under `addresses`, it lists the
name the session gives each composed agent, skill and command, per kind. A bound role is
listed beside them, as the entry it is bound to.

- For Claude Code, the plugin prefixes every name: `harness:<name>`.
- For the other tools, the name is the one the module wrote.

`--address <kind>/<name>`, such as `skills/deploy`, prints that one name alone. A launcher
uses it to build its first prompt from an entry point, such as `/harness:implement`.

## Flags of compose

| Flag                     | What it does                                                                                   |
| ------------------------ | ---------------------------------------------------------------------------------------------- |
| `-f <stack>`             | Compose this `qory-stack.yaml`, `qory.yaml` or `harness.yaml` instead of the one found.        |
|                          | When the checkout's own document extends a stack, this one is its base.                        |
| `--dry-run`              | Print the report. Write nothing.                                                               |
| `--runtime claude,codex` | Render for these runtimes instead of the document's.                                           |
| `--model opus`           | Write this model instead of the document's.                                                    |
| `--force`                | Replace a tracked, unmodified file where a link goes. `git checkout --` restores it.           |
| `--update`               | Re-fetch every git source, tags included.                                                      |
| `--check`                | Exit 6 when a file or link of the home, or a variable a launch sets, differs from the stack,   |
|                          | the modules and `qory.yaml`. Write nothing. The checkout's links and the rest of the report    |
|                          | are not compared.                                                                              |
| `--home <dir>`           | Compose under a directory outside the checkout. Write nothing into the checkout.               |
| `--no-links`             | Write no link and no exclude line into the checkout. The tree goes under `.qory`.              |

`--force`, `--update` and `--no-links` have keys in `qory.yaml` too: `force`, `update` and
`harness.links: none`.

A link never takes the place of a path `qory` did not write unless you say so. The
compose stops, names the path, and says whether `--force` replaces it:

```text
✗ harness is a tracked directory, not a link qory wrote.
  A previous compose replaced it (the report lists it as replaced); it has been restored since.
  It has no local changes. To replace it again: qory harness compose --force
  (git checkout -- harness restores it)
```

`--force` replaces only a path git can restore. A path git does not track, or one with
changes, is refused under `--force` too. The refusal says why and names the changed
files.

## Removing the harness

`qory harness remove` removes the composed tree and its links from the checkout.

- A home outside the checkout is removed with its report. The checkout is not touched:
  nothing was written there.
- `--runtime <name>` removes that runtime's links and directory only. The rest stays
  composed.

## The qory.yaml files

Every setting has a default, so every file is optional.

- **The repository's file** is committed. It holds what the repository decides: its
  stack, and what a [worktree](worktrees.md) needs. It may be called `harness.yaml`
  instead, a name that does not mention the tool that reads it.
- **Your file**, in `~/.config/qory` (or `$XDG_CONFIG_HOME/qory`), defines how `qory` runs
  on this machine, for every repository.
- **A file in a directory above the checkout**, one you own, holds choices for every
  checkout below it.

They apply in this order, each over the one before it:

1. yours,
2. those above the checkout, the farthest first,
3. the repository's own.

A command-line flag overrides every file. `qory config` shows every setting and the file
it came from.

### Writing them

`qory setup repo` writes the repository's file into the current directory:

- the repository's own stack, with one runtime and one module,
- every key a repository commits, shown,
- that module under `harness/`, with its manifest and `AGENTS.md`.

A stack already there, in `qory.yaml` or `qory-stack.yaml`, is kept. So is every file
already there.

`qory setup machine` writes yours, with every key at its default. A file already there is
kept. It holds:

- the runtime and model to compose for, instead of the document's,
- `force` and `update`,
- where a worktree goes, and what it is called,
- the git timeout and cache,
- environment variables, which every launch sets, but a name the runtime's own launch
  template sets.

The reference, one page per command, is under [commands](commands/qory.md).

## The format

The stack, the module manifest, `qory.yaml`, the composition rules and the runtimes are
specified in [contracts/harness/v1](../contracts/harness/v1/README.md). It has JSON
schemas, and the fixtures the test suite runs.
