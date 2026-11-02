## qory work-item show

Print the work item as JSON

### Synopsis

Print the work item as JSON: the ticket, the stage's name, the round, the stage's inputs and
outputs with each output's check, the newest verdict and gaps, answers, and notes from
people.

In a run, qory reads the work item from Qory Apiary through the gateway. Outside a run,
qory prints .qory/work-item.json, and exits 3 when that file does not exist.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#commands-of-a-work-item

```
qory work-item show [flags]
```

### Examples

```
  qory work-item show > work-item.json
```

### Options

```
  -h, --help   help for show
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory work-item](qory_work-item.md)	 - Read the work item of this run

