## qory run resend

Send a finished run's record to the server again, completing it first

### Synopsis

Send the record of a run that is over to the server of runner.yaml, for a run
whose runner died or whose server was away: the step a job runs last, whatever
happened before it. The run is named by its id, the directory under .qory/runs in this
checkout. The server's configuration is fetched first, signed, and says where the
events go.

The run directory says what the server accepted, so only the rest is sent, in order,
until it is accepted or --wait is over. A record with no ai.qory.run.exited, which a
runner that died leaves, gets one first, with the reason runner_lost, and the
containers and networks the run's wall left are removed. A run whose runner still
lives is refused. A server may see an event twice and discards it by its id.

The exit status is 0 when the server has everything, 1 when events remain, which
are under the run directory's undelivered then.

```
qory run resend <run-id> [flags]
```

### Options

```
  -h, --help            help for resend
      --wait duration   how long to keep trying a server that does not accept (default 2m0s)
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help says what
```

### SEE ALSO

* [qory run](qory_run.md)	 - Start a runtime on the composed harness, observed and recorded

