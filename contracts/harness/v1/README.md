# Harness compose format, v1alpha1

What `qory harness compose` reads and what it writes.

## Three files

| File | Lives in | Defines |
|---|---|---|
| `harness-compose.yaml` | the repository root, or an ancestor directory covering several repositories | the profile: the ordered layers, their sources, the target runtime and model, the excludes |
| `harness.yaml` | the root of a layer | the layer: its variants per runtime, the variables it exports |
| `qory.yaml` | the user's configuration directory, an ancestor directory, or the repository root | the configuration: how qory runs on this machine, for this person, in this checkout |

`harness.yaml` is optional. A directory holding `skills/`, `agents/` and the other entry
kinds, with no manifest, is a valid layer with one variant. `qory.yaml` is optional too:
every setting has a default.

Every document carries `apiVersion` and `kind`. A reader refuses a version it does not read
and names the versions it does. `v1alpha1` says the format may still change.

## Discovery

The profile, in order: the `-f <file>` flag; `harness-compose.yaml` in the checkout root;
the nearest `harness-compose.yaml` in an ancestor directory that the current user owns. An
ancestor's file owned by another user is not read.

The configuration is every `qory.yaml` found, applied in this order, each overriding the
one before it: `$XDG_CONFIG_HOME/qory/qory.yaml`, else `~/.config/qory/qory.yaml`; the
files of the checkout's ancestor directories that the current user owns, the farthest
first; the file in the checkout root. A compose flag overrides every file.

## The profile

```yaml
apiVersion: qory.ai/v1alpha1
kind: HarnessProfile
name: nextjs-app                 # optional; default: owner/name from the origin remote
target:
  runtime: claude                # or a list: [claude, codex]
  model: opus                    # optional
layers:
  - name: core
    source: {git: https://github.com/acme/harness, ref: v2.4.0, path: core}
    exclude:
      skills: [test]             # the nextjs layer ships this repository's test skill
      agents: [reviewer]
  - name: nextjs
    source: {path: ../harness/nextjs}
    variant: claude              # optional; forces a variant of the layer
  - name: team
    source: {path: ./harness}
    link: harness                # optional; <checkout>/harness -> .qory/harness/layers/team
extensions:                      # optional; carried into the report, not read
  acme:
    required_check: Harness self-tests
```

| Field | Required | Meaning |
|---|---|---|
| `apiVersion` | yes | `qory.ai/v1alpha1` |
| `kind` | yes | `HarnessProfile` |
| `name` | no | the profile's name in the report. Default: `owner/name` from the origin remote, else the directory name |
| `target.runtime` | yes | the program that runs the harness, one of the runtimes in §Runtimes, or a list of them to compose for at once |
| `target.model` | no | written into the settings of every targeted runtime that has a project-level place for it (§Runtimes) |
| `layers` | yes | ordered, at least one. Order decides the merge order of settings and instruction sections |
| `layers[].name` | yes | unique within the profile and one path segment, since it names `layers/<name>` in the composed tree; used in excludes, the report and error messages |
| `layers[].source` | yes | `{path: <dir>}`, relative to the profile file; or `{git: <url>, ref: <tag, branch or commit>}` with an optional `path` to the layer's directory inside the repository (§Sources) |
| `layers[].exclude` | no | entries of this layer left out, by kind: `skills`, `agents`, `commands`, `output-styles`, `hooks`, `mcp` |
| `layers[].variant` | no | forces one of the layer's variants instead of the one named like the targeted runtime |
| `layers[].link` | no | a name at the checkout root, one path segment, linked to the layer's directory in the composed tree, so a permission rule or a script names the layer's files by a checkout-relative path: `harness/scripts/check.sh`. A soft link (§Rendering), named once across the layers |
| `extensions` | no | one map per namespace, written into the report as it is and printed by `qory harness inspect`; qory reads nothing in it |

The schema is [profile.schema.json](profile.schema.json).

## Sources

A **path source** reads a directory as it stands. Its pin is `working-tree`, marked dirty in
the report when git sees uncommitted changes under it.

A **git source** reads a repository at a ref: a tag, a branch, or a commit by its full id.
The ref is resolved once and its commit fetched, at depth one, into
`qory/sources/<url>/<commit>` under the user's cache directory, the URL made into a
directory name with a short hash appended, beside a `refs/<ref>` file naming the commit the
ref last resolved to; the pin is that commit. A compose after that reads the cache and
needs no network: the checkout's own report says which commit it was composed from, and
that commit serves again as long as the profile still names the same URL and ref. An edited
ref resolves anew. A checkout that has never composed the source takes the ref's last
resolution from the cache, not the remote's current head. `qory harness compose --update`
resolves every git source's ref again, which is how a branch ref moves, and it moves for
that checkout alone: clones are kept per commit, and another checkout composed from the
earlier commit keeps reading it until its own `--update`. A path inside the repository
that links outside it is refused. `path` names the layer's directory inside the repository, for a
repository that holds several layers. The report and the messages write a git source as
`<url>#<ref>` or `<url>#<ref>:<path>`.

## The layer manifest

```yaml
apiVersion: qory.ai/v1alpha1
kind: HarnessLayer
name: nextjs
variants:
  default: claude                # the variant a runtime without its own gets, or `fail`
  claude: {}
  codex: {agents: agents/codex}  # this variant reads its agents from another directory
env:
  HARNESS_HOME: .                # exported as the layer's root in the composed tree
  HARNESS_TOOLS: scripts/tools   # a path inside the layer
```

`env` names the variables the layer exports, each the path of a file or directory inside
the layer, `.` for its root. The compose writes each as
`$QORY_HARNESS_HOME/layers/<name>/<path>` and the runtimes with a place for environment
get it there (§Runtimes), so a script the layer ships reads its own location from the
variable it has always read. Two layers exporting one name with different values fail the
compose unless the configuration's `env` names it; `QORY_HARNESS_HOME` is qory's own.

A layer's tree holds these entry kinds:

```
AGENTS.md                        merged; the instruction section, read by every runtime
skills/<name>/SKILL.md           atomic; the Agent Skills layout, read by every runtime
agents/<name>.md                 atomic; frontmatter name, description, and the keys some runtimes read (model, tools, mode); body = system prompt
commands/<name>.md               atomic; frontmatter description; body = prompt, $ARGUMENTS for the arguments
output-styles/<name>.md          atomic; Claude Code only
hooks/<file>                     atomic; scripts that a settings fragment names as $QORY_HARNESS_HOME/hooks/<file>; files only
mcp/<name>.json                  atomic; one MCP server: the object a runtime's own file holds under the server's name
settings/<runtime>/<file>       merged; one fragment per target file of one runtime, in that runtime's format
<anything else>                  the layer's own files, reached as $QORY_HARNESS_HOME/layers/<name>/<path>
```

`AGENTS.md` and skills are read by every runtime. Agents and commands are written in each
runtime's format from the one file above; a runtime without the concept skips the kind and
the compose says so. Every path an entry, a fragment or `AGENTS.md` resolves to, symlinks
followed, lies inside the layer; a link that leaves the layer fails the compose, so the
harness reads nothing the report does not name. Settings are runtime-native: `settings/claude/settings.json`,
`settings/codex/config.toml`, `settings/gemini/settings.json`, `settings/cursor/hooks.json`,
and so on, merged per file across layers.

An MCP server is one JSON object per file, in the shape Claude Code's `.mcp.json` holds under
`mcpServers`: `command`, `args` and `env` for a stdio server, `url` and `headers` for a
remote one, `type` for either, and `description` for a note to the layer's readers, which
the compose leaves out of every runtime's file. Exactly one of `command` and `url`, and
no other key; the schema is [mcp.schema.json](mcp.schema.json). Every runtime with a project-level place for
servers gets it there, rewritten where its shape differs (§Runtimes). A directory under
`hooks/`, or a link to one, fails the compose: a hook is one file, and a script's helpers
live anywhere else in the layer, under `scripts/` say, reached through `layers/<name>` in
the composed tree.

The schema is [layer.schema.json](layer.schema.json).

## Composition rules

1. **One flat tree, one entry per name.** Every atomic entry links into `<kind>/<name>` from
   the layer that provides it.
2. **Excludes first, then the collision check.** After each layer's `exclude` is applied, an
   atomic name provided by more than one layer fails the compose. The error names every
   layer and the excludes that resolve it; this is its text, which the fixtures hold, and
   `qory harness compose` prints the same as a table under a `Fix` heading with the
   profile lines to paste:

   ```
   skills/test is provided by 3 layers: core@working-tree, nextjs@working-tree, team@working-tree
     keep one and exclude the others, for example
       core:   exclude: {skills: [test]}
       nextjs: exclude: {skills: [test]}
   ```

   There is no last-wins and no rename.
3. **An exclude that names nothing fails**, so a layer that stops shipping an entry is
   noticed rather than silently composed.
4. **Merged kinds merge in layer order.** A settings target file merges across the layers
   that ship a fragment for it, JSON or TOML by extension: `permissions` lists concatenate
   and deduplicate, `hooks` arrays concatenate, `env` merges with later wins, other objects
   deep-merge, other lists and scalars are replaced. `AGENTS.md` is the concatenation of the
   layers' files, separated by a blank line. An MCP server is atomic and follows rules 1 to
   3; where a settings fragment also names servers, the composed servers are written on top.
5. **Variants resolve per layer.** The forced `variant`, else the one named like
   `target.runtime`, else the manifest's `default`, else the layer root when the manifest
   declares no variants. A manifest with variants, none for the runtime and no default, or
   `default: fail`, fails the compose.
6. **Everything is reported.** The report names every layer with its pin, every entry with
   its layer, every exclude and every variant chosen. `qory harness inspect` prints it.
7. **Nothing composed is committed.** The link in the checkout is excluded through the
   clone-local exclude file, never the repository's own ignore file.

## Rendering

The composed tree is written into the checkout at `.qory/harness`, beside its report
`.qory/harness-report.json`, and `.qory` is excluded through the clone-local exclude file.
`.qory` must be a real directory or absent: a symlink or a file there, which a repository
could commit, is refused, because everything qory writes and removes goes through it. The
compose runs inside a git working tree only. The tree holds the runtime-agnostic parts
once, `AGENTS.md`, `skills/` and `hooks/`, one link
per layer at `layers/<name>` to the layer's own directory, and one directory per runtime
with that runtime's files. Every atomic entry is a symlink into its layer. Every
`$QORY_HARNESS_HOME`, braced or not, in a settings fragment or an MCP server becomes the
tree's absolute path, so `$QORY_HARNESS_HOME/layers/core/scripts/check.sh` runs the script
the core layer ships. The links survive a moved checkout; the absolute paths written into
settings do not, so a checkout that moves is composed again.

A tree holds every runtime the checkout is composed for, so one checkout serves two agents
at once and a person can stop working in one and continue in the other. A compose renders
the targeted runtimes and every runtime already in the tree, because the tree is replaced
whole, and each runtime's links keep resolving. A layer whose variants differ between two
targeted runtimes cannot be rendered for both, since the tree holds one copy of each entry:
the compose refuses and names both runtimes.

The checkout gets relative links into the tree, listed per runtime below, each excluded
through the clone-local exclude file, so every path a runtime checks resolves inside the
project. A link to a file, such as `AGENTS.md`, is one
symlink. A link to a directory, such as `.claude`, is a real directory in the checkout
holding one symlink per entry of the tree's directory, `.claude/skills` and
`.claude/settings.json` alike, so the checkout's own files there stay: Claude Code's
`.claude/settings.local.json` survives every compose, and a repository may keep its own
agents under `.github/agents` beside the linked ones. A compose also takes back what an
earlier one linked and this one does not. `qory harness remove --runtime <name>` takes one
runtime's links and leaves a link another composed runtime shares, such as `.agents/skills`.
A link is written, pruned or removed only where the checkout is: a directory on the way
that is itself a symlink, a committed `.github` link say, is passed over for a soft link
and refused for a hard one, and nothing behind it is touched. An exclude line goes with
its link, and the `.qory` line with the directory, so a path the repository later adds
there is not hidden; worktrees of one repository share one exclude file, so a remove in
one worktree drops the lines the links of another still use until its next compose.

A layer's `link` is a soft link at the checkout root to `layers/<name>` in the tree,
written after the runtimes' links, excluded, pruned when the profile drops it, and
refused when it names a path a runtime links or `.qory`.

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

| `target.runtime` | Program | Links in the checkout | Model written to | MCP servers written to | Environment written to | Kinds with no place |
|---|---|---|---|---|---|---|
| `claude` | Claude Code | `.claude`, `.mcp.json` | `.claude/settings.json` `model` | `.mcp.json` `mcpServers` | `.claude/settings.json` `env` | none |
| `codex` | Codex CLI | `.codex`, `.agents/skills`, `AGENTS.override.md` | `.codex/config.toml` `model` | `.codex/config.toml` `mcp_servers` | `.codex/config.toml` `shell_environment_policy.set` | commands, output-styles |
| `gemini` | Gemini CLI | `.gemini`, `GEMINI.md` | `.gemini/settings.json` `model.name` | `.gemini/settings.json` `mcpServers` | not written | output-styles |
| `opencode` | OpenCode | `.opencode`, `.agents/skills`, `opencode.json`, `AGENTS.md` | `opencode.json` `model` | `opencode.json` `mcp`, in OpenCode's shape | not written | output-styles |
| `cursor` | Cursor, Cursor CLI | `.cursor`, `.agents/skills`, `AGENTS.md` | not written; a global CLI setting | `.cursor/mcp.json` `mcpServers` | not written | commands, output-styles |
| `copilot` | GitHub Copilot CLI | `.github/agents`, `.github/hooks`, `.agents/skills`, `AGENTS.md` | not written; a user setting | not written; a user file | not written | commands, output-styles, mcp |
| `amp` | Amp | `.amp`, `.agents/skills`, `AGENTS.md` | not written; Amp picks by mode | `.amp/settings.json` `amp.mcpServers` | not written | agents, commands, output-styles |
| `goose` | Goose | `.agents/skills`, `.agents/agents`, `AGENTS.md` | not written; no project file | not written; a user file | not written | commands, output-styles, mcp |
| `any` | any program that reads `AGENTS.md` and `.agents/skills` | `.agents/skills`, `AGENTS.md` | not written | not written | not written | agents, commands, output-styles, hooks, mcp |

The environment a runtime gets is the variables the layers export, the configuration's
`env` over them, and `QORY_HARNESS_HOME` as the tree's absolute path over both.

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
  and `model`, and `opencode.json` when a layer ships one, the profile names a model or the
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

`.qory/harness-report.json`, `version` 1: the profile name and file, the target, the
checkout, the home, the layers, the entries with their layer, the excludes, the checkout
paths a `--force` compose replaced, the `env` the harness exports with
`$QORY_HARNESS_HOME` in place of the home, and the profile's `extensions` as written. The
target's `runtime` is always an array: the runtimes the home holds after the compose, the
targeted ones first. A layer carries its `name`, its `manifest_name` when the manifest
gives one, its `source` as the profile writes it, its `pin`, `dirty` when git saw
uncommitted changes under a path source, the `variant` chosen, and its `link` when the
profile names one. A path source's pin is `working-tree`; a git source's pin is twelve
characters of its commit. `qory harness inspect` refuses a report of another version.

## The configuration

```yaml
apiVersion: qory.ai/v1alpha1
kind: QoryConfig
runtime: [claude, codex]         # instead of the profile's target.runtime
model: opus                      # instead of the profile's target.model
force: true                      # replace a tracked, unmodified file where a link goes
update: always                   # fetch every git source again on each compose
git:
  timeout: 10m                   # the longest one git command may run
  cache: /var/cache/qory         # where git sources are fetched to
env:
  HARNESS_PROFILE: nextjs        # exported to every runtime with a place for it
```

| Key | Default | Meaning |
|---|---|---|
| `runtime` | the profile's `target.runtime` | one runtime name or a list; `--runtime` wins over it |
| `model` | the profile's `target.model` | `--model` wins over it |
| `force` | `false` | what `--force` does on every compose; `--force=false` wins over it |
| `update` | `never` | `always` fetches every git source again on each compose; `--update` and `--update=false` win over it |
| `git.timeout` | `10m` | a git command running past it is stopped and the compose fails |
| `git.cache` | the user's cache directory, `qory/sources` under `~/Library/Caches`, `$XDG_CACHE_HOME` or `~/.cache` | absolute, or relative to the file naming it |
| `env` | none | variables written over what the layers export; a name two layers export with different values needs one here |

Every key is optional; a file naming none is read and changes nothing. `qory config`
prints every effective value and the file it came from.

## Exit status

| Status | Meaning |
|---|---|
| 0 | done |
| 1 | anything not listed below: a file that could not be written, a git source that could not be fetched |
| 2 | a mistake in the input: the command line, the profile, a layer, a fragment |
| 3 | a collision, printed with the excludes that resolve it |
| 4 | a path qory did not write standing where a link goes, which `--force` may replace |
