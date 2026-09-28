## qory setup completion

Print the completion script for your shell

### Synopsis

Print the completion script for the shell in $SHELL, or the one the argument names.

Source it at every start; do not keep a copy. It is generated from this binary, so it
always matches it. The lines setup shell adds already source it.

```
qory setup completion [bash|zsh|fish|powershell] [flags]
```

### Examples

```
  source <(qory setup completion zsh)   # zsh or bash
  qory setup completion fish | source   # fish
```

### Options

```
  -h, --help   help for completion
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory setup](qory_setup.md)	 - Set up a repository, this machine, or your shell

