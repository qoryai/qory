## qory apiary

Debug runs on a Qory Apiary server

### Synopsis

Work with a Qory Apiary server as a person who debugs its runs.

  enrol          make a diagnostics key and enrol it with a code from Qory Apiary
  run diagnose   print a run's diagnostics document

The commands sign their requests with a diagnostics key: an access key of kind
diagnostics, made on this computer. Its secret stays in ~/.config/qory/apiary/<host>,
Qory Apiary keeps its public half, and it reads only the diagnostics of runs.

More: https://github.com/qoryai/qory/blob/main/docs/apiary.md

### Options

```
  -h, --help   help for apiary
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory](qory.md)	 - Get your coding agent ready to work, and keep it in check
* [qory apiary enrol](qory_apiary_enrol.md)	 - Enrol a diagnostics key with a code from Qory Apiary
* [qory apiary run](qory_apiary_run.md)	 - Read what Qory Apiary recorded about a run

