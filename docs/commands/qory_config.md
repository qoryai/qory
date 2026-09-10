## qory config

Print the effective configuration and where each value comes from

### Synopsis

Print the effective configuration and where each value comes from.

Every setting has a default. A qory.yaml sets the keys it names; the files apply in this
order, each overriding the one before it: the user's, in $XDG_CONFIG_HOME/qory or
~/.config/qory, then the ones in the checkout's ancestor directories the current user
owns, the farthest first, then the one in the checkout root. A compose flag overrides
every file.

```
qory config [flags]
```

### Options

```
  -h, --help   help for config
```

### SEE ALSO

* [qory](qory.md)	 - Compose the harness a runtime loads from modules

