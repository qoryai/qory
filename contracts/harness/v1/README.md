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
    source: {path: ../harness/core}
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
| `layers[].source` | yes | `{path: <dir>}`, relative to the profile file |
| `layers[].exclude` | no | entries of this layer left out, by kind: `skills`, `agents`, `commands`, `output-styles`, `hooks` |
| `layers[].variant` | no | forces one of the layer's variants instead of the one named like the targeted runtime |

The schema is [profile.schema.json](profile.schema.json).

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
hooks/<file>                     atomic; scripts that a settings fragment names as $QORY_HARNESS_HOME/hooks/<file>
settings/<runtime>/<file>       merged; one fragment per target file of one runtime, in that runtime's format
```

`AGENTS.md` and skills are read by every runtime. Agents and commands are written in each
runtime's format from the one file above; a runtime without the concept skips the kind and
the compose says so. Settings are runtime-native: `settings/claude/settings.json`,
`settings/codex/config.toml`, `settings/gemini/settings.json`, `settings/cursor/hooks.json`,
and so on, merged per file across layers.

The schema is [layer.schema.json](layer.schema.json).

## Composition rules

1. **One flat tree, one entry per name.** Every atomic entry links into `<kind>/<name>` from
   the layer that provides it.
2. **Excludes first, then the collision check.** After each layer's `exclude` is applied, an
   atomic name provided by more than one layer fails the compose. The message lists every
   runtime and prints the exclude lines that resolve it:

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
   layers' files, separated by a blank line.
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
The tree holds the tool-agnostic parts once, `AGENTS.md`, `skills/` and `hooks/`, and one
directory per runtime with that runtime's files. Every atomic entry is a symlink into its
layer. Every `$QORY_HARNESS_HOME` in a settings fragment becomes the tree's absolute path.

A tree holds every runtime the checkout is composed for, so one checkout serves two agents
at once and a person can stop working in one and continue in the other. A compose renders
the targeted runtimes and every runtime already in the tree, because the tree is replaced
whole, and each runtime's links keep resolving. A layer whose variants differ between two
targeted runtimes cannot be rendered for both, since the tree holds one copy of each entry:
the compose refuses and names both runtimes.

The checkout gets relative links into the tree, listed per runtime below, each excluded
through the clone-local exclude file, so a moved checkout keeps working and every path a
tool checks resolves inside the project. A directory link such as `.claude` is refused when the checkout
holds a real directory there. A root file link such as `AGENTS.md` is skipped and reported
when the checkout has its own, so a repository's instructions stay its own.

## Runtimes

| `target.runtime` | Program | Links in the checkout | Model written to | Kinds with no place |
|---|---|---|---|---|
| `claude` | Claude Code | `.claude` | `.claude/settings.json` `model` | none |
| `codex` | Codex CLI | `.codex`, `.agents/skills`, `AGENTS.override.md` | `.codex/config.toml` `model` | commands, output-styles |
| `gemini` | Gemini CLI | `.gemini`, `GEMINI.md` | `.gemini/settings.json` `model.name` | output-styles |
| `opencode` | OpenCode | `.opencode`, `.agents/skills`, `opencode.json`, `AGENTS.md` | `opencode.json` `model` | output-styles |
| `cursor` | Cursor, Cursor CLI | `.cursor`, `.agents/skills`, `AGENTS.md` | not written; a global CLI setting | commands, output-styles |
| `copilot` | GitHub Copilot CLI | `.github/agents`, `.github/hooks`, `.agents/skills`, `AGENTS.md` | not written; a user setting | commands, output-styles |
| `amp` | Amp | `.amp`, `.agents/skills`, `AGENTS.md` | not written; Amp picks by mode | agents, commands, output-styles |
| `goose` | Goose | `.agents/skills`, `.agents/agents`, `AGENTS.md` | not written; no project file | commands, output-styles |
| `any` | any tool that reads `AGENTS.md` and `.agents/skills` | `.agents/skills`, `AGENTS.md` | not written | agents, commands, output-styles, hooks |

Per runtime, the files written into its directory:

- **claude**: links for every kind, `settings.json` with `env.QORY_HARNESS_HOME` and the
  model, and the instructions written in full as `CLAUDE.md`. They are not imported from
  `AGENTS.md`, because Claude Code resolves the `.claude` link to its real path and asks
  about an import found through a link on every start.
- **codex**: `config.toml` with the model and any other `settings/codex/` file, one
  `agents/<name>.toml` per agent with the body as `developer_instructions`. Codex reads
  `AGENTS.override.md` before `AGENTS.md`, and a project `.codex` only in a trusted project.
- **gemini**: `settings.json` with `model.name`, links for skills and hooks, one
  `agents/<name>.md` with `name` and `description`, one `commands/<name>.toml` with
  `$ARGUMENTS` as `{{args}}`. Gemini reads a project `.gemini` only in a trusted folder.
- **opencode**: links for commands and hooks, `agents/<name>.md` with `description`, `mode`
  and `model`, and `opencode.json` when a layer ships one or the profile names a model.
- **cursor**: `agents/<name>.md` with `name`, `description` and `model`, links for hooks,
  and the `settings/cursor/` files such as `hooks.json`, `cli.json` and `mcp.json`.
- **copilot**: `agents/<name>.agent.md` with `name`, `description`, `tools` and `model`, and
  the `settings/copilot/` files, which are hook files under `hooks/`.
- **amp**: the `settings/amp/` files such as `settings.json`.
- **goose**: `agents/<name>.md` with `name`, `description` and `model`.
- **any**: nothing of its own.

Programs that read `AGENTS.md` and `.agents/skills` and have no package of their own include
DeepSeek Harness, Kilo Code, Kimi CLI, Factory Droid, Mistral Vibe, JetBrains Junie, Augment
CLI, Warp, Windsurf, Zed and Crush; compose for them with `target.runtime: any`. A
package for one of them under `internal/render/` implements one interface.

## The report

`.qory/harness-report.json`, version 1: the profile name and file, the target, the checkout,
the home, the layers with source and pin, the entries with their layer, and the excludes. A
path source's pin is `working-tree`, marked dirty when git sees uncommitted changes under it.
