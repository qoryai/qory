## qory apiary run diagnose

Print a run's diagnostics document from Qory Apiary

### Synopsis

Print a run's diagnostics document: the run, its queue entry, each dispatch, the
preparation of its machine, its last events and the end of its log, as one JSON
document.

Pass the run's number or its id. The number is the one on the run's page in Qory
Apiary, 18342 in "Run 18342", and the number after /runs/ in the page's address. The
run id is the rn_ id that the Qory Apiary API returns.

qory signs the request with the diagnostics key in ~/.config/qory/apiary/<host>, and
prints the document only after its signature verifies under the server key pinned
there. The document goes to stdout as Qory Apiary sent it, so a pipe or a file receives
JSON alone; errors go to stderr.

--server selects the server by its URL. Without it, qory takes the one server it has a
diagnostics key for.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/apiary.md

```
qory apiary run diagnose <run number or id> [flags]
```

### Examples

```
  qory apiary run diagnose 18342
  qory apiary run diagnose rn_0123456789abcdef
  qory apiary run diagnose 18342 --server https://apiary.example
  qory apiary run diagnose rn_0123456789abcdef > diagnostics.json
```

### Options

```
  -h, --help            help for diagnose
      --server string   the Qory Apiary server, by its URL; needed when diagnostics keys for several servers are on this computer
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory apiary run](qory_apiary_run.md)	 - Read what Qory Apiary recorded about a run

