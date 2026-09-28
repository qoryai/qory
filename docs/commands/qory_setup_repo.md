## qory setup repo

Write the repository's qory.yaml and its own module

### Synopsis

Set the repository up for qory.

It writes a qory.yaml into the current directory: the repository's own stack, with one
runtime and one module, and every key a repository commits. It writes that module under
harness/, with its manifest and AGENTS.md.

A stack already there, in qory.yaml or qory-stack.yaml, is kept. So is every file that is
already there.

Commit this qory.yaml. It decides for everyone who clones the repository: the stack, and
what a worktree needs. Your own settings go in the qory.yaml that setup machine writes.

```
qory setup repo [flags]
```

### Options

```
  -h, --help   help for repo
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory setup](qory_setup.md)	 - Set up a repository, this machine, or your shell

