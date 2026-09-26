## qory harness remove

Remove the composed harness and its links from the checkout

### Synopsis

Remove the composed harness and its links from the checkout.

A home outside the checkout, under the directory --home or harness.home sets, is
removed with its report, and the checkout is not touched: nothing was written there.

```
qory harness remove [flags]
```

### Options

```
  -h, --help             help for remove
      --home string      where the harness is composed: a directory outside the checkout, one home per checkout under it, or .qory/harness (qory.yaml: harness.home)
      --runtime string   remove this runtime's links and directory only, and keep the rest composed
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory harness](qory_harness.md)	 - Compose, inspect, remove and launch the harness of a checkout

