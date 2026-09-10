## qory setup machine

Write your qory.yaml in ~/.config/qory: how qory runs on this machine

### Synopsis

Write the machine's qory.yaml, in $XDG_CONFIG_HOME/qory or ~/.config/qory: how qory runs
on this machine, every key shown at its default. A file that is already there is kept.

This qory.yaml is yours, never committed, and applies to every repository you work in:
the runtime and model to compose for instead of the stack's, force and update, where a
worktree goes and what it is called, the git timeout and cache, and environment
variables. The repository's own qory.yaml, which setup repo writes, is read on top of it,
and qory config shows every key with the file it came from.

```
qory setup machine [flags]
```

### Options

```
  -h, --help   help for machine
```

### SEE ALSO

* [qory setup](qory_setup.md)	 - Set up the repository, the machine's configuration, or your shell

