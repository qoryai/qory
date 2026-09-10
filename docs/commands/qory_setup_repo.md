## qory setup repo

Write the repository's qory.yaml: its stack, its module, its worktree settings

### Synopsis

Set the repository up for qory.

setup repo writes into the current directory a qory.yaml holding the repository's own
stack, one runtime and one module, with every key a repository commits shown, and that
module under harness with its manifest and AGENTS.md. A directory whose qory.yaml already
names a stack, or that holds a qory-stack.yaml, keeps its stack. A file that is already
there is kept.

This qory.yaml is committed and decides for everyone who clones the repository: the
stack under harness, and what a worktree needs under worktree. How qory runs on one
machine, for every repository, is the qory.yaml that setup machine writes.

```
qory setup repo [flags]
```

### Options

```
  -h, --help   help for repo
```

### SEE ALSO

* [qory setup](qory_setup.md)	 - Set up the repository, the machine's configuration, or your shell

