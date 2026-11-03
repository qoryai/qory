## qory run verdict

Store the run's verdict

### Synopsis

Store the run's verdict as a version of the artifact verdict. Qory Apiary acts on it when the
run ends. --gaps reads the text of what is missing from a file.

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
  qory run verdict changes-requested --gaps gaps.md
```

### Options

```
      --gaps string   read the text of what is missing from this file
  -h, --help          help for verdict
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory run](qory_run.md)	 - Run the agent on its harness, observed and recorded

