# 🐝 qory

`qory` composes the harness your coding agent reads, from the modules a repository names:
a core your team shares, one for the framework, and what this repository alone needs. The
repository commits that list, not copies of the skills, agents, commands and settings.

## The problem

Your coding agent reads instructions, skills, agents, commands and settings from the
repository. You have more than one repository, and more than one agent. Claude Code reads
`.claude`, Codex reads `.codex`, Gemini CLI reads `.gemini`. So every repository holds a
copy per tool, and when the shared set improves, the copies drift. Nothing tells you which
version a checkout runs with.

## What qory does

A stack names the modules in order: the shared core, the framework, the repository's own.
`qory` composes them into one tree and renders that tree the way each tool reads it. The
tree is linked into the checkout and kept out of git. A report names the module every
entry came from.

One module, written once, serves Claude Code, Codex CLI, Gemini CLI, OpenCode, Cursor,
GitHub Copilot CLI, Amp, Goose, and every tool that reads `AGENTS.md` and `.agents/skills`.
A checkout can hold two of them at once, and both agents read the same harness.

The model is Docker's: a module is an image, the stack is the Compose file, and the
composed tree is what runs. One rule differs on purpose: when two modules provide the same
entry, `qory` refuses to compose until the stack says which one to keep. There is no
last-wins.

## Three steps

1. Write `qory-stack.yaml` in the repository:

   ```yaml
   apiVersion: qory.ai/v1alpha1
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

   Every module carries a `qory-module.yaml` naming it. A repository that takes a stack as
   delivered writes `qory-compose.yaml` instead: it names the stack under `extends` and adds
   its own modules, and the stack's modules cannot be changed.

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
qory harness init      # writes a stack and two modules
qory hc                # or: qory hc --runtime codex
claude                 # type /hello
qory hr
```

Two modules, both convinced they invented greeting. The README that `init` writes says what
to delete to watch `qory` refuse the collision.

## Commands

```sh
qory harness init        # write the hello example into the current directory
qory harness compose     # compose the stack into the checkout you stand in   (qory hc)
qory harness inspect     # the report: every entry and the module it came from (qory hi)
qory harness remove      # remove the composed tree and its links               (qory hr)
qory config              # every setting, its value and the file it came from
```

Flags worth knowing on `compose`:

```
--dry-run              print the report and write nothing
--runtime claude,codex render for these runtimes instead of the stack's
--model opus           write this model instead of the stack's
--force                replace a tracked, unmodified file where a link goes
--update               fetch every git source again
```

How `qory` runs on a machine is `qory.yaml`, in `~/.config/qory` or in the checkout. Every
setting has a default, so the file is optional. The reference, one page per command, is
under [docs/commands](docs/commands/qory.md).

## What you get

- The files your tool reads, linked to a composed tree under `.qory` and kept out of git.
  Your own `settings.local.json` stays yours.
- Settings merged from every module, in the tool's own format. Permission lists and hooks
  join; a value set twice to different things is a collision, never a silent override.
- MCP servers, one file each in a module, written where every tool reads them.
- Modules from a directory beside the repository, or from a git repository at a tag,
  pinned by commit in the report.
- One instruction file, joined from the modules in order, under the name each tool wants.
- A report that names the module of every entry, and a refusal with the lines that resolve
  it when two modules provide the same one.

## The format

The stack, the compose file, the module manifest, the composition rules and the runtimes
are specified in [contracts/harness/v1](contracts/harness/v1/README.md), with JSON schemas
and the fixtures the test suite runs.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Contributions are made under the agreement in
[CLA.md](CLA.md).

## Licence

Apache License 2.0. See `LICENSE`. Qory™ is a trademark of 8wonders GmbH;
`TRADEMARKS.md` says what you may do with the name.
