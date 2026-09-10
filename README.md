# 🐝 qory

`qory` composes the harness a runtime like Claude Code or Codex loads, from the modules a
repository names: a core your team shares, one for the framework, a specialised one for
marketing or automation, and whatever this repository alone needs. The repository commits
that list, not copies of the skills, agents, commands and settings the modules hold.

## The problem

You have instructions, skills, agents, commands and settings that your coding agent reads
from the repository, and you have more than one repository and more than one agent. Claude
Code reads `.claude`, Codex reads `.codex` and `AGENTS.override.md`, Gemini CLI reads
`.gemini` and `GEMINI.md`, OpenCode reads `.opencode` and `opencode.json`. So every
repository holds a copy per tool, and when the shared set improves, the copies drift. A
skill fixed in one place is stale in the next, and nothing tells you which version a
checkout runs with.

## What qory does

`qory` reads a compose file, the stack, that names the modules in order: the shared core,
the framework, the specialised one, the repository's own. It composes them into one tree and
renders that tree the way the target tool reads it. The tree is linked into the checkout and
left out of git, so what the repository carries is the stack and its own module, and a
report names the module every entry came from.

One module, written once, serves Claude Code, Codex CLI, Gemini CLI, OpenCode, Cursor,
GitHub Copilot CLI, Amp, Goose, and every tool that reads `AGENTS.md` and `.agents/skills`.
`qory harness compose --runtime codex` renders the same stack for another one.

A checkout can hold two of them at once: `runtime: [claude, codex]`, or
`qory hc --runtime claude,codex`, and both agents read the same harness in the same
directory. Stop working in one, continue in the other, and nothing has to be composed
again.

The model is Docker's: a module is an image, the compose file is the Compose file, and the
composed tree is what runs. One rule differs on purpose. When two modules provide the same
entry, `qory` refuses to compose until the stack says which one to keep. There is no
last-wins.

## Three steps

1. Write `qory-stack.yaml` in the repository, or in a directory above several
   repositories:

   ```yaml
   apiVersion: qory.ai/v1alpha1
   target:
     runtime: claude          # or both at once: [claude, codex]
     model: opus
   modules:
     - name: core             # what every repository of yours gets, pinned to a tag
       source: {git: https://github.com/acme/harness, ref: v2.4.0, path: core}
     - name: nextjs           # the framework
       source: {path: ../harness/nextjs}
     - name: marketing        # a specialised module, on the repositories that want it
       source: {path: ../harness/marketing}
     - name: app              # this repository's own, committed with it
       source: {path: ./harness}
       link: harness          # optional: <checkout>/harness reaches the module's files
   ```

   Every module carries a `qory-module.yaml` naming it, and a module named alone is read
   from `modules/<name>` at the repository root. A repository that takes an operator's
   harness as delivered writes `qory-compose.yaml` instead, naming the stack under
   `extends` and adding its own modules; the base's modules come first and cannot be
   changed.

2. Compose it:

   ```sh
   qory harness compose
   ```

3. Start your agent in the checkout. It reads the composed tree through the files it
   already looks for.

## Install

With Homebrew, on macOS:

```sh
brew install qoryai/tap/qory
```

With the install script, on macOS and Linux. It downloads the release binary for the
machine, verifies its checksum, and puts it in `~/.local/bin`:

```sh
curl -fsSL https://raw.githubusercontent.com/qoryai/qory/main/install.sh | sh
```

Set `QORY_BIN_DIR` to install somewhere else, and `QORY_VERSION` to a tag such as `v0.2.0`
to install a version other than the latest. Any version manager that reads GitHub releases works as well, for example
`mise use -g ubi:qoryai/qory`.

From source, with Go 1.27 or later:

```sh
go install github.com/qoryai/qory@latest
```

The binary lands in `$(go env GOPATH)/bin`; put that directory on your `PATH`.

## Try it

```sh
mkdir hello && cd hello && git init
qory harness init      # writes a stack and two modules
qory hc                # or: qory hc --runtime codex
claude                 # type /hello; with --runtime codex, gemini or opencode, that program
qory hr
```

Two modules, both convinced they invented greeting. The README that `init` writes says what
to delete to watch `qory` refuse the collision. The same files are in
[examples/hello](examples/hello/README.md).

## Commands

```sh
qory harness init        # write the hello example into the current directory
qory harness compose     # compose the stack into the checkout you stand in   (qory hc)
qory harness inspect     # print the report: every entry and the module it came from (qory hi)
qory harness remove      # remove the composed tree and its links               (qory hr)
qory config              # print every setting, its value and the file it came from
qory version
```

`qory harness compose --dry-run` prints the report and writes nothing. `-f <file>` reads a
stack instead of discovering one. `--runtime` and `--model` override the stack's target
for one compose, and `--runtime` takes a list: `--runtime claude,codex`. `--force` replaces
a file the repository tracks, unmodified, where a link goes, and `git checkout --` brings
it back. `--update` fetches every git source again. `-v` prints one line per entry.
`qory harness remove --runtime codex` drops one runtime and keeps the rest composed.

The exit status tells the failures apart: 2 for a mistake in the input, 3 for a collision,
4 for a path `qory` would not replace, 1 for anything else.

How `qory` runs on a machine is a `qory.yaml`, in `~/.config/qory`, in a directory above
the checkouts, or in the checkout root, the nearest winning and every flag over all of
them. It sets the runtime and model in place of the stack's, `force` and `update` as
standing choices, the git timeout and cache directory, and variables exported to the
runtime. Every setting has a default, so the file is optional.

The reference, one page per command, is under [docs/commands](docs/commands/qory.md).

## What you get

- The files your tool reads, linked to a composed tree under `.qory` in the checkout, all of
  it excluded from git through the clone-local exclude file. The tree is built once; each
  tool gets its own directory in it. `.claude` is a real directory with one link per entry,
  so the `settings.local.json` Claude Code writes stays yours.
- Settings merged from every module per target file, in the tool's own format: permission
  lists concatenated and deduplicated, hooks concatenated, hook commands rewritten to the
  composed tree's path. A module's other files, its scripts say, are there too, as
  `$QORY_HARNESS_HOME/modules/<name>/…`, at a checkout-root name the stack chooses, and
  in a variable the module exports, `HARNESS_HOME: .` in its manifest.
- MCP servers, one JSON file each in a module, written where every tool reads them:
  `.mcp.json` for Claude Code, `config.toml` for Codex, and so on.
- Modules from a directory beside the repository, or from a git repository at a tag,
  fetched once and pinned by commit in the report.
- One instruction file, concatenated from the modules in order, presented as `CLAUDE.md`,
  `AGENTS.override.md` or `GEMINI.md` where a tool wants another name.
- Agents and commands written in each tool's format from one source file, and a line in
  the compose output for every kind a tool has no place for.
- A report, printed by `qory harness inspect`, that names the module of every entry and every
  exclude the stack made.
- A refusal, with the lines that resolve it, when two modules provide the same entry.

## The format

The compose file, the module manifest `qory-module.yaml`, the composition rules and
the runtimes are specified in [contracts/harness/v1](contracts/harness/v1/README.md), with
JSON schemas and the fixtures the test suite runs.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Contributions are made under the agreement in
[CLA.md](CLA.md).

## Licence

Apache License 2.0. See `LICENSE`. Qory™ is a trademark of 8wonders GmbH;
`TRADEMARKS.md` says what you may do with the name.
