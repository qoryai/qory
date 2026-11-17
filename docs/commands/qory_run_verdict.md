## qory run verdict

Store the run's verdict

### Synopsis

Store the run's verdict as a version of the artifact verdict. Qory Apiary acts on it when the
run ends.

escalate needs --points, a file with a JSON array of 1 to 20 points for a person. Each
point is {text, action}: text is one sentence on one line, 1 to 500 characters, on what
is wrong or missing, with no action, and action is answer or run_again. A point with the
action answer also has question, the id of an open question. A point with the action
run_again may have fix, one line of 1 to 500 characters on how a person fixes the
point. qory checks the file against contracts/harness/v1/verdict.schema.json before it
sends or keeps anything.

Any verdict may propose a track for the work item. Pass --track with the id of a track that
the gate of the run's stage offers, such as small-change, and --track-reason with the
reason, one sentence on one line of 1 to 300 characters. Pass both or neither. The optional
--track-kind is the kind of the change, one line of 1 to 80 characters; it needs the pair.
qory checks the three before it sends or keeps anything. It exits 2 for a mistake
in the id, the reason or the pair, and 4 for a mistake in the kind. It also
exits 4 when Qory Apiary refuses the verdict with 422 invalid_artifact, which
includes a track that the gate does not offer. Line 1 of the error is the fix, and Qory
Apiary's text follows.

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
  qory run verdict passed --track small-change --track-reason "The change touches one file."
  qory run verdict escalate --points points.json
```

### Options

```
  -h, --help                  help for verdict
      --points string         read the points of escalate, a JSON array, from this file
      --track string          propose the track with this id, one that the gate of the run's stage offers; pass it with --track-reason
      --track-kind string     give the kind of the change, one line of 1 to 80 characters; pass it with --track and --track-reason
      --track-reason string   give the reason for --track, one sentence on one line of 1 to 300 characters; pass it with --track
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory run](qory_run.md)	 - Run the agent on its harness, observed and recorded

