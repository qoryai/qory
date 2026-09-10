# 🐝 qory

`qory` composes the harness a runtime like Claude Code or Codex loads, from the layers a
repository names: a core your team shares, one for the framework, a specialised one for
marketing or automation, and whatever this repository alone needs. The repository commits
that list, not copies of the skills, agents, commands and settings the layers hold.

## The problem

You have instructions, skills, agents, commands and settings that your coding agent reads
from the repository, and you have more than one repository and more than one agent. Claude
Code reads `.claude`, Codex reads `.codex` and `AGENTS.override.md`, Gemini CLI reads
`.gemini` and `GEMINI.md`, OpenCode reads `.opencode` and `opencode.json`. So every
repository holds a copy per tool, and when the shared set improves, the copies drift. A
skill fixed in one place is stale in the next, and nothing tells you which version a
checkout runs with.

## What qory does

`qory` reads a compose file, the profile, that names the layers in order: the shared core,
the framework, the specialised one, the repository's own. It composes them into one tree and
renders that tree the way the target tool reads it. The tree is linked into the checkout and
left out of git, so what the repository carries is the profile and its own layer, and a
report names the layer every entry came from.

One layer, written once, serves Claude Code, Codex CLI, Gemini CLI, OpenCode, Cursor,
GitHub Copilot CLI, Amp, Goose, and every tool that reads `AGENTS.md` and `.agents/skills`.
`qory harness compose --runtime codex` renders the same profile for another one.

A checkout can hold two of them at once: `runtime: [claude, codex]`, or
`qory hc --runtime claude,codex`, and both agents read the same harness in the same
directory. Stop working in one, continue in the other, and nothing has to be composed
again.

The model is Docker's: a layer is an image, the compose file is the Compose file, and the
composed tree is what runs. One rule differs on purpose. When two layers provide the same
entry, `qory` refuses to compose until the profile says which one to keep. There is no
last-wins.

## Three steps

1. Write `harness-compose.yaml` in the repository, or in a directory above several
   repositories:

   ```yaml
   apiVersion: qory.ai/v1alpha1
   kind: HarnessProfile
   target:
     runtime: claude          # or both at once: [claude, codex]
     model: opus
   layers:
     - name: core             # what every repository of yours gets
       source: {path: ../harness/core}
     - name: nextjs           # the framework
       source: {path: ../harness/nextjs}
     - name: marketing        # a specialised layer, on the repositories that want it
       source: {path: ../harness/marketing}
     - name: app              # this repository's own, committed with it
       source: {path: ./harness}
   ```

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

Set `QORY_BIN_DIR` to install somewhere else, and `QORY_VERSION` to install a version other
than the latest. Any version manager that reads GitHub releases works as well, for example
`mise use -g ubi:qoryai/qory`.

From source, with Go 1.27 or later:

```sh
go install github.com/qoryai/qory@latest
```

The binary lands in `$(go env GOPATH)/bin`; put that directory on your `PATH`.

## Try it

```sh
mkdir hello && cd hello && git init
qory harness init      # writes a profile and two layers
qory hc                # or: qory hc --runtime codex
claude                 # type /hello; or codex, gemini, opencode
qory hr
```

Two layers, both convinced they invented greeting. The README that `init` writes says what
to delete to watch `qory` refuse the collision. The same files are in
[examples/hello](examples/hello/README.md).

## Commands

```sh
qory harness init        # write the hello example into the current directory
qory harness compose     # compose the profile into the checkout you stand in   (qory hc)
qory harness inspect     # print the report: every entry and the layer it came from (qory hi)
qory harness remove      # remove the composed tree and the link                 (qory hr)
qory version
```

`qory harness compose --dry-run` prints the report and writes nothing. `-f <file>` reads a
profile instead of discovering one. `--runtime` and `--model` override the profile's target
for one compose, and `--runtime` takes a list: `--runtime claude,codex`. `-v` prints one
line per entry.

The reference, one page per command, is under [docs/commands](docs/commands/qory.md).

## What you get

- The files your tool reads, linked to a composed tree under `.qory` in the checkout, all of
  it excluded from git through the clone-local exclude file. The tree is built once; each
  tool gets its own directory in it.
- Settings merged from every layer per target file, in the tool's own format: permission
  lists concatenated and deduplicated, hooks concatenated, hook commands rewritten to the
  composed tree's path.
- One instruction file, concatenated from the layers in order, presented as `CLAUDE.md`,
  `AGENTS.override.md` or `GEMINI.md` where a tool wants another name.
- Agents and commands written in each tool's format from one source file, and a line in
  the compose output for every kind a tool has no place for.
- A report, printed by `qory harness inspect`, that names the layer of every entry and every
  exclude the profile made.
- A refusal, with the lines that resolve it, when two layers provide the same entry.

## The format

The compose file, the optional layer manifest `harness.yaml`, the composition rules and
the runtimes are specified in [contracts/harness/v1](contracts/harness/v1/README.md), with
JSON schemas and the fixtures the test suite runs.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Contributions are made under the agreement in
[CLA.md](CLA.md).

## Licence

Apache License 2.0. See `LICENSE`. Qory™ is a trademark of 8wonders GmbH;
`TRADEMARKS.md` says what you may do with the name.
