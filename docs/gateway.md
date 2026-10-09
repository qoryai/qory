# The gateway as a service

`qory gateway` runs [Forager](https://github.com/qoryai/forager)'s gateway on this
machine as a service, until it is stopped, for the runs of other machines.

A gateway decides every connection of the runs that go through it by the run's policy,
records it, sets credentials on requests, and sends the runs' events to Qory Apiary. It is
the one part of a run that talks to Qory Apiary. `qory run` on this machine needs no
service: it starts a gateway of its own for each run (see [docs/run.md](run.md)).

```sh
qory gateway                          # on gateway.listen of forager.yaml
qory gateway --listen 0.0.0.0:8443    # --listen wins over gateway.listen
```

## forager.yaml on the gateway's machine

The `gateway` section of `~/.config/qory/forager.yaml` sets it:

```yaml
# ~/.config/qory/forager.yaml
apiVersion: qory.dev/v1alpha1
gateway:
  listen: 0.0.0.0:8443                  # the address the other machines' runs reach
  tls:                                  # required unless listen is a loopback address
    certificate: gateway.pem            # the certificate chain, PEM
    key: gateway-key.pem                # its private key, PEM
  server:                               # required: Qory Apiary, as for qory run
    url: https://apiary.example
    access_key_id: ak_0123456789abcdef
    apiary_public_key:
      - {alg: ed25519, public_key: mptNqtgGKgLhLZxmOGfpBQkdeBNH7QN3Qs9ETNumy8Q}
  egress:                               # optional: narrows Qory Apiary's policy
    mode: enforce
    allow: [github.com, "*.github.com"]
  run_credentials:                      # required: the issuers whose run credentials open runs
    - issuer: https://issuer.example
      audience: qory-gateway
      algorithms: [RS256]
      keys:
        - {kid: k1, alg: RS256, public_key_file: issuer-k1.pem}
      allow: {claim: namespace, values: [example-namespace]}
      labels:
        forge: {value: example-forge}
        repository: {claims: [namespace, project], join: "/"}
        run_key: {claim: sub}
      details:
        requester: {claim: requester}
      introspection:                    # optional
        url: https://issuer.example/introspect
        client_id: example-gateway
        client_secret_file: issuer-introspection-secret
        cache: 30s                      # default: the heartbeat interval, 30s
```

| Key | What it is |
|---|---|
| `gateway.listen` | The address qory gateway listens on for the runs of other machines, host:port. qory run never listens on it: it starts a gateway of its own on a loopback port for each run. |
| `gateway.tls.certificate`, `gateway.tls.key` | The certificate and key the gateway serves the other machines with, files in PEM. The address speaks TLS 1.3 alone. Without them, `gateway.listen` must be a loopback address. |
| `gateway.run_credentials` | The issuers whose signed run credentials open runs at the gateway. What an issuer's run credential holds, how the gateway verifies it, and how a client presents it: [Run credentials](https://github.com/qoryai/forager/blob/main/docs/gateway-run-credentials.md), Forager's page. |
| `gateway.server`, `gateway.egress`, `gateway.credentials`, `gateway.integrations` | As for `qory run`: see [forager.yaml](run.md#forageryaml). |

A path in `gateway.tls` and `gateway.run_credentials` is relative to the directory of
`forager.yaml`, unless it is absolute. `qory config` lists each value, the paths as the
file writes them; it never prints what a key or secret file holds. `forager.yaml`'s
schema, `gateway.run_credentials` included, is
[forager.schema.json](../contracts/harness/v1/forager.schema.json).

## The access key

The gateway is the node toward Qory Apiary, with this machine's access key, as `qory run`'s
own gateway is. Enrol the key on the gateway's machine with `qory access-key enrol`. The
secret is read from `--access-key-secret-fd`, else `QORY_ACCESS_KEY_SECRET`, else the file
`access-key-secret` beside `forager.yaml`: see
[The access key and the instance](run.md#the-access-key-and-the-instance).

## Running it

Before it listens, `qory gateway` describes every integration `gateway.integrations`
declares, then fetches Qory Apiary's signed configuration. It prints:

```text
qory gateway: integration github: /usr/local/bin/qory-github 1.2.0
qory gateway: node nd_0123456789abcdef, instance i_…
qory gateway: listening on [::]:8443
```

SIGINT or SIGTERM stops it: it takes no new run, sends what it holds, and exits 0.

```text
qory gateway: stopping
✓ the gateway stopped
```

It refuses to start, before it listens:

- `~/.config/qory/forager.yaml has no gateway section, so there is no gateway to run`
- `<file>: gateway.listen is required to run the gateway as a service: the address the
  other machines reach, such as 0.0.0.0:8443; or --listen`
- `<file>: gateway.listen "<v>" is not host:port, such as 0.0.0.0:8443`, and the same
  for `--listen`
- `<file>: gateway.server is required to run the gateway as a service: it reports every
  run to Qory Apiary and takes the runs' policies from it`
- `<file>: gateway.tls is required with a gateway.listen other machines reach: a run's
  events carry its prompts and terminal output; set gateway.tls.certificate and
  gateway.tls.key`
- `<file>: gateway.run_credentials is required to serve other machines: their runs
  bring run credentials, and the gateway verifies each one`

An address it cannot listen on is `listen on <addr>: <err>`.

## What it keeps

The gateway keeps its directory in qory's state directory: `~/.local/state/qory/gateway`,
or `$XDG_STATE_HOME/qory/gateway` when that is set to an absolute path, mode 0700. There,
Forager keeps the gateway's own certificate authority, `authority/ca.pem`, made at the
first start and read at every later one, and the record of each run under `runs/`.
