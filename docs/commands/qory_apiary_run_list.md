## qory apiary run list

List the runs on a Qory Apiary server, the last created first

### Synopsis

List the runs on a Qory Apiary server, the last created first, one line per run:
its id, its number, the workspace, the workflow, the work item's number, the state, when
it started and how long it ran. A - stands for a value the run does not have.

Each flag selects runs, and a run is listed when it matches every flag given. qory
checks each value before it sends the request. --since takes a duration, such as 1h,
which qory counts back from now, or a time, such as 2026-11-01T09:00:00Z.

When more runs match, the last line, on stderr, is the command that lists the next
page. Pass the id of a failed run to qory apiary run diagnose.

qory signs the request with the diagnostics key in ~/.config/qory/apiary/<host>, and
prints the list only after its signature verifies under the server key pinned there.
--json prints the list as Qory Apiary sent it, so a pipe or a file receives JSON alone.

--server selects the server by its URL. Without it, qory takes the one server it has a
diagnostics key for.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/apiary.md

```
qory apiary run list [flags]
```

### Examples

```
  qory apiary run list
  qory apiary run list --state failed --since 24h
  qory apiary run list --workspace acme/web --work-item 7
  qory apiary run list --server https://apiary.example
  qory apiary run list --json > runs.json
```

### Options

```
      --before <run id>               list the runs after the run <run id>, for the next page
  -h, --help                          help for list
      --json                          print the list as Qory Apiary sent it, one JSON object
      --limit <number>                list at most <number> runs, from 1 to 100 (default 25)
      --server string                 the Qory Apiary server, by its URL; needed when diagnostics keys for several servers are on this computer
      --since <time>                  list the runs created at or after <time>: a duration back from now, such as 1h, or an RFC 3339 time
      --state <state>                 list the runs in <state>: pending, running, completed, failed, cancelled, paused or lost
      --work-item <number>            list the runs of the work item <number> in the workspace; needs --workspace
      --workflow <name>               list the runs that start the workflow <name>, such as hello
      --workspace <org>/<workspace>   list the runs of one workspace, as <org>/<workspace>, such as acme/web
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory apiary run](qory_apiary_run.md)	 - List runs and read what Qory Apiary recorded about one

