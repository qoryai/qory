## qory setup machine

Write your own qory.yaml: how qory runs on this machine

### Synopsis

Write your own qory.yaml, in ~/.config/qory ($XDG_CONFIG_HOME/qory). Every key is shown
at its default. A file already there is kept.

This file is yours, and never committed. It applies to every repository: the runtime and
model, force and update, where worktrees go and what they are called, the git timeout
and cache, and environment variables.

A repository's own qory.yaml is read on top of it. qory config shows where each value
came from.

```
qory setup machine [flags]
```

### Options

```
  -h, --help   help for machine
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory setup](qory_setup.md)	 - Set up a repository, this machine, or your shell

