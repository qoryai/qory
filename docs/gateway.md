# The gateway as a service

`qory gateway` runs [Forager](https://github.com/qoryai/forager)'s gateway on this
machine as a service, until it is stopped, for the runs of other machines.

A gateway decides every connection of the runs that go through it by the run's policy,
records it, sets credentials on requests, and sends the runs' events to Qory Apiary.
`qory run` on this machine needs no service: it starts a gateway itself for each run
(see [docs/run.md](run.md)).

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
  tls:                                  # required: a gateway serves other machines
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
```

| Key | What it is |
|---|---|
| `gateway.listen` | The address qory gateway listens on for the runs of other machines, host:port. qory run never listens on it: it starts a gateway itself on a loopback port for each run. |
| `gateway.tls.certificate`, `gateway.tls.key` | The certificate and key the gateway serves the other machines with, files in PEM. The address speaks TLS 1.3 alone. `qory gateway` needs them, whatever address it listens on: a gateway serves other machines. |
| `gateway.server`, `gateway.egress`, `gateway.egress.paths`, `gateway.credentials`, `gateway.tools`, `gateway.integrations` | As for `qory run`: see [forager.yaml](run.md#forageryaml). |
| `gateway.run_limits` | The limits of each run the gateway serves: see [Limits of each run](run.md#limits-of-each-run). |
| `gateway.stack_hosts` | The hosts each run's stack proxy reaches: see [Hosts of the stack proxy](run.md#hosts-of-the-stack-proxy). |
| `gateway.lock_memory` | On Linux, `true` keeps the gateway's memory out of swap: see [Keep the gateway's memory out of swap](run.md#keep-the-gateways-memory-out-of-swap). |

A path in `gateway.tls` is relative to the directory of `forager.yaml`, unless it is
absolute. `qory config` lists each value, the paths as the file writes them; it never
prints what a key or secret file holds. `forager.yaml`'s schema is
[forager.schema.json](../contracts/harness/v1/forager.schema.json).

### The gateway's machine and private addresses

Behind a wall, and for every run through a separate gateway, the gateway reaches these
addresses only for a host that `gateway.egress.allow` of this `forager.yaml` lists by
name, and that the run's policy allows:

- this machine: `localhost`, loopback, the unspecified addresses `0.0.0.0` and `::`, and
  every address of its network interfaces;
- a private network address in `10/8`, `172.16/12`, `192.168/16`, `100.64/10`, `0/8`,
  `198.18/15`, `240/4`, `fc00::/7` or `fec0::/10`;
- an IPv6 address that contains an IPv4 address of those ranges, such as
  `::ffff:10.0.0.5`.

A `*.` entry, such as `"*.corp.example"`, does not open them, and no entry of Qory
Apiary's policy does. Behind a wall, and for every run through a separate gateway, the
gateway never reaches a link-local address, such as `169.254.169.254`, or a cloud metadata
address, whatever the lists contain.

For example, if your model proxy's host name `models.corp.example` resolves to
`10.0.0.5`, list that name:

```yaml
gateway:
  egress:
    mode: enforce
    allow: [github.com, "*.github.com", models.corp.example]
```

If the host is not listed, the run's request gets `403 Forbidden` and the line `qory:
egress to models.corp.example:443 denied by the gateway: to reach the gateway's machine
or a private network address, list the host itself, not a *. pattern, in the allow list
of the node's policy, and allow it in the run's policy; …`. The run's record has a
`dev.qory.run.egress` with the decision `denied`, the outcome `refused` and the rule
`wall:own-address`, in the modes `enforce` and `observe`.

## The machines behind it

A machine whose runs go through this gateway names no gateway: each run brings a run key,
which its starter got from Qory Apiary and which names this gateway. Such a machine reaches
the gateway over https alone, so a gateway that serves them has `gateway.tls`. See
[Through a separate gateway](run.md#through-a-separate-gateway). If nobody watches the
runs on those machines, run them with a wall: see [When to run without a
wall](run.md#when-to-run-without-a-wall).

For a starter or a machine with a separate access key that reaches Qory Apiary only through it, the
gateway passes on enrolment, the server's configuration, registrations and outcomes,
unchanged, and nothing else. They set `gateway.server.via`: see [A machine with a separate access
key, behind a gateway](run.md#a-machine-with-a-separate-access-key-behind-a-gateway).

## The access key

The gateway is the node toward Qory Apiary, with this machine's access key, as the gateway
`qory run` starts itself is. The gateway's access key is a gateway's: enrol it with a code from the
gateway's page in Qory Apiary, with `qory access-key enrol` on the gateway's machine. The
secret is read from `--access-key-secret-fd`, else `QORY_ACCESS_KEY_SECRET`, else the file
`access-key-secret` beside `forager.yaml`: see
[The access key and the instance](run.md#the-access-key-and-the-instance).

### Enrol from the code file of Qory Apiary

On an instance's gateway, Qory Apiary writes the gateway's enrolment code into a file, and
`qory gateway` enrols from it, with no command typed by a person. Set
`gateway.server.enrolment_code_file` to that file, relative to the directory of
`forager.yaml` unless absolute:

```yaml
gateway:
  server:
    url: https://apiary.example
    enrolment_code_file: /run/qory/enrolment/code
```

When `qory gateway` starts with no access key, from neither `--access-key-secret-fd`,
`QORY_ACCESS_KEY_SECRET` nor `access-key-secret`:

1. If the file is not there yet, it prints once: `qory gateway: waiting for the
   enrolment code at /run/qory/enrolment/code; Qory Apiary writes it once this gateway
   is installed`, and looks again every 2 seconds until the file exists. SIGINT or
   SIGTERM stops it, and it exits 0.
2. It reads the file only when the path has no link in it, the file is a regular file,
   and only root and the user running qory may write the file and each folder above it.
   A folder that root owns with the sticky bit set, such as `/tmp`, passes, since no
   other user may replace a file in it.
   The file contains the code, `qecg_…`, and at most one newline after it.
3. It enrols with the code against `gateway.server.url`, as `qory access-key enrol` does,
   and prints one line: `qory gateway: enrolled the access key ak_… (fingerprint …) with
   the code in /run/qory/enrolment/code; the secret is in <dir>/access-key-secret`.
4. It starts as it does with a key it had.

With an access key, `qory gateway` ignores the setting. qory never writes or removes the
file: Qory Apiary deletes it once the key is live. No line or error contains the code.
When the enrolment does not complete, `qory gateway` exits with the error that `qory
access-key enrol` prints; started again within the code's 15 minutes, it retries with the
same key.

For a file outside these rules, it stops, exit 2, with one of these errors:

- `set gateway.server.enrolment_code_file to a path with no link in it, such as <path>:
  <file> leads through a link`
- `set gateway.server.enrolment_code_file to the file itself, not a link: <file> is a link`
- `set gateway.server.enrolment_code_file to a regular file: <file> is not one`
- `make root or the user running qory the owner of <path>: it is owned by <user>, and
  only root and the user running qory may write gateway.server.enrolment_code_file and
  its folders, except a folder that root owns with the sticky bit set`
- `remove the group's write permission from <path>: the group <group> may write it, and
  only root and the user running qory may write gateway.server.enrolment_code_file and
  its folders, except a folder that root owns with the sticky bit set`, for any group,
  root's included
- `remove the write permission of other users from <path>: every user may write it, and
  only root and the user running qory may write gateway.server.enrolment_code_file and
  its folders, except a folder that root owns with the sticky bit set`
- `write the gateway's enrolment code (qecg_…) into <file>: the file is empty`
- `write only the gateway's enrolment code into <file>, with at most one newline after
  it: the file contains more`, for anything else in the file, or more than 1 KiB
- `write a gateway's enrolment code (qecg_…) into <file>: the file does not contain an
  enrolment code of Qory Apiary`, and for a code of another kind, `… the file contains
  the enrolment code of an access key of kind <kind>`
- `set gateway.server.enrolment_code_file to a file that qory can read: <file>: <err>`

`qory run`, `qory run pool`, `qory run register` and `qory run end` stop when the setting
is there, exit 2: `remove gateway.server.enrolment_code_file from <file>: qory gateway alone enrols
from it, and qory run does not`.

## Running it

Before it listens, `qory gateway` describes every integration `gateway.integrations`
declares, then fetches Qory Apiary's signed configuration. It prints:

```text
qory gateway: integration github: /usr/local/bin/qory-github 1.2.0
qory gateway: node ng_0123456789abcdef, instance i_…
qory gateway: listening on [::]:8443
```

SIGINT or SIGTERM stops it: it takes no new run, sends what it holds, and exits 0.

```text
qory gateway: stopping
✓ the gateway stopped
```

It refuses to start, before it listens:

- `~/.config/qory/forager.yaml has no gateway section, so there is no gateway to run`
- `<file>: gateway.server.via does nothing for qory gateway: a gateway reaches the server
  directly; remove gateway.server.via`
- `<file>: gateway.listen is required to run the gateway as a service: the address the
  other machines reach, such as 0.0.0.0:8443; or --listen`
- `<file>: gateway.listen is not host:port, such as 0.0.0.0:8443`, and for `--listen`,
  `--listen "<v>" is not host:port, such as 0.0.0.0:8443`
- `<file>: gateway.server is required to run the gateway as a service: it reports every
  run to Qory Apiary and takes the runs' policies from it`
- `gateway.tls is required with gateway.listen: a gateway serves other machines`

An address it cannot listen on is `listen on <addr>: <err>`.

Once Qory Apiary's configuration names the key's node, it refuses a key that is not a
gateway's: `qory gateway needs a gateway's access key, and this one is node <id>'s: enrol
a key with a code from the gateway's page in Qory Apiary`.

A gateway's access key gets a configuration that lists no `workspaces`: the gateway
takes each run's workspace from the run's run key, and a configuration that lists any is
refused.

## What it keeps

The gateway keeps its directory in qory's state directory: `~/.local/state/qory/gateway`,
or `$XDG_STATE_HOME/qory/gateway` when that is set, mode 0700; qory refuses a relative
`XDG_STATE_HOME`. There, Forager keeps the gateway's certificate authority,
`authority/ca.pem`, made at the first start and read at every later one, and the record of
each run under `runs/`.
