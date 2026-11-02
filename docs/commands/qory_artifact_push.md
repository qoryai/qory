## qory artifact push

Upload files as one new version of an artifact

### Synopsis

Upload the files as one new version of the artifact <type>/<name>, keep a copy in the store
under <type>/<name>/v<N>/, and print the version. - reads one file from standard input.
--name separates several artifacts of one type, such as mockups of two screens. The store
is .qory/artifacts, or /tmp/qory-artifacts when the run's checkout is read-only.

qory exits 4 with the type and the limit for a type that the workspace does not have or the
workflow may not write, for too many files, and for files over the type's size limit. One
version is at most about 24 MiB and 100 files through the run API, whatever the type's limit.

Outside a run, qory keeps the files in .qory/artifacts and sends nothing.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#commands-of-a-work-item

```
qory artifact push <type> <file>... [flags]
```

### Examples

```
  qory artifact push screenshot shots/checkout.png --name checkout --note "After the fix."
  qory artifact push explainer -                 # read one file from standard input
```

### Options

```
  -h, --help          help for push
      --name string   the artifact's name, for several artifacts of one type (default main)
      --note string   a note on this version, for a person to read
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory artifact](qory_artifact.md)	 - Pull and push the artifacts of a work item

