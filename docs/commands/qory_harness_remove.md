## qory harness remove

Remove the harness and its links from the checkout

### Synopsis

Remove the composed harness and its links from the checkout. A home in the checkout goes with
everything in the .qory directory, also .qory/artifacts and .qory/work-item.json, except
.qory/harness.lock, the lock of the home, which stays with its line in .git/info/exclude.
--runtime does the same when it removes the last composed runtime.

A home outside the checkout, under --home or harness.home, is removed with its report; its
lock file, <home>.lock beside it, stays. The checkout is not touched: nothing was written there.

While a qory run uses the home, remove stops with exit 1 and removes nothing: wait for
that run to end.

```
qory harness remove [flags]
```

### Options

```
  -h, --help             help for remove
      --home string      where the harness is composed: a directory outside the checkout, one home per checkout under it, or .qory/harness (qory.yaml: harness.home)
      --runtime string   remove this runtime's links and directory only, and keep the rest composed; the last composed runtime removes the whole home
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory harness](qory_harness.md)	 - Build the agent's harness from modules

