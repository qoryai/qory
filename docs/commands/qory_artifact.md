## qory artifact

Pull and push the artifacts of a work item

### Synopsis

Pull the artifacts of this run's work item into the store, and push new versions. The store
is .qory/artifacts, or /tmp/qory-artifacts when the run's checkout is read-only.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#commands-of-a-work-item

### Options

```
  -h, --help   help for artifact
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory](qory.md)	 - Get your coding agent ready to work, and keep it in check
* [qory artifact pull](qory_artifact_pull.md)	 - Fetch a work item's artifacts and print their directory
* [qory artifact push](qory_artifact_push.md)	 - Upload files as one new version of an artifact

