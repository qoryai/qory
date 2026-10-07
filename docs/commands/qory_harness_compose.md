## qory harness compose

Build the harness into the checkout you stand in

### Synopsis

Build the harness from the stack's modules into the checkout you stand in.

The tree goes to .qory/harness. Each agent's own paths link into it. Git does not see it.
When two modules provide the same entry, compose refuses until the stack picks one.

With --home, or harness.home in qory.yaml, the tree goes outside the checkout, one home
per checkout. The checkout then gets nothing. qory harness launch prints how an agent
reads such a home. --no-links writes the tree to .qory/harness, with no link and no
exclude line.

--verbose prints each entry and the module it came from.

More: https://github.com/qoryai/qory/blob/main/docs/harness.md

```
qory harness compose [flags]
```

### Examples

```
  qory harness compose                   # build the harness here
  qory harness compose --runtime codex   # for Codex instead
  qory harness compose --dry-run         # print the report, write nothing
  qory harness compose --check           # in CI: exit 6 when the tree is behind
```

### Options

```
      --check            compare the home with the stack and modules, write nothing, and exit 6 when a file, a link or a launch variable differs; the checkout's links and the rest of the report are not compared
      --dry-run          print the report and write nothing
  -f, --file string      the qory-stack.yaml, qory.yaml or harness.yaml to compose instead of the one found; when the checkout's own document extends a stack, this one is its base
      --force            replace a tracked, unmodified file where a link goes; git checkout -- restores it (qory.yaml: force)
  -h, --help             help for compose
      --home string      where the harness is composed: a directory outside the checkout, one home per checkout under it, or .qory/harness (qory.yaml: harness.home)
      --model string     write this model instead of target.model (qory.yaml: model)
      --no-links         write no link and no exclude line into the checkout; harness launch prints how an agent reads the home (qory.yaml: harness.links: none)
      --runtime string   render for these runtimes instead of target.runtime, comma separated (amp, any, claude, codex, copilot, cursor, gemini, goose, opencode; qory.yaml: runtime)
      --update           fetch every git source again, not the cached clone (qory.yaml: update)
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory harness](qory_harness.md)	 - Build the agent's harness from modules

