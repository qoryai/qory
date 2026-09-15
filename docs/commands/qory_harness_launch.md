## qory harness launch

Print the arguments that start a runtime on the composed harness

### Synopsis

Print the arguments that start a runtime on the composed harness, on one line, quoted for a
shell, so a launcher runs the program with them:

  cd <checkout> && eval claude "$(qory harness launch --runtime claude)"

For claude they are --plugin-dir for the skills, agents, commands and output styles,
--settings for the permissions, hooks, environment and model, --mcp-config for the
servers when the compose holds one, --append-system-prompt-file for the instructions,
and --setting-sources user, so no .claude of the checkout or of a directory above it is
read. The home is found the way compose finds it, from --home, harness.home or the
checkout you stand in; the paths printed are absolute, so the line works wherever the
home is. The other runtimes read their harness from the checkout alone, through the
links a compose writes there, and have no launch spec.

--verbose adds nothing here.

```
qory harness launch [flags]
```

### Options

```
  -h, --help             help for launch
      --home string      where the harness is composed: a directory outside the checkout, one home per checkout under it, or .qory/harness (qory.yaml: harness.home)
      --runtime string   the runtime to start, one the harness is composed for; the only one when left out
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help says what
```

### SEE ALSO

* [qory harness](qory_harness.md)	 - Compose, inspect, remove and launch the harness of a checkout

