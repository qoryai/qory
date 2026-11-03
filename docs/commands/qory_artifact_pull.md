## qory artifact pull

Fetch a work item's artifacts and print their directory

### Synopsis

Fetch the work item's artifacts into the store, under <type>/<name>/v<N>/, one directory per
version with its files, and write index.json there: the types, names, versions, who wrote
each, and approvals. Then print the store's directory. Read the files from that directory:
it is .qory/artifacts, or /tmp/qory-artifacts when the run's checkout is read-only.

With no --version, qory fetches the newest version of each output of the stage, and the
version of each input that its gate approved. For any other artifact, it fetches the
approved version, or the newest when none is approved. <type> limits the fetch to one type.
--version needs <type>, and fetches that version of each artifact of the type.

Outside a run, qory fetches nothing, and says so.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#commands-of-a-work-item

```
qory artifact pull [<type>] [flags]
```

### Examples

```
  qory artifact pull                          # every input and output
  qory artifact pull demo-video --version 2   # one version of one type
```

### Options

```
  -h, --help          help for pull
      --version int   fetch this version number of each artifact of <type>
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory artifact](qory_artifact.md)	 - Pull and push the artifacts of a work item

