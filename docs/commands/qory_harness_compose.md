## qory harness compose

Compose the stack's modules into the checkout you stand in

### Synopsis

Compose the stack's modules into the checkout you stand in.

The home, the composed tree, is .qory/harness in the checkout, linked from the paths
each runtime reads and excluded from git. With --home or harness.home in qory.yaml set to
a directory outside the checkout, the tree goes under that directory instead, one home
per checkout named after it, and the checkout gets nothing: no link, no .qory, no
exclude line. A runtime reads such a home through the arguments qory harness launch
prints. --no-links keeps the checkout untouched with the home inside it too.

--verbose prints one line per entry, the entry and the module it came from.

```
qory harness compose [flags]
```

### Options

```
      --check            compare the home with what the stack and modules define and write nothing; exit 6 when a file or link differs. The links from the checkout into the home and the report are not compared
      --dry-run          print the report and write nothing
  -f, --file string      the qory-stack.yaml, or the qory.yaml or harness.yaml whose harness section to compose, instead of discovering one. A stack this flag selects, in a checkout whose own document extends one, is that document's base in place of extends
      --force            replace a tracked, unmodified file of the checkout where a link goes; git checkout -- restores it (qory.yaml: force)
  -h, --help             help for compose
      --home string      where the harness is composed: a directory outside the checkout, one home per checkout under it, or .qory/harness (qory.yaml: harness.home)
      --model string     write this model instead of target.model (qory.yaml: model)
      --no-links         write nothing into the checkout, no link and no exclude line; qory harness launch prints how a runtime reads the home (qory.yaml: harness.links: none)
      --runtime string   render for these runtimes instead of target.runtime, comma separated (amp, any, claude, codex, copilot, cursor, gemini, goose, opencode; qory.yaml: runtime)
      --update           fetch every git source again instead of reading the cached clone (qory.yaml: update)
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory harness](qory_harness.md)	 - Compose, inspect, remove and launch the harness of a checkout

