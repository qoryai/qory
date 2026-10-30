# Debug runs on Qory Apiary

`qory apiary run diagnose <run id>` prints what Qory Apiary recorded about one run, as
one JSON document, so that a person or an agent can find why the run failed. qory signs
the request with a diagnostics key stored on this computer, which
`qory apiary enrol <server> <code>` makes and enrols once per server.

```sh
qory apiary enrol https://apiary.example qecd_F1XT-0RE0-0000-0000-0000-0000-00.uoES-kuj1vk0sq0qoGlmAg
qory apiary run diagnose rn_0123456789abcdef
qory apiary run diagnose rn_0123456789abcdef --server https://apiary.example
qory apiary run diagnose rn_0123456789abcdef > diagnostics.json
```

## The diagnostics key

A diagnostics key is an access key of kind `diagnostics`, an Ed25519 key made on this
computer. Its secret stays on this computer, and Qory Apiary keeps only its public half.
The key reads the diagnostics of runs and nothing else.

A diagnostics key is kept apart from this machine's node access key: qory never reads it
from `forager.yaml` or from the files beside `forager.yaml`, and `qory apiary enrol`
writes neither.

## Enrol a diagnostics key

1. An instance admin issues a diagnostics enrolment code in Qory Apiary, under
   **Instance settings > Diagnostics keys**. The code starts with `qecd_`, is valid for
   15 minutes and works once.
2. Run `qory apiary enrol <server> <code>`, with the server's URL, such as
   `https://apiary.example`. The URL is https, or http to `localhost` or `127.0.0.1`
   alone, with no path, query or `#`.

qory makes the key in the server's directory, described in the next section, and sends
Qory Apiary the public key, the name of this computer and a proof that this computer
has the secret. The response must be signed with the server key whose fingerprint is in
the code. qory then writes `access-key-secret`, `apiary.json` and `instance-id`, and
prints one line:

```text
Enrolled diagnostics key ak_0123456789abcdef for apiary.example.
```

The key is active from that response on. It stays active until an instance admin
revokes it under **Instance settings > Diagnostics keys**.

A code for another kind of key, such as a node's `qecn_` code, stops the command before
anything is made or sent. If the server's directory contains a key already, the command
stops too: remove the directory to enrol a new key, and ask an instance admin to revoke
the old one.

### When the command stops

qory writes each step of the enrolment to the server's directory, so that a stop after
Qory Apiary used the code does not lose the key without a message:

1. Before the request, the new secret goes to `access-key-secret.new`, and
   `enrolment-pending` records the SHA-256 of the code, the key's fingerprint and the
   time.
2. When the signed response comes, qory writes it to `enrolment-answer`, with the
   request, before anything else changes.
3. qory renames `access-key-secret.new` to `access-key-secret`, writes `apiary.json` and
   `instance-id`, and removes `enrolment-pending` and `enrolment-answer`, in that order.

If the command stops after step 2, run the same command again: qory finishes the
enrolment from `enrolment-answer`, at any time, without sending the code again. If no
response came, the same command within the code's 15 minutes retries with the same key.
If Qory Apiary refuses the code or the key, qory moves the new secret aside to
`access-key-secret.old.<Unix time>`; get a new code then.

## The files

`qory apiary enrol` writes the key to one directory per server, under the user's
configuration directory, `$XDG_CONFIG_HOME/qory` or `~/.config/qory`, and
`qory apiary run diagnose` reads it from there:

```text
~/.config/qory/apiary/                  mode 0700
  apiary.example/                       mode 0700, one directory per server
    access-key-secret                   mode 0600, the key's secret, one line
    apiary.json                         mode 0600, the server, the key's id, the server's key
    instance-id                         mode 0600, this computer's instance id
    locks/key.lock                      the lock held while the key is written or read
```

During an enrolment, the directory also contains the files of the steps in
[When the command stops](#when-the-command-stops).

The directory's name is the server's host in lower case. When the server's URL has a
port other than 443 for https, or 80 for http, the name ends with `_` and the port:
`https://apiary.example:8443` is `apiary.example_8443`. When the URL is http, the name
starts with `http_`: `http://localhost:4000` is `http_localhost_4000`, and
`https://localhost:4000` is `localhost_4000`.

`apiary.json` is one JSON object:

```json
{
  "version": 1,
  "server": "https://apiary.example",
  "access_key_id": "ak_0123456789abcdef",
  "apiary_public_key": [{"alg": "ed25519", "public_key": "mptNqtgGKgLhLZxmOGfpBQkdeBNH7QN3Qs9ETNumy8Q"}]
}
```

`apiary_public_key` is the server's public key, the pin. qory prints a document only
after its signature verifies under one of the keys listed there.

qory stops with an error on a secret that it cannot keep private: an `access-key-secret` that is a
symbolic link or not a regular file, a file mode that grants anything to the group or
to others, and a server directory that does. The first line of each error is the fix,
such as ``Run `chmod 600 <path>` ``.

## Run it

1. Copy the run id, `rn_` and 16 characters, from the run's page in Qory Apiary.
2. Run `qory apiary run diagnose <run id>`. With keys for several servers on this
   computer, add `--server <url>` to select one.

qory sends `GET <server>/v1/runs/<run id>/diagnostics`, signed with the key, and waits
at most 10 seconds. The document goes to stdout exactly as Qory Apiary sent it, followed
by a line ending when it has none. Nothing else goes to stdout, so a pipe or a file
receives JSON alone. Errors go to stderr.

The document follows the diagnostics contract, `contracts/diagnostics/v1` in
[qoryai/integrations](https://github.com/qoryai/integrations). It contains the run, its
queue entry, each dispatch, the preparation of the machine with the step that stopped it,
the last events and the end of the run's log.

## Errors

Both commands exit 2 in these cases, and 1 for every other failure:

- a mistake in the command line: the arguments, the server URL, the code or the run id
- neither `HOME` nor `XDG_CONFIG_HOME` is set
- no diagnostics key for the server, or keys for several servers and no `--server`
- an error when qory reads the stored diagnostics key, such as a secret file that
  grants access to the group or others
- for `qory apiary enrol`: a key that is there already, a host name of this computer
  that does not fit the name of an access key, or a clock before 1970

Each error's first line starts with the fix.

`qory apiary enrol`:

| First line | What happened |
|---|---|
| `Use a diagnostics enrolment code, qecd_..., from Instance settings > Diagnostics keys in Qory Apiary: ...` | The code is not a diagnostics enrolment code, or does not read. Nothing was sent. |
| `Remove <dir> to enrol a new diagnostics key for <host>: the directory contains a diagnostics key already.` | The server's directory contains a key. Nothing was sent. |
| `Get a new diagnostics enrolment code from Instance settings > Diagnostics keys in Qory Apiary: <host> refused the code as used, expired or cancelled (unauthorized, 401).` | The code was used, has expired or was cancelled. |
| `Ask an instance admin of <host> to revoke the access key <id>: Qory Apiary enrolled it for ..., not as a diagnostics key, so qory kept nothing.` | Qory Apiary enrolled the key as another kind. The key is active, and qory deleted its secret. |
| `Check that <server> is the Qory Apiary server that issued the code, then run the same command again within 15 minutes: ...` | The response is not signed with the server key whose fingerprint is in the code. qory stored no key. |
| `Check that <server> is reachable, then run the same command again within 15 minutes to retry with the same key: <error>.` | The request did not reach Qory Apiary, or no response came. |
| `Run the same command again to finish the enrolment: Qory Apiary enrolled the key <id>, and <error>.` | A file of the key could not be written after Qory Apiary enrolled it. |

`qory apiary run diagnose`:

| First line | What happened |
|---|---|
| ``Run `qory apiary enrol <server> <code>` first: no diagnostics key in <dir>.`` | No server directory, or no key in it. |
| ``Pass --server <url>: diagnostics keys for several servers are in <dir>: a, b.`` | Keys for more than one server are on this computer. |
| ``Remove <dir>, then run `qory apiary enrol <server> <code>` again: <host> did not accept the diagnostics key <id> (401).`` | Qory Apiary has no such key, has revoked it, or this computer's clock is more than five minutes off. |
| `Check the run id: <host> has no run <id> (not_found).` | Qory Apiary has no run with that id. |
| `Update qory to a release that works with <host>: the request was not accepted: unsupported_contract_version (400).` | Qory Apiary does not serve the contract version that this qory sends. |
| `Check the run with the instance admin of <host>: the request was not accepted: <code> (<status>).` | Qory Apiary refused the request with another signed code. |
| `Check that <server> is the server the diagnostics key in <dir> was enrolled with: ...` | The response does not verify under the pinned key, and qory printed nothing. |
| `Check that <server> is reachable: <error>.` | The request did not reach Qory Apiary, or timed out. |

qory never prints the key's secret, the enrolment code, the proof, a signature or the
request's headers.
