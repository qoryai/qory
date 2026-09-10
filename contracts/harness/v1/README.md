# Harness compose format, v1alpha1

What `qory harness compose` reads and what it writes.

## Two files

| File | Lives in | Defines |
|---|---|---|
| `harness-compose.yaml` | the repository root, or an ancestor directory covering several repositories | the profile: the ordered layers, their sources, the target runtime and model, the excludes |
| `harness.yaml` | the root of a layer | the layer: its variants per runtime |

`harness.yaml` is optional. A directory holding `skills/`, `agents/` and the other entry
kinds, with no manifest, is a valid layer with one variant.

Every document carries `apiVersion` and `kind`. A reader refuses a version it does not read
and names the versions it does. `v1alpha1` says the format may still change.

## Discovery

In order: the `-f <file>` flag; `harness-compose.yaml` in the checkout root; the nearest
`harness-compose.yaml` in an ancestor directory that the current user owns. A file another
user could write is not read.

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
```

| Field | Required | Meaning |
|---|---|---|
| `apiVersion` | yes | `qory.ai/v1alpha1` |
| `kind` | yes | `HarnessProfile` |
| `name` | no | the profile's name in the report. Default: `owner/name` from the origin remote, else the directory name |
| `target.runtime` | yes | the program that runs the harness, one of the runtimes in §Runtimes, or a list of them to compose for at once |
| `target.model` | no | written into the settings of every targeted runtime that has a project-level place for it (§Runtimes) |
| `layers` | yes | ordered, at least one. Order decides the merge order of settings and instruction sections |
| `layers[].name` | yes | unique within the profile; used in excludes, the report and error messages |
| `layers[].source` | yes | `{path: <dir>}`, relative to the profile file; or `{git: <url>, ref: <tag, branch or commit>}` with an optional `path` to the layer's directory inside the repository (§Sources) |
| `layers[].exclude` | no | entries of this layer left out, by kind: `skills`, `agents`, `commands`, `output-styles`, `hooks`, `mcp` |
| `layers[].variant` | no | forces one of the layer's variants instead of the one named like the targeted runtime |

The schema is [profile.schema.json](profile.schema.json).

## Sources

A **path source** reads a directory as it stands. Its pin is `working-tree`, marked dirty in
the report when git sees uncommitted changes under it.

A **git source** reads a repository at a ref: a tag, a branch, or a commit by its full id.
The ref is resolved once and its commit fetched, at depth one, into
`qory/sources/<url>/<commit>` under the user's cache directory; the pin is that commit. A
compose after that reads the cache and needs no network. `qory harness compose --update`
resolves every git source's ref again, which is how a branch ref moves, and it moves for
that checkout alone: clones are kept per commit, so a checkout composed from the earlier
commit keeps reading it. `path` names the layer's directory inside the repository, for a
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
```

A layer's tree holds these entry kinds:

```
AGENTS.md                        merged; the instruction section, tool-agnostic
skills/<name>/SKILL.md           atomic; the Agent Skills layout, tool-agnostic
agents/<name>.md                 atomic; frontmatter name, description, and the keys some tools read (model, tools, mode); body = system prompt
commands/<name>.md               atomic; frontmatter description; body = prompt, $ARGUMENTS for the arguments
output-styles/<name>.md          atomic; Claude Code only
hooks/<file>                     atomic; scripts that a settings fragment names as $QORY_HARNESS_HOME/hooks/<file>; files only
mcp/<name>.json                  atomic; one MCP server: the object a runtime's own file holds under the server's name
settings/<runtime>/<file>       merged; one fragment per target file of one runtime, in that runtime's format
<anything else>                  the layer's own files, reached as $QORY_HARNESS_HOME/layers/<name>/<path>
```

`AGENTS.md` and skills are read by every runtime. Agents and commands are written in each
runtime's format from the one file above; a runtime without the concept skips the kind and
the compose says so. Settings are runtime-native: `settings/claude/settings.json`,
`settings/codex/config.toml`, `settings/gemini/settings.json`, `settings/cursor/hooks.json`,
and so on, merged per file across layers.

An MCP server is one JSON object per file, in the shape Claude Code's `.mcp.json` holds under
`mcpServers`: `command`, `args` and `env` for a stdio server, `url` for a remote one. Every
runtime with a project-level place for servers gets it there, rewritten where its shape
differs (§Runtimes). A directory under `hooks/` fails the compose: a hook is one file, and a
script's helpers live anywhere else in the layer, under `scripts/` say, reached through
`layers/<name>` in the composed tree.

The schema is [layer.schema.json](layer.schema.json).

## Composition rules

1. **One flat tree, one entry per name.** Every atomic entry links into `<kind>/<name>` from
   the layer that provides it.
2. **Excludes first, then the collision check.** After each layer's `exclude` is applied, an
   atomic name provided by more than one layer fails the compose. The message lists every
   layer and prints the exclude lines that resolve it:

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
The tree holds the tool-agnostic parts once, `AGENTS.md`, `skills/` and `hooks/`, one link
per layer at `layers/<name>` to the layer's own directory, and one directory per runtime
with that runtime's files. Every atomic entry is a symlink into its layer. Every
`$QORY_HARNESS_HOME`, braced or not, in a settings fragment or an MCP server becomes the
tree's absolute path, so `$QORY_HARNESS_HOME/layers/core/scripts/check.sh` runs the script
the core layer ships.

A tree holds every runtime the checkout is composed for, so one checkout serves two agents
at once and a person can stop working in one and continue in the other. A compose renders
the targeted runtimes and every runtime already in the tree, because the tree is replaced
whole, and each runtime's links keep resolving. A layer whose variants differ between two
targeted runtimes cannot be rendered for both, since the tree holds one copy of each entry:
the compose refuses and names both runtimes.

The checkout gets relative links into the tree, listed per runtime below, each excluded
through the clone-local exclude file, so a moved checkout keeps working and every path a
tool checks resolves inside the project. A link to a file, such as `AGENTS.md`, is one
symlink. A link to a directory, such as `.claude`, is a real directory in the checkout
holding one symlink per entry of the tree's directory, `.claude/skills` and
`.claude/settings.json` alike, so the checkout's own files there stay: Claude Code's
`.claude/settings.local.json` survives every compose, and a repository may keep its own
agents under `.github/agents` beside the linked ones. A compose also takes back what an
earlier one linked and this one does not. `qory harness remove --runtime <name>` takes one
runtime's links and leaves a link another composed runtime shares, such as `.agents/skills`.

A path qory did not write is never replaced. Where a hard link goes, such as
`.claude/settings.json`, it fails the compose; where a soft link goes, such as `AGENTS.md`,
it is skipped and reported, so a repository's instructions stay its own. With
`--force`, a file or directory that git tracks and that is unmodified is removed for the
link, either kind, and listed in the report under `replaced`; `git checkout --` brings it
back, and `qory harness remove` prints that command. Anything untracked or modified is still
refused, because git could not restore it.

## Runtimes

| `target.runtime` | Program | Links in the checkout | Model written to | MCP servers written to | Kinds with no place |
|---|---|---|---|---|---|
| `claude` | Claude Code | `.claude`, `.mcp.json` | `.claude/settings.json` `model` | `.mcp.json` `mcpServers` | none |
| `codex` | Codex CLI | `.codex`, `.agents/skills`, `AGENTS.override.md` | `.codex/config.toml` `model` | `.codex/config.toml` `mcp_servers` | commands, output-styles |
| `gemini` | Gemini CLI | `.gemini`, `GEMINI.md` | `.gemini/settings.json` `model.name` | `.gemini/settings.json` `mcpServers` | output-styles |
| `opencode` | OpenCode | `.opencode`, `.agents/skills`, `opencode.json`, `AGENTS.md` | `opencode.json` `model` | `opencode.json` `mcp`, in OpenCode's shape | output-styles |
| `cursor` | Cursor, Cursor CLI | `.cursor`, `.agents/skills`, `AGENTS.md` | not written; a global CLI setting | `.cursor/mcp.json` `mcpServers` | commands, output-styles |
| `copilot` | GitHub Copilot CLI | `.github/agents`, `.github/hooks`, `.agents/skills`, `AGENTS.md` | not written; a user setting | not written; a user file | commands, output-styles, mcp |
| `amp` | Amp | `.amp`, `.agents/skills`, `AGENTS.md` | not written; Amp picks by mode | `.amp/settings.json` `amp.mcpServers` | agents, commands, output-styles |
| `goose` | Goose | `.agents/skills`, `.agents/agents`, `AGENTS.md` | not written; no project file | not written; a user file | commands, output-styles, mcp |
| `any` | any tool that reads `AGENTS.md` and `.agents/skills` | `.agents/skills`, `AGENTS.md` | not written | not written | agents, commands, output-styles, hooks, mcp |

Per runtime, the files written into its directory:

- **claude**: links for every kind, `settings.json` with `env.QORY_HARNESS_HOME` and the
  model, `mcp.json` with the servers, linked as `.mcp.json` at the checkout root, and the
  instructions written in full as `CLAUDE.md`. They are not imported from `AGENTS.md`,
  because Claude Code resolves a link to its real path and asks about an import found
  through one on every start.
- **codex**: `config.toml` with the model, the servers as `[mcp_servers.<name>]` tables and
  any other `settings/codex/` file, one `agents/<name>.toml` per agent with the body as
  `developer_instructions`. Codex reads `AGENTS.override.md` before `AGENTS.md`, and a
  project `.codex` only in a trusted project.
- **gemini**: `settings.json` with `model.name` and `mcpServers`, links for skills and hooks,
  one `agents/<name>.md` with `name` and `description`, one `commands/<name>.toml` with
  `$ARGUMENTS` as `{{args}}`. Gemini reads a project `.gemini` only in a trusted folder.
- **opencode**: links for commands and hooks, `agents/<name>.md` with `description`, `mode`
  and `model`, and `opencode.json` when a layer ships one, the profile names a model or the
  compose holds a server. A server with `url` becomes `{type: remote, url}`, any other
  `{type: local, command: [command, args...], environment: env}`.
- **cursor**: `agents/<name>.md` with `name`, `description` and `model`, links for hooks,
  and the `settings/cursor/` files such as `hooks.json`, `cli.json` and `mcp.json`, the last
  with the servers under `mcpServers`.
- **copilot**: `agents/<name>.agent.md` with `name`, `description`, `tools` and `model`, and
  the `settings/copilot/` files, which are hook files under `hooks/`.
- **amp**: the `settings/amp/` files such as `settings.json`, with the servers under
  `amp.mcpServers`.
- **goose**: `agents/<name>.md` with `name`, `description` and `model`.
- **any**: nothing of its own.

Programs that read `AGENTS.md` and `.agents/skills` and have no package of their own include
DeepSeek Harness, Kilo Code, Kimi CLI, Factory Droid, Mistral Vibe, JetBrains Junie, Augment
CLI, Warp, Windsurf, Zed and Crush; compose for them with `target.runtime: any`. A
package for one of them under `internal/render/` implements one interface.

## The report

`.qory/harness-report.json`, version 1: the profile name and file, the target, the checkout,
the home, the layers with source and pin, the entries with their layer, the excludes, and
the checkout paths a `--force` compose replaced. A path source's pin is `working-tree`,
marked dirty when git sees uncommitted changes under it; a git source's pin is its commit.

## Exit status

| Status | Meaning |
|---|---|
| 0 | done |
| 1 | anything not listed below: a file that could not be written, a git source that could not be fetched |
| 2 | a mistake in the input: the command line, the profile, a layer, a fragment |
| 3 | a collision, printed with the excludes that resolve it |
| 4 | a path qory did not write standing where a link goes, which `--force` may replace |
