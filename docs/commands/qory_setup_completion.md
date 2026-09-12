## qory setup completion

Print the completion script; setup shell makes your shell source it

### Synopsis

Print the completion script for a shell, the one in $SHELL unless named. The script
is for the shell to source at every start, not to read or to keep: it is generated from
the command tree, so it always matches the binary. The lines setup shell adds source it;
to source it yourself, source <(qory setup completion zsh) in zsh or bash, and
qory setup completion fish | source in fish.

```
qory setup completion [bash|zsh|fish|powershell] [flags]
```

### Options

```
  -h, --help   help for completion
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help says what
```

### SEE ALSO

* [qory setup](qory_setup.md)	 - Set up the repository, the machine's configuration, or your shell

