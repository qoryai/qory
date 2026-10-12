## qory run end

End a run a starter registered, and say how it went

### Synopsis

Say how a run went, as its starter. The server records the outcome, and a run that is still
running ends: its runtime is stopped. A run has one outcome: the same one again gets the
same answer, and a different one is refused.

--outcome is completed, failed or cancelled. Without it, the run is cancelled, with no
outcome given. --reason is a code of yours, such as checks_failed: a lower-case letter,
then lower-case letters, digits and _, at most 64. qory run says it with spaces for
underscores. --reason needs --outcome.

The request is signed with the access key of the node pool that registered the run, read as
qory run register reads it, and goes through gateway.server.via when that is set.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#how-a-run-ends-there

```
qory run end <run-id> [flags]
```

### Examples

```
  qory run end "$run_id" --outcome completed --reason all_checks_passed
  qory run end "$run_id" --outcome failed --reason checks_failed
  qory run end "$run_id"                                   # cancelled, with no outcome given
```

### Options

```
      --access-key-secret-fd int   read the access key's secret from this file descriptor, 3 or above; it wins over QORY_ACCESS_KEY_SECRET and the access-key-secret file
  -h, --help                       help for end
      --outcome string             how the run went: completed, failed or cancelled (default none: cancelled, with no outcome given)
      --reason string              why, a code of yours such as checks_failed; needs --outcome
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory run](qory_run.md)	 - Run the agent on its harness, observed and recorded

