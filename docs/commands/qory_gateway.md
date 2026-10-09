## qory gateway

Run this machine's gateway for the runs of other machines

### Synopsis

Run a gateway on this machine as a service, until it is stopped.

A gateway decides every connection of the runs that go through it by the run's policy,
records it, sets credentials on requests, and sends the runs' events to Qory Apiary. It is
the one part of a run that talks to Qory Apiary. qory run on this machine needs no service:
it starts a gateway of its own for each run.

The gateway section of forager.yaml in ~/.config/qory sets it:

  listen        the address the other machines' runs reach, such as 0.0.0.0:8443
  tls           the certificate and key it serves the other machines with
  server        Qory Apiary, this machine's access key's id, and Qory Apiary's public key
  egress        the hosts a run may reach; it narrows Qory Apiary's policy
  credentials   secrets the gateway sets on requests; the agents never have them
  integrations  programs that supply such secrets
  run_credentials    the issuers whose signed run credentials open runs of clients with no session

SIGINT or SIGTERM stops it: it takes no new run, sends what it holds, and exits 0.

More: https://github.com/qoryai/qory/blob/main/docs/gateway.md

```
qory gateway [flags]
```

### Examples

```
  qory gateway                          # on gateway.listen of forager.yaml
  qory gateway --listen 0.0.0.0:8443
```

### Options

```
      --access-key-secret-fd int   read the access key's secret from this file descriptor, 3 or above; it wins over QORY_ACCESS_KEY_SECRET and the access-key-secret file
  -h, --help                       help for gateway
      --listen string              the address to listen on, host:port; it wins over gateway.listen of forager.yaml
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory](qory.md)	 - Get your coding agent ready to work, and keep it in check

