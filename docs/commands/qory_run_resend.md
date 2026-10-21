## qory run resend

Send a finished run's record to the server again

### Synopsis

Send a finished run's record to the server in forager.yaml again: after a Forager process that
died, or a server that was away. A job runs it last, whatever happened before.

Only what the server has not accepted is sent. When the run started its gateway itself, a
record Forager left open is closed first, unless the server never opened the run. The
containers and networks its wall left are removed. A run that is still running is refused.

The exit status is 0 when the server has everything, or never opened the run, and 1 when
events remain, or the server said stop.

A run that went through a separate gateway sends its record to that gateway instead,
with its run key: QORY_RUN_KEY, or, on a machine that registered the run itself, the run
key qory kept for it. The exit status is 0 when nothing is left to send, and 1 when events
remain or the gateway takes no more of them.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#resending-a-runs-record

```
qory run resend <run-id> [flags]
```

### Examples

```
  qory run resend "$run_id"             # the last step of a job
  qory run resend "$run_id" --wait 10m  # keep trying for ten minutes
```

### Options

```
      --access-key-secret-fd int   read the access key's secret from this file descriptor, 3 or above; it wins over QORY_ACCESS_KEY_SECRET and the access-key-secret file
  -h, --help                       help for resend
      --run-key-fd int             read the run key from this file descriptor, 0 for stdin, in place of QORY_RUN_KEY, which an unwalled agent may read
      --wait duration              how long to keep trying a server that does not accept (default 2m0s)
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory run](qory_run.md)	 - Run the agent on its harness, observed and recorded

