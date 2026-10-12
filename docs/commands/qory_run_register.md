## qory run register

Register a run with the server, and write its run key for the job to a descriptor

### Synopsis

Register a run with the server in forager.yaml, for a job a runner behind a gateway runs.
This is the starter's command: it signs the registration with the access key of the node
pool, and the key stays on this machine. The server answers with the run's run key, which
names the run and the gateway it goes through.

qory writes the run key to the descriptor --run-key-fd names, and every other line on
stderr; stdout gets nothing. Give the job the run key as a secret: whoever holds it can
act for this run until the run ends. On the runner, qory run takes it from QORY_RUN_KEY,
or from --run-key-fd. Nothing is written on this machine.

The access key's secret comes from --access-key-secret-fd, else QORY_ACCESS_KEY_SECRET,
else access-key-secret, as for qory run. With gateway.server.via set, the registration goes
through that gateway, unchanged.

Use a new run id for each attempt, a retry included. --instance-id names the runner the run
takes a place on: by default the run's id, for a runner used once. --attach-within is
how long the runner has to start the run; by default the server's window, and never more
than the server allows.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#for-a-starter

```
qory run register [flags]
```

### Examples

```
  run_key="$(qory run register --run-key-fd 3 --label issue=77 --title "Fix the parser" 3>&1)"
  run_key="$(qory run register --run-key-fd 3 --run-id "$run_id" --instance-id runner-07 --attach-within 15m 3>&1)"
```

### Options

```
      --access-key-secret-fd int   read the access key's secret from this file descriptor, 3 or above; it wins over QORY_ACCESS_KEY_SECRET and the access-key-secret file
      --attach-within duration     how long the runner has to start the run, in whole seconds, such as 15m (default the server's; the server allows at most its limit)
      --details string             a JSON object of the run's details, read from this file, or - for stdin when stdin is not a terminal; at most 8192 bytes compacted, 4 levels deep
  -h, --help                       help for register
      --instance-id string         the runner the run takes a place on, 1 to 64 of A-Z, a-z, 0-9, dot, underscore and dash (default the run id: a runner used once)
      --kind string                what kind of run it is, such as review or fix, reported when the run starts
      --label stringArray          a key=value name for the run, reported in its events; repeatable
      --run-id string              the run's id, a UUID in lower case (default a new one)
      --run-key-fd int             write the run key to this file descriptor, 3 or above; required
      --subject stringArray        what the run works on, type=<type>,ref=<ref>[,url=<url>][,title=<title>], such as type=ticket,ref=7; title takes the rest of the value, commas too; repeatable
      --title string               the run's title, for a person to read, reported when the run starts
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory run](qory_run.md)	 - Run the agent on its harness, observed and recorded

