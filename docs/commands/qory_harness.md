## qory harness

Build the agent's harness from modules

### Synopsis

Build the agent's harness from modules.

A harness is what an agent reads: instructions, skills, agents, commands, settings and
MCP servers. A module is one piece of it. The stack in qory.yaml lists modules in order.
compose builds one tree from them, for every agent the stack targets.

More: https://github.com/qoryai/qory/blob/main/docs/harness.md

```
qory harness [flags]
```

### Options

```
  -h, --help   help for harness
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory](qory.md)	 - Get your coding agent ready to work, and keep it in check
* [qory harness compose](qory_harness_compose.md)	 - Build the harness into the checkout you stand in
* [qory harness inspect](qory_harness_inspect.md)	 - Show where every entry of the harness came from
* [qory harness launch](qory_harness_launch.md)	 - Print the command that starts an agent on the harness
* [qory harness remove](qory_harness_remove.md)	 - Remove the harness and its links from the checkout

