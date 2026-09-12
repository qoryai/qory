## qory setup shell

Add completions and a function that follows worktree add and remove to your shell

### Synopsis

Add qory's completions and a function that follows a worktree add into the worktree and
a remove back to the main checkout to your shell's rc file. A program cannot change the
directory of the shell that ran it, so the function runs worktree add and remove with
--path and cd's to the path they print. The shell is the one in $SHELL; setup shell shows
the lines, asks before writing them, and says how to reload. With --print it prints the
lines and writes nothing, for an rc file a tool of yours owns:
eval "$(qory setup shell --print)".

```
qory setup shell [flags]
```

### Options

```
  -h, --help    help for shell
      --print   print the lines and write nothing, for an rc file a tool of yours owns
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help says what
```

### SEE ALSO

* [qory setup](qory_setup.md)	 - Set up the repository, the machine's configuration, or your shell

