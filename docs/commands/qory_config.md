## qory config

Show every setting, its value, and the file it came from

### Synopsis

Print every setting, its value, and the file it came from.

Every setting has a default. The qory.yaml files apply in this order, each over the one
before it:

  1. yours, in ~/.config/qory ($XDG_CONFIG_HOME/qory)
  2. those in the directories above the checkout that you own, the farthest first
  3. the checkout's own

A compose flag overrides every file.

The machine's runner.yaml is listed under runner. Each integration it declares is
described as before a run; one that does not describe is an error. An integration whose
name the credentials section defines too is listed as shadowed.

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
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory](qory.md)	 - Get your coding agent ready to work, and keep it in check

