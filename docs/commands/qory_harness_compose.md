## qory harness compose

Compose the stack's modules into the checkout you stand in

### Synopsis

Compose the stack's modules into the checkout you stand in.

--verbose prints one line per entry, the entry and the module it came from.

```
qory harness compose [flags]
```

### Options

```
      --check            compare the home with what the stack and modules say now and write nothing; exit 6 when a file or link differs. The links from the checkout into the home and the report are not compared
      --dry-run          print the report and write nothing
  -f, --file string      the qory-stack.yaml, or the qory.yaml or harness.yaml whose harness section to compose, instead of discovering one. A stack named here, in a checkout whose own document extends one, is that document's base in place of extends
      --force            replace a tracked, unmodified file of the checkout where a link goes; git checkout -- restores it (qory.yaml: force)
  -h, --help             help for compose
      --model string     write this model instead of target.model (qory.yaml: model)
      --runtime string   render for these runtimes instead of target.runtime, comma separated (amp, any, claude, codex, copilot, cursor, gemini, goose, opencode; qory.yaml: runtime)
      --update           fetch every git source again instead of reading the cached clone (qory.yaml: update)
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help says what
```

### SEE ALSO

* [qory harness](qory_harness.md)	 - Compose, inspect and remove the harness of a checkout

