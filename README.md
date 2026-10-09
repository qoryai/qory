# 🐝 qory

`qory` gets your coding agent ready to work, and keeps it in check.

It does three things:

1. **It builds the harness.** A harness is what your agent reads: instructions, skills,
   agents, commands, settings. You write it once, as modules. `qory` composes it for
   every agent you use.
2. **It prepares the workspace.** One worktree per branch. Set up and ready to work.
3. **It runs the agent safely.** Every connection is recorded. You decide what the agent
   may reach. Your tokens stay outside.

<p align="center">
  <img src="docs/assets/slogan.png" alt="Don't worry, use Qory" width="720">
</p>

## Why

You have many repositories. You use more than one agent. Each agent reads its own folder:
`.claude`, `.codex`, `.gemini`. So every repository keeps copies. The copies drift.
Nobody knows which version a checkout runs.

And an agent acts on its own. It calls hosts you don't see. It holds your tokens.
Afterwards, you can't tell what it did.

## Install

```sh
brew install qoryai/tap/qory
```

Or with the install script, which puts the binary in `~/.local/bin`. Or with Go 1.27 or
above:

```sh
curl -fsSL https://raw.githubusercontent.com/qoryai/qory/main/install.sh | sh
go install github.com/qoryai/qory@latest
```

`qory update` installs the newest release, the same way this `qory` was installed. Every
command tells you when a newer release is out. `QORY_NO_UPDATE_CHECK=1` turns that off.
More: [qory update](docs/commands/qory_update.md).

## Try it

```sh
mkdir hello && cd hello
qory setup example       # a stack with two modules
qory harness compose     # build the harness into this folder
claude                   # type /hello
qory run -- -p "/hello"  # the same, observed and recorded
qory harness remove      # clean up
```

## 1. Build the harness

A **module** is one piece of harness. One piece for every repository of your team. One
for apps on your framework. One for this repository alone.

A **stack** lists modules in order. The repository commits it in `qory.yaml`:

```yaml
apiVersion: qory.dev/v1alpha1
harness:
  target:
    runtime: claude          # or [claude, codex]
  modules:
    - name: core             # shared by every repository, pinned to a tag
      source: {git: https://github.com/acme/harness, ref: v2.4.0, path: core}
    - name: app              # this repository's own
      source: {path: ./harness}
```

`qory harness compose` builds one tree from the stack. It writes the tree the way each
agent reads it. The tree stays out of git.

- **One module, every agent.** Claude Code, Codex, Gemini CLI, OpenCode, Cursor, Copilot
  CLI, Amp, Goose, and any tool that reads `AGENTS.md`.
- **No silent overrides.** Two modules ship the same skill? `qory` refuses until the
  stack picks one.
- **A report.** `qory harness inspect` shows where every entry came from.

Think of Docker: a module is an image, the stack is the Compose file.

More: [docs/harness.md](docs/harness.md).

## 2. Prepare the workspace

```sh
qory worktree add feature     # ../wt-feature, on branch feature, harness composed
qory worktree add --pr 7      # check out pull request 7
qory worktree remove          # remove the worktree and its branch
```

`qory.yaml` says what a new worktree needs: files to link, commands to run.

More: [docs/worktrees.md](docs/worktrees.md).

## 3. Run the agent safely

`qory run` starts the agent on its harness.

- **Observed.** All traffic goes through a proxy on your machine. Each run is recorded in
  `~/.local/state/qory/runs/`, or under `$XDG_STATE_HOME/qory` when that is set to an
  absolute path, outside the checkout.
- **Fenced.** Add a **wall**: the agent runs in a container. Its only way out is the
  proxy. You decide which hosts it may reach.
- **No tokens inside.** The agent gets a placeholder. The proxy adds the real token on
  the way out.
- **Reported.** Optionally, every run reports to your server.

```sh
qory run                                    # observed and recorded
qory run --wall docker --image my-agent:1   # in a container; you build the image
```

More: [docs/run.md](docs/run.md).

## Commands

| Command                | What it does                                              |
| ---------------------- | --------------------------------------------------------- |
| `qory setup repo`      | Write the repository's `qory.yaml`                        |
| `qory setup example`   | Write the hello example into this folder                  |
| `qory setup machine`   | Write your own `qory.yaml`: how `qory` runs here          |
| `qory setup shell`     | Completions, and a shell that follows worktrees           |
| `qory harness compose` | Build the harness (`qory hc`)                             |
| `qory harness inspect` | Show where each entry came from (`qory hi`)               |
| `qory harness remove`  | Remove the harness (`qory hr`)                            |
| `qory harness launch`  | Print the command that starts an agent on it (`qory hl`)  |
| `qory worktree add`    | Add a worktree, ready to work (`qory wa`)                 |
| `qory worktree remove` | Remove a worktree and its branch (`qory wr`)              |
| `qory worktree list`   | List every worktree and its branch (`qory wl`)            |
| `qory run`             | Run the agent, observed and recorded                      |
| `qory gateway`         | Run this machine's gateway for the runs of other machines |
| `qory config`          | Show every setting and where it comes from                |
| `qory update`          | Install the newest release                                |

Every command: [docs/commands](docs/commands/qory.md). The file format:
[contracts/harness/v1](contracts/harness/v1/README.md).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Contributions are made under the agreement in
[CLA.md](CLA.md).

## Licence

Apache License 2.0. See `LICENSE`. Qory™ is a trademark of 8wonders GmbH;
`TRADEMARKS.md` defines what you may do with the name.
