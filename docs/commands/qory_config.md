## qory config

Print the effective configuration and where each value comes from

### Synopsis

Print the effective configuration and where each value comes from.

Every setting has a default. A qory.yaml sets the keys it names; the files apply in this
order, each overriding the one before it: the user's, in $XDG_CONFIG_HOME/qory or
~/.config/qory, then the ones in the checkout's ancestor directories the current user
owns, the farthest first, then the one in the checkout root. A compose flag overrides
every file.

The machine's runner.yaml is listed under runner. Each integration it declares is
described as before a run, its program's describe run and its settings checked, and
listed with the credential it defines; one that does not describe is an error.

--verbose adds nothing here.

```
qory config [flags]
```

### Options

```
  -h, --help   help for config
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help says what
```

### SEE ALSO

* [qory](qory.md)	 - Compose the harness a runtime loads from modules

