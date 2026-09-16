## qory harness launch

Print the command that starts a runtime on the composed harness

### Synopsis

Print the command that starts a runtime on the composed harness, on one line quoted for a
POSIX shell, so a launcher runs it as it is, with arguments of its own after it:

  cd <checkout> && eval "$(qory harness launch --runtime claude)"

The line is the runtime's own launch template, with harness.launch.<runtime> in
qory.yaml over it: the program, the arguments that hand it the home's files, and the
variables it takes them from, ${dir} being the runtime's directory in the home. For
claude it is --plugin-dir, --settings, --mcp-config, --append-system-prompt-file and
--setting-sources user; for codex it is CODEX_HOME. A group of arguments naming a file
the compose did not write, mcp.json without a server say, is left out. The home is found
the way compose finds it, from --home, harness.home or the checkout you stand in; the
paths printed are absolute, so the line works wherever the home is. --json prints the
command, the arguments and the variables as one JSON object, for a launcher that spawns
the program without a shell, with the names the session registers the composed agents,
skills and commands under on that launch under addresses, per kind, a bound role beside
them as the entry it is bound to: harness:<name> for claude, whose plugin prefixes
every kind, the name as the module wrote it elsewhere. --address <kind>/<name> prints
that one registered name alone, for a launcher that builds its first prompt from an
entry point, /harness:implement say. A runtime that reads its harness from the
checkout alone has no launch template, and the verb says so.

--verbose adds nothing here.

```
qory harness launch [flags]
```

### Options

```
      --address string   print the name the session registers this <kind>/<name>, or bound role, under on this launch, and nothing else
  -h, --help             help for launch
      --home string      where the harness is composed: a directory outside the checkout, one home per checkout under it, or .qory/harness (qory.yaml: harness.home)
      --json             print the command, the arguments, the variables and the registered names as one JSON object
      --runtime string   the runtime to start, one the harness is composed for; the only one when left out
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help says what
```

### SEE ALSO

* [qory harness](qory_harness.md)	 - Compose, inspect, remove and launch the harness of a checkout

