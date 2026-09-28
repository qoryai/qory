## qory setup shell

Add completions, and make your shell follow worktree add and remove

### Synopsis

Add qory's completions to your shell's rc file, and a function that follows worktrees:
it changes into a new worktree on add, and back to the main checkout on remove.

A program cannot change the directory of the shell that ran it. So the function runs
worktree add and remove with --path, and changes to the path they print.

The shell is the one in $SHELL. setup shell shows the lines, asks before writing them,
and says how to reload. --print prints them and writes nothing.

```
qory setup shell [flags]
```

### Examples

```
  qory setup shell                     # add the lines to your rc file
  eval "$(qory setup shell --print)"   # or load them from an rc file a tool of yours owns
```

### Options

```
  -h, --help    help for shell
      --print   print the lines and write nothing
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory setup](qory_setup.md)	 - Set up a repository, this machine, or your shell

