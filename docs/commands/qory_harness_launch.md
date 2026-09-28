## qory harness launch

Print the command that starts an agent on the harness

### Synopsis

Print the command that starts an agent on the composed harness.

The command is one line, quoted for a POSIX shell. Run it as it is, with your own
arguments after it. It is the agent's launch template; harness.launch.<runtime> in
qory.yaml changes it. The paths are absolute, so the line works from anywhere.

--json prints the same as one JSON object, with the name the session gives each composed
agent, skill and command. --address <kind>/<name> prints one of those names alone.

An agent that reads its harness from the checkout alone has no launch template.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/harness.md#a-home-outside-the-checkout

```
qory harness launch [flags]
```

### Examples

```
  eval "$(qory harness launch --runtime claude)"   # start Claude Code on the harness
  qory harness launch --json                       # the same, as JSON, for a launcher
  qory harness launch --address skills/deploy      # the name the session gives a skill
```

### Options

```
      --address string   print only the name the session gives this <kind>/<name> or bound role, such as skills/deploy
  -h, --help             help for launch
      --home string      where the harness is composed: a directory outside the checkout, one home per checkout under it, or .qory/harness (qory.yaml: harness.home)
      --json             print the command, arguments, variables and registered names as one JSON object
      --runtime string   the runtime to start, one the harness is composed for; the only one when left out
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory harness](qory_harness.md)	 - Build the agent's harness from modules

