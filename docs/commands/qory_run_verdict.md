## qory run verdict

Store the run's verdict

### Synopsis

Store the run's verdict as a version of the artifact verdict. Qory Apiary acts on it when the
run ends.

escalate needs --points, a file with a JSON array of 1 to 20 points for a person. Each
point is {text, action}: text is one sentence of 1 to 500 characters on what is wrong or
missing, and action is answer or run_again. A point with the action answer also has
question, the id of an open question. qory checks the file against
contracts/harness/v1/verdict.schema.json before it sends or keeps anything.

Outside a run, qory prints the verdict and keeps it in .qory/artifacts/verdict/; nothing
acts on it.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#commands-of-a-work-item

```
qory run verdict passed|changes-requested|escalate [flags]
```

### Examples

```
  qory run verdict passed
  qory run verdict escalate --points points.json
```

### Options

```
  -h, --help            help for verdict
      --points string   read the points of escalate, a JSON array, from this file
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory run](qory_run.md)	 - Run the agent on its harness, observed and recorded

