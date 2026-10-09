# Running a session

`qory run` starts your agent on its composed harness, inside the session of
[Forager](https://github.com/qoryai/forager). Every connection the agent makes goes
through a proxy on your machine, Forager's gateway. The session is recorded.

## What `qory run` does

It is the same launch as `qory harness launch` (see [the harness](harness.md)). Two things
are added:

- Every connection the runtime makes goes through a proxy on your machine, and is
  recorded.
- The session is written as events, beside its output. The events hold the session's
  output, what Forager observes, and what the runtime reports itself.

```sh
qory run                              # the composed runtime, at your terminal
qory run claude -- -p "Reply pong"    # one headless turn; arguments after -- go to the runtime
```

The runtime is the one the harness is composed for. When it is composed for several, the
first argument selects one. Arguments after `--` go to the runtime, after the launch
template's own.

qory computes the home itself, as compose does, and refuses a run whose harness report
names another: run `qory harness compose` again. The report fixes no variable and adds no
mount. Behind a wall, `harness.home` comes from the `qory.yaml` in qory's configuration
directory alone: another `qory.yaml` that moves the home refuses a walled run.

The exit status is the runtime's.

### At a terminal, or on pipes

At a terminal, the session runs on a pseudo-terminal. The runtime's own interface works,
and its bytes are captured as well.

The session runs on pipes instead, and `qory` reads the runtime's structured output, when:

- `--headless` is given,
- there is no terminal, or
- an argument the runtime's descriptor lists as headless is given, such as `-p` for
  Claude Code. With it the runtime has no interface anyway, so
  `qory run claude -- -p '…'` needs no flag.

## The record

Each run is recorded on your machine, outside the checkout, in qory's state directory:
`~/.local/state/qory/runs/<checkout>-<hash>/<id>/`, or under `$XDG_STATE_HOME/qory` when
that is set to an absolute path. `<checkout>` is the checkout's directory name, and
`<hash>` the first 12 hex digits of the SHA-256 of its full path, with links resolved.
Each checkout has its own folder. The state directory, its `runs` directory and the
checkout's folder are mode 0700: only you can open them. qory names the run's folder
when the run ends:

```
qory run: the record is in /home/you/.local/state/qory/runs/app-3f9a1c0b7d2e/0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5e6f
```

It holds:

- `events.jsonl`: one event per line.
- `output.log`: the session's bytes.

`qory run resend` sends a finished run's record to the server again: after a Forager process
that died, or a server that was away. See [Resending a run's record](#resending-a-runs-record).

## Try it: the hello example

This walkthrough uses the hello example from the [README](../README.md#try-it). Run it in
that directory. It goes in two steps: observed, then behind a wall with the model's token
kept outside.

### Observed

```sh
qory run -- -p "/hello"         # one headless turn, observed
```

The last line names the run's record: `events.jsonl` in that folder says what the agent
reached, what it printed and how it ended.

### Behind a wall

Without a wall, the proxy sees only programs that honour it. A **wall** starts the agent
in a container. The container's one route out is the proxy.

A wall needs the `docker` command. It also needs an image that contains the agent. Qory
publishes none: build Qory's from a checkout of `qory` at the commit `qory version`
prints. See [Qory's images](#qorys-images).

```sh
git clone https://github.com/qoryai/qory
git -C qory checkout <commit>            # the commit qory version prints
docker build -t qory-agent qory/images/agent
```

On a Mac, first set `wall.helper` to the Linux build of the same `qory` release. See
[The wall](#the-wall).

A walled run of Claude Code can keep its model credential outside the container. Define
the wall, the credential and what the agent may reach in `~/.config/qory/forager.yaml`:

```yaml
apiVersion: qory.dev/v1alpha1
gateway:
  egress:
    mode: enforce
    allow: [api.anthropic.com]
  credentials:
    model:
      env: CLAUDE_CODE_OAUTH_TOKEN
      hosts: [api.anthropic.com]
      auth: {scheme: bearer}
      placeholders: [CLAUDE_CODE_OAUTH_TOKEN]
wall:
  adapter: docker
  image: qory-agent
```

Then pass the run a policy that selects the credential. Keep the policy outside the
checkout:

```yaml
# ~/hello-policy.yaml
version: 1
egress:
  mode: enforce
  allow: [api.anthropic.com]
credentials:
  - name: model
```

```sh
export CLAUDE_CODE_OAUTH_TOKEN=...       # from `claude setup-token`
qory run --policy ~/hello-policy.yaml -- -p "/hello"
```

The agent greets you as before. What changed:

- Inside the container, `CLAUDE_CODE_OAUTH_TOKEN` is a placeholder.
- The proxy sets the real token on each request to `api.anthropic.com`.
- The record lists those requests with the credential's name, `model`, as `credential`
  in `dev.qory.run.egress`. It never lists the token's value.
- Everything else the agent tries to reach is denied and recorded.

A run whose policy selects a credential needs a wall; without one it does not start. See
[Credentials the agent never has](#credentials-the-agent-never-has).

## forager.yaml

One optional file defines what Forager does on this machine:
`~/.config/qory/forager.yaml`. It lives beside your `qory.yaml`, and nowhere else. So a
repository cannot set it. It has three sections beside `apiVersion`: `gateway`, the
proxy's policy, the server, the credentials and the integrations; `session`, this
instance and what applies to every run; and `wall`, the container. `gateway.listen`,
`gateway.tls` and `gateway.run_credentials` are `qory gateway`'s: `qory run` does not use
them. See [the gateway as a service](gateway.md).

`session.gateway` names a gateway on another machine instead, for a machine that holds no
`gateway` section: see [Through a separate gateway](#through-a-separate-gateway).

```yaml
# ~/.config/qory/forager.yaml
apiVersion: qory.dev/v1alpha1
gateway:
  egress:                  # what the runtime may reach; enforce denies the rest
    mode: enforce          # or observe: record everything, deny only what deny lists
    allow: [api.anthropic.com, github.com, "*.github.com"]
    deny: [gist.github.com]                # denied in either mode, whatever allow lists
  server:                  # the server every run reports to; optional
    url: https://apiary.example           # a scheme and a host, nothing after
    access_key_id: ak_0123456789abcdef    # this machine's access key; its secret is not in this file
    apiary_public_key:                    # the server's key, which signs every answer
      - {alg: ed25519, public_key: mptNqtgGKgLhLZxmOGfpBQkdeBNH7QN3Qs9ETNumy8Q}
  credentials:             # tokens the proxy sets on requests; a run's policy selects them
    model:                                # the model credential, kept outside the container
      env: CLAUDE_CODE_OAUTH_TOKEN        # or file: an absolute path; or adapter: a program
      hosts: [api.anthropic.com]
      auth: {scheme: bearer}              # or basic with username, or header with header
      paths: [/v1/*]                      # where on its hosts the token goes; optional
      placeholders: [CLAUDE_CODE_OAUTH_TOKEN]
  integrations:            # programs that mint a credential; optional
    github:                               # qory-github, found on the PATH
      settings: {app_id: 123456, private_key_file: /etc/qory/github-app.pem}
session:
  instance:                # optional
    name: build-01         # how the server shows this machine; the host name by default
  run:                     # optional
    timeout: 5h30m         # stop a runtime that runs this long
    stop_signal: SIGINT    # requests it to stop; the runtime's descriptor's, else SIGTERM
    stop_grace: 30s        # between that signal and SIGKILL; 10s
wall:                      # start the runtime in a container; optional
  adapter: docker
  image: example.com/agent@sha256:…     # the runtime and your toolchain, FROM Qory's
  env: [NODE_ENV]                       # names; nothing else of your environment goes in
  memory: 14g                           # at most 14 GB of memory; also cpus, pids_limit, shm_size; optional
```

Without `gateway.egress`, everything the runtime reaches is allowed and recorded. A
`forager.yaml` that does not read means no run.

## Runtimes

`qory run` runs whichever runtime the harness is composed for. Nothing in `forager.yaml`
is particular to one runtime.

Forager's session reads what it needs to know about a runtime from a **descriptor**. A
descriptor is a file of data, in the format of the [Forager
contract](https://github.com/qoryai/forager/blob/main/contracts/forager/v1/README.md#the-runtime).
It says:

- how the runtime's hooks are installed,
- what its output means as events,
- which signal asks it to stop,
- which variables hold its model credential: see [The model
  credential](#the-model-credential).

Forager ships the descriptor for Claude Code. `~/.config/qory/runtimes/<runtime>.yaml`
describes another runtime, or replaces the one shipped.

A runtime that nothing describes still runs. The run, its log and its egress are
recorded. The session's own events are not.

## Hosts a module declares

A module declares the hosts it reaches, under `egress` in its manifest. The compose
unions them into the report, together with the hosts the runtime declares.

Forager reports them as `harness_hosts` in `dev.qory.run.policy_applied`, for a
receiver to compare with the policy. They decide nothing. The policy alone defines what
the runtime reaches.

## Time limits

A denied connection is recorded, and the session goes on. Only a time limit you set ends
a session.

Set it with `--timeout 5h30m`, or with `session.run.timeout`. When the limit is reached:

- the runtime is stopped,
- `dev.qory.run.exited` records the limit as the reason,
- `qory run` exits 124, as `timeout(1)` does.

`--timeout 0` lifts the limit `forager.yaml` sets.

### How the runtime is stopped

The runtime is stopped at the limit, or when `qory run` gets a signal. Then:

1. The runtime gets the stop signal: `--stop-signal` or `session.run.stop_signal`, else the one
   its descriptor sets, else `SIGTERM`.
2. After the grace time, it gets `SIGKILL`. The grace time is `--stop-grace` or
   `session.run.stop_grace`, 10s unless set. It is the time a session needs to close what it has
   open.

Runtimes differ in what a signal means. One closes its session on `SIGINT` and drops it
on `SIGTERM`. So the signal is yours to choose: `SIGTERM`, `SIGINT`, `SIGHUP`, `SIGQUIT`,
`SIGUSR1` or `SIGUSR2`.

`session.run.timeout`, `session.run.stop_signal` and `session.run.stop_grace` in
`forager.yaml` set these for every run on the machine.

## A run's variables

Several sources may set a variable of the agent's process. For each name, the run takes
the value of the highest source that sets it:

1. The values qory and the runtime fix: Forager's own names, `QORY_HARNESS_HOME`, and
   the variables of the runtime's own launch template, such as Codex's `CODEX_HOME`. No
   other source overrides them, `env` in `qory.yaml`, a settings fragment's and a
   module's export included. The `env` of `harness.launch.<runtime>` replaces the
   template's variables, and its values are defaults.
2. The server's run configuration. See [The server](#the-server).
3. `--env`, the run's own.
4. `wall.env`, the machine's.
5. The harness's defaults: `env` in `qory.yaml`, the `env` of
   `harness.launch.<runtime>`, the `env` a settings fragment sets, and what the modules
   export.
6. The shell `qory run` starts in. A walled run takes none of it but the names
   `wall.env` and `--env` list.

Forager's deny list, names such as `PATH` and `DOCKER_HOST`, leaves out a value of
sources 2 to 5, a module's export included.

`--env` and `wall.env` name variables of `qory run`'s environment. `--env` needs no
wall; `--image`, `--mount` and the limits do. A run without a wall gets none of the
server's variables.

A value that loses is left out, and the run starts. When a value of `--env` loses, `qory
run` prints a line that says why:

```
qory run: LOG_LEVEL from --env is not used: apiary.example.com sets it
qory run: LOG_LEVEL from --env is not used: no source may set it
qory run: LOG_LEVEL from --env is not used: the harness sets it
```

`dev.qory.run.policy_applied` lists every variable by name, never a value: the source
whose value applies, and each value that lost, with its source and why.

## The server

`qory run` starts Forager's gateway on this machine for the run. With a server
configured, the gateway starts by fetching the server's configuration. It signs every
request with the access key's secret, and checks every answer against the server's key
it pins, `apiary_public_key`. The run does not start unless the server answers.

The configuration defines where the gateway sends the run's events, and whether the
server has a run configuration. The gateway fetches the run configuration with the run's labels, the
checkout's forge and repository among them. It reloads it when the server reports it
changed. It may hold:

- `security_policy`, the server's policy. `gateway.egress` of `forager.yaml` narrows it:
  see [A run's own policy](#a-runs-own-policy). Without it, `gateway.egress` is the
  run's policy.
- `variables`, which reach the agent's process. A value of the server wins over every
  source but the values qory and the runtime fix, and a run without a wall gets none of
  them. See [A run's variables](#a-runs-variables).

`--local` runs with the files alone and the machine's policy. The server is not
contacted.

### The access key and the instance

The server knows this machine by its access key, an Ed25519 key. A machine gets its key
in one of two ways: a key generated on the node's page in Qory Apiary, set on the
machine as the `QORY_` variables the page shows, or a key the machine enrols with a
code, with `qory access-key enrol`. See [Enrol with a code](#enrol-with-a-code). The
server keeps only its public key: a key enrolled with a code is made on the machine, and
a key generated on the node's page in Qory Apiary is made in the browser, which shows
its secret once, for you to put on the machine or in a CI's secret store. A key is never
rotated: a new one is enrolled or generated, and the old one revoked.

`gateway.server` of `forager.yaml` holds two values of the key:

- `access_key_id`, the key's id: `ak_` and 16 characters, which the server assigns.
- `apiary_public_key`, the pin: the server's public keys. Every answer of the server is
  verified under one of them. A pin that lists the Forager contract's published fixture
  key is refused.

`QORY_ACCESS_KEY_ID` and `QORY_APIARY_PUBLIC_KEY`, the pin as JSON, hold them when the
file does not. A value set in both is refused.

The key's secret is never in `forager.yaml`. qory reads it from the file descriptor
`--access-key-secret-fd` names, else from `QORY_ACCESS_KEY_SECRET`, else from the file
`access-key-secret` beside `forager.yaml`. The secret is one line: `qak_` and 43
characters. The file is read only when it is a regular file, not a link, that you own
and that grants nothing to the group or to others, in a directory that is yours alone.
The Forager contract's published fixture key is refused.

`--access-key-secret-fd` is a flag of `qory run` and `qory run resend`. The descriptor is
3 or above. qory reads it to its end and closes it first, so nothing it starts inherits
it:

```sh
qory run --access-key-secret-fd 3 -- -p "$prompt" 3< "$secret_file"
```

A run without the id or the secret does not start. One without a pin does not start
either, `apiary_public_key_missing`.

`QORY_ACCESS_KEY_ID`, `QORY_ACCESS_KEY_SECRET` and `QORY_APIARY_PUBLIC_KEY` stay
Forager's. Every qory command reads them into memory when it starts, and removes them
from its environment before it starts anything. So no session, worktree command, tool or
integration it starts inherits them. A `wall.env` or `--env`
that names one is refused.

`gateway.server.access_key`, `gateway.server.secret` and `QORY_SERVER_SECRET` held a
workspace access key, which servers no longer accept. The two keys are refused, and so is
`QORY_SERVER_SECRET` when `forager.yaml` has `gateway.server`. Remove the two keys from
`forager.yaml` and unset `QORY_SERVER_SECRET` first, since `qory access-key enrol` reads
the file and refuses them too, then connect the machine as a node, with
`qory access-key enrol` or a key generated on the node's page in Qory Apiary. qory
removes `QORY_SERVER_SECRET` from its environment with the three variables, server or
not, and a `wall.env` or `--env` that names it is refused.

Each machine that runs qory is an **instance** of its node. qory names it on every
request:

- Its id is kept in the file `instance-id` beside `forager.yaml`, created by the first
  run with a server. An id that was not made on this machine is replaced. When qory
  cannot write the directory, the id is the process's own, and qory says so.
- `session.instance.name` is its display name on the server. Unless set, it is the host name, or
  the host name's first label when the whole does not fit: 1 to 64 of `A-Z`, `a-z`,
  `0-9`, dot, underscore and dash, starting with a letter or a digit.

`qory run` prints the node and the instance once the server's configuration is read:
`qory run: node <id>, instance <id>`. When the server refuses, qory says what the
refusal means and what to do, then Forager's words and the code:

| Code | What it means |
| --- | --- |
| `unauthorized` | the server refused the request: it does not know the access key, has revoked it, or this machine's clock is more than five minutes off; check the clock, else move this machine to a new key with `qory access-key enrol --replace`; for a key from `QORY_ACCESS_KEY_SECRET` or `--access-key-secret-fd`, enrol a new key |
| `answer_unsigned` | an answer does not verify under the pin |
| `instance_limit` | the node's live instances are at its limit |
| `run_closed` | the server closed the run before it started; the exit status is 1 |

#### Enrol with a code

An owner or administrator in Qory Apiary creates an enrolment code on the node's page.
The code is used once, and lasts 15 minutes.

```sh
qory access-key enrol https://apiary.example qec_…
```

qory makes the key, keeps its secret in `access-key-secret.new`, mode `0600`, and prints
its fingerprint. It sends the server the public key, named `session.instance.name`, else
the host name; when the host name does not fit a name, set `session.instance.name`. The key is
active as soon as the server answers.

The server's answer is signed. qory moves the secret to `access-key-secret`, then
writes `gateway.server.access_key_id` into `forager.yaml`, and `gateway.server.url` and the
pin, `gateway.server.apiary_public_key`, when the file has none. A file without a
`gateway` section gains one at its end. A pin already there is kept. The rest of the
file, its comments and its order stay as they are.

qory keeps the verified answer, with the request it answers, in `enrolment-answer`
beside `forager.yaml`, mode `0600`, until the enrolment is finished. Should enrol stop
after the answer came, the same command finishes the enrolment on this machine from
that file, at any time, without asking the server again, which refuses a used code.

Before it makes a key, qory checks the code against the pin `forager.yaml` has, so a code
of another server is refused. When `forager.yaml` names another `gateway.server.url`,
enrol refuses: enrol with that server, or change `gateway.server.url` first.

When `access-key-secret` exists, enrol refuses, so it never replaces this machine's key
unasked: use `--replace`, below, to move the machine to a new key, or `--print` for a
key kept elsewhere. The one exception is a retry: the same command, with the same code, within
the 15 minutes, while `access-key-secret` still holds the key made for it, retries with
that key.

When the enrolment does not complete:

| Answer | What qory does |
| --- | --- |
| 401, `unauthorized` | the code was used or has expired. The secret made for it, in `access-key-secret.new`, is moved aside, and enrolling needs a new code. If you did not use the code, someone else did: tell the owner or administrator in Qory Apiary who made it. The code's issuer must revoke the key it enrolled |
| `key_invalid` | the server refused the key. The secret made for it, in `access-key-secret.new`, is moved aside, and enrolling needs a new code |
| `key_limit` | the node already holds two keys. qory keeps the key: once an owner or administrator in Qory Apiary has revoked one of them, the same command within the 15 minutes succeeds |
| 429, `rate_limited`, signed | this code was tried too often. `forager.yaml` is not changed. qory keeps the key, and the same command, run later within the code's 15 minutes, retries with it. An unsigned 429 is an `answer_unsigned` |
| `answer_unsigned`, or no answer | the answer does not verify under the server's key the code names, or never came. `forager.yaml` is not changed. qory keeps the key, and the same command within the 15 minutes retries with it |

A secret enrol moves aside goes to `access-key-secret.old.<Unix time>`. It is deleted
once a new key is enrolled and a run uses it. Enrol moves aside only
`access-key-secret.new`: a refused enrolment never moves or changes
`access-key-secret`.

Enrol refuses while `QORY_ACCESS_KEY_ID`, `QORY_ACCESS_KEY_SECRET` or
`QORY_APIARY_PUBLIC_KEY` is set: the key it keeps in this machine's files would
contradict the variable. Unset it, or use `--print`. It also refuses while a run without
a wall is running on this machine: it must end before a key is made.

#### Replace the machine's key

```sh
qory access-key enrol --replace https://apiary.example qec_…
```

`--replace` moves a machine that holds a key to a new one, with a new code. The old key
stays in use until the new one is active. qory makes the new key in
`access-key-secret.new`, mode `0600`, and enrols it; `access-key-secret` and
`forager.yaml` stay as they are until the server's signed answer, so an enrolment that
does not complete leaves the old key working, and the table above says what to do. Then
the new secret takes the place of `access-key-secret`, `forager.yaml` names the new key,
and the old secret is removed. qory names the old key: it still works on Qory Apiary
until an owner or administrator revokes it on the node's page, unless it is revoked
already.

The enrolment uses the code alone, never the old key, so a key the server has revoked
can be replaced too. Before the server's answer, the same command within the code's 15
minutes retries with the new key; after it, the same command finishes the replacement
on this machine, at any time. On a machine without a key,
`--replace` enrols as the command does without it. `--replace` and `--print` do not go
together.

Should `enrolment-answer` be lost or damaged after a replacement stopped between the new
secret taking the place of `access-key-secret` and `forager.yaml` naming the new key, the
old key's secret is still in `access-key-secret.replaced` beside `forager.yaml`, which
still names the old key's id: move it back to `access-key-secret` to restore the
machine, unless the old key was revoked, and revoke the new key on the node's page in
Qory Apiary, since it is active on the server.

#### When `enrolment-answer` is refused

`enrolment-answer` holds no secret: the enrolment request, with the new key's public
key, and the server's signed answer to it. qory refuses one that does not verify for the
command's server, code and key, and one that is not an answer at all stops every enrol
without `--print`. Removing it is safe: it loses no secret, only the way to finish the
enrolment without the server, which refuses the code it used. Once it is removed, what
to do depends on what the directory holds:

- `access-key-secret.new` is still there: the server enrolled the new key, and nothing
  was put in place, so the machine still holds its old key, if it had one. Revoke the
  new key, the one enrol named in `enrolled as …`, on the node's page in Qory Apiary,
  then enrol with a new code, with `--replace` when the machine holds a key; enrol moves
  the unused new key aside.
- `access-key-secret.new` is gone, and `forager.yaml` does not name the new key: the new
  key is in `access-key-secret`, beside another key's id or none. Revoke the new key on
  the node's page in Qory Apiary. After a replacement, move `access-key-secret.replaced`
  back as the paragraph above says; otherwise enrol again with a new code and
  `--replace`.
- `forager.yaml` names the new key: the enrolment is done, and nothing needs running.
  After a replacement, revoke the old key on the node's page in Qory Apiary, unless it
  is revoked already, and delete `access-key-secret.replaced` if it is still there: it
  holds the old key's secret.

#### For a CI

With `--print`, enrol writes no key and no setting on this machine. It prints
the settings on stdout, one `NAME=value` line each, and everything else on stderr.

`qory access-key enrol --print <server> <code>` prints three lines:

```sh
QORY_ACCESS_KEY_ID=ak_0123456789abcdef
QORY_ACCESS_KEY_SECRET=qak_…
QORY_APIARY_PUBLIC_KEY=[{"alg":"ed25519","public_key":"mptNqtgGKgLhLZxmOGfpBQkdeBNH7QN3Qs9ETNumy8Q"}]
```

A key generated on the node's page in Qory Apiary comes as the same three variables.

Only `QORY_ACCESS_KEY_SECRET` belongs in the CI's secret store. The id and the pin are
plain settings; `QORY_APIARY_PUBLIC_KEY` is the pin as JSON. The CI's `forager.yaml` then
needs `gateway.server.url` alone.

The key is for another machine, so `enrol --print` leaves the server and the pin of
this machine's `forager.yaml` aside: it checks the code against `QORY_APIARY_PUBLIC_KEY`
when that is set. The key's name is still this machine's, and the key is active as soon
as the server answers. It keeps nothing, so an enrolment whose answer is lost or does
not verify cannot be retried: get a new code, and have the owner or administrator in
Qory Apiary revoke the key qory printed the fingerprint of, should Qory Apiary show it.

#### Stored secrets need a wall

When the server lists stored secrets for the machine's access key, every run needs a
wall. An unwalled run is refused, `server_needs_wall`, once the server's configuration
is read.

qory then keeps a marker, the file `stored-secrets` beside `forager.yaml`. While it is
there, an unwalled run is refused before it starts, `--local` and a `forager.yaml`
without a server included. With the secret from `access-key-secret`, a server's
configuration that lists no stored secrets removes it. With the secret from
`QORY_ACCESS_KEY_SECRET` or `--access-key-secret-fd`, the marker stays as it is.

`qory access-key enrol` writes the marker before it makes a key, unless `--print`, since
the new key may receive stored secrets. It is removed as above.

## Through a separate gateway

A machine whose runs go through a gateway on another machine, or a service on this one
(see [the gateway as a service](gateway.md)), names it in `session.gateway` of its own
`forager.yaml`. `qory run` then starts no gateway of its own and holds no access key. The
session reaches the gateway over TLS 1.3, and every request carries the run credential
its issuer signed, which the gateway verifies. The proxy, the policy, the credentials and
the access key are the gateway's, on its machine, and the gateway reports the run to Qory
Apiary. Without `session.gateway`, nothing changes: `qory run` starts a gateway of its own
for each run.

```yaml
# ~/.config/qory/forager.yaml, on a machine behind a gateway
apiVersion: qory.dev/v1alpha1
session:
  gateway:
    url: https://gateway.example:8443
    ca_file: gateway-ca.pem                          # optional
    certificate_sha256: 47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=   # optional
    run_credential_file: /run/issuer/run-credential  # or --run-credential-fd, or QORY_RUN_CREDENTIAL_SECRET
```

| Key | What it is |
|---|---|
| `session.gateway` | The gateway this machine's runs go through, on another machine or as a service on this one. Without it, qory run starts a gateway of its own for each run from the gateway section. With it, this machine holds no access key, and this file holds no gateway section: each run brings its run credential. |
| `session.gateway.url` | The gateway: an https URL of a host and an optional port, with nothing after. |
| `session.gateway.ca_file` | A PEM file of the certificate authority that signed the gateway's certificate, when the system's roots do not hold it. qory reads it relative to forager.yaml's directory, and it takes the place of the system's roots for this link. |
| `session.gateway.certificate_sha256` | Optional: the SHA-256 of the gateway certificate's public key, base64. With it, qory accepts only a certificate with that key, and still checks its chain. |
| `session.gateway.run_credential_file` | The file that holds this run's run credential, which its issuer signed. qory reads it again before each request, so an issuer that refreshes it keeps the run going. A file whose mode grants the group or others read or write is refused. Instead: --run-credential-fd, or QORY_RUN_CREDENTIAL_SECRET. A flag never holds the credential itself. |

`qory config` lists each value; with no gateway named it shows
`session.gateway.url  (none: qory run starts a gateway for each run)  default`.

### The run credential

`qory run` takes the run credential from `--run-credential-fd`, else
`QORY_RUN_CREDENTIAL_SECRET`, else the file `session.gateway.run_credential_file` names,
relative to the directory of `forager.yaml` unless it is absolute.

- `--run-credential-fd`: read the run credential from this open file descriptor, for a
  machine whose runs go through a gateway (forager.yaml:
  session.gateway.run_credential_file). qory reads it until the run ends, and no program
  qory starts inherits it.
- `QORY_RUN_CREDENTIAL_SECRET`: on a machine whose runs go through a gateway, the run
  credential its issuer signed. qory takes it out of its environment when it starts; no
  program qory starts receives it. `--env` and `wall.env` refuse it, as they refuse the
  access key's variables.
- The file is read again before each request. `--run-credential-fd` is a stream: whoever
  starts `qory run` keeps the descriptor open and writes each fresh run credential to it
  as a new line, and qory uses the latest complete line it has read; a partial line waits
  for its newline. A writer that closes the descriptor after a single credential needs no
  newline. `QORY_RUN_CREDENTIAL_SECRET` is read once, when `qory run` starts, so a
  credential from it can't be renewed during the run; a run longer than its credential
  needs `session.gateway.run_credential_file` or `--run-credential-fd`. When the
  credential comes from the file, a file that isn't there or can't be reached, a
  directory, and a regular file qory can't open are refused before the run starts; a
  named pipe or another special file isn't opened before the first request. A file
  whose mode grants the group or others read or write is
  refused before the run starts, and again at each read. A walled run whose mounts hold
  it is refused before it starts, as a mount of Forager's own files is.

An unwalled run's agent runs as you. It can read what qory started with, its environment
and your files included: the run credential's file, and `QORY_RUN_CREDENTIAL_SECRET`,
which qory takes out of the environment the programs it starts receive, but not out of
the one it started with. No program qory starts inherits `--run-credential-fd`, but on
Linux a program running as you can open another's descriptors through `/proc`: such an
agent can open qory's, or the writer's when the writer runs as you, and read the run
credentials written to it, which qory then doesn't get. On Linux, then, no source of the
run credential is out of an unwalled agent's reach; only a wall, or running the agent as
another user, keeps it out. On macOS, a program can't open another's descriptors that
way. Give the descriptor a pipe, not a file the agent could open.

The run credential stays in qory's memory for the whole run. Where your programs may
debug one another, as on Linux with `kernel.yama.ptrace_scope` 0, an unwalled agent
running as you can read it there.

The run credential never appears in qory's output or in the run's record. qory reads its
`exp` only to say when it expired.

The run's first line names the gateway and the run:

```text
qory run: through the gateway gateway.example:8443, run 0192…
```

The run's labels are the run credential's: the gateway sets them. qory still sends the
checkout's forge and repository, and the gateway refuses a run whose checkout is not the
credential's target. A key of `--details` that the credential decides must hold the
credential's value. `--title`, `--kind`, `--subject` and the other keys of `--details`
are the run's own.

### What is refused

Before anything starts, `qory run` refuses:

- `<file>: gateway: this machine's runs go through the gateway session.gateway.url names,
  so it runs no gateway, and its file holds none: Qory Apiary's access key and the
  credentials' secrets belong on the gateway's machine. Remove the gateway section, or
  remove session.gateway to run the gateway here`
- `<access-key-secret path> exists, and this machine's runs go through the gateway
  session.gateway.url names: a machine behind a gateway holds no access key; remove the
  file`
- `<variable> is set, and this machine's runs go through the gateway session.gateway.url
  names: a machine behind a gateway holds no access key; unset it`, for
  `QORY_ACCESS_KEY_ID`, `QORY_ACCESS_KEY_SECRET` and `QORY_APIARY_PUBLIC_KEY`, and in the
  same words for `--access-key-secret-fd`.
- `--local runs with a gateway of this run's own and no server, and this machine has no
  gateway section: its runs go through the gateway session.gateway.url names`
- `--label works only when this machine runs its own gateway; behind a gateway the run
  credential sets the labels`
- `--policy is the run's own policy for a gateway the run starts itself, and this
  machine's runs go through the gateway session.gateway.url names`
- `<file>: session.gateway.ca_file <path>: <err>`, and `<file>: session.gateway.ca_file
  <path> holds no PEM certificate`
- `this machine's runs go through the gateway session.gateway.url names, and there is no
  run credential: set session.gateway.run_credential_file, --run-credential-fd or
  QORY_RUN_CREDENTIAL_SECRET`
- `<file>: session.gateway.run_credential_file <path>: <err>`, for the run credential's
  file when the credential comes from it and it isn't there or can't be reached, is a
  directory (`<err>` is then `is a directory`, as for `ca_file`), or is a regular file
  qory can't open.
- `<path> is mode <mode>, which grants access to the group or others: chmod 600 <path>`,
  for the run credential's file whose mode grants the group or others read or write.

The gateway refuses a run, and qory says:

- `the gateway refused this run credential`, whatever the gateway found wrong with it;
- `this checkout is <forge>/<repository>, and the run credential is for
  <forge>/<repository>`;
- `--details <f> sets requester to "a", and the run credential says "b": the credential
  decides; remove the key`, for each such key.

A run the gateway ends because its run credential expired says when, by where the
credential came from:

- from its file: `the run credential expired at <time>, and its file holds no fresh one`;
- from `--run-credential-fd`: `the run credential expired at <time>, and the descriptor
  gave no fresh one`;
- from `QORY_RUN_CREDENTIAL_SECRET`: `the run credential expired at <time>;
  QORY_RUN_CREDENTIAL_SECRET is read once, so a run longer than its credential needs
  session.gateway.run_credential_file or --run-credential-fd`.

A run the gateway ends because the credential's issuer reports that the run has ended
says `qory run: the gateway ended the run: the run credential's issuer reports that the
run has ended`. Either end fails the run, exit 1.

### Resending through the gateway

`qory run resend <run-id>` sends the gateway what it did not accept of the run's record:
after a session that died, or a gateway that was out of reach. It reaches the gateway as
`qory run` did, with a run credential of the run, from `--run-credential-fd`, else
`QORY_RUN_CREDENTIAL_SECRET`, else the file, and refuses before anything is sent what
`qory run` refuses. The run directory records what the gateway accepted, and only the rest
is sent. It doesn't close the record: the gateway ends a run whose session it lost. It
removes the containers and networks the run's wall left, as without a gateway.

- `<n> events were accepted; nothing is left to send to the gateway`: exit 0.
- `the gateway never opened run <id>, so there is nothing to send; its record stays in
  <dir>`: exit 0, for a run the gateway never opened, one it refused at its start, say.
- `<n> events were accepted and <m> were not; <dir>/undelivered contains them`: the
  gateway didn't accept them within `--wait`.
- `the run credential expired at <time>, so the gateway takes no more of this run's
  events; they stay in <dir>`.
- `the gateway refused this run credential`, and `the gateway refused this run
  credential: it differs from the one the run started with`.
- `the run credential's issuer reports that the run has ended, so the gateway takes no
  more of this run's events; they stay in <dir>`; `the gateway ended the run with the
  reason <reason>, so it takes no more of this run's events; they stay in <dir>`; `the
  server closed the run, so the gateway takes no more of this run's events; they stay in
  <dir>`.

Each but the first two is exit 1, and the events stay in the run directory. A run that ran
with a gateway of its own on this machine is refused: its record goes to the server, so
resend it with a `forager.yaml` that defines the server and no `session.gateway`.

## Runs started by another system

A system that starts runs of its own sets their id and labels, and says what each is
about. It passes each run its policy:

```sh
qory run --run-id "$uuid" --label run_key=1234 --label issue=77 \
  --policy /etc/qory/policy.yaml --timeout 5h30m --stop-grace 30s -- -p "$prompt"
```

- `--run-id` is a new UUID, in lower case, for each attempt, a retry included. A caller
  that has only a key of its own for the work, such as `run_key` here, passes it as a
  label, never as the run id.
- The labels go into `dev.qory.run.started`, and onto the run configuration request.
  There, a server ties the run to its own records and chooses its policy.

Two labels come from the checkout's origin remote, unless `--label` sets them:

| Label        | Value                                                    | For `git@github.com:acme/shop.git` |
| ------------ | -------------------------------------------------------- | ---------------------------------- |
| `forge`      | the remote's host                                        | `github.com`                       |
| `repository` | the remote's path, without the leading slash and `.git`  | `acme/shop`                        |

A checkout with no remote, or with a remote on this machine, has neither label.

### What a run is about

Labels identify a run to a system. Four flags describe it to the people who read its
record:

```sh
qory run --kind review --title "Review the parser change" \
  --subject type=ticket,ref=7,url=https://tickets.example.com/7 \
  --subject "type=ticket,ref=8,title=Parser drops the last line, sometimes" \
  --details details.json -- -p "$prompt"
```

| Flag        | What it is                                               | At most                             |
| ----------- | -------------------------------------------------------- | ----------------------------------- |
| `--kind`    | what kind of run it is, a word of yours such as `review` | 64 bytes                            |
| `--title`   | the run's title                                          | 256 bytes                           |
| `--subject` | what the run works on; repeatable                        | 16 subjects                         |
| `--details` | a JSON object of your own, from a file or stdin          | 8192 bytes compacted, 4 levels deep |

Each is optional. An empty `--kind`, `--title` or `--details` is none. They go into
`about` on `dev.qory.run.started`, and no other event repeats them. The run configuration
request does not carry them, so a server does not choose the run's policy by them. qory
reads none of them from the checkout or the environment: the run carries what the flags
say.

A subject is `type=<type>,ref=<ref>`, then `url=<url>` and `title=<title>` when you have
them:

- `type`: a word or words of yours, such as `ticket` or `pull request`: `a-z` and `0-9`,
  each word joined to the next by one space, `_`, `.` or `-`, at most 64 bytes. Quote a
  type with a space: `--subject "type=pull request,ref=42"`.
- `ref`: what the subject is called where it lives, such as `7`. 1 to 256 bytes.
- `url`: an absolute `http` or `https` URL without a user name or password, at most 2048
  bytes. Write a comma in it as `%2C`.
- `title`: at most 256 bytes. It takes the rest of the value, commas and `=` included, so
  it comes last, and a script passes a title as it is:
  `--subject "type=ticket,ref=$number,title=$title"`.

No two subjects have the same type and ref.

`--details` reads the file it names, relative to the working directory, or stdin for `-`.
It holds one JSON object: at most 8192 bytes once compacted, where `<`, `>` and `&` count
six bytes each, keys of 1 to 64 bytes, and at most 4 levels deep, the object itself the
first; an array is a level too. `{}` is no details. With `-`, qory reads stdin to its end
before the agent starts, so the agent's stdin is empty; at a terminal, `-` is refused.

Text in all four is UTF-8 without control characters. A value outside these limits is an
input error, exit status 2, and the run does not start. The run above starts with:

```json
"about": {
  "kind": "review",
  "title": "Review the parser change",
  "subjects": [
    {"type": "ticket", "ref": "7", "url": "https://tickets.example.com/7"},
    {"type": "ticket", "ref": "8", "title": "Parser drops the last line, sometimes"}
  ],
  "details": {"queue": "nightly", "attempt": 2}
}
```

### A run's own policy

`--policy` passes one run's own policy. It is in the Forager contract's format. Keep it
outside the checkout. It suits a machine that serves runs of different kinds.

It only narrows. `gateway.egress` of the machine's `forager.yaml` decides how:

| `gateway.egress` in `forager.yaml` | What the run reaches                            |
| ---------------------------------- | ----------------------------------------------- |
| mode `enforce`                     | the policy file's hosts that the section covers |
| mode `observe`, or no section      | the policy file, as it is                       |

The `deny` lists of both apply either way.

With a server, `--policy` is refused unless `--local` is given. `gateway.egress` of
`forager.yaml` narrows the `security_policy` of the server's run configuration:

- The mode is `enforce` when either side sets it.
- Under `enforce`, a host passes only when the allow list of every side under `enforce`
  covers it.
- A host either side's `deny` covers is denied, in either mode.

`--local` keeps `--policy`, and the server is not contacted.

The access key's secret stays Forager's: see [The access key and the
instance](#the-access-key-and-the-instance).

## Credentials the agent never has

Behind a wall, a run needs no credential inside the container. `forager.yaml` defines the
credentials the machine has, under `gateway.credentials`, each by its name. A credential's token
comes from one of three places:

- a variable of `qory run`'s environment, `env`,
- a file, `file`,
- an adapter, `adapter` (see [Adapters](#adapters)).

A run's policy selects among them by name, with an argument for an adapter, such as a
repository. A policy defines no credential of its own.

```yaml
# ~/.config/qory/forager.yaml
gateway:
  credentials:
    model:                                  # a token from qory run's environment
      env: CLAUDE_CODE_OAUTH_TOKEN
      hosts: [api.anthropic.com]
      auth: {scheme: bearer}                # or basic with a username, or header with a name
      placeholders: [CLAUDE_CODE_OAUTH_TOKEN]
    product:                                # a token from an adapter of yours
      adapter: [/opt/adapters/code-host, --repo, "${argument}"]
      argument: '[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+'
```

```yaml
# the run's policy, passed with --policy
version: 1
egress:
  mode: enforce
  allow: [api.anthropic.com, git.example.com, api.git.example.com]
credentials:
  - name: model
  - {name: product, argument: acme/shop}
```

How it works:

- Forager's gateway keeps each token outside the container.
- Its proxy sets the token on the requests to the hosts the token is for.
- Where a program wants a credential set, the container gets a placeholder. It never gets
  the token.

A run whose policy selects a credential needs a wall; without one it does not start.

### Adapters

An **adapter** is a program of yours, written for one kind of host, such as a source code
host. It runs outside the container. It prints:

- the token,
- the token's expiry,
- the hosts, the scheme and the paths the token is for.

So `qory` defines no host of its own. `argument` is the pattern the policy's argument must
match whole, and the gateway puts the argument where the adapter's words say
`${argument}`. `hosts` and `paths` on an adapter's entry are the most its answer may claim.
An [integration](#integrations) is an adapter that describes itself.

### The model credential

Claude Code reads its model credential from `CLAUDE_CODE_OAUTH_TOKEN`, a token from
`claude setup-token`, or from `ANTHROPIC_API_KEY`. A walled run gets it one of two ways:

- **Passed in.** `wall.env` or `--env` names the variable, and the agent holds the
  credential inside the container.
- **Kept outside.** A credential with the variable as a placeholder, which the run's
  policy selects:

```yaml
# ~/.config/qory/forager.yaml
gateway:
  credentials:
    model:
      env: CLAUDE_CODE_OAUTH_TOKEN          # read once at run start
      hosts: [api.anthropic.com]
      auth: {scheme: bearer}                # for ANTHROPIC_API_KEY: {scheme: header, header: x-api-key}
      paths: [/v1/*]
      placeholders: [CLAUDE_CODE_OAUTH_TOKEN]
```

- Inside the container, the placeholder's variable holds a placeholder,
  `qory-sets-the-credential-outside-the-enclosure`. The runtime's other declared and
  reserved variables are empty there.
- With `paths: [/v1/*]`, under `enforce`, a request to `api.anthropic.com` outside `/v1/`
  is refused. Under `observe`, it is sent on without the token, and recorded.
- A run that passes a value for a placeholder does not start, `placeholder_conflict`. So
  once a credential keeps the model credential outside, `wall.env` and `--env` no longer
  name its variable.

### A static key

An API that takes a static key gets a credential with `env` or `file`:

```yaml
# ~/.config/qory/forager.yaml
gateway:
  credentials:
    tracker:
      file: /home/dev/.config/qory/tracker.key   # read at each use
      hosts: [api.tracker.example.com]
      auth: {scheme: header, header: X-Api-Key}
      paths: [/v2/*]                        # optional: the requests the key is set on
      placeholders: [TRACKER_KEY]           # optional
```

- `env` names a variable of `qory run`'s environment, read once at run start. `file` is
  an absolute path, read at each use, so whatever rotates it needs to tell no one.
- `hosts` is required: the hosts the key is set on.
- `auth.scheme` is `bearer`, `basic` with `username`, or `header` with `header`, the
  header's name.
- `paths`, when set, are where on its hosts the key goes. Under `enforce` they are also
  the run's whole reach on those hosts. Without them, the key goes on every path.
- Each of `placeholders` is a variable that holds a placeholder inside the container.
- `env` and `file` take no argument; an argument is an adapter's.
- A walled run that passes into the container the variable an `env` credential reads does
  not start, `variable_reserved`.

### TLS on credential hosts

For the hosts of the credentials the policy selects and the hosts with path rules, and no
other, the proxy ends the container's TLS itself. It uses an authority made for the run.
The authority's key never leaves the gateway.

The container receives one bundle to trust: its image's own authorities, and the run's
certificate. The bundle goes in through these variables:

- `SSL_CERT_FILE`
- `GIT_SSL_CAINFO`
- `NODE_EXTRA_CA_CERTS`
- `REQUESTS_CA_BUNDLE`
- `CURL_CA_BUNDLE`
- `AWS_CA_BUNDLE`

Or through the variables `wall.ca_env` lists.

The record lists the terminated hosts. For each request to one, it lists the method, the
path and `credential`, the name of the credential whose token it carried.

Every other host stays a tunnel that nobody reads.

## Integrations

An **integration** is a program that connects a run to an outside system, and describes
itself. It gives a run a credential through its credential role: a token minted for the
run, which the agent must not hold. The program runs on your machine, outside the wall.
The agent never holds its secrets, or the token it mints.

- Qory publishes its own, each in a repository of its own, such as
  [`qory-github`](https://github.com/qoryai/qory-github). Its integration is named
  `github`.
- A program of yours starts from the
  [integration template](https://github.com/qoryai/integration-template).

Both are declared and checked the same way. The rules a program follows are the
[integration
contract](https://github.com/qoryai/integrations/tree/main/contracts/integration/v1).

Declare an integration in `forager.yaml`, under `gateway.integrations`, with its `program`
and its `settings`. A run's policy selects its credential by the key, as
`{name: github, argument: acme/shop}`. Before the run, `qory` runs `<program> describe`.

### What an integration describes

`<program> describe` prints the integration's description: one JSON document. It takes
no settings and reaches no network.

| Field | What it is |
|---|---|
| `name` | the integration's name, such as `github`: the key a machine declares it under, unless the machine chooses another |
| `title`, `description` | text for a listing or a form |
| `program_version` | the program's own version, a string. `qory run` and `qory config` show it |
| `settings` | a JSON Schema, draft 2020-12, of the settings the program takes. A property marked `writeOnly` is a secret: `qory` refuses its value, and takes `<name>_file` in its place, the path of a file that holds it |
| `roles` | the roles it plays, each under its role's name, such as `credential`. `qory` expands the `credential` role and leaves the others as they are. An integration that plays no role `qory` expands is refused |

The **credential** role mints or fetches a token for the run's argument. `argument`,
required, is the RE2 pattern the policy's argument must match whole. `hosts` are the most
hosts the token may be set on.

The description of `github`, shortened. Its argument is one repository or several of one
owner, separated by commas:

```json
{"version": 1, "name": "github", "title": "GitHub", "program_version": "0.1.0",
 "settings": {"type": "object", "additionalProperties": false, "required": ["app_id"],
   "properties": {
     "app_id": {"title": "App id", "type": ["integer", "string"]},
     "private_key": {"title": "Private key", "type": "string", "writeOnly": true},
     "private_key_file": {"title": "Private key file", "type": "string"},
     "permissions": {"title": "Permissions", "type": "object",
                     "additionalProperties": {"enum": ["read", "write"]}}}},
 "roles": {"credential": {"argument": "[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}(,[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100})*",
                          "hosts": ["github.com", "api.github.com"]}}}
```

### The integrations entry

`forager.yaml` lists the machine's integrations under `gateway.integrations`, each under a
key:

```yaml
# ~/.config/qory/forager.yaml
gateway:
  integrations:
    github:                                 # qory-github, found on the PATH
      settings:
        app_id: 123456
        private_key_file: /etc/qory/github-app.pem
        permissions: {contents: write, pull_requests: write}
    tracker:                                # a program of yours, by its path
      program: /opt/acme/bin/acme-tracker
      settings: {url: https://tracker.acme.example}
```

- The key is 1 to 64 of `a-z`, `0-9`, `_` and `-`, starting with a letter or a digit. It
  is the name of the credential the integration defines.
- `program` is the program's absolute path, or its name on the `PATH`. It is
  `qory-<key>` when absent.
- `settings` is the settings document. It is `{}` when absent.

Any other key of an entry is refused.

#### What `qory` does with a declaration

Before a run, `qory` runs `<program> describe`. The program prints:

- its description,
- the settings it takes, as a JSON Schema,
- the roles it plays.

`qory` checks the settings against that schema.

The credential role defines a credential. Its name is the declaration's key. It is as if
the file contained:

```yaml
gateway:
  credentials:
    github:
      adapter: [/usr/local/bin/qory-github, credential, --settings, '{"app_id":123456,"private_key_file":"/etc/qory/github-app.pem","permissions":{"contents":"write","pull_requests":"write"}}', --, "${argument}"]
      argument: '[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}(,[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100})*'
      hosts: [github.com, api.github.com]
```

A policy selects it by the key: `{name: github, argument: acme/shop}`.

#### Which program runs

- `program` sets the program's absolute path, or its name on the `PATH`.
- Without `program`, the program is `qory-<key>` on the `PATH`.
- On a machine whose `PATH` is not its owner's alone, set `program` to each program's
  absolute path.

`qory run` prints the program it found, and its version, on a line of its own:

```
qory run: integration github: /usr/local/bin/qory-github 0.1.0
```

#### Where a program may live

`qory` runs only a program the run cannot write. The program must be:

- outside the checkout, and
- outside every mount the wall makes. A mount that is, contains or lies inside the
  program's directory stops the run, `mount_contains_forager_files`.

A mount that is or contains the file of a `<name>_file` setting, or a link on the way
to it, stops the run the same way, `mount_contains_forager_files`, for each integration
the run describes: the ones its policy selects, every one when a server supplies the
policy.

`qory` judges a program by where its links lead: a `program` that is a link is judged by
where it resolves.

The rule applies to each run as it starts. A program written into a directory while that
directory was mounted is judged by where it is, on every run that follows. So keep a
program's directory out of the mounts.

#### Who may own and write it

One rule covers:

- the resolved file,
- every directory above it, up to `/`,
- every directory above each link on the way.

The rule:

1. Root, or the user running `qory`, owns each of them. The same holds for each link on
   the way.
2. Other users may write none of them.
3. A group may write one when the group is root's (gid 0), `wheel` or `admin`. It may
   also when the group is the owner's primary group and has the owner's name.

A directory that root owns with the sticky bit set, such as `/tmp` or `/nix/store`, keeps
the rule. A default Homebrew install keeps it. So does a `~/go/bin` of a user's private
group.

#### Settings and secrets

The gateway hands the credential role its settings on its command line, and nothing on
standard input. It starts the role as:

```sh
<program> credential --settings <json> -- <argument>
```

- `<program>` is the program's absolute path, its links resolved.
- `<json>` is the integration's `settings` in `forager.yaml`, whole, as compact JSON in
  the file's order, every `$` written `\u0024`. It is `{}` when there are none.
- `--` is always there, with exactly one argument after it: the argument the run's
  policy gives the credential, the empty string when it gives none.
- Standard input is empty. The environment is the one `qory` runs in, without the
  access key's variables.

A command line is no place for a secret, so `qory` refuses a secret's value in the
settings before the run starts, and names the `<name>_file` to set in its place: the
path of a file that holds the secret, which the program reads itself.

The credential role of `github` above starts as:

```sh
/usr/local/bin/qory-github credential --settings '{"app_id":123456,"private_key_file":"/etc/qory/github-app.pem","permissions":{"contents":"write","pull_requests":"write"}}' -- acme/shop
```

`qory` checks the settings against the description's schema before the run. The error
names the setting and the rule, never a value.

#### A name `gateway.credentials` defines too

A name `gateway.credentials` defines itself belongs to that section. Then:

- `qory run` and `qory config` print a line of their own that says so, such as:

  ```
  qory run: forager.yaml: gateway.credentials.github defines the credential github, and gateway.integrations.github defines none
  ```

- `qory config` describes the integration and lists it as shadowed,
- a run leaves it undescribed.

#### Which integrations are described

- A run whose policy is on this machine describes the integrations its `credentials`
  select.
- A run whose policy the server supplies describes every one.
- `qory gateway` describes every one: Qory Apiary supplies every run's policy.
- `qory config` describes every integration, and lists what each defines.

### What stops a run

Each of these stops the run before the agent starts:

| Who checks | What stops the run |
|---|---|
| `qory` | the program is not found: an absolute `program` this user may not run, or a name that is not on the `PATH` |
| `qory` | the program is inside the checkout or a read-write mount of the wall, judged by where its links lead |
| `qory` | the program, a directory above it or a link on the way breaks [Who may own and write it](#who-may-own-and-write-it) |
| `qory` | `describe` does not answer within 10 seconds, fails, or prints a description the integration contract refuses |
| `qory` | the description refuses the settings, a secret's value among them |
| `qory` | the integration plays no role `qory` expands |
| Forager | the policy selects a credential the machine does not define |
| Forager | the policy selects a credential, and the run has no wall |
| Forager | the policy's argument does not match the credential's `argument` whole |
| Forager | two credentials claim the same host |
| Forager | under `enforce`, a host of a credential that the allow list does not cover |
| Forager | the adapter does not answer within a minute, fails, or prints a document the Forager contract refuses |
| Forager | the adapter's answer claims a host or a path above the entry's `hosts` and `paths` |
| Forager | the run passes a value for a placeholder, `placeholder_conflict` |

`qory run` prints why the run did not start. The
[Forager contract](https://github.com/qoryai/forager/tree/main/contracts/forager/v1) has
every code.

### The credential role

The gateway starts `<program> credential --settings <json> -- <argument>` before the
agent, outside the wall, for each integration credential the run's policy selects. The
program answers with the token, its expiry, and how the token is set: the hosts, the
scheme and the paths. It may name placeholders.

- The token stays with the gateway. Its proxy sets it on the requests to the answer's
  hosts and paths.
- Each variable the answer names as a placeholder holds a placeholder inside the
  container, such as `GH_TOKEN` and `GITHUB_TOKEN` for `github`. So `git` and `gh` start,
  and send the placeholder. The proxy replaces it.
- Five minutes before the token expires, the gateway runs the role again. It also does
  when a host answers `401` to a request it set the token on, at most once every thirty
  seconds.
- A renewal that fails keeps the old token, and `qory run` prints
  `credential <name> was not renewed: …`.

Under `enforce`, a credential whose hosts the allow list does not cover stops the run
before it starts.

#### Paths

The answer's paths are where the token goes on its hosts. Under `enforce`, they are also
the run's whole reach on those hosts. The gateway refuses every other path there, another
organization's repository included.

`egress.paths` in a policy limits a host to paths as well, with or without a credential.

Under `enforce`:

- A path outside the answer's paths is refused.
- On a host with both the answer's paths and `egress.paths`, a path passes only when it
  matches both lists.

Under `observe`:

- A path outside the answer's paths is sent on without the token, and recorded.
- On a host with both lists, the rest is sent on and recorded. The token goes only where
  the answer's paths match.

In either mode, a path that could be read two ways is refused on these hosts, such as one
with an encoded slash.

### See an integration

`qory config` describes every integration, as a run does. An entry whose program does not
describe is an error. It lists:

- `gateway.integrations.<key>`: the program's path and its version, `<path> <version>`,
  with `, shadowed by gateway.credentials.<key>` when `gateway.credentials` defines the
  name too;
- `gateway.credentials.<key>`: `integration <key>`, for the credential the integration
  defines.

A run's record shows what each credential did:

- `dev.qory.run.policy_applied` lists under `credentials` each credential the run holds:
  its `name`, `hosts` and `scheme`, and its `argument` and `paths` when it has them. It
  never lists a token.
- `dev.qory.run.egress` names the `credential` whose token a request carried.

## Resending a run's record

`qory run resend <run-id>` sends a run's record to the server `forager.yaml` defines. It
is for a run whose Forager process died, or whose server was away. End a job with it, whatever
happened before it. Behind a gateway, it sends the record to that gateway: see [Resending
through the gateway](#resending-through-the-gateway).

The run is selected by its id: its folder in this checkout's run records, as [The
record](#the-record) names it. The
server's configuration is fetched first, signed. It defines where the events go.

- The run directory records what the server accepted. Only the rest is sent, in order.
  Nothing the server accepted is sent again.
- A server may still see an event twice. It discards the copy by the event's id.
- After a Forager process that died, it first closes the record: `dev.qory.run.exited`
  with `reason: gateway_lost`. It also removes the containers and networks the run's wall left.
- It refuses a run that is running.
- It keeps sending until the server accepts, or `--wait` is over. The wait is two
  minutes unless set.

The exit status is 0 when the server has everything. It is 1 when events remain. Those
stay under the run directory's `undelivered`.

The formats are in the
[Forager contract](https://github.com/qoryai/forager/tree/main/contracts/forager/v1).

## The wall

The proxy sees only programs that honour it. A **wall** makes the rest fail.

Turn it on with a `wall` section, or with `--wall docker --image <image>` for one run.
`--wall none` runs one run without the section's wall. With a wall:

- The runtime starts in a container, on a network with no route out.
- It reaches the proxy, and nothing else, through a relay.
- The container sees the checkout and the composed home, at their own paths, and the
  run's own record directory, read-only. It sees nothing else of your machine.
- Of your environment, the container gets the ones `wall.env` or `--env` lists, and
  nothing else. The harness's launch variables, fixed and defaults, reach the agent in
  the container as they do outside it. See [A run's variables](#a-runs-variables).
- Forager, the policy, the record and the access key's secret stay outside.

A wall needs the `docker` command, and an engine behind it. It also needs an image that
contains the runtime: Qory's, or yours FROM it. See [Qory's images](#qorys-images).
`wall.images` defines several, and a run's policy selects one: see [The agent's
images](#the-agents-images).

What to know:

- **The model credential.** `wall.env` or `--env` passes it in, and the agent holds it.
  A credential with its variable as a placeholder keeps it outside: see [The model
  credential](#the-model-credential). A run that then passes a value for that variable
  does not start, `placeholder_conflict`. A subscription login kept in a Mac's Keychain
  does not reach a container. Use an API key, or a token from `claude setup-token`.
- **The helper on a Mac.** Inside the container, the relay, the hook forwarder and what
  starts an image's own Docker are `qory`'s own Linux build, mounted read-only. On Linux, that is the binary you run. On a Mac, download the
  Linux archive of the same release, for your engine's architecture. Set `wall.helper` to
  that binary.
- **What the container sees.** The checkout it was started in, and the run's own record
  directory, read-only, and no other directory. `--mount <path>[:ro]` or `wall.mounts` shows it another one, at its own path, such as a
  sibling checkout the session reads. A socket is never mounted. A mount is refused
  before the run starts, `mount_contains_forager_files`, when it is, contains or lies
  inside one of these:
  - this machine's qory configuration directory, which holds `forager.yaml`, the access
    key and the user `qory.yaml`;
  - a file qory reads from that directory and a link takes elsewhere: where the last
    link leads, and, for a mount that is or contains it, every link on the way;
  - qory's state directory, which holds the run records, and, for a mount that is or
    contains it, every link on the way to it;
  - the file of a `<name>_file` setting of an integration the run describes, where its
    path leads, and, for a mount that is or contains it, every link on the way to it;
  - one of Forager's own program and temporary files, such as a program it starts
    outside the wall.

  For a mount that is or contains the access key, `qory run` says:

  ```
  qory run: the mount <host path> contains <dir>, which holds this machine's access key; the agent could read the key, so the run does not start. Mount a narrower path
  ```

  For any other, it says:

  ```
  qory run: the mount <host path> contains <path>, which holds one of Forager's files; the agent could change it, so the run does not start. Mount a narrower path
  ```

  For a read-only mount, it says `the agent could read it` instead.

  For the state directory, it says:

  ```
  qory run: the mount <host path> contains <dir>, which holds qory's run records; the agent could change them, so the run does not start. Mount a narrower path
  ```

  For a read-only mount of the state directory, it says `the agent could read them`
  instead.

  For a link on the way to the state directory, to a file of the configuration
  directory or to a `<name>_file` setting's file, it names the link, which leads to
  them:

  ```
  qory run: the mount <host path> contains <link>, which leads to qory's run records; the agent could point it elsewhere, so the run does not start. Mount a narrower path
  qory run: the mount <host path> contains <link>, which leads to one of Forager's files; the agent could point it elsewhere, so the run does not start. Mount a narrower path
  ```

  For a read-only mount, it leaves out `; the agent could point it elsewhere`.

  When the path is the checkout or the working directory, it says `the workspace <path>`
  instead of `the mount <path>`.
- **Two walled runs at once.** Two walled runs can share a checkout, or a mount of the
  same path. While another walled run is going, or its containers were left behind,
  Forager refuses a place that lies inside, or is reached through, a writable place of
  that run's, and a writable place that holds one of that run's,
  `mount_shared_with_run`; a place that is the same path as one of that run's is not
  refused. The refusal names the other run: once it has ended, remove the containers
  `docker ps --all --filter label=dev.qory.run=<id>` lists, and it no longer counts. While both are going, each agent sees what the
  other writes there, and changes it where its own place is writable: the two can clash
  on data, but neither gets a way out of its wall. A read-only mount of a path another
  run has writable is accepted too, so what it shows can change during the run.
- **Git in a worktree.** In a git worktree, the repository's data lives in the main
  checkout, outside the worktree. So git inside the container works there only with that
  directory mounted. A clone works as it is.
- **Limits.** `--cpus`, `--memory`, `--pids-limit` and `--shm-size`, or the keys of those
  names under `wall`, limit what the container uses. Sizes are written the way Docker
  writes them: a number and `b`, `k`, `m` or `g`, in either case. So `14g` is 14 GB and
  `2g` is 2 GB; `14GB` or `14GiB` is refused. A headless browser wants `--shm-size 2g`:
  an engine's default `/dev/shm` is 64 MB.
- **Your own machine.** Behind a wall, the proxy reaches your own machine only for a host
  that the policy's `egress.allow` lists itself, in either mode. It never reaches the
  cloud metadata address. For a local model endpoint or MCP server, list your machine's host name, and
  point the harness at that name. `localhost` inside the container is the container.
- **Hooks in a virtual machine.** With the engine in a virtual machine, as on a Mac, the
  runtime's hooks do not reach the session. So a walled run there has no hook events. The
  log, the egress record and the structured output are there. On a Linux host, the hooks
  cross.

### Qory's images

`qory`'s repository defines four images under `images/`, for `linux/amd64` and
`linux/arm64`. They are not published: you build them, by hand as below, and a workflow
of the repository builds and checks them on a change. Build them from a checkout of
`qory` at the commit `qory version` prints, since each commit pins the runtime at the
version the Forager it builds against is written for:

```sh
git clone https://github.com/qoryai/qory && cd qory
git checkout <commit>                    # the commit qory version prints
docker build -t qory-agent images/agent
docker build -t qory-agent-docker --build-arg BASE=qory-agent images/agent-docker
docker build -t qory-agent-go --build-arg BASE=qory-agent images/agent-go
docker build -t qory-agent-go-docker --build-arg BASE=qory-agent-docker \
  --build-arg TITLE=agent-go-docker images/agent-go
```

| Image | What it holds |
|---|---|
| `agent` | Claude Code at the version Forager's descriptor is written against, `git`, `gh`, Node, the system's authorities |
| `agent-docker` | `agent`, and Docker's daemon and command |
| `agent-go` | `agent`, and Go |
| `agent-go-docker` | `agent-docker`, and Go |

Every version and every download's sum is a build argument, with its default in the
Dockerfile. A build checks each download against its sum, and installs Debian's packages
from the snapshot the base image was built from.

What each of them does:

- **Any user.** The container runs as your user, whom the image does not know. `HOME`
  is `/home/agent`, which every user may write.
- **No setuid program.** None is needed, and none is there.
- **Nothing on its own.** It has no entry point. Claude Code and `gh` neither update
  themselves nor ask for input, and Claude Code sends no telemetry or error reports,
  which a run under `enforce` would otherwise show as denied connections.
- **Docker.** `agent-docker` holds the daemon for a Docker of the agent's own, under a
  runtime such as Sysbox. The image starts nothing: define it in `wall.images` with
  `runtime: sysbox-runc` and `docker: true`, and `qory run nest` starts the daemon. See
  [A Docker of the agent's own](#a-docker-of-the-agents-own).
- **Go.** `agent-go` has Go at the version the image pins, with `GOTOOLCHAIN=local`, so a
  `go.mod` that asks for a newer one fails instead of downloading it. `GOPATH` and the
  caches are under `HOME`. It has no C compiler, so cgo is off.

To add what your agents need, build yours FROM one of them, and check it:

```dockerfile
FROM qory-agent
RUN apt-get update && apt-get install -y --no-install-recommends python3 \
 && rm -rf /var/lib/apt/lists/*
```

```sh
docker build -t my-agent:1 .
qory image check my-agent:1
```

Push it to a registry of yours and name it by digest, in `wall.image` or as a `ref` of
`wall.images`, so every run starts the same one.

`qory image check` checks an image against what the wall needs of it. With no image, it
checks `wall.image`. A name of `wall.images`, given or in `wall.image`, is read as that
image's `ref` first, as `--image` reads it. It prints a line per check, and exits 1 when
one fails.

- From outside: the engine holds the image, for the platform the engine runs; the image
  sets `HOME`; the reference is pinned by digest or not, which fails nothing. Then it
  reads every file of the image, as `docker export` writes them, and fails a file that
  is setuid or setgid or has capabilities of its own.
- From inside: it starts the image the way the wall starts the agent, as a user the image
  does not know, with no capability and no network. `qory`'s Linux build then checks
  that `HOME` takes a file from that user, the authorities are where the wall reads
  them, `/bin/sh` is there for the runtime's hooks and the session's API-key approval,
  `claude` is the version Forager's descriptor is written against, and `git` and
  `gh` run. It reports whether `dockerd` is in a system directory, and checks that what
  the daemon runs is there too.

The Linux build is the one the wall uses: `wall.helper`, for your engine's architecture,
or on Linux the `qory` you run when `wall.helper` is not set.

## The agent's images

A machine that serves several kinds of work defines several images in `forager.yaml`,
each by a name. A run's policy selects one by that name. A repository never names an
image.

```yaml
# ~/.config/qory/forager.yaml
wall:
  adapter: docker
  image: go                 # the default: a name below, or a reference
  images:
    go:
      ref: ghcr.io/acme/agent-go@sha256:…
    go-docker:              # experimental: a Docker of the agent's own
      ref: ghcr.io/acme/agent-go-docker@sha256:…
      runtime: sysbox-runc
      docker: true
```

```yaml
# the run's policy, from the server or passed with --policy
version: 1
egress:
  mode: enforce
  allow: [api.anthropic.com]
image: go-docker
```

- `ref` is the image. Pin it by digest to run the same image every time.
- `runtime` is the container runtime the wall starts the image under. Your engine must
  have it. Without it, the engine's default runs the image.
- `wall.image` sets the default, and `--image` sets another for one run: a name of
  `wall.images`, or a reference. A name is read as that image first.
- A policy's selection wins over the default, `--image` included. A run whose policy
  selects no image starts in the default.
- A run whose own `--policy` selects an image needs no default. A machine that reports
  to a server sets `wall.image`: the server's run configuration arrives once the run
  starts, and may select none.
- `qory config` lists the images, and shows whether `wall.image` is a name of
  `wall.images` or a reference. A mistyped name shows as a reference.

Before the run starts, `qory run` refuses a `--policy` that selects an image
`wall.images` does not define, or one for a run without a wall. A server's run
configuration that selects such an image stops the run as it starts. Every command that
reads `forager.yaml` refuses `docker: true` without a `runtime`.

`dev.qory.run.started` names the image the run started in: `image`, the reference. For an
image of `wall.images` it also has `image_name`, and `container_runtime` and
`docker: true` when they apply. `dev.qory.run.policy_applied` records `image` when the
policy selects one.

### A Docker of the agent's own

*Experimental: it may change.*

With `docker: true`, the agent gets a Docker daemon inside its container, never your
machine's. Its tests can start a database. It can build and run images.

Define an image with `docker: true` only where every run may get one: any policy can
select it.

It needs [Sysbox](https://github.com/nestybox/sysbox), registered with your engine as
`sysbox-runc`, and `runtime: sysbox-runc` on the image. Sysbox gives the container a root
of its own, a user of your machine that is not root. A Mac's own engine has no Sysbox:
run these images on a Linux machine that has it.

What the image holds:

- `dockerd`, and what it starts, `containerd`, `runc` and `iptables` among them, in
  `/usr/local/sbin`, `/usr/local/bin`, `/usr/sbin`, `/usr/bin`, `/sbin` or `/bin`. These
  directories are the daemon's whole `PATH`; the run's `PATH` is never searched.
  [Qory's](#qorys-images) `agent-docker` and `agent-go-docker` have them there, as does
  `docker:dind`. `qory image check` says whether an image does.
- The agent's user and group, in its `/etc/passwd` and `/etc/group`, when `wall.user`
  names the user by name, or by a uid with no gid.

How it works:

- The wall starts the container as its root, and `qory`'s helper runs there as that
  root. It starts `dockerd` on its Unix socket alone, then runs the agent as its user,
  with no capabilities.
- The daemon gets none of the run's environment but the proxy and, when the run has
  one, its certificate bundle. The agent gets the run's environment.
- The helper writes the agent's docker configuration, which gives the containers the
  agent starts the proxy, to `/run/qory/docker`: the agent's alone, mode `0700`, in a
  `/run/qory` that is root's, mode `0755`, so the agent's `docker` command reads it. A
  `/run/qory` already in the image is set to that owner and mode, and a link at either
  path stops the run. A `DOCKER_CONFIG` the run sets, and not empty, takes the place of
  this configuration, and must then set the proxy itself.
- The agent reaches the daemon's socket through its group. Whoever reaches the socket is
  the container's root: inside the container, and nowhere else.
- The containers the agent starts are inside the wall. A plain one, one on the host's
  network and a privileged one all reach the proxy and nothing else. What they reach is
  decided and recorded like the agent's own traffic.
- The daemon pulls through the proxy, so allow the registries it pulls from. For Docker
  Hub: `registry-1.docker.io`, `auth.docker.io`, and the hosts it sends the layers from,
  such as `production.cloudfront.docker.com`. The record lists the ones it was denied.
- The containers the agent starts do not trust the run's authority unless the agent mounts
  its bundle into them.
- Mount nothing your machine's root must protect: the container's root may reach the
  mounts as your machine's root.
