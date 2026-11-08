# Debug runs on Qory Apiary

`qory apiary run list` lists the runs on Qory Apiary, so that you can find the run that
failed. `qory apiary run diagnose <run id>` prints what Qory Apiary recorded about one
run, as one JSON document, so that a person or an agent can find why the run failed.
qory signs each request with a diagnostics key stored on this computer, which
`qory apiary enrol <server> <code>` makes and enrols once per server.

```sh
qory apiary enrol https://apiary.example qecd_F1XT-0RE0-0000-0000-0000-0000-00.uoES-kuj1vk0sq0qoGlmAg
qory apiary run list --state failed --since 24h
qory apiary run diagnose rn_0123456789abcdef
qory apiary run diagnose rn_0123456789abcdef --server https://apiary.example
qory apiary run diagnose rn_0123456789abcdef > diagnostics.json
```

## The diagnostics key

A diagnostics key is an access key of kind `diagnostics`, an Ed25519 key made on this
computer. Its secret stays on this computer, and Qory Apiary keeps only its public half.
The key lists runs and reads their diagnostics, and nothing else.

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
`qory apiary run list` and `qory apiary run diagnose` read it from there:

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

`apiary_public_key` is the server's public key, the pin. qory prints a run list or a
document only after its signature verifies under one of the keys listed there.

qory stops with an error on a secret that it cannot keep private: an `access-key-secret` that is a
symbolic link or not a regular file, a file mode that grants anything to the group or
to others, and a server directory that does. The first line of each error is the fix,
such as ``Run `chmod 600 <path>` ``.

## List the runs

`qory apiary run list` prints one line per run, the last created first, 25 runs by
default:

```text
🐝 apiary.example · runs
  rn_0123456789abcdeh  42  acme/web  hello  7  completed  2026-11-01T10:02:10Z  13m32s
  rn_0123456789abcdeg  41  acme/web  hello  6  failed     2026-11-01T09:01:00Z  1m35s
  rn_0123456789abcdef  40  acme/web  -      -  pending    -                     -
Next page: qory apiary run list --workspace acme/web --limit 3 --before rn_0123456789abcdef
```

The columns are the run's id, its number in the workspace, the workspace as
`<organisation>/<workspace>`, the workflow, the work item's number, the state, when the
run started, in UTC, and how long it ran, such as `45s`, `4m12s` or `1h02m`. A `-`
stands for a value the run does not have. A state other than the six that `--state`
takes is printed as Qory Apiary sent it.

Each flag selects runs, and a run is listed when it matches every flag given:

| Flag | Selects |
|---|---|
| `--workspace <org>/<workspace>` | the runs of one workspace, such as `acme/web`: two slugs of 1 to 40 characters, `a-z`, `0-9` and `-`, with no `-` at either end |
| `--work-item <number>` | the runs of one work item, by its number in the workspace; only with `--workspace` |
| `--workflow <name>` | the runs that start one workflow, such as `hello`: `a-z` first, then `a-z`, `0-9` and `-`, 55 characters at most |
| `--state <state>` | the runs in one state: `pending`, `running`, `completed`, `failed`, `cancelled` or `lost` |
| `--since <time>` | the runs created at or after a time: a duration counted back from now, such as `30m`, `1h` or `24h`, or an RFC 3339 time, such as `2026-11-01T09:00:00Z` |
| `--limit <number>` | at most this many runs, from 1 to 100; 25 without the flag |
| `--before <run id>` | the runs after this run in the list: the next page |

qory checks each value before it sends anything, and sends `--since` as a time in UTC.
It sends `GET <server>/v1/runs` with the flags as its query, signed with the key, and
waits at most 10 seconds. It reads the key of the one server it has a key for.

When more runs match, the last line is the command that lists the next page: every
flag you gave, `--since` as the time qory sent, and `--before` with the last run of the
page. The runs go to stdout, and the line for the next page to stderr, so a pipe
receives the runs alone. When no run matches, qory prints one line on stderr and exits
0.

With `--json`, qory prints the run list as Qory Apiary sent it, after its signature and
the contract's `runs.schema.json` check, and nothing else on stdout. The list follows
`contracts/diagnostics/v1/runs.schema.json` in
[qoryai/integrations](https://github.com/qoryai/integrations); its `next` is the value
of `--before` for the next page, and `null` on the last page.

## Run it

1. Copy **Run id** from Details on the run's page, or from `qory apiary run list`. The
   id is `rn_` and 16 characters, and the Qory Apiary API also returns it.
2. Run `qory apiary run diagnose <run id>`. With keys for several servers on
   this computer, add `--server <url>` to select one.

A run number is per workspace, so it does not identify a run. qory stops with an error,
before it sends anything, for a run number and for any other value that is not a run id.
It sends the run id as you typed it, `GET <server>/v1/runs/<run id>/diagnostics`, signed
with the key, and waits at most 10 seconds. The document goes to stdout exactly as Qory
Apiary sent it, followed by a line ending when it has none. Nothing else goes to stdout,
so a pipe or a file receives JSON alone. Errors go to stderr.

The document follows the diagnostics contract, `contracts/diagnostics/v1` in
[qoryai/integrations](https://github.com/qoryai/integrations). It contains the run, its
queue entry, each dispatch, the preparation of the machine with the step that stopped it,
the last events and the end of the run's log. The document contains the run's `rn_` id
and not its number.

## Errors

Each command exits 2 in these cases, and 1 for every other failure:

- a mistake in the command line: the arguments, the server URL, the code, the run id, or
  a flag of `qory apiary run list` with a value outside its rule
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
| `Use the run's id, rn_ and 16 characters: this command takes only that id.` | The value is not a run id, and the next line contains it. Nothing was sent. |
| `Check the run id: <host> has no run <run> (not_found).` | Qory Apiary has no run with that id. |
| `Update qory to a release that works with <host>: the request was not accepted: unsupported_contract_version (400).` | Qory Apiary does not serve the contract version that this qory sends. |
| `Check the run with the instance admin of <host>: the request was not accepted: <code> (<status>).` | Qory Apiary refused the request with another signed code. |
| `Check that <server> is the server the diagnostics key in <dir> was enrolled with: ...` | The response does not verify under the pinned key, and qory printed nothing. |
| `Check that <server> is reachable: <error>.` | The request did not reach Qory Apiary, or timed out. |

`qory apiary run list` stops with the lines of `qory apiary run diagnose` for a missing
key, a key that Qory Apiary did not accept, a response that does not verify and a
server that is not reachable, and with these:

| First line | What happened |
|---|---|
| `Pass --<flag> as ...` | The value of the flag is outside its rule, and the next line contains it. Nothing was sent. |
| `Add --workspace <org>/<workspace>: a work item's number counts in one workspace.` | `--work-item` without `--workspace`. Nothing was sent. |
| `Change or remove a flag to list more runs: no run on <host> matches <flags>.` | No run matches the flags. qory exits 0. |
| `No action needed: <host> has no runs.` | Qory Apiary has no runs. qory exits 0. |
| `Check --<flag> <value>: <host> did not accept the value (invalid_request, 400).` | Qory Apiary did not accept the value, such as a workspace it does not have, or a `--before` that is the id of no run. |
| `Update qory to a release that works with <host>: the request was not accepted: unsupported_contract_version (400).` | Qory Apiary does not serve the contract version that this qory sends. |
| `Check the run list with the instance admin of <host>: the request was not accepted: <code> (<status>).` | Qory Apiary returned another signed code. |
| `Check the run list with the instance admin of <host>: the response does not follow runs.schema.json, so qory printed nothing.` | The signed response is not a run list of the contract. |

qory never prints the key's secret, the enrolment code, the proof, a signature or the
request's headers.
