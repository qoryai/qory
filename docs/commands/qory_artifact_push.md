## qory artifact push

Upload files as one new version of an artifact

### Synopsis

Upload the files as one new version of the artifact <type>/<name>, keep a copy in the store
under <type>/<name>/v<N>/, and print the version. - reads one file from standard input.
--name separates several artifacts of one type, such as mockups of two screens. In a run,
the types questions and review-comments take only the name main. The store is
.qory/artifacts, or /tmp/qory-artifacts when the run's checkout is read-only.

qory exits 4 with the type and the limit for a type that the workspace does not have,
for too many files, and for files over the type's size limit. One version is at most about
24 MiB and 100 files through the run API, whatever the type's limit. In a walled run, qory
also exits 4 with the fix line and the findings when the harness's push check for <type>
does not pass.

--withdraw stores a version of <type>/<name> with no files and the mark withdrawn, in place of
uploading files. A gate, a stage input and the work item's pages leave out a name whose
newest version is withdrawn. A later push of files lists the name again.

qory exits 4 with a fix line, and sends nothing, for a withdrawal of a name with no version,
a name whose newest version is already a withdrawal, an upload by a person, or a version
that a member approved at a gate, and for files given with --withdraw. A run cannot
withdraw questions, review-comments, verdict or proposal, which Qory Apiary reads itself,
or a type that the process template sets withdrawable: false for. In a run, qory sends
that push, Qory Apiary reads the template's artifact types and refuses it with 422, and
qory exits 4 with that refusal. Outside a run, qory refuses those types itself, reads the
template from the factory modules that the checkout exports, and has no approvals to read.

Outside a run, qory keeps the files, or the record of a withdrawal, in .qory/artifacts and
sends nothing.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#commands-of-a-work-item

```
qory artifact push <type> [<file>...] [flags]
```

### Examples

```
  qory artifact push screenshot shots/checkout.png --name checkout --note "After the fix."
  qory artifact push explainer -                 # read one file from standard input
  qory artifact push demo-script --name scenario-2 --withdraw --note "demo-scenarios v3 removed scenario-2."
```

### Options

```
  -h, --help          help for push
      --name string   the artifact's name, for several artifacts of one type (default main; in a run, only main for questions and review-comments)
      --note string   a note on this version, for a person to read
      --withdraw      store a version of <type>/<name> with no files, which withdraws the name
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory artifact](qory_artifact.md)	 - Pull and push the artifacts of a work item

