# Harness compose format, v1alpha1

What `qory harness compose` reads and what it writes.

## Three files

| File | Lives in | Defines |
|---|---|---|
| `qory-stack.yaml` | a directory named by `extends` or `-f`; an ancestor directory covering several repositories; the root of the harness repository that delivers it | a stack delivered to be extended: the ordered modules, the target runtime and model, the excludes, what a checkout extending it may add |
| `qory-module.yaml` | the root of a module | the module: its name, its variants per runtime, the variables it exports |
| `qory.yaml` | the repository root, committed; the user's configuration directory and the checkout's ancestor directories, for the machine | the repository's document and the machine's: under `harness`, this repository's own stack, its target and modules, or the stack it extends and the modules it appends, and the runtime and model this machine composes for; under `worktree`, what a worktree of the repository needs and where the machine puts one; `git` and `env` (§The configuration) |

Every module carries a `qory-module.yaml`; it is what names the module, and a directory
without one is not a module. `qory.yaml` is optional: every setting has a default. Each
file name is fixed, so a repository with several stacks holds one directory per stack,
each with its `qory-stack.yaml`. A directory holds `qory-stack.yaml` or a `qory.yaml` whose
`harness` section names modules, never both; the file name says which document a file
holds, and no document carries a kind.

Every document carries `apiVersion`. A reader refuses a version it does not read
and names the versions it does. `v1alpha1` says the format may still change.

## Discovery

What to compose, in order: the `-f <file>` flag; `qory-stack.yaml` in the checkout root,
or the checkout's `qory.yaml` when its `harness` section names modules; the nearest of
either in an ancestor directory that the current user owns. An ancestor's file owned by
another user is not read. A `qory-stack.yaml` found in the checkout root must declare an
`extending` block: it is a stack delivered to be extended, and one nothing can extend is
a repository's own stack in the wrong file, refused with the place it goes. The same file
named with `-f`, in an ancestor directory, or reached through `extends` is read as it is.

The configuration is every `qory.yaml` found, applied in this order, each overriding the
one before it: `$XDG_CONFIG_HOME/qory/qory.yaml`, else `~/.config/qory/qory.yaml`; the
files of the checkout's ancestor directories that the current user owns, the farthest
first; the file in the checkout root. A compose flag overrides every file.

## The stack

```yaml
apiVersion: qory.ai/v1alpha1
name: nextjs-app                 # optional; default: owner/name from the origin remote
description: The web app's harness   # optional; carried into the report
target:
  runtime: claude                # or a list: [claude, codex]
  model: opus                    # optional
modules:
  - name: core                   # read from modules/core at this repository's root
    exclude:
      skills: [test]             # the nextjs module ships this repository's test skill
      agents: [reviewer]
  - source: {git: https://github.com/acme/harness, ref: v2.4.0, path: modules/nextjs}
    variant: claude              # optional; forces a variant of the module
  - name: team
    source: {path: ./harness}    # a name and a source: the manifest must say team
    link: harness                # optional; <checkout>/harness -> .qory/harness/modules/team
extensions:                      # optional; carried into the report, not read
  acme:
    required_check: Harness self-tests
```

A repository holds its own stack in the `harness` section of its `qory.yaml`, with the
same keys, or extends a stack delivered as a `qory-stack.yaml` and appends its own modules
there (§Extending a stack):

```yaml
# qory.yaml, its own stack
apiVersion: qory.ai/v1alpha1
harness:
  target: {runtime: claude}
  modules:
    - name: app
      source: {path: ./harness}
```

```yaml
# qory.yaml, extending a stack
apiVersion: qory.ai/v1alpha1
harness:
  extends: {git: git@git.example.com:acme/harness, ref: main, path: nextjs-15}
  modules:
    - name: app
```

The section takes `name`, `description`, `target`, `modules` and `extensions` as a stack
does, never `extending`; a stack to be extended is a `qory-stack.yaml`. With `extends` in
place of `target`, it names the base: a directory holding a `qory-stack.yaml`, as
`{path: <dir>}` or `{git: <url>, ref: <ref>, path: <dir>}`. The schema is
[config.schema.json](config.schema.json).

| Field | Required | Meaning |
|---|---|---|
| `apiVersion` | yes | `qory.ai/v1alpha1` |
| `qory` | no | the qory versions the stack is written for: comparators such as `>=0.3.0 <0.4.0`, every one of which has to hold. A compose on a qory outside the range is refused with status 5; a build from source between tags, which has no version, composes and says the range was not checked. A stack delivered to be extended states its minimum here, and every checkout extending it inherits the range |
| `name` | no | the stack's name in the report. Default: `owner/name` from the origin remote, else the directory name |
| `description` | no | what the stack is for, carried into the report and printed by `qory harness inspect` |
| `target.runtime` | yes | the program that runs the harness, one of the runtimes in §Runtimes, or a list of them to compose for at once |
| `target.model` | no | written into the settings of every targeted runtime that has a project-level place for it (§Runtimes) |
| `modules` | yes | ordered, at least one. Order decides the order of the instruction sections |
| `modules[].name` | one of name and source | the module's name, the one its `qory-module.yaml` declares, one path segment. Alone, it is the address too: `modules/<name>` at the root of the repository the stack is in |
| `modules[].source` | one of name and source | `{path: <dir>}`, relative to the stack file; or `{git: <url>, ref: <tag, branch or commit>}` with an optional `path` to the module's directory inside the repository (§Sources). Alone, the module's name is its manifest's; with a name, the manifest must carry that name |
| `modules[].exclude` | no | entries of this module left out, by kind: `skills`, `agents`, `commands`, `output-styles`, `hooks`, `mcp`, `files` |
| `modules[].variant` | no | forces one of the module's variants instead of the one named like the targeted runtime |
| `modules[].link` | no | a name at the checkout root, one path segment, linked to the module's directory in the composed tree, so a permission rule or a script names the module's files by a checkout-relative path: `harness/scripts/check.sh`. A hard link (§Rendering), named once across the modules |
| `extensions` | no | one map per namespace, written into the report as it is and printed by `qory harness inspect`; qory reads nothing in it |
| `extending` | no | what a module of a checkout extending this stack may ship: `kinds`, `instructions`, `settings`, `files` (§Extending a stack). A base without the block cannot be extended |

A name is composed once: two entries resolving to modules of one name fail the compose,
since a name is one directory in the composed tree.

The schema is [stack.schema.json](stack.schema.json).

## Extending a stack

An operator delivers a harness as a stack; a product repository extends it in the
`harness` section of its `qory.yaml` and adds its own modules. The base is closed and the
checkout appends:

- The base's modules come first, in the base's order, with the base's excludes, variants
  and links; a relative source of the base resolves inside the base's own repository. The
  checkout cannot name, exclude, reorder or re-source a base module.
- The target is the base's. A `harness` section that sets `target` beside `extends` is refused; a
  `--runtime` or a `harness.runtime` may pick among the runtimes the base lists and
  nothing else; a model from anywhere but the base is refused when the base names one.
- The `harness`, `git` and `env` keys of the checkout's own `qory.yaml` are not read under
  `extends`, and the compose says so. The runner's configuration, in the user's directory
  or an ancestor the runner's user owns, is the only one. The `worktree` section is the
  repository's and is read.
- The base's `extending` block says what an appended module may ship: `kinds`, from
  `skills`, `agents`, `commands` and `output-styles`, never `hooks` or `mcp`, since the
  runner executes those without the agent; `instructions: true` for its `AGENTS.md`,
  appended after the base's; `settings`, the dotted key paths a fragment may set, such as
  `permissions.allow`; `files`, the `<runtime>/<path>` prefixes its files may sit under,
  such as `claude/rules/`, matched by path segment, so `claude/rules` covers
  `claude/rules/a/b.md` and not `claude/rules-private/x.md`, and a prefix naming a file
  covers that file only. A module shipping anything else fails the compose and the message
  names it. A base without the block is closed to extension.
- An entry of an appended module that collides with a base entry fails with status 3, and
  the message says the entry belongs to the base, `<name>@<pin>`, and to rename it; no
  exclude resolves it. The message names that one entry and nothing else of the base.
- Both stacks' `extensions` go into the report; a namespace the base declares is the
  base's, and a checkout declaring it too is refused.
- The report records the base with its source and pin, and marks the base's modules.
- A base that cannot be fetched, on a machine without access to its repository, fails
  with status 1 and "the stack <source> is not reachable from here".
- A base does not extend another.

The base's hooks and deny rules reach every extending checkout unchanged: `hooks` and
`permissions` concatenate (§Composition rules) and nothing of the base can be excluded.
What the kinds list removes is the code that runs without the agent's involvement; a
script inside an appended skill runs when the agent runs it, under the base's hooks and
rules. A wide `files` prefix, `claude/` say, is safe for the same reason: the paths a
runtime reads as settings are reserved for every module (§Rendering), so a file cannot
reach them. Keeping a copy of the harness from leaving the machine is the runner's egress
control, not the format's.

## Sources

A **path source** reads a directory as it stands. Its pin is `working-tree`, marked dirty in
the report when git sees uncommitted changes under it.

A **git source** reads a repository at a ref: a tag, a branch, or a commit by its full id.
The ref is resolved once and its commit fetched, at depth one, into
`qory/sources/<url>/<commit>` under the user's cache directory, the URL made into a
directory name with a short hash appended, beside a `refs/<ref>` file naming the commit the
ref last resolved to; the pin is that commit. A compose after that reads the cache and
needs no network: the checkout's own report says which commit it was composed from, and
that commit serves again as long as the stack still names the same URL and ref. An edited
ref resolves anew. A checkout that has never composed the source takes the ref's last
resolution from the cache, not the remote's current head. `qory harness compose --update`
resolves every git source's ref again, which is how a branch ref moves, and it moves for
that checkout alone: clones are kept per commit, and another checkout composed from the
earlier commit keeps reading it until its own `--update`. A path inside the repository
that links outside it is refused. `path` names the module's directory inside the repository, for a
repository that holds several modules. The report and the messages write a git source as
`<url>#<ref>` or `<url>#<ref>:<path>`.

## The module manifest

```yaml
# qory-module.yaml
apiVersion: qory.ai/v1alpha1
name: nextjs                     # the module's name: modules/nextjs in the composed tree
description: Next.js 15 conventions and the e2e skill
variants:
  default: claude                # the variant a runtime without its own gets, or `fail`
  claude: {}
  codex: {agents: agents/codex}  # this variant reads its agents from another directory
env:
  HARNESS_HOME: .                # exported as the module's root in the composed tree
  HARNESS_TOOLS: scripts/tools   # a path inside the module
```

`description`, optional, says what the module is for; the report carries it and `qory harness
inspect` prints it beside the module. `name` is the module's identity: the stack refers to the module by it, the composed tree
holds the module at `modules/<name>`, and the report and every message use it. The
directory the module is stored under is not consulted. `env` names the variables the module exports, each the path of a file or directory inside
the module, `.` for its root. The compose writes each as
`$QORY_HARNESS_HOME/modules/<name>/<path>` and the runtimes with a place for environment
get it there (§Runtimes), so a script the module ships reads its own location from the
variable it has always read. Two modules exporting one name with different values fail the
compose unless the configuration's `env` names it; `QORY_HARNESS_HOME` is qory's own.

A module's tree holds these entry kinds:

```
AGENTS.md                        merged; the instruction section, read by every runtime
skills/<name>/SKILL.md           atomic; the Agent Skills layout, read by every runtime
agents/<name>.md                 atomic; frontmatter name, description, and the keys some runtimes read (model, tools, mode); body = system prompt
commands/<name>.md               atomic; frontmatter description; body = prompt, $ARGUMENTS for the arguments
output-styles/<name>.md          atomic; Claude Code only
hooks/<file>                     atomic; scripts that a settings fragment names as $QORY_HARNESS_HOME/hooks/<file>; files only
mcp/<name>.json                  atomic; one MCP server: the object a runtime's own file holds under the server's name
files/<runtime>/<path>           atomic; one file the runtime reads at <path> under its directory, any depth
settings/<runtime>/<file>       merged; one fragment per target file of one runtime, in that runtime's format
<anything else>                  the module's own files, reached as $QORY_HARNESS_HOME/modules/<name>/<path>
```

`AGENTS.md` and skills are read by every runtime. Agents and commands are written in each
runtime's format from the one file above; a runtime without the concept skips the kind and
the compose says so. Every path an entry, a fragment or `AGENTS.md` resolves to, symlinks
followed, lies inside the module; a link that leaves the module fails the compose, so the
harness reads nothing the report does not name. Settings are runtime-native: `settings/claude/settings.json`,
`settings/codex/config.toml`, `settings/gemini/settings.json`, `settings/cursor/hooks.json`,
and so on, merged per file across modules.

`files/<runtime>/<path>` is for what a runtime reads from its directory beyond the kinds
above: Claude Code's `.claude/rules/*.md`, Cursor's `.cursor/rules/*.mdc`, Copilot's
`.github/instructions/*.instructions.md`. Each file is one entry named `<runtime>/<path>`,
linked to `<path>` under the runtime's directory in the checkout (§Runtimes), so two modules
shipping one path collide like two skills of one name and an exclude names the path. A
path the runtime reads as settings, one qory writes, or one a kind is linked at is
reserved and fails the compose, and so does a runtime qory does not render; a file
directly under `files/` fails, since the runtime segment decides where it lands. A
directory under `settings/<runtime>/` fails and the message points here. A file for a
runtime the stack does not target is left out, like a fragment for one.

An MCP server is one JSON object per file, in the shape Claude Code's `.mcp.json` holds under
`mcpServers`: `command`, `args` and `env` for a stdio server, `url` and `headers` for a
remote one, `type` for either, and `description` for a note to the module's readers, which
the compose leaves out of every runtime's file. Exactly one of `command` and `url`, and
no other key; the schema is [mcp.schema.json](mcp.schema.json). Every runtime with a project-level place for
servers gets it there, rewritten where its shape differs (§Runtimes). A directory under
`hooks/`, or a link to one, fails the compose: a hook is one file, and a script's helpers
live anywhere else in the module, under `scripts/` say, reached through `modules/<name>` in
the composed tree.

The schema is [module.schema.json](module.schema.json).

## Composition rules

1. **One flat tree, one entry per name.** Every atomic entry links into `<kind>/<name>` from
   the module that provides it, a file into `<runtime>/<path>`.
2. **Excludes first, then the collision check.** After each module's `exclude` is applied, an
   atomic name provided by more than one module fails the compose. The error names every
   module and the excludes that resolve it; this is its text, which the fixtures hold, and
   `qory harness compose` prints the same as a table under a `Fix` heading with the
   stack lines to paste:

   ```
   skills/test is provided by 3 modules: core@working-tree, nextjs@working-tree, team@working-tree
     keep one and exclude the others, for example
       core:   exclude: {skills: [test]}
       nextjs: exclude: {skills: [test]}
   ```

   There is no last-wins and no rename.
3. **An exclude that names nothing fails**, so a module that stops shipping an entry is
   noticed rather than silently composed.
4. **Merged kinds join, and a value is set once.** A settings target file merges across
   the modules that ship a fragment for it, JSON or TOML by extension: objects deep-merge,
   `permissions` lists concatenate and deduplicate, `hooks` arrays concatenate. Every
   other list and every scalar is set by one module; another module may repeat the value and
   may not change it. Two fragments setting one key path to different values fail the
   compose with `settings/<runtime>/<file>: <dotted.key.path> is set by modules <a> and <b>
   with different values`. A top-level `env` key the configuration sets takes the
   configuration's value and never collides; an `env` key a fragment sets and a module
   manifest exports with a different value fails the same way, unless the configuration
   sets it. `AGENTS.md` is the concatenation of the modules' files in module order, separated
   by a blank line, the one place the order of `modules` decides anything. An MCP server is
   atomic and follows rules 1 to 3; where a settings fragment also names servers, the
   composed servers are written on top.
5. **Variants resolve per module.** The forced `variant`, else the one named like
   `target.runtime`, else the manifest's `default`, else the module root when the manifest
   declares no variants. A manifest with variants, none for the runtime and no default, or
   `default: fail`, fails the compose.
6. **Everything is reported.** The report names every module with its pin, every entry with
   its module, every exclude and every variant chosen. `qory harness inspect` prints it.
7. **Nothing composed is committed.** The link in the checkout is excluded through the
   clone-local exclude file, never the repository's own ignore file.

## Rendering

The composed tree is written into the checkout at `.qory/harness`, beside its report
`.qory/harness-report.json`, and `.qory` is excluded through the clone-local exclude file.
`.qory` must be a real directory or absent: a symlink or a file there, which a repository
could commit, is refused, because everything qory writes and removes goes through it. The
compose runs inside a git working tree only. The tree holds the runtime-agnostic parts
once, `AGENTS.md`, `skills/` and `hooks/`, one link
per module at `modules/<name>` to the module's own directory, and one directory per runtime
with that runtime's files. Every atomic entry is a symlink into its module. Every
`$QORY_HARNESS_HOME`, braced or not, in a settings fragment or an MCP server becomes the
tree's absolute path, so `$QORY_HARNESS_HOME/modules/core/scripts/check.sh` runs the script
the core module ships. The links survive a moved checkout; the absolute paths written into
settings do not, so a checkout that moves is composed again.

A tree holds every runtime the checkout is composed for, so one checkout serves two agents
at once and a person can stop working in one and continue in the other. A compose renders
the targeted runtimes and every runtime already in the tree, because the tree is replaced
whole, and each runtime's links keep resolving. A module whose variants differ between two
targeted runtimes cannot be rendered for both, since the tree holds one copy of each entry:
the compose refuses and names both runtimes.

The checkout gets relative links into the tree, listed per runtime below, each excluded
through the clone-local exclude file, so every path a runtime checks resolves inside the
project. A link to a file, such as `AGENTS.md`, is one
symlink. A link to a directory, such as `.claude`, is a real directory in the checkout
holding one symlink per entry of the tree's directory, `.claude/skills` and
`.claude/settings.json` alike, so the checkout's own files there stay: Claude Code's
`.claude/settings.local.json` survives every compose, and a repository may keep its own
agents under `.github/agents` beside the linked ones. A directory a module's files make in
the tree, `.claude/rules` say, is one such symlink, like `.claude/skills`: a repository
that keeps its own rules there moves them into a module, under `extends` into its own
module with `claude/rules/` in the base's `files` list. A compose also takes back what an
earlier one linked and this one does not. `qory harness remove --runtime <name>` takes one
runtime's links and leaves a link another composed runtime shares, such as `.agents/skills`.
A link is written, pruned or removed only where the checkout is: a directory on the way
that is itself a symlink, a committed `.github` link say, is passed over for a soft link
and refused for a hard one, and nothing behind it is touched. An exclude line goes with
its link, and the `.qory` line with the directory, so a path the repository later adds
there is not hidden; worktrees of one repository share one exclude file, so a remove in
one worktree drops the lines the links of another still use until its next compose.

A module's `link` is a hard link at the checkout root to `modules/<name>` in the tree,
written after the runtimes' links, excluded, pruned when the stack drops it, and
refused when it names a path a runtime links or `.qory`. Hard, because the permission
rules and scripts of the harness depend on that path: a file or a foreign link there
fails the compose with status 4, and `--force` replaces it when git can restore it.
`qory harness remove` takes every link at the checkout root into `modules/`, with or
without a report, so a `.qory` deleted by hand leaves no link behind.

The links listed below are the full set; a link to a file the compose did not produce is
left out, `AGENTS.md`, `AGENTS.override.md` and `GEMINI.md` without instructions,
`.mcp.json` without a server or a `settings/claude/mcp.json` fragment, `opencode.json`
without a model, a server or a fragment.

A path qory did not write is never replaced. Where a hard link goes, such as
`.claude/settings.json`, it fails the compose; where a soft link goes, such as `AGENTS.md`,
it is skipped and reported, so a repository's instructions stay its own. With
`--force`, a file or directory that git tracks and that is unmodified is removed for the
link, either kind, and listed in the report under `replaced`; `git checkout --` brings it
back, and `qory harness remove` prints that command. Anything untracked or modified, and a
directory holding an untracked or ignored file, is still refused, because git could not
restore it. A compose that fails after replacing something has written the report already,
so the hint is not lost.

## Runtimes

| `target.runtime` | Program | Links in the checkout | Model written to | MCP servers written to | Environment written to | Files land in | Kinds with no place |
|---|---|---|---|---|---|---|---|
| `claude` | Claude Code | `.claude`, `.mcp.json` | `.claude/settings.json` `model` | `.mcp.json` `mcpServers` | `.claude/settings.json` `env` | `.claude/<path>` | none |
| `codex` | Codex CLI | `.codex`, `.agents/skills`, `AGENTS.override.md` | `.codex/config.toml` `model` | `.codex/config.toml` `mcp_servers` | `.codex/config.toml` `shell_environment_policy.set` | `.codex/<path>` | commands, output-styles |
| `gemini` | Gemini CLI | `.gemini`, `GEMINI.md` | `.gemini/settings.json` `model.name` | `.gemini/settings.json` `mcpServers` | not written | `.gemini/<path>` | output-styles |
| `opencode` | OpenCode | `.opencode`, `.agents/skills`, `opencode.json`, `AGENTS.md` | `opencode.json` `model` | `opencode.json` `mcp`, in OpenCode's shape | not written | `.opencode/<path>` | output-styles |
| `cursor` | Cursor, Cursor CLI | `.cursor`, `.agents/skills`, `AGENTS.md` | not written; a global CLI setting | `.cursor/mcp.json` `mcpServers` | not written | `.cursor/<path>` | commands, output-styles |
| `copilot` | GitHub Copilot CLI | `.github/agents`, `.github/hooks`, `.agents/skills`, `AGENTS.md` | not written; a user setting | not written; a user file | not written | `.github/<path>`, one soft link per file | commands, output-styles, mcp |
| `amp` | Amp | `.amp`, `.agents/skills`, `AGENTS.md` | not written; Amp picks by mode | `.amp/settings.json` `amp.mcpServers` | not written | `.amp/<path>` | agents, commands, output-styles |
| `goose` | Goose | `.agents/skills`, `.agents/agents`, `AGENTS.md` | not written; no project file | not written; a user file | not written | nowhere | commands, output-styles, mcp, files |
| `any` | any program that reads `AGENTS.md` and `.agents/skills` | `.agents/skills`, `AGENTS.md` | not written | not written | not written | nowhere | agents, commands, output-styles, hooks, mcp, files |

The environment a runtime gets is the variables the modules export, the configuration's
`env` over them, and `QORY_HARNESS_HOME` as the tree's absolute path over both.

A `files/<runtime>/<path>` entry may not use a path the runtime reserves under its
directory, whatever stack composes it: the files qory writes, the directories a kind is
linked at, and every file the program reads as settings, which a fragment under
`settings/<runtime>/` sets instead. A directory reserves what is below it, by path
segment. The refusal names the module, the entry and the reason, with status 2:

| `target.runtime` | qory writes | The program reads as settings | A kind is linked at | Runs without the agent |
|---|---|---|---|---|
| `claude` | `CLAUDE.md`, `settings.json`, `mcp.json` | `settings.local.json` | `skills`, `agents`, `commands`, `hooks`, `output-styles` | |
| `codex` | `config.toml` | | `agents` | |
| `gemini` | `settings.json` | | `skills`, `hooks`, `agents`, `commands` | |
| `opencode` | `opencode.json` | | `commands`, `hooks`, `agents` | |
| `cursor` | | `mcp.json`, `hooks.json`, `cli.json`, `environment.json` | `agents`, `hooks` | |
| `copilot` | | | `agents`, `hooks` | `workflows` |
| `amp` | | `settings.json` | | |
| `goose` | | | `agents` | |
| `any` | | | | |

Per runtime, the files written into its directory:

- **claude**: links for every kind, `settings.json` with the environment under `env` and
  the model, `mcp.json` with the servers, linked as `.mcp.json` at the checkout root, and the
  instructions written in full as `CLAUDE.md`. They are not imported from `AGENTS.md`,
  because Claude Code resolves a link to its real path and asks about an import found
  through one on every start.
- **codex**: `config.toml` with the model, the servers as `[mcp_servers.<name>]` tables,
  the environment under `[shell_environment_policy.set]`, which Codex passes to every
  command it runs, and any other `settings/codex/` file, one `agents/<name>.toml` per agent with the body as
  `developer_instructions`. Codex reads `AGENTS.override.md` before `AGENTS.md`, and a
  project `.codex` only in a trusted project.
- **gemini**: `settings.json` with `model.name` and `mcpServers`, links for skills and hooks,
  one `agents/<name>.md` with `name` and `description`, one `commands/<name>.toml` with
  `$ARGUMENTS` as `{{args}}`. Gemini reads a project `.gemini` only in a trusted folder.
- **opencode**: links for commands and hooks, `agents/<name>.md` with `description`, `mode`
  and `model`, and `opencode.json` when a module ships one, the stack names a model or the
  compose holds a server. A server with `url` becomes `{type: remote, url, headers}`, any
  other `{type: local, command: [command, args...], environment: env}`.
- **cursor**: `agents/<name>.md` with `name`, `description` and `model`, links for hooks,
  and the `settings/cursor/` files such as `hooks.json`, `cli.json` and `mcp.json`, the last
  with the servers under `mcpServers`.
- **copilot**: `agents/<name>.agent.md` with `name`, `description`, `tools` and `model`, and
  the `settings/copilot/` files written under `hooks/`, so `settings/copilot/hooks.json`
  reaches `.github/hooks/hooks.json`.
- **amp**: the `settings/amp/` files such as `settings.json`, with the servers under
  `amp.mcpServers`.
- **goose**: `agents/<name>.md` with `name`, `description` and `model`.
- **any**: nothing of its own.

Programs that read `AGENTS.md` and `.agents/skills` and have no package of their own include
DeepSeek Harness, Kilo Code, Kimi CLI, Factory Droid, Mistral Vibe, JetBrains Junie, Augment
CLI, Warp, Windsurf, Zed and Crush; compose for them with `target.runtime: any`. A
package for one of them under `internal/render/` implements one interface.

## The report

`.qory/harness-report.json`, `version` 1: the stack name and file, the target, the
checkout, the home, the modules, the entries with their module, the excludes, the checkout
paths a `--force` compose replaced, the `env` the harness exports with
`$QORY_HARNESS_HOME` in place of the home, and the stack's `extensions` as written. The
target's `runtime` is always an array: the runtimes the home holds after the compose, the
targeted ones first. A module carries its `name`, its `source` as the stack writes it,
its `pin`, `dirty` when git saw uncommitted changes under a path source, the `variant`
chosen, its `link` when the stack names one, and `base` when it is the base stack's.
The report of a checkout that extends a stack records the `base`: its `name`, `source` and `pin`. A path source's pin is `working-tree`; a git source's pin is twelve
characters of its commit. The report records the `qory` that wrote it, its `version`,
`commit` and `source`, `release` or `source`, as `qory version --json` reports them,
and leaves the field out when the build carries no version. `qory harness inspect`
refuses a report of another version.

## The configuration

One file name, one schema, read at three levels: the user's file,
`$XDG_CONFIG_HOME/qory/qory.yaml` or `~/.config/qory/qory.yaml`, and the files of the
checkout's ancestor directories carry the machine's choices; the file in the checkout
root is committed with the repository and carries what the repository needs. Every key may
appear at any level and the nearest file wins.

```yaml
apiVersion: qory.ai/v1alpha1
harness:
  runtime: [claude, codex]       # instead of the stack's target.runtime
  model: opus                    # instead of the stack's target.model
  force: true                    # replace a tracked, unmodified file where a link goes
  update: always                 # fetch every git source again on each compose
  extends: {git: git@git.example.com:acme/harness, ref: main, path: nextjs-15}
  modules:                       # with extends or a target: what this checkout composes
    - name: app
worktree:
  dir: ..                        # where worktrees go, relative to the main checkout
  name: wt-{branch}              # a worktree's directory name; {repo} is the main checkout's
  base: main                     # the branch a new worktree branch starts from
  link: [.env, .env.local]       # linked from the main checkout into a new worktree
  copy: [config/local.json]      # copied once into a new worktree
  run:
    add: [pnpm install]          # run in a new worktree, after links and copies
    remove: [docker compose down] # run in a worktree before it is removed
git:
  timeout: 10m                   # the longest one git command may run
  cache: /var/cache/qory         # where git sources are fetched to
env:
  HARNESS_PROFILE: nextjs        # exported to every runtime with a place for it
```

| Key | Default | Meaning |
|---|---|---|
| `qory` | none | the qory versions the file is written for, as in a stack; read under `extends` as well, since it can only narrow the base's range |
| `harness.runtime` | the stack's `target.runtime` | one runtime name or a list; `--runtime` wins over it |
| `harness.model` | the stack's `target.model` | `--model` wins over it |
| `harness.force` | `false` | what `--force` does on every compose; `--force=false` wins over it |
| `harness.update` | `never` | `always` fetches every git source again on each compose; `--update` and `--update=false` win over it |
| `harness.target`, `harness.modules` | none | in a checkout's file, its own stack: the target and the modules, with `name`, `description` and `extensions` beside them, as in a `qory-stack.yaml` |
| `harness.extends`, `harness.modules` | none | in a checkout's file, the stack it extends and the modules it appends; `extends` takes the place of `target` (§Extending a stack) |
| `worktree.dir` | `..` | where `qory worktree add` puts a worktree, relative to the main checkout unless absolute |
| `worktree.name` | `wt-{branch}` | one directory name under `worktree.dir`; `{branch}` is the branch with each slash made a dash, `{repo}` the main checkout's directory name |
| `worktree.base` | the remote's HEAD branch, else the main checkout's branch | the branch a new worktree branch starts from; it has to hold a commit, so a repository with none yet is refused |
| `worktree.link` | none | paths inside the checkout, linked from the main checkout into a new worktree; one missing there is reported, not an error |
| `worktree.copy` | none | paths copied once into a new worktree |
| `worktree.run.add` | none | commands run in a new worktree after links, copies and the compose, in order; a failure stops with the worktree kept |
| `worktree.run.remove` | none | commands run in a worktree before it is removed, in order |
| `git.timeout` | `10m` | a git command running past it is stopped and the compose fails |
| `git.cache` | the user's cache directory, `qory/sources` under `~/Library/Caches`, `$XDG_CACHE_HOME` or `~/.cache` | absolute, or relative to the file naming it |
| `env` | none | variables written over what the modules export; a name two modules export with different values needs one here |

Every key is optional; a file naming none is read and changes nothing. A list, such as
`worktree.link`, is the nearest file's whole. `qory config` prints every effective value
and the file it came from. Under `extends` the `harness`, `git` and `env` keys of the
checkout's own file are not read (§Extending a stack). The schema is
[config.schema.json](config.schema.json).

## Exit status

| Status | Meaning |
|---|---|
| 0 | done |
| 1 | anything not listed below: a file that could not be written, a git source that could not be fetched |
| 2 | a mistake in the input: the command line, the stack, a module, a fragment |
| 3 | a collision, printed with the excludes that resolve it |
| 4 | a path qory did not write standing where a link goes, which `--force` may replace |
| 5 | the running qory is outside the range a document's `qory` key declares |
