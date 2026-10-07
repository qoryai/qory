## qory

Get your coding agent ready to work, and keep it in check

### Synopsis

qory gets your coding agent ready to work, and keeps it in check.

  harness   build the agent's harness from modules, once for every agent you use
  worktree  give each branch its own worktree, ready to work
  run       run the agent behind a proxy: recorded, fenced, no tokens inside

It serves Claude Code, Codex, Gemini CLI, OpenCode, Cursor, Copilot CLI, Amp, Goose, and
any tool that reads AGENTS.md.

Start with qory setup repo in a repository, or qory setup example to try it.

Shortcuts:
  hc  harness compose
  hi  harness inspect
  hr  harness remove
  hl  harness launch
  wa  worktree add
  wr  worktree remove
  wl  worktree list

More: https://github.com/qoryai/qory

### Options

```
  -h, --help      help for qory
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory access-key](qory_access-key.md)	 - Make this machine's access key for its server
* [qory config](qory_config.md)	 - Show every setting, its value, and the file it came from
* [qory harness](qory_harness.md)	 - Build the agent's harness from modules
* [qory image](qory_image.md)	 - Check an image the wall runs the agent in
* [qory run](qory_run.md)	 - Run the agent on its harness, observed and recorded
* [qory setup](qory_setup.md)	 - Set up a repository, this machine, or your shell
* [qory update](qory_update.md)	 - Install the newest release of qory
* [qory version](qory_version.md)	 - Print the version, the build and the harness format this qory reads
* [qory worktree](qory_worktree.md)	 - Add, remove and list worktrees

