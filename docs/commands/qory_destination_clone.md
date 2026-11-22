## qory destination clone

Clone destinations into /tmp/qory-destinations

### Synopsis

Clone each destination, by its id in destinations in $QORY_HARNESS_HOME/inputs.json, at depth
one into /tmp/qory-destinations/<system>/<path>, and print one line per id, in the order
given: the id and the folder. A destination cloned before in this session is not cloned
again, and its line is the same. A session clones at most 100 destinations.

The clone goes through the wall's proxy, which reads with the run's credential.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#clone-a-destination

```
qory destination clone <id>... [flags]
```

### Examples

```
  /qory/qory destination clone tg_0a1b2c3d4e5f6g7h
```

### Options

```
  -h, --help   help for clone
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory destination](qory_destination.md)	 - Clone the destinations of this run

