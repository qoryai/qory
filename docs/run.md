# Running a session

`qory run` starts your agent on its composed harness, inside the session runner of
[qoryai/runner](https://github.com/qoryai/runner). Every connection the agent makes goes
through a proxy on your machine. The session is recorded.

## What `qory run` does

It is the same launch as `qory harness launch` (see [the harness](harness.md)). Two things
are added:

- Every connection the runtime makes goes through a proxy on your machine, and is
  recorded.
- The session is written as events, beside its output. The events hold the session's
  output, what the runner observes, and what the runtime reports itself.

```sh
qory run                              # the composed runtime, at your terminal
qory run claude -- -p "Reply pong"    # one headless turn; arguments after -- go to the runtime
```

The runtime is the one the harness is composed for. When it is composed for several, the
first argument selects one. Arguments after `--` go to the runtime, after the launch
template's own.

qory computes the home itself, as compose does, and refuses a run whose harness report
names another: run `qory harness compose` again. The report fixes no variable and adds no
mount. Behind a wall, `harness.home` comes from your own `qory.yaml`: a checkout's
`qory.yaml` that moves the home refuses a walled run.

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

`qory run resend` sends a finished run's record to the server again: after a runner that
died, or a server that was away. See [Resending a run's record](#resending-a-runs-record).

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

A walled run of Claude Code takes its model credential from a connection, never from the
container's environment. Define the wall, the connection and what the agent may reach in
`~/.config/qory/runner.yaml`:

```yaml
apiVersion: qory.dev/v1alpha1
egress:
  mode: enforce
  allow: [api.anthropic.com]
wall:
  adapter: docker
  image: qory-agent
connections:
  - kind: runtime
    id: claude
    name: claude
    secrets: {oauth_token: {source: external, name: CLAUDE_OAUTH}}
secrets:
  local:
    CLAUDE_OAUTH:
      env: CLAUDE_OAUTH
      hosts: [api.anthropic.com]
```

```sh
export CLAUDE_OAUTH=...                  # from `claude setup-token`
qory run -- -p "/hello"
```

The agent greets you as before. What changed:

- Inside the container, `CLAUDE_CODE_OAUTH_TOKEN` is a placeholder.
- The proxy sets the real token on each request to `api.anthropic.com`.
- The record lists those requests with the connection's id, `claude`. It never lists the
  token's value.
- Everything else the agent tries to reach is denied and recorded.

From now on every run on this machine needs a wall, since `runner.yaml` has a
connection. See [Credentials the agent never has](#credentials-the-agent-never-has).

## runner.yaml

One optional file defines what the runner does on this machine:
`~/.config/qory/runner.yaml`. It lives beside your `qory.yaml`, and nowhere else. So a
repository cannot set it.

```yaml
# ~/.config/qory/runner.yaml
apiVersion: qory.dev/v1alpha1
egress:                  # what the runtime may reach; enforce denies the rest
  mode: enforce          # or observe: record everything, deny only what deny lists
  allow: [api.anthropic.com, "*.github.com"]
  deny: [gist.github.com]                # denied in either mode, whatever allow lists
server:                  # the server every run reports to; optional
  url: https://apiary.example           # a scheme and a host, nothing after
  access_key_id: ak_0123456789abcdef    # this machine's access key; its secret is not in this file
  apiary_public_key:                    # the server's key, which signs every answer
    - {alg: ed25519, public_key: mptNqtgGKgLhLZxmOGfpBQkdeBNH7QN3Qs9ETNumy8Q}
instance:                # optional
  name: build-01         # how the server shows this machine; the host name by default
wall:                    # start the runtime in a container; optional
  adapter: docker
  image: example.com/agent@sha256:…     # the runtime and your toolchain, FROM Qory's
  env: [NODE_ENV]                       # names; nothing else of your environment goes in
  memory: 14g                           # at most 14 GB of memory; also cpus, pids_limit, shm_size; optional
connections:             # what each credential comes from; optional
  - kind: runtime                       # the model credential, kept outside the container
    id: claude
    name: claude
    secrets: {oauth_token: {source: external, name: CLAUDE_OAUTH}}
secrets:                 # this machine's values, each with the hosts it may go to
  local:
    CLAUDE_OAUTH: {env: CLAUDE_OAUTH, hosts: [api.anthropic.com]}
run:                     # optional
  timeout: 5h30m         # stop a runtime that runs this long
  stop_signal: SIGINT    # requests it to stop; the runtime's descriptor's, else SIGTERM
  stop_grace: 30s        # between that signal and SIGKILL; 10s
```

Without an `egress` section, everything the runtime reaches is allowed and recorded. A `runner.yaml`
that does not read means no run.

## Runtimes

`qory run` runs whichever runtime the harness is composed for. Nothing in `runner.yaml`
is particular to one runtime.

The runner reads what it needs to know about a runtime from a **descriptor**. A
descriptor is a file of data, in the format of the [runner
contract](https://github.com/qoryai/runner/blob/main/contracts/runner/v1/README.md#the-runtime).
It says:

- how the runtime's hooks are installed,
- what its output means as events,
- which signal asks it to stop,
- which secrets it needs: see [The model credential](#the-model-credential).

The runner ships the descriptor for Claude Code. `~/.config/qory/runtimes/<runtime>.yaml`
describes another runtime, or replaces the one shipped.

A runtime that nothing describes still runs. The run, its log and its egress are
recorded. The session's own events are not.

## Hosts a module declares

A module declares the hosts it reaches, under `egress` in its manifest. The compose
unions them into the report, together with the hosts the runtime declares.

The runner reports them as `harness_hosts` in `dev.qory.run.policy_applied`, for a
receiver to compare with the policy. They decide nothing. The policy alone defines what
the runtime reaches.

## Time limits

A denied connection is recorded, and the session goes on. Only a time limit you set ends
a session.

Set it with `--timeout 5h30m`, or with `run.timeout`. When the limit is reached:

- the runtime is stopped,
- `dev.qory.run.exited` records the limit as the reason,
- `qory run` exits 124, as `timeout(1)` does.

`--timeout 0` lifts the limit `runner.yaml` sets.

### How the runtime is stopped

The runtime is stopped at the limit, or when `qory run` gets a signal. Then:

1. The runtime gets the stop signal: `--stop-signal` or `run.stop_signal`, else the one
   its descriptor sets, else `SIGTERM`.
2. After the grace time, it gets `SIGKILL`. The grace time is `--stop-grace` or
   `run.stop_grace`, 10s unless set. It is the time a session needs to close what it has
   open.

Runtimes differ in what a signal means. One closes its session on `SIGINT` and drops it
on `SIGTERM`. So the signal is yours to choose: `SIGTERM`, `SIGINT`, `SIGHUP`, `SIGQUIT`,
`SIGUSR1` or `SIGUSR2`.

`run.timeout`, `run.stop_signal` and `run.stop_grace` in `runner.yaml` set these for
every run on the machine.

## A run's variables

Several sources may set a variable of the agent's process. For each name, the run takes
the value of the highest source that sets it:

1. The values qory and the runtime fix: the runner's own names, `QORY_HARNESS_HOME`, and
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

The runner's deny list, names such as `PATH` and `DOCKER_HOST`, leaves out a value of
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

With a server configured, the runner starts by fetching the server's configuration. It
signs every request with the access key's secret, and checks every answer against the
server's key it pins, `apiary_public_key`. It does not start unless the server answers.

The configuration defines where the events go, and whether the server has a run
configuration. The runner fetches the run configuration with the run's labels, the
checkout's forge and repository among them. It reloads it when the server reports it
changed. It may hold:

- `security_policy`, the server's policy. The node's policy narrows it: see [A run's own
  policy](#a-runs-own-policy). Without it, the node's policy is the run's.
- `connections`, the run's connections. Connections go per kind and name: the
  machine's, `runner.yaml`'s `connections:`, fill the rest, and where both have one of
  the same kind and name, the server's wins. One of the machine's whose hosts overlap a
  server connection's is left out and shown. See [Credentials the agent never
  has](#credentials-the-agent-never-has).
- `variables`, which reach the agent's process. A value of the server wins over every
  source but the values qory and the runtime fix, and a run without a wall gets none of
  them. See [A run's variables](#a-runs-variables).

`--local` runs with the files alone and the machine's policy. The server is not
contacted.

### The access key and the instance

The server knows this machine by its access key, an Ed25519 key. A machine gets its key
in one of two ways: a key generated on the node's page in the server, set on the machine
as the `QORY_` variables the page shows, or a key the machine enrols with a code, with
`qory access-key enrol`. See [Enrol with a code](#enrol-with-a-code). The server keeps
only its public key: a key enrolled with a code is made on the machine, and a key
generated on the node's page is made in the browser, which shows its secret once, for
you to put on the machine or in a CI's secret store. A key is never rotated: a new one
is enrolled or generated, and the old one revoked.

`runner.yaml`'s `server` section holds two values of the key:

- `access_key_id`, the key's id: `ak_` and 16 characters, which the server assigns.
- `apiary_public_key`, the pin: the server's public keys. Every answer of the server is
  verified under one of them. A pin that lists the runner contract's published fixture
  key is refused.

`QORY_ACCESS_KEY_ID` and `QORY_APIARY_PUBLIC_KEY`, the pin as JSON, hold them when the
file does not. A value set in both is refused.

The key's secret is never in `runner.yaml`. qory reads it from the file descriptor
`--access-key-secret-fd` names, else from `QORY_ACCESS_KEY_SECRET`, else from the file
`access-key-secret` beside `runner.yaml`. The secret is one line: `qak_` and 43
characters. The file is read only when it is a regular file, not a link, that you own
and that grants nothing to the group or to others, in a directory that is yours alone.
The runner contract's published fixture key is refused.

`--access-key-secret-fd` is a flag of `qory run` and `qory run resend`. The descriptor is
3 or above. qory reads it to its end and closes it first, so nothing it starts inherits
it:

```sh
qory run --access-key-secret-fd 3 -- -p "$prompt" 3< "$secret_file"
```

A run without the id or the secret does not start. One without a pin does not start
either, `apiary_public_key_missing`.

`QORY_ACCESS_KEY_ID`, `QORY_ACCESS_KEY_SECRET` and `QORY_APIARY_PUBLIC_KEY` stay the
runner's. Every qory command reads them into memory when it starts, and removes them
from its environment before it starts anything. So no session, worktree command, tool or
integration it starts inherits them. A `wall.env` or `--env`
that names one is refused.

`server.access_key`, `server.secret` and `QORY_SERVER_SECRET` held a workspace access
key, which servers no longer accept. The two keys are refused, and so is
`QORY_SERVER_SECRET` when `runner.yaml` has a `server` section. Remove the two keys from
`runner.yaml` and unset `QORY_SERVER_SECRET` first, since `qory access-key enrol` reads
the file and refuses them too, then connect the machine as a node, with
`qory access-key enrol` or a key generated on the node's page in the server. qory
removes `QORY_SERVER_SECRET` from its environment with the three variables, server or
not, and a `wall.env` or `--env` that names it is refused.

Each machine that runs qory is an **instance** of its node. qory names it on every
request:

- Its id is kept in the file `instance-id` beside `runner.yaml`, created by the first
  run with a server. An id that was not made on this machine is replaced. When qory
  cannot write the directory, the id is the process's own, and qory says so.
- `instance.name` is its display name on the server. Unless set, it is the host name, or
  the host name's first label when the whole does not fit: 1 to 64 of `A-Z`, `a-z`,
  `0-9`, dot, underscore and dash, starting with a letter or a digit.

`qory run` prints the node and the instance once the server's configuration is read:
`qory run: node <id>, instance <id>`. When the server refuses, qory says what the
refusal means and what to do, then the runner's words and the code:

| Code | What it means |
| --- | --- |
| `unauthorized` | the server refused the request: it does not know the access key, has revoked it, or this machine's clock is more than five minutes off; check the clock, else enrol a new key |
| `answer_unsigned` | an answer does not verify under the pin |
| `instance_limit` | the node's live instances are at its limit |
| `run_closed` | the server closed the run before it started; the exit status is 1 |

#### Enrol with a code

An owner or administrator of the server creates an enrolment code on the node's page.
The code is used once, and lasts 15 minutes.

```sh
qory access-key enrol https://apiary.example qec_…
```

qory makes the key, keeps its secret in `access-key-secret`, mode `0600`, and prints its
fingerprint. It sends the server the public key, named `instance.name`, else the host
name; when the host name does not fit a name, set `instance.name`. The key is active as
soon as the server answers.

The server's answer is signed. qory writes `server.access_key_id` into `runner.yaml`,
and `server.url` and the pin, `server.apiary_public_key`, when the file has none. A pin
already there is kept. The rest of the file, its comments and its order stay as they
are.

Before it makes a key, qory checks the code against the pin `runner.yaml` has, so a code
of another server is refused. When `runner.yaml` names another `server.url`, enrol
refuses: enrol with that server, or change `server.url` first.

When `access-key-secret` exists, enrol refuses, so it never replaces this machine's key
unasked: use `--replace`, below, to move the machine to a new key, or `--print` for a
key kept elsewhere. The one exception is a retry: the same command, with the same code, within
the 15 minutes, while `access-key-secret` still holds the key made for it, retries with
that key.

When the enrolment does not complete:

| Answer | What qory does |
| --- | --- |
| 401, `unauthorized` | the code was used or has expired. The secret made for it is moved aside, and enrolling needs a new code. If you did not use the code, someone else did: tell the owner or administrator who made it. The code's issuer must revoke the key it enrolled |
| `key_invalid` | the server refused the key. The secret made for it is moved aside, and enrolling needs a new code |
| `key_limit` | the node already holds two keys. qory keeps the key: once an owner or administrator has revoked one of them, the same command within the 15 minutes succeeds |
| 429, `rate_limited`, signed | this code was tried too often. `runner.yaml` is not changed. qory keeps the key, and the same command, run later within the code's 15 minutes, retries with it. An unsigned 429 is an `answer_unsigned` |
| `answer_unsigned`, or no answer | the answer does not verify under the server's key the code names, or never came. `runner.yaml` is not changed. qory keeps the key, and the same command within the 15 minutes retries with it |

A secret enrol moves aside goes to `access-key-secret.old.<Unix time>`. It is deleted
once a new key is enrolled and a run uses it.

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
`runner.yaml` stay as they are until the server's signed answer, so an enrolment that
does not complete leaves the old key working, and the table above says what to do. Then
the new secret takes the place of `access-key-secret`, `runner.yaml` names the new key,
and the old secret is removed. qory names the old key: it still works on the server
until an owner or administrator revokes it on the node's page.

The enrolment uses the code alone, never the old key, so a key the server has revoked
can be replaced too. The same command within the code's 15 minutes retries with the new
key, and finishes a replacement that stopped part way. On a machine without a key,
`--replace` enrols as the command does without it. `--replace` and `--print` do not go
together.

#### For a CI

With `--print`, enrol writes no key and no setting on this machine. It prints
the settings on stdout, one `NAME=value` line each, and everything else on stderr.

`qory access-key enrol --print <server> <code>` prints three lines:

```sh
QORY_ACCESS_KEY_ID=ak_0123456789abcdef
QORY_ACCESS_KEY_SECRET=qak_…
QORY_APIARY_PUBLIC_KEY=[{"alg":"ed25519","public_key":"mptNqtgGKgLhLZxmOGfpBQkdeBNH7QN3Qs9ETNumy8Q"}]
```

A key generated on the node's page in the server comes as the same three variables.

Only `QORY_ACCESS_KEY_SECRET` belongs in the CI's secret store. The id and the pin are
plain settings; `QORY_APIARY_PUBLIC_KEY` is the pin as JSON. The CI's `runner.yaml` then
needs `server.url` alone.

The key is for another machine, so `enrol --print` leaves the server and the pin of
this machine's `runner.yaml` aside: it checks the code against `QORY_APIARY_PUBLIC_KEY`
when that is set. The key's name is still this machine's, and the key is active as soon
as the server answers. It keeps nothing, so an enrolment whose answer is lost or does
not verify cannot be retried: get a new code, and have the owner or administrator
revoke the key qory printed the fingerprint of, should the server show it.

#### Stored secrets need a wall

When the server lists stored secrets for the machine's access key, every run needs a
wall. An unwalled run is refused, `server_needs_wall`, once the server's configuration
is read.

qory then keeps a marker, the file `stored-secrets` beside `runner.yaml`. While it is
there, an unwalled run is refused before it starts, `--local` and a `runner.yaml`
without a server included. With the secret from `access-key-secret`, a server's
configuration that lists no stored secrets removes it. With the secret from
`QORY_ACCESS_KEY_SECRET` or `--access-key-secret-fd`, the marker stays as it is.

`qory access-key enrol` writes the marker before it makes a key, unless `--print`, since
the new key may receive stored secrets. It is removed as above.

## Runs started by another system

A system that starts runs of its own sets their id and labels. It passes each run its
policy:

```sh
qory run --run-id "$uuid" --label run_key=1234 --label issue=77 \
  --policy /etc/factory/shop-policy.yaml --timeout 5h30m --stop-grace 30s -- -p "$prompt"
```

- `--run-id` is the id the caller already has. It is a UUID in lower case.
- The labels go into `dev.qory.run.started`, and onto the run configuration request.
  There, a server ties the run to its own records and chooses its policy.

Two labels come from the checkout's origin remote, unless `--label` sets them:

| Label        | Value                                                    | For `git@github.com:acme/shop.git` |
| ------------ | -------------------------------------------------------- | ---------------------------------- |
| `forge`      | the remote's host                                        | `github.com`                       |
| `repository` | the remote's path, without the leading slash and `.git`  | `acme/shop`                        |

A checkout with no remote, or with a remote on this machine, has neither label.

### A run's own policy

`--policy` passes one run's own policy. It is in the runner contract's format. Keep it
outside the checkout. It suits a machine that serves runs of different kinds.

It only narrows. The `egress` section of the machine's `runner.yaml` decides how:

| `egress` in `runner.yaml`     | What the run reaches                            |
| ----------------------------- | ----------------------------------------------- |
| mode `enforce`                | the policy file's hosts that the section covers |
| mode `observe`, or no section | the policy file, as it is                       |

The `deny` lists of both apply either way.

The two together are the node's policy. With a server whose run configuration has a
`security_policy`, the node's policy narrows the server's:

- The mode is `enforce` when either side sets it.
- Under `enforce`, a host passes only when the allow list of every side under `enforce`
  covers it.
- A host either side's `deny` covers is denied, in either mode.
- On a host either side limits to paths, a request must match an entry of every side
  that lists the host.
- An image both sides select must be the same, else the run does not start,
  `image_unknown`.

`--local` keeps `--policy`, and the server is not contacted.

The access key's secret stays the runner's: see [The access key and the
instance](#the-access-key-and-the-instance).

## Credentials the agent never has

Behind a wall, a run needs no credential inside the container. Connections decide every
credential a run sends. A policy selects none.

A **connection** links each secret something needs to a secret. What needs one is of
three kinds:

| Kind | What it is | Where the value goes |
|---|---|---|
| `runtime` | the agent's runtime, such as Claude Code. Its descriptor declares the secrets it needs | the declaration's hosts, by its scheme |
| `service` | an API with a static key: exact hosts, a scheme, and the secrets it needs | the service's hosts, by its scheme |
| `integration` | a program that mints a credential for the run | the hosts its description lists, as its answer says. See [Integrations](#integrations) |

A run's connections go per kind and name. The server's run configuration sets the
ones it has, and `runner.yaml`'s `connections:` fill the rest. Where both have one of
the same kind and name, the server's wins. One of `runner.yaml`'s whose hosts overlap a
server connection's is left out and shown.

Connections need a wall. A run with a connection and no wall does not start,
`connection_needs_wall`.

How it works:

- The runner keeps each value outside the container.
- Its proxy sets the value on the requests to the connection's hosts.
- Where a program wants a credential set, the container gets a placeholder. It never gets
  the value.

### The model credential

Claude Code declares two secrets for its model, and a run uses one of them:

| Declaration | Variable | How it is sent |
|---|---|---|
| `api_key` | `ANTHROPIC_API_KEY` | the header `x-api-key` |
| `oauth_token` | `CLAUDE_CODE_OAUTH_TOKEN` | `bearer` |

Both go to `api.anthropic.com`, on the paths `/v1/*`. A walled run of Claude Code needs a
runtime connection that supplies one of them:

```yaml
# ~/.config/qory/runner.yaml
connections:
  - kind: runtime
    id: claude
    name: claude                          # the runtime
    secrets: {oauth_token: {source: external, name: CLAUDE_OAUTH}}   # or api_key
secrets:
  local:
    CLAUDE_OAUTH:
      env: CLAUDE_OAUTH                   # read once at run start
      hosts: [api.anthropic.com]
```

- Inside the container, the chosen declaration's variable holds a placeholder,
  `qory-sets-the-credential-outside-the-enclosure`. The runtime's other declared and
  reserved variables are empty there.
- Under `enforce`, a request to `api.anthropic.com` outside `/v1/` is refused. Under
  `observe`, it is sent on without the value, and recorded.
- With no runtime connection that supplies one, a walled run does not start,
  `runtime_secret_missing`. A connection that supplies both is refused,
  `runtime_secret_choice`.
- A walled run whose environment holds `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_API_KEY` or
  `CLAUDE_CODE_OAUTH_TOKEN` does not start, `runtime_secret_conflict`. That environment
  is what `wall.env`, `--env` and the launch pass in. So never pass the model credential
  with them.
- A mount that is or contains `~/.claude/.credentials.json` is refused,
  `mount_contains_credential_files`.

### A static key

An API that takes a static key is a **service**. Its connection defines it inline:

```yaml
# ~/.config/qory/runner.yaml
connections:
  - kind: service
    id: tracker
    name: Acme tracker
    hosts: [api.tracker.example.com]
    paths: [/v2/*]                        # optional: the requests the key is set on
    auth: {scheme: bearer, secret: key}
    declares: [{id: key, title: Tracker key, name: TRACKER_KEY}]
    secrets: {key: {source: external, name: TRACKER_KEY}}
secrets:
  local:
    TRACKER_KEY:
      file: ~/.config/qory/tracker.key    # read at each use
      hosts: [api.tracker.example.com]
```

- `hosts` are exact DNS names: no `*.`, no IP address, else `connection_host_invalid`.
- `auth.scheme` is `bearer`, `header` with `header`, or `basic` with `username` or
  `username_secret`. `auth.secret` is the declaration whose value is sent.
- A declaration with `name` gets the placeholder in that variable.
- A service takes no argument. A credential that needs one, or every host below a domain,
  is an integration.

### Secrets of this machine

`secrets.local` holds this machine's values, each under a name:

- `env`, a variable of `qory run`'s environment, read once at run start;
- `file`, a file read at each use;
- or `values`, several values, each under a value id, which a reference selects with
  `value_id`.

`hosts` is required on every entry: the most hosts the value may be sent to. A connection
that sends it to a host they do not cover stops the run, `secret_hosts_exceeded`.

- A connection names a value of this machine with `{source: external, name: <NAME>}`.
  The connections in `runner.yaml` name no other kind.
- `secrets.providers` lists where such a name is looked up, in order. It is `[local]`
  unless set. A name no provider has stops the run, `secret_unresolved`.
- A run that passes a variable an `env` entry reads into the container does not start,
  `variable_reserved`.
- A value the server stores is sealed to this machine's access key and fetched for the
  run. A host that receives one is verified against public roots only. A host that
  receives this machine's values alone is verified against the machine's trust store.

### TLS on credential hosts

For the hosts a connection sets a value on and the hosts with path rules, and no other,
the proxy ends the container's TLS itself. It uses an authority made for the run. The
authority's key never leaves the runner.

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
path and the `connection`, the id of the connection whose value it carried.

Every other host stays a tunnel that nobody reads.

## Integrations

An **integration** is a program that connects a run to an outside system, and describes
itself. It gives a run credentials, through its credential role: a credential minted for
the run, which the agent must not hold. The program runs on your machine, outside the
wall. The agent never holds its secrets, or the credential it mints.

- Qory publishes its own, each in a repository of its own, such as
  [`qory-github`](https://github.com/qoryai/qory-github). Its integration is named
  `github`.
- A program of yours starts from the
  [integration template](https://github.com/qoryai/integration-template).

Both are installed, connected and checked the same way. The rules a program follows are
the [integration
contract](https://github.com/qoryai/integrations/tree/main/contracts/integration/v1).

Using one takes three steps:

1. **Install** it from its release: `qory integration install <source>`. `runner.yaml`
   gets an entry under `integrations:`.
2. **Connect** it: a connection says which of its roles a run uses, with which argument,
   settings and secrets. A server's run configuration carries connections, or
   `runner.yaml` does.
3. **Run** behind a wall. At the start, the runner checks the program against its entry
   and the connection, then starts the roles the connection chose.

### What an integration describes

`<program> describe` prints the integration's description: one JSON document. It takes
no settings and reaches no network. Every release publishes the same bytes as
`description.json`.

| Field | What it is |
|---|---|
| `name` | the integration's name, such as `github`. Its entry in `runner.yaml` and every connection to it use this name |
| `title`, `description` | text for a listing or a form |
| `publisher` | who publishes the program, as the program names it. Nothing verifies it |
| `program_version` | the program's own version, a string. A release's is `X.Y.Z`, its version |
| `settings` | a JSON Schema of the settings the program takes. A property marked `writeOnly` is a secret |
| `roles` | the ways it offers, each under its role's name, such as `credential`. A run starts only the roles its connection's `ways` names, and leaves any other role of the description as it is |

Each role lists, in `settings`, the names of the settings it may receive, and in
`required` the ones it needs. A secret is listed by its `<name>`. Its value comes either
as `<name>` or as `<name>_file`, the path of a file that holds it.

The **credential** role mints or fetches a token for the run's argument. `argument` is
the pattern the argument must match whole. A role without `argument` ignores the
connection's argument and gets the empty string. `hosts` are the most hosts the token
may be set on.

The description of `github`, shortened. Its argument is one repository or several of one
owner, separated by commas:

```json
{"version": 1, "name": "github", "title": "GitHub",
 "publisher": {"name": "Qory", "url": "https://qory.dev"}, "program_version": "1.4.0",
 "settings": {"type": "object", "properties": {
   "app_id": {"title": "App id", "type": ["integer", "string"]},
   "private_key": {"title": "Private key", "type": "string", "writeOnly": true,
                   "x-secret-name": "GITHUB_APP_PRIVATE_KEY"},
   "private_key_file": {"title": "Private key file", "type": "string"}}},
 "roles": {"credential": {"argument": "[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}(,[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100})*",
                          "hosts": ["github.com", "api.github.com"],
                          "settings": ["app_id", "private_key"],
                          "required": ["app_id", "private_key"]}}}
```

### Install an integration

```sh
qory integration install <source> [--version X.Y.Z] [--replace]
```

```sh
qory integration install github.com/qoryai/qory-github --version 1.4.0
qory integration install gitlab.com/acme/tools/tracker
qory integration install https://downloads.example.com/tracker/description.json
```

- `--version X.Y.Z` installs that release. Without it, `qory` installs the latest.
- `--replace` lets an install replace an entry installed from another source.

Install the version your connections name. A server's connection always names one, and
the program's `program_version` must equal it exactly, else the run is refused,
`integration_version_mismatch`. The latest release is a convenience.

An integration is installed only when you run this command. A run never installs one,
and nothing a server sends makes the machine install one: a server that could would run
a program of its choosing on your machine.

#### Sources

The source is where the integration's public releases are. It is one of:

- **A repository on GitHub**, `github.com/<owner>/<repo>`, such as
  `github.com/qoryai/qory-github`.
- **A project on GitLab**, `gitlab.com/<group>/<project>`, or in subgroups
  `gitlab.com/<group>/<subgroup>/<project>`, such as `gitlab.com/acme/tools/tracker`.
- **A repository on Codeberg**, `codeberg.org/<owner>/<repo>`.
- **An `https://` URL of a release's `description.json`.** It ends in
  `/description.json`, and the release's other files are in the same directory. A URL
  source is one release.

A repository source on any other host is refused. A repository source has no scheme and
no `.git` at the end. In every form, each path segment is 1 to 100 characters, and starts
with a letter, a digit, `_` or `-`. So no `.` or `..`, and no `%`.

A URL source is refused unless:

- the host is a lower-case DNS name with at least one dot, whose last label starts with a
  letter. So no IP address, port, userinfo, query or fragment;
- the host is not `localhost`, and does not end in `.localhost`, `.local`, `.internal` or
  `.home.arpa`.

For every source, and after every redirect, `qory` refuses to download from an address
that is loopback, private, link-local or unspecified. It checks the address it connects
to.

A release's files must be downloadable without a token.

#### What install downloads and checks

A release is three kinds of file:

- `description.json`, what `<program> describe` prints, byte for byte;
- `<program>_X.Y.Z_<os>_<arch>.tar.gz`, for `linux` and `darwin`, `amd64` and `arm64`,
  with the program at the archive's root;
- `checksums.txt`, the SHA-256 of each archive and of `description.json`.

`X.Y.Z` is the release's version: three numbers, no leading zeros, nothing before or
after. On GitHub, GitLab and Codeberg the release is tagged `vX.Y.Z`.

From a repository, `qory` installs release `X.Y.Z` with `--version`, else the latest: on
GitHub and Codeberg the newest that is neither a draft nor a prerelease, on GitLab the
one with the latest `released_at`, an upcoming release included. From a URL source, it
installs the release at that URL, and `--version` must be its `program_version`. It
fetches each file of release `X.Y.Z` here:

| Source | A file of release `X.Y.Z` |
|---|---|
| `github.com/<owner>/<repo>` | `https://github.com/<owner>/<repo>/releases/download/vX.Y.Z/<file>` |
| `gitlab.com/<path>` | `https://gitlab.com/api/v4/projects/<path, URL-encoded>/releases/vX.Y.Z/downloads/<file>`, through GitLab's API |
| `codeberg.org/<owner>/<repo>` | `https://codeberg.org/<owner>/<repo>/releases/download/vX.Y.Z/<file>` |
| a URL source | `<file>` in the directory of `description.json` |

The [release rule](https://github.com/qoryai/integrations#release-rule) has the rest.
Then `qory`:

1. fetches `description.json` and `checksums.txt`, and checks `description.json` against
   `checksums.txt`;
2. checks the description against the integration contract, and that its
   `program_version` is the release's version;
3. refuses a release whose `name` has an entry installed from another source, unless
   `--replace` is given. The message names both sources;
4. fetches the archive for this machine, and checks it against `checksums.txt`;
5. puts the program at `$XDG_DATA_HOME/qory/integrations/<name>/<version>/<program>`,
   `~/.local/share/qory` when `XDG_DATA_HOME` is not set, and holds it to the rules of
   [Who may own and write it](#who-may-own-and-write-it);
6. runs `<program> describe`, which must print `description.json` byte for byte;
7. writes the entry in `runner.yaml`.

A step that fails stops the install, and `runner.yaml` stays as it was.

`qory` prints the integration's name and version, and its publisher beside the source's
owner. The owner is what the source proves: the owner on GitHub or Codeberg, such as
`github.com/qoryai`, the group path on GitLab, or the host of a URL source. A publisher
that differs from the owner is shown as the program gives it.

### The integrations entry

`runner.yaml` lists the machine's integrations under `integrations:`, each under the
description's `name`:

```yaml
# ~/.config/qory/runner.yaml
integrations:
  github:
    path: /home/dev/.local/share/qory/integrations/github/1.4.0/qory-github
    source: github.com/qoryai/qory-github
    description_sha256: 91b9db5dadffb87f43cb7a50c64c973fe1cec8a20055dce9da6a879502853f32
    arguments: '^acme/[a-z0-9._-]+$'      # a bound you add: below
    settings:
      app_id: '123456'
```

| Key | What it is |
|---|---|
| `path` | the program's absolute path |
| `source` | the source it was installed from. A connection's `source` must equal it |
| `description_sha256` | the lower-case hex SHA-256 of the release's `description.json`. The program's `describe` must print the same bytes at every run |
| `ways`, `arguments`, `settings`, `paths` | optional bounds on what a server chooses: [below](#bounds-on-what-a-server-chooses) |

`qory integration install` writes `path`, `source` and `description_sha256`. You write
the bounds.

One entry holds one `path`, so the machine serves one version of an integration at a
time.

A program of your own that has no release has an entry you write yourself, with `path`
alone. The runner checks such an entry by name and version only, not by a digest, so
keeping the program at that path unchanged is yours to do. A server checks each
connection against the release's `description.json`, so a server connects only an
integration published as a release.

#### Bounds on what a server chooses

The server leads; the machine only narrows. An entry may bound what a server's
connection chooses. The bounds apply to connections a server sends, and to no other. A
connection in `runner.yaml` is the owner's own: it meets each chosen role's description
alone.

| Bound | What it allows | Otherwise |
|---|---|---|
| `ways` | the roles a server may choose | `integration_way_not_allowed`. Absent, it narrows nothing |
| `arguments` | an RE2 pattern the connection's argument must match whole | `integration_argument_not_allowed` |
| `settings` | per setting, a fixed value or `{pattern: <RE2>}`. A setting it does not list is refused | `integration_settings_not_allowed` |
| `paths` | by host, the most paths the credential role's answer may claim | `integration_hosts_exceeded` |

A fixed value of `settings` is this machine's. For a server's connection, the runner
writes it into the document of every chosen role that lists the setting. The server's
value for that setting must be absent or equal to it, else
`integration_settings_not_allowed`. A `{pattern: <RE2>}` bounds the server's value.

A `<name>_file` setting is a path on this machine. A server's connection that holds one
is refused, `integration_settings_not_allowed`. One comes from `runner.yaml` alone: from
a connection there, or from a fixed value of `settings` here.

A server's connection that references a secret of this machine (see [In
runner.yaml](#in-runneryaml)) needs both `arguments` and `settings`. Without
`arguments` it is refused, `integration_argument_not_allowed`. Without `settings` its
settings must be `{}`, else `integration_settings_not_allowed`.

#### Where a program may live

`qory` runs only a program the run cannot write. The program must be:

- outside the checkout, and
- outside every mount the wall makes. A mount that is, contains or lies inside the
  program's directory stops the run, `mount_contains_runner_files`.

A mount that is or contains the file of a `<name>_file` setting, or a link on the way
to it, stops the run the same way, `mount_contains_runner_files`, for each integration
the run describes: the ones its policy selects, every one when a server supplies the
policy.

`qory` judges a program by where its links lead: a `path` that is a link is judged by
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
group, and the directory `qory integration install` installs into.

### Connect an integration

A run uses an integration through a **connection**. The policy selects no integration. A
connection names:

| Key | What it is |
|---|---|
| `kind` | `integration` |
| `id` | the connection's id |
| `name` | the integration's name: its key under `integrations:` |
| `source`, `version` | where its releases are, and the `program_version` it must have. A server's connection always has both. In `runner.yaml` they are optional |
| `ways` | the roles the run uses, such as `credential`. At least one, each a role the description defines |
| `argument` | the run's one argument, such as `acme/shop`. Each chosen role that has an `argument` pattern must match it whole. A role without one ignores it and gets the empty string |
| `settings` | the plain settings |
| `secrets` | each secret the chosen roles list, by its setting's name, linked to a secret |

A run's connections go per kind and name. The server's run configuration sets the ones
it has, and `runner.yaml`'s `connections:` fill the rest. Where both have one of the same
kind and name, the server's wins. One of `runner.yaml`'s whose hosts overlap a server
connection's is left out and shown.

Connections need a wall. A run with a connection and no wall does not start,
`connection_needs_wall`.

#### From a server

A server's run configuration carries the connections it chose for the run:

```json
{"kind": "integration", "id": "con_0b5n6t2r9y4f7j3s", "name": "github",
 "source": "github.com/qoryai/qory-github", "version": "1.4.0", "ways": ["credential"],
 "argument": "acme/shop", "settings": {"app_id": "123456"},
 "secrets": {"private_key": {"id": "sec_9c4r7t2y5b8n1h3e", "name": "GITHUB_APP_PRIVATE_KEY"}}}
```

- The server checks `source`, `ways`, `argument` and `settings` against the
  release's `description.json`, and that each key of `secrets` is a secret a chosen role
  lists.
- A secret with an `id` is a value the server stores. The runner fetches it for the run,
  sealed to the machine's access key, and holds it in memory alone.
- A secret `{source: external, name: <NAME>}` is a value of this machine, from
  `secrets.local`. Each one needs the bounds [above](#bounds-on-what-a-server-chooses).
- The machine's [bounds](#bounds-on-what-a-server-chooses) narrow what the server chose.

#### In runner.yaml

A machine without a server takes its connections from `runner.yaml`. With a server, the
connections there fill in the kinds and names the server leaves out, and one whose hosts
overlap a server connection's is left out and shown. Their secrets come from
`secrets.local`:

```yaml
# ~/.config/qory/runner.yaml
connections:
  - kind: integration
    id: github
    name: github
    ways: [credential]
    argument: acme/shop
    settings: {app_id: '123456'}
    secrets: {private_key: {source: external, name: GITHUB_APP_PRIVATE_KEY}}
secrets:
  local:
    GITHUB_APP_PRIVATE_KEY:
      file: ~/.config/qory/github-app.pem   # read at each use; or env: a variable, read at start
      hosts: [github.com, api.github.com]
```

- A connection's `id` here is 1 to 64 of `a-z`, `0-9`, `_` and `-`, starting with a
  letter or a digit. Its secrets are `{source: external, name}` references alone.
- An entry of `secrets.local` takes its value from `file` or `env`, or holds several
  under `values`, each by a value id, which a reference selects with `value_id`.
- `hosts` is required on every entry: the most hosts the value may be sent to. For an
  integration, the hosts compared are the credential role's `hosts`, when it lists the
  secret. A host it does not cover stops the run, `secret_hosts_exceeded`.
- `secrets.providers` lists where a reference is looked up, in order. It is `[local]`
  unless set.
- A `<name>_file` setting may stand in `settings` here, such as
  `private_key_file: /home/dev/.config/qory/github-app.pem`, in place of the secret.
- The entry's bounds do not apply here: a connection in `runner.yaml` is the owner's own,
  and meets each chosen role's description alone.

`hosts` bounds where the proxy sets the credential the program mints. The program itself
receives the raw value, and where it sends it is the program's.

#### Settings and secrets

The runner hands the credential role its settings on its command line, and nothing on
standard input. It starts the role as:

```sh
<program> credential --settings <json> -- <argument>
```

- `<program>` is the program's absolute path, its links resolved.
- `<json>` is the integration's `settings` in `runner.yaml`, whole, as compact JSON in
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
/usr/local/bin/qory-github credential --settings '{"app_id":"123456","private_key_file":"/home/dev/.config/qory/github-app.pem"}' -- acme/shop
```

The runner checks each role's document, in order:

1. It holds only the names that role lists, and the connection holds nothing that no
   chosen role lists. Else `integration_settings_not_allowed`. So nothing unused travels.
2. Every name of the role's `required` is there, a secret in either form. Else
   `integration_settings_invalid`.
3. It is valid against the description's `settings`. Else `integration_settings_invalid`.
4. It does not hold both `<name>` and `<name>_file`. Else `integration_settings_invalid`.

A name in both `settings` and `secrets` is refused, `run_configuration_invalid`.

What a program writes to standard error is reported with every value of its standard
input, and the credential it returned, replaced by `[redacted]`.

### At a run's start

Before the agent starts, the runner checks each integration connection. Each check stops
the run with its code:

| Check | Code |
|---|---|
| `source` is well formed: the patterns, and no refused host | `run_configuration_invalid` |
| the run has a wall | `connection_needs_wall` |
| the machine has an entry of that `name` | `integration_missing` |
| for a server's connection, the argument and the settings stay within the entry's `arguments` and `settings`, and no `<name>_file` setting is there | `integration_argument_not_allowed`, `integration_settings_not_allowed` |
| `describe` starts, exits 0, and prints a description the contract accepts | `integration_failed` |
| the description's `name` is the connection's | `integration_name_mismatch` |
| its `program_version` is the connection's `version`, when the connection has one | `integration_version_mismatch` |
| the connection's `source` is the entry's, and the SHA-256 of `describe`'s output is the entry's `description_sha256` | `integration_source_mismatch` |
| each role of `ways` is one the description defines | `integration_role_missing` |
| for a server's connection, each role of `ways` is within the entry's `ways` | `integration_way_not_allowed` |
| the argument matches each chosen role's `argument` pattern. A role without one ignores the argument | `integration_argument_not_allowed` |
| each role's settings document passes the checks of [Settings and secrets](#settings-and-secrets) | `integration_settings_not_allowed`, `integration_settings_invalid`, `integration_settings_too_large` |
| every secret resolves, and a value of this machine goes only to hosts its `hosts` cover | `secret_unresolved`, `secret_hosts_exceeded` |
| no host is set by two connections | `connection_host_conflict` |
| no `*.` host covers a public suffix | `connection_host_public_suffix` |
| no placeholder is a name the run sets or reserves, or one the run passes a value for | `placeholder_conflict` |
| the credential role starts and answers within a minute, with a document its schema accepts | `integration_failed` |
| the answer claims no host or path above the description's `hosts` and the entry's `paths` | `integration_hosts_exceeded` |
| the answer sets no header the runner reserves | `connection_header_reserved` |

An entry without `source` and `description_sha256`, such as a program of your own, is
checked by name and version alone.

A run that a check stops does not start. With a server, after the ping, its record ends
with `dev.qory.run.refused`, which carries the code. The
[runner's contract](https://github.com/qoryai/runner/tree/main/contracts/runner/v1) has
every code.

### The credential role

With `credential` in `ways`, the runner starts `<program> credential -- <argument>`
before the agent, outside the wall. The program answers with the token, its expiry, and
how the token is set: the hosts, the scheme and the paths. It may name placeholders.

- The token stays with the runner. The proxy sets it on the requests to the answer's
  hosts and paths.
- Each variable the answer names as a placeholder holds a placeholder inside the
  container, such as `GH_TOKEN` and `GITHUB_TOKEN` for `github`. So `git` and `gh` start,
  and send the placeholder. The proxy replaces it.
- Five minutes before the token expires, the runner runs the role again. It also does
  when a host answers `401` to a request it set the token on, at most once every thirty
  seconds.
- A renewal that fails keeps the old token. Each request it is set on is then recorded
  with `renewal_failed: true`, until a renewal succeeds.
- A host that receives a token minted from a value the server stores is verified against
  public roots only. A token minted from this machine's values alone is verified against
  the machine's trust store.

A host the policy denies gets no token, and the run goes on. The record lists that host
in the connection's `hosts_denied`.

#### Paths

The answer's paths are where the token goes on its hosts. Under `enforce`, they are also
the run's whole reach on those hosts. The runner refuses every other path there, another
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

### Upgrade an integration

Install it again, at the new version:

```sh
qory integration install github.com/qoryai/qory-github --version 1.5.0
```

- The entry keeps its name and its bounds: `ways`, `arguments`, `settings` and `paths`.
- `path`, `source` and `description_sha256` become the new release's, with `--replace`
  too.
- An install from another source than the entry's is refused unless `--replace` is
  given. The message names both sources.

One entry holds one `path`, so the machine serves one version at a time. A server's
connection names a version, and a run whose connection names another than the installed
program's is refused, `integration_version_mismatch`. So change the connection's version
on the server and install that version on the machine together. Runs in between are
refused.

### See an integration

`qory config` lists each entry under `runner.integrations.`, with its path, its source,
its digest and its bounds. It runs each program's `describe`, as a run does, and shows
the name, the version, the roles and their hosts, and the publisher beside the source's
owner. An entry whose program does not describe is an error.

A run's record shows what each connection did:

- `dev.qory.run.policy_applied` lists each connection: its id, `name`, `source`,
  `version`, `ways` and `argument`; its secrets by name, never a value; where and how the
  proxy sets the token, `uses`; and `hosts_denied`.
- `dev.qory.run.egress` names the `connection` whose token a request carried.

## Resending a run's record

`qory run resend <run-id>` sends a run's record to the server `runner.yaml` defines. It
is for a run whose runner died, or whose server was away. End a job with it, whatever
happened before it.

The run is selected by its id: its folder in this checkout's run records, as [The
record](#the-record) names it. The
server's configuration is fetched first, signed. It defines where the events go.

- The run directory records what the server accepted. Only the rest is sent, in order.
  Nothing the server accepted is sent again.
- A server may still see an event twice. It discards the copy by the event's id.
- After a runner that died, it first closes the record: `dev.qory.run.exited` with
  `reason: runner_lost`. It also removes the containers and networks the run's wall left.
- It refuses a run that is running.
- It keeps sending until the server accepts, or `--wait` is over. The wait is two
  minutes unless set.

The exit status is 0 when the server has everything. It is 1 when events remain. Those
stay under the run directory's `undelivered`.

The formats are in the runner's
[contract](https://github.com/qoryai/runner/tree/main/contracts/runner/v1).

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
- The runner, the policy, the record and the access key's secret stay outside.

A wall needs the `docker` command, and an engine behind it. It also needs an image that
contains the runtime: Qory's, or yours FROM it. See [Qory's images](#qorys-images).
`wall.images` defines several, and a run's policy selects one: see [The agent's
images](#the-agents-images).

What to know:

- **The model credential.** It comes from a runtime connection, and stays outside: see
  [The model credential](#the-model-credential). `wall.env` and `--env` never carry it;
  a walled run whose environment holds it does not start, `runtime_secret_conflict`. A
  subscription login kept in a Mac's Keychain does not reach a container. Use an API
  key, or a token from `claude setup-token`.
- **The helper on a Mac.** Inside the container, the relay, the hook forwarder and what
  starts an image's own Docker are `qory`'s own Linux build, mounted read-only. On Linux, that is the binary you run. On a Mac, download the
  Linux archive of the same release, for your engine's architecture. Set `wall.helper` to
  that binary.
- **What the container sees.** The checkout it was started in, and the run's own record
  directory, read-only, and no other directory. `--mount <path>[:ro]` or `wall.mounts` shows it another one, at its own path, such as a
  sibling checkout the session reads. A socket is never mounted. A mount is refused
  before the run starts, `mount_contains_runner_files`, when it is, contains or lies
  inside one of these:
  - this machine's qory configuration directory, which holds `runner.yaml`, the access
    key and the user `qory.yaml`;
  - a file qory reads from that directory and a link takes elsewhere: where the last
    link leads, and, for a mount that is or contains it, every link on the way;
  - qory's state directory, which holds the run records, and, for a mount that is or
    contains it, every link on the way to it;
  - the file of a `<name>_file` setting of an integration the run describes, where its
    path leads, and, for a mount that is or contains it, every link on the way to it;
  - one of the runner's own program and temporary files, such as a program it starts
    outside the wall.

  For a mount that is or contains the access key, `qory run` says:

  ```
  qory run: the mount <host path> contains <dir>, which holds this machine's access key; the agent could read the key, so the run does not start. Mount a narrower path
  ```

  For any other, it says:

  ```
  qory run: the mount <host path> contains <path>, which holds one of the runner's files; the agent could change it, so the run does not start. Mount a narrower path
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
  qory run: the mount <host path> contains <link>, which leads to one of the runner's files; the agent could point it elsewhere, so the run does not start. Mount a narrower path
  ```

  For a read-only mount, it leaves out `; the agent could point it elsewhere`.

  When the path is the checkout or the working directory, it says `the workspace <path>`
  instead of `the mount <path>`.
- **Git in a worktree.** In a git worktree, the repository's data lives in the main
  checkout, outside the worktree. So git inside the container works there only with that
  directory mounted. A clone works as it is.
- **Limits.** `--cpus`, `--memory`, `--pids-limit` and `--shm-size`, or the keys of those
  names under `wall`, limit what the container uses. Sizes are written the way Docker
  writes them: a number and `b`, `k`, `m` or `g`, in either case. So `14g` is 14 GB and
  `2g` is 2 GB; `14GB` or `14GiB` is refused. A headless browser wants `--shm-size 2g`:
  an engine's default `/dev/shm` is 64 MB.
- **Your own machine.** Behind a wall, the proxy reaches your own machine only for a host
  that `egress.allow` lists itself, in either mode. It never reaches the cloud metadata
  address. For a local model endpoint or MCP server, list your machine's host name, and
  point the harness at that name. `localhost` inside the container is the container.
- **Hooks in a virtual machine.** With the engine in a virtual machine, as on a Mac, the
  runtime's hooks do not reach the runner. So a walled run there has no hook events. The
  log, the egress record and the structured output are there. On a Linux host, the hooks
  cross.

### Qory's images

`qory`'s repository defines four images under `images/`, for `linux/amd64` and
`linux/arm64`. They are not published: you build them, by hand as below, and a workflow
of the repository builds and checks them on a change. Build them from a checkout of
`qory` at the commit `qory version` prints, since each commit pins the runtime at the
version its runner is written against:

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
| `agent` | Claude Code at the version the runner's descriptor is written against, `git`, `gh`, Node, the system's authorities |
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
  them, `/bin/sh` is there for the runtime's hooks and the runner's API-key approval,
  `claude` is the version the runner's descriptor is written against, and `git` and
  `gh` run. It reports whether `dockerd` is in a system directory, and checks that what
  the daemon runs is there too.

The Linux build is the one the wall uses: `wall.helper`, for your engine's architecture,
or on Linux the `qory` you run when `wall.helper` is not set.

## The agent's images

A machine that serves several kinds of work defines several images in `runner.yaml`,
each by a name. A run's policy selects one by that name. A repository never names an
image.

```yaml
# ~/.config/qory/runner.yaml
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
reads `runner.yaml` refuses `docker: true` without a `runtime`.

`dev.qory.run.started` names the image the run started in: `image`, the reference. For an
image of `wall.images` it also has `image_name`, and `container_runtime` and
`docker: true` when they apply. `dev.qory.run.policy_applied` records `image` when the
policy selects one.

### A Docker of the agent's own

*Experimental. It may change, or go, without notice.*

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
- Mount nothing into such a run that your machine's root must protect. Whether the
  container's root reaches the mounts as your machine's root is not yet verified.
