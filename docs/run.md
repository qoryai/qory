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
template's arguments.

qory computes the home itself, as compose does, and refuses a run whose harness report
names another: run `qory harness compose` again. The report fixes no variable and adds no
mount. Behind a wall, `harness.home` comes from the `qory.yaml` in qory's configuration
directory alone: another `qory.yaml` that moves the home refuses a walled run.

When the run ends, qory says how, and its exit status goes with it:

- `✓ <runtime> exited 0` or `✗ <runtime> exited <n>`: the runtime exited by itself, and
  the exit status is the runtime's.
- `✗ <runtime> ended on the signal <signal>`, such as `SIGKILL` from elsewhere: exit 1.
- `✗ the run was cancelled: qory run got SIGTERM, and <runtime> was stopped`, or
  `SIGINT`, when the signal came while the runtime ran: exit 1. A signal that comes after
  the runtime exited changes neither its line nor the exit status.
- `✗ the run was cancelled: it reached the time limit of <d>, and <runtime> was
  stopped`: exit 124 (see [Time limits](#time-limits)).
- `✗ the run failed: its events could not be recorded, and <runtime> was stopped`: the
  gateway could not take an event the session sent, exit 1.
- `✗ the run was lost: it lost contact with the gateway for 1m30s, and <runtime> was
  stopped`: exit 1.
- `✗ the run failed: the gateway stopped during the run, and <runtime> was stopped`:
  exit 1.

When the run starts its gateway itself, the gateway may record the run's end after the runtime
exited by itself. A second line then follows the runtime's, such as `✗ the run was lost:
it lost contact with the gateway for 1m30s` or `✗ the run failed: its events could not be
recorded`, and the exit status follows it: 0 when the run completed, 1 otherwise. An end
that agrees with the runtime's exit adds no line.

Through a separate gateway, the run's starter ends the run with `qory run end`: see
[How a run ends there](#how-a-run-ends-there).

### At a terminal, or on pipes

At a terminal, the session runs on a pseudo-terminal. The runtime's interface works,
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
that is set; qory refuses a relative `XDG_STATE_HOME`. `<checkout>` is the checkout's
directory name, and `<hash>` the first 12 hex digits of the SHA-256 of its full path, with
links resolved. Each checkout has a separate folder. The state directory, its `runs`
directory and the checkout's folder are mode 0700: only you can open them. The state
directory also keeps, in `places`, the record of the places each walled run used. qory
checks it before it runs git or acts on a qory file there; a qory started with another
`HOME` or `XDG_STATE_HOME` does not see those records, so it does not check those places.
qory names the run's folder when the run ends:

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

On a machine that holds an access key, a run without a wall is refused, `key_needs_wall`:
go on to [Behind a wall](#behind-a-wall). See [An access key needs a
wall](#an-access-key-needs-a-wall).

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

A walled run of Claude Code can keep its model key outside the container. Define the
wall, the model key and what the agent may reach in `~/.config/qory/forager.yaml`:

```yaml
apiVersion: qory.dev/v1alpha1
gateway:
  egress:
    mode: enforce
    allow: [api.anthropic.com]
  credentials:
    model:
      env: CLAUDE_CODE_OAUTH_TOKEN
      format: anthropic-messages
      endpoint: https://api.anthropic.com
      auth: {scheme: bearer, oauth: true}
wall:
  adapter: docker
  image: qory-agent
```

Then pass the run a policy that selects the model key. Keep the policy outside the
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

- Inside the container, `CLAUDE_CODE_OAUTH_TOKEN` is a placeholder, and
  `ANTHROPIC_BASE_URL` is `https://api.anthropic.com`.
- The proxy sets the real token on each request to `api.anthropic.com`.
- The record lists those requests with the credential's name, `model`, as `credential`
  in `dev.qory.run.egress`. It never lists the token's value.
- Everything else the agent tries to reach is denied and recorded.

If you run it without a wall, the agent can read the token too. qory and the agent run as
the same user. On macOS, the agent can read the environment qory started with, where
`CLAUDE_CODE_OAUTH_TOKEN` is set. On Linux, it can read the environment that the program
that started qory, such as your shell, started with, which may contain the token. The
agent can also read the environment of any other process of yours that started with the
token set. To keep the token out of the agent's reach without a wall, run the gateway on
another machine, as in [Through a separate gateway](#through-a-separate-gateway).

### When to run without a wall

If you start the agent yourself on your machine, a run without a wall is fine. There, an
agent you start without qory can reach your credentials too.

If nobody watches the run, such as a run that a work item or a schedule starts, run it
with a wall. Text the agent reads, such as an issue, a web page or a file, can contain
instructions that make the agent act against you (prompt injection). Without a wall, such
an agent can read every credential stored on this machine, and a program that ignores the
proxy reaches any host. To run with a wall, see [Behind a wall](#behind-a-wall) and [The
wall](#the-wall).

## forager.yaml

One optional file defines what Forager does on this machine:
`~/.config/qory/forager.yaml`. It lives beside your `qory.yaml`, and nowhere else. So a
repository cannot set it. It has three sections beside `apiVersion`: `gateway`, the
proxy's policy, the server, the credentials, the tools, the integrations and the limits of
each run; `session`, this instance and what applies to every run; and `wall`, the
container. `gateway.listen` and `gateway.tls` are `qory gateway`'s: `qory run` does not use
them. See [the gateway as a service](gateway.md).

`gateway.server.via` reaches the server through a gateway, on a machine whose node goes
through one; a runner whose starter registers its runs needs no such setting: see
[Through a separate gateway](#through-a-separate-gateway).

```yaml
# ~/.config/qory/forager.yaml
apiVersion: qory.dev/v1alpha1
gateway:
  egress:                  # what the runtime may reach; enforce denies the rest
    mode: enforce          # or observe: record everything, deny only what deny lists
    allow: [api.anthropic.com, api.example.com, github.com, "*.github.com"]
    deny: [gist.github.com]                # denied in either mode, whatever allow lists
    paths:                                 # the paths a run may request on a host; every run needs a wall
      api.example.com: ["/v1/*"]
  server:                  # the server every run reports to; optional
    url: https://apiary.example           # a scheme and a host, nothing after
    access_key_id: ak_0123456789abcdef    # this machine's access key; its secret is not in this file
    apiary_public_key:                    # the server's key, which signs every answer
      - {alg: ed25519, public_key: mptNqtgGKgLhLZxmOGfpBQkdeBNH7QN3Qs9ETNumy8Q}
    # via: {url: https://gateway.example:8443}   # reach the server through a gateway
  credentials:             # tokens the proxy sets on requests; a run's policy selects them
    model:                                # a model key, kept outside the container
      env: CLAUDE_CODE_OAUTH_TOKEN        # or file: an absolute path; or adapter: a program
      format: anthropic-messages          # the API the endpoint serves
      endpoint: https://api.anthropic.com # the key goes to this host alone
      auth: {scheme: bearer, oauth: true} # or header with header, or basic with username
    tracker:                              # a token for other hosts
      env: TRACKER_TOKEN
      hosts: [api.example.com]
      auth: {scheme: header, header: X-Api-Key}
      paths: [/v1/*]                      # where on its hosts the token goes; optional
  integrations:            # programs that mint a credential; optional
    github:                               # qory-github, found on the PATH
      settings: {app_id: 123456, private_key_file: /etc/qory/github-app.pem}
  run_limits:              # the limits of each run; optional
    connections: 256                      # open at once, 1 to 65536
    requests_per_second: 50               # 1 to 10000
    burst: 200                            # requests at once after a pause, 1 to 100000
  lock_memory: true        # Linux only: keep the gateway's memory out of swap; optional
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
`forager.yaml` that does not read means no run. Behind a wall, the runtime reaches your
machine or a private network address, such as `10.0.0.5`, only for a host that
`gateway.egress.allow` of this `forager.yaml` lists by name. Through a separate gateway, the
rule covers the gateway's machine, and the list that counts is `gateway.egress.allow` of the
gateway's `forager.yaml`. See [The gateway's machine and private
addresses](gateway.md#the-gateways-machine-and-private-addresses).

## Runtimes

`qory run` runs whichever runtime the harness is composed for. Nothing in `forager.yaml`
is particular to one runtime.

Forager's session reads what it needs to know about a runtime from a **descriptor**. A
descriptor is a file of data, in the format of the [Forager
contract](https://github.com/qoryai/forager/blob/v0.7.0/contracts/forager/v1/README.md#the-runtime).
It says:

- how the runtime's hooks are installed,
- what its output means as events,
- which signal asks it to stop,
- which variables hold its model key: see [The model key](#the-model-key).

Forager ships the descriptor for Claude Code. `~/.config/qory/runtimes/<runtime>.yaml`
describes another runtime, or replaces the one shipped.

A runtime that nothing describes still runs. The run, its log and its egress are
recorded. The session's events are not.

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

## Limits of each run

`gateway.run_limits` in `forager.yaml` limits each run on the gateway, so that one run
cannot slow down the others. Each run has separate counters.

```yaml
# ~/.config/qory/forager.yaml
gateway:
  run_limits:
    connections: 256          # open at once, 1 to 65536
    requests_per_second: 50   # 1 to 10000
    burst: 200                # requests at once after a pause, 1 to 100000
```

- `connections` is the most connections a run has open through the gateway at once. The
  next one gets `429 Too Many Requests` at once.
- `requests_per_second` is how many requests a run sends a second, and `burst` how many it
  sends at once after a pause. A request past the limit waits up to 5 seconds, then gets
  `429 Too Many Requests`.
- A field you leave out has its default: 256, 50 and 200.
- A value outside its range stops every qory command that reads `forager.yaml`, with the
  fix, such as `set gateway.run_limits.connections to a number from 1 to 65536, or remove
  it for the default of 256`.
- The record has a `dev.qory.run.egress` with the rule `wall:connection-limit` or
  `wall:rate-limit` for a request that gets the `429`, at most one per limit and second.

`qory gateway` applies the same limits to each run it serves. A `qory run` through a
separate gateway gets that gateway's limits.

## A run's variables

Several sources may set a variable of the agent's process. For each name, the run takes
the value of the highest source that sets it:

1. The values qory and the runtime fix: Forager's names, `QORY_HARNESS_HOME`, and the
   variables of the runtime's launch template, such as Codex's `CODEX_HOME`. No
   other source overrides them, `env` in `qory.yaml`, a settings fragment's and a
   module's export included. The `env` of `harness.launch.<runtime>` replaces the
   template's variables, and its values are defaults.
2. The server's run configuration. See [The server](#the-server).
3. `--env`, the run's.
4. `wall.env`, the machine's.
5. The harness's defaults: `env` in `qory.yaml`, the `env` of
   `harness.launch.<runtime>`, the `env` a settings fragment sets, and what the modules
   export.
6. The shell `qory run` starts in. A walled run takes none of it but the names
   `wall.env` and `--env` list.

Forager's deny list, names such as `PATH` and `DOCKER_HOST`, leaves out a value of
sources 2 to 5, a module's export included.

`--env` and `wall.env` name variables of `qory run`'s environment. `--env` needs no
wall; `--image`, `--mount` and the limits do. A run against the server always has a
wall, since the machine holds an access key: see [An access key needs a
wall](#an-access-key-needs-a-wall).

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
it pins, `apiary_public_key`. The run does not start unless the server answers. Such a
machine holds an access key, so every run needs a wall: see [An access key needs a
wall](#an-access-key-needs-a-wall).

For a node's or node pool's access key, the configuration lists in `workspaces` one id,
`ws_` and 16 characters: the workspace the node or pool belongs to. A configuration
without it is refused, and the run does not start.

The configuration defines where the gateway sends the run's events, and where the run
registers, `run`. A configuration without `run` is refused, and the run does not start.
The gateway registers the run with its labels, the checkout's forge and repository among
them, and the server answers with the run configuration. It fetches it again when the
server reports it changed. It may hold:

- `security_policy`, the server's policy. `gateway.egress` of `forager.yaml` narrows it:
  see [A run's policy file](#a-runs-policy-file). Without it, `gateway.egress` is the
  run's policy.
- `variables`, which reach the agent's process. A value of the server wins over every
  source but the values qory and the runtime fix. See [A run's
  variables](#a-runs-variables).

Behind a separate gateway, the starter, or this machine's qory, registers the run, and the
gateway gets the run configuration.

Once the server accepts the registration, the gateway writes the record's first line,
`dev.qory.run.registered`. It names the run's `workspace`, its `node_id` and its
`instance_id`, the instance id the registration was signed with. This line stays on this
machine: it is never sent to the server.

`--local` runs with the files alone and the machine's policy. The server is not
contacted. While `access-key-secret`, a key file beside it or `QORY_ACCESS_KEY_SECRET`
holds an access key, such a run still needs a wall.

### The access key and the instance

The server knows this machine by its access key, an Ed25519 key. A machine gets its key
in one of two ways: a key generated on the node's page in Qory Apiary, set on the
machine as the `QORY_` variables the page shows, or a key the machine enrols with a
code, with `qory access-key enrol`. See [Enrol with a code](#enrol-with-a-code). The
server keeps only its public key: a key enrolled with a code is made on the machine, and
a key generated on the node's page in Qory Apiary is made in the browser, which shows
its secret once, for you to put on the machine or in a starter's secret store. A key is
never rotated: a new one is enrolled or generated, and the old one revoked.

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

`--access-key-secret-fd` is a flag of `qory run`, `qory run resend`, `qory run register`,
`qory run end` and `qory gateway`. The descriptor is 3 or above. qory reads it to its end
and closes it first, so nothing it starts inherits it:

```sh
qory run --access-key-secret-fd 3 -- -p "$prompt" 3< "$secret_file"   # with wall in forager.yaml
```

A run against the server without the id or the secret does not start, and `qory run
register` and `qory run end` send nothing. Without a pin, neither does any of them,
`apiary_public_key_missing`.

`QORY_ACCESS_KEY_ID`, `QORY_ACCESS_KEY_SECRET` and `QORY_APIARY_PUBLIC_KEY` stay
Forager's, and so does `QORY_RUN_KEY`, the run key a starter gives a job. Every qory
command reads them into memory when it starts, and removes them from its environment
before it starts anything. So no session, worktree command, tool or integration it starts
inherits them. A `wall.env` or `--env` that names one is refused. An unwalled agent can
still read the environment qory started with on macOS, and on Linux the starting
environment of the program that started qory, so a run while `QORY_ACCESS_KEY_SECRET` or
`QORY_RUN_KEY` is set needs a wall: see [An access key needs a
wall](#an-access-key-needs-a-wall) and [The run key](#the-run-key).

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
  cannot write the directory, the id is the process's alone, and qory says so.
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
| `node_uses_gateway` | the machine's node goes through a gateway, and `gateway.server.via` is not set: set it, or set the node to "Direct" in Qory Apiary. See [A machine with a separate access key, behind a gateway](#a-machine-with-a-separate-access-key-behind-a-gateway) |

A gateway's access key runs no sessions. When the server's configuration names a gateway's
node, `qory run` stops before the run registers:

```text
✗ this machine's gateway serves other machines and runs no sessions; give this machine a separate node
```

#### Enrol with a code

An owner or administrator in Qory Apiary creates an enrolment code on the node's page.
The code is used once, and lasts 15 minutes.

```sh
qory access-key enrol https://apiary.example qecn_…
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
| 401, `unauthorized` | the code was used or has expired. The secret made for it, in `access-key-secret.new`, is moved aside, and enrolling needs a new code. If you did not use the code, someone else did: tell the owner or administrator in Qory Apiary who made it. They must revoke the key the code enrolled |
| `key_invalid` | the server refused the key. The secret made for it, in `access-key-secret.new`, is moved aside, and enrolling needs a new code |
| `key_limit` | the node already holds two keys. qory keeps the key: once an owner or administrator in Qory Apiary has revoked one of them, the same command within the 15 minutes succeeds |
| 429, `rate_limited`, signed | this code was tried too often. `forager.yaml` is not changed. qory keeps the key, and the same command, run later within the code's 15 minutes, retries with it. An unsigned 429 is an `answer_unsigned` |
| `answer_unsigned`, or no answer | the answer does not verify under the server's key the code names, or never came. `forager.yaml` is not changed. qory keeps the key, and the same command within the 15 minutes retries with it |

A secret enrol moves aside goes to `access-key-secret.old.<Unix time>`. It is deleted
once a new key is enrolled and a run uses it. Enrol moves aside only
`access-key-secret.new`: a refused enrolment never moves or changes
`access-key-secret`. A key the server enrolled for something else is deleted from
`access-key-secret.new` instead, since it is active until it is revoked in Qory Apiary.

Enrol refuses while `QORY_ACCESS_KEY_ID`, `QORY_ACCESS_KEY_SECRET` or
`QORY_APIARY_PUBLIC_KEY` is set: the key it keeps in this machine's files would
contradict the variable. Unset it, or use `--print`. It also refuses while a run without
a wall is running on this machine: it must end before a key is made.

#### Replace the machine's key

```sh
qory access-key enrol --replace https://apiary.example qecn_…
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

#### For a starter

A starter is a CI, or any system that starts jobs on runners. It keeps a node pool's access
key in its secret store, and the runners keep none.

With `--print`, enrol writes no key and no setting on this machine. It prints
the settings on stdout, one `NAME=value` line each, and everything else on stderr.

`qory access-key enrol --print <server> <code>` prints three lines:

```sh
QORY_ACCESS_KEY_ID=ak_0123456789abcdef
QORY_ACCESS_KEY_SECRET=qak_…
QORY_APIARY_PUBLIC_KEY=[{"alg":"ed25519","public_key":"mptNqtgGKgLhLZxmOGfpBQkdeBNH7QN3Qs9ETNumy8Q"}]
```

A key generated on the node's page in Qory Apiary comes as the same three variables.

Only `QORY_ACCESS_KEY_SECRET` belongs in the starter's secret store. The id and the pin are
plain settings; `QORY_APIARY_PUBLIC_KEY` is the pin as JSON. The starter's `forager.yaml`
then needs `gateway.server.url` alone, and `gateway.server.via` when it reaches Qory Apiary
only through the gateway. Such a starter enrols through the gateway too, with `--via`: see
[A machine with a separate access key, behind a gateway](#a-machine-with-a-separate-access-key-behind-a-gateway).

For each job, the starter registers the run, and the run key goes to the job:

```sh
run_key="$(qory run register --run-key-fd 3 --run-id "$run_id" --label issue=77 --title "Fix the parser" 3>&1)"
```

`qory run register` registers the run with the pool's key and writes the run key to the
descriptor `--run-key-fd` names, 3 here, which `3>&1` hands to the command substitution.
stdout gets nothing, and every other line goes to stderr:

```text
qory run register: node np_f1xt0re000000000, instance 0192f0c1-7d4e-7a2b-8c3d-4e5f6a7b8c9d
qory run register: run 0192f0c1-7d4e-7a2b-8c3d-4e5f6a7b8c9d, through the gateway gateway.example:8443, to start by 2026-10-11T12:10:00Z
The run key goes to the job as a secret: whoever holds it can act for this run until the run ends. Nothing was written on this machine.
```

Give the job the run key as a secret. Capture it, never echo it: a CI masks only the values
it was told are secret, so register it as masked where the CI can.

Use a new run id for each attempt, a retry included; without `--run-id`, qory makes one,
and the second line names it. `--instance-id` names the runner the run takes a place on:
by default the run's id, for a runner used once. `--attach-within` is how long the
runner has to start the run, in whole seconds, such as `15m`; by default the server's
window, and never more than the server allows.

The labels and what the run is about go to `qory run register`: `--label`, `--kind`,
`--title`, `--subject` and `--details`. With a run key, `qory run` refuses them, and adds
no `forge` or `repository` of the checkout's: give them as labels.

On the runner, `qory run` given the run key goes through the gateway the run key names. The
runner holds no access key, and its `forager.yaml` names no server and no gateway. The job
hands qory the run key on a descriptor, here stdin:

```sh
printf %s "$run_key" | qory run --run-key-fd 0 -- -p "$prompt"
```

Keep the run key in a shell variable, as here, rather than export `QORY_RUN_KEY`:
`--run-key-fd` together with an exported `QORY_RUN_KEY` is refused.

A job step that holds the run key in its environment exposes it to an unwalled agent: the
agent runs as you, and can read that environment, whatever the variable is called. Behind a
gateway, run the agent with a wall. See [The run key](#the-run-key).

When the job is done, or to stop it: `qory run end "$run_id" --outcome failed --reason
checks_failed`. Without `--outcome`, the run is cancelled, with no outcome given. A run has
one outcome. See [How a run ends there](#how-a-run-ends-there).

`qory run register` refuses before it registers, exit 2: a `forager.yaml` without a server,
`forager.yaml defines no server to register the run with`; no `--run-key-fd`, `qory run
register writes the run key to a descriptor, and --run-key-fd names none: name one of 3 or
above, such as --run-key-fd 3`; a descriptor below 3, or one that is not open; and a
gateway's access key, once the server's configuration names a gateway's node: `this access
key is a gateway's, and a gateway registers no runs: register with the key of the node pool
the run belongs to`. When the server refuses the registration, qory says what the refusal
means and what to do, then Forager's words and the code:

| Code | What it means |
| --- | --- |
| `no_default_gateway` | the key's node is set to "Default gateway", and no default gateway serves its workspace: mark a gateway as default in Qory Apiary, or choose one for the node |
| `gateway_not_serving` | the gateway the key's node names no longer serves the node's workspace: choose another gateway for the node in Qory Apiary |
| `gateway_without_key` | the gateway the key's node goes through has no active access key, so no run could start there: enrol a key for the gateway, then register again |
| `node_is_direct` | the key's node is set to "Direct": each of its machines registers each run itself, and a starter registers none. Choose a gateway for the node in Qory Apiary |
| `gateway_key` | the access key is a gateway's, and a gateway registers no runs: register with the key of the node pool the run belongs to |
| `instance_limit` | the node's live instances have reached its limit, so the run is not registered: wait for a run to end, or have an owner or administrator in Qory Apiary clear an instance |
| `run_id_used` | the run id is already used by another run: leave out `--run-id`, or give a new one |

A run key that cannot be written once the run registered is said as `the run <run-id> is
registered, and its run key could not be written to --run-key-fd <n>: <err>; end the run
with qory run end <run-id>`.

The key is for the starter, so `enrol --print` leaves the server and the pin of this
machine's `forager.yaml` aside: it checks the code against `QORY_APIARY_PUBLIC_KEY`
when that is set. The key's name is still this machine's, and the key is active as soon
as the server answers. It keeps nothing, so an enrolment whose answer is lost or does
not verify cannot be retried: get a new code, and have the owner or administrator in
Qory Apiary revoke the key qory printed the fingerprint of, should Qory Apiary show it.

#### Stored secrets

When the server lists stored secrets for the machine's access key, qory keeps a marker, the
file `stored-secrets` beside `forager.yaml`. The marker stops no run. Stored secrets reach
only a run that signs with this machine's access key, and on a machine that holds that
key, qory starts no run without a wall: see [An access key
needs a wall](#an-access-key-needs-a-wall). With the secret from `access-key-secret`, a
server's configuration that lists no stored secrets removes the marker. With the secret
from `QORY_ACCESS_KEY_SECRET` or `--access-key-secret-fd`, the marker stays as it is.

`qory access-key enrol` writes the marker before it makes a key, unless `--print`, since
the new key may receive stored secrets. It is removed as above.

#### An access key needs a wall

On a machine that holds an access key, every run needs a wall. An unwalled run is refused before
it starts, `key_needs_wall`, whether the key comes from `access-key-secret` or a key file beside
it, from `QORY_ACCESS_KEY_SECRET`, and, against the server, from `--access-key-secret-fd` too.
`--local` and a `forager.yaml` without a server are included, except for a key given by
`--access-key-secret-fd` alone. To run agents without a wall, keep the key with a starter and
run the machine behind a gateway: see [For a starter](#for-a-starter).

The refusal says where the key is:

```text
/home/you/.config/qory/access-key-secret holds this machine's access key, so every run needs a wall: --wall docker, or wall in forager.yaml; or keep the key with a starter, and run this machine without it, behind a gateway (key_needs_wall)
```

It begins `QORY_ACCESS_KEY_SECRET holds an access key` for the variable, and
`--access-key-secret-fd gives an access key` for the descriptor. A key file beside
`access-key-secret` is `access-key-secret.new`, `access-key-secret.replaced`, or a secret
enrol moved aside, `access-key-secret.old.<Unix time>`: an unwalled agent could read each,
and the refusal names the one it found.

`QORY_ACCESS_KEY_SECRET` refuses an unwalled `--local` run too, though the run contacts no
server: on macOS the agent can read the environment qory started with, and on Linux the
starting environment of the program that started qory. A key given by
`--access-key-secret-fd` alone does not: qory reads the descriptor and closes it before
anything starts, so an unwalled run that contacts no server, `--local` or one whose
`forager.yaml` has no server, may have it. An unwalled run against the server with that key
is refused.

### A checkout's labels

The server chooses a run's policy by the run's labels. qory takes two of them from the
checkout's origin remote, `forge` and `repository`, and an agent can change the remote by
rewriting `.git/config`. So qory keeps the two labels the origin remote gives at the
checkout's first run the server accepts, directly or through `gateway.server.via`. They
are kept in `labels/` beside `forager.yaml`, mode 0700, one file per checkout, mode 0600,
named by the SHA-256 of the checkout's real path. A run the server refuses keeps nothing.
An agent without a wall runs as you and can rewrite `labels/` too, so the labels hold for
walled runs.

A later run whose origin remote gives other labels does not start. It is refused before
anything starts or registers, `labels_changed`, exit 2, and no event is written:

```text
the checkout /home/you/src/app first ran against the server as forge git.example.com, repository acme/app, and its origin remote now gives forge git.example.com, repository acme/shop; the server chooses a run's policy by its labels, so the run does not start: if you changed the remote, run the same command with --relabel (labels_changed)
```

A checkout without an origin remote reads `no forge, no repository`. If you changed the
remote, run the same command with `--relabel`: the run starts, and once the server
accepts it, qory keeps the new labels and says so:

```text
qory run: the checkout's labels are now forge git.example.com, repository acme/shop
```

A label given with `--label` is used as given: it is neither checked nor kept. A run
with `--local`, a run without a server and a run given a run key neither check nor keep
labels: behind a gateway its run key names, the run's starter chose its labels.
`--relabel` is refused with `--local` and with a run key. When the labels cannot be
kept, qory says so in one line, and the run goes on.

## Through a separate gateway

A run goes through a separate gateway when it is given a run key, by `--run-key-fd` or
`QORY_RUN_KEY`, or when `forager.yaml` sets `gateway.server.via` on a machine with a separate
access key. qory then starts no gateway itself. The run key names the gateway; no setting
does.

The gateway is `qory gateway` on another machine, or a service on this one: see [the
gateway as a service](gateway.md). The session reaches it over TLS 1.3, at the address the
run key names, and every request carries the run key. The proxy, the policy, the
credentials and the access key are the gateway's, on its machine, and the gateway reports
the run to Qory Apiary. The run's policy selects its credentials. When it selects none, the
gateway can select them for a run, with a wall or without, by the run's labels: see
[gateway.selects](gateway.md#the-machines-behind-it). Without a run key or
`gateway.server.via`, nothing changes: `qory run` starts a gateway itself for each run.

The run's first line names the gateway and the run:

```text
qory run: through the gateway gateway.example:8443, run 0192…
```

### The run key

A run's starter gets the run key from Qory Apiary with `qory run register`, and gives it to
the job: see [For a starter](#for-a-starter). It names the run and the gateway. `qory run`
takes it from the descriptor `--run-key-fd` names, or from `QORY_RUN_KEY`:

- `--run-key-fd <n>`: 0 for stdin, or a descriptor of 3 or above. qory reads it to its end
  and closes it before anything starts, so nothing it starts inherits it. With 0, the
  agent's stdin is empty.
- `QORY_RUN_KEY`: qory takes it out of its environment when it starts, as it does the access
  key's variables, so no program it starts receives it. `--env` and `wall.env` refuse it.
  Only a walled run takes it.

Giving it both ways is refused: `--run-key-fd and QORY_RUN_KEY both give a run key; give it
one way`. White space around the key is left out.

An unwalled agent behind a gateway holds at most the run key. qory takes `QORY_RUN_KEY` out
of the environment the agent gets. On Linux, an agent that runs as you without
`CAP_SYS_PTRACE` reads neither qory's environment nor its memory: qory is not dumpable. But
it can read, through `/proc`, the environment that the program that started qory started
with, such as a job step's shell, and that environment may contain `QORY_RUN_KEY`. The agent
can also read the environment of any other process of yours that started with
`QORY_RUN_KEY` set. On macOS, an agent that runs as you can read the environment qory
started with. Within the live run, whoever holds the run key can post events and open proxy
connections as the session, for that run only. A wall keeps it out.

So an unwalled run with `QORY_RUN_KEY` set is refused before anything starts:

```text
QORY_RUN_KEY is in the environment qory started with, which an unwalled agent can read (on macOS always, on Linux through the program that started qory), so a run with it needs a wall: --wall docker, or wall in forager.yaml; or give the run key with --run-key-fd, such as --run-key-fd 0 for stdin (run_key_needs_wall)
```

`--run-key-fd` keeps the run key out of qory's environment, not out of the job's: see [For a
starter](#for-a-starter). Behind a gateway, run the agent with a wall.

The run key never appears in qory's output. A value that is not a run key is refused, exit
2, naming the part that is wrong and never the value: `QORY_RUN_KEY is not a run key:
<reason>`, or `--run-key-fd <n> gives no run key: <reason>`.

The run's id is the run key's. A `--run-id` that differs is refused, in Forager's words:
`the run id <id> is not the one QORY_RUN_KEY names, <key's id>`.

The run's labels and what it is about are its registration's, which its starter made:
`--label`, `--kind`, `--title`, `--subject` and `--details` are refused, such as `--kind and
--title go into the run's registration, and a run key is given, by QORY_RUN_KEY or
--run-key-fd: its starter registered the run already, so give them to qory run register`.
qory sends no `forge` or `repository` of the checkout's either: the starter gives them as
labels.

A run given a run key starts Claude Code with `--setting-sources user`,
`--strict-mcp-config` and `--mcp-config` with the harness's MCP list, or an empty one when
the harness has none: Claude Code reads no settings of the checkout's `.claude` and no
`.mcp.json`, and loads no MCP server but those `--mcp-config` names. Other runtimes get
none of these flags. For any runtime, a run given a run key reads no `git`, `env` or
machine key under `harness`, such as `launch` or `home`, from the checkout's `qory.yaml`:
your `qory.yaml` and the files above the checkout set those. Without a wall, Claude Code
reads the settings, `CLAUDE.md`, plugins and memory of the user running qory, as
`--setting-sources user` reads them. Behind a wall, `CLAUDE_CONFIG_DIR` is
`/tmp/qory-agent-home` in the container, an empty folder of mode 0700 that qory makes after
the commands the repository's configuration runs, last before Claude Code starts; the run is
refused when anything is at that path then. The model key comes through the gateway, so
the empty folder needs no login. `HOME` stays the image's. Claude Code starts through
qory, which removes the image's values of the variables named `ANTHROPIC_*`, `CLAUDE_*`,
`QORY_*`, `MCP_*` and `OTEL_*`, of `NODE_OPTIONS`, `NODE_PATH`, `LD_PRELOAD`, `LD_AUDIT`,
`BASH_ENV` and `ENV`, of `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY` and `ALL_PROXY` in either
case, and of `SSL_CERT_FILE`, `SSL_CERT_DIR` and `NODE_EXTRA_CA_CERTS`; the values Forager,
the wall and the server set for the run stay, and so does a value of a variable named
`ANTHROPIC_*`, `CLAUDE_*`, `QORY_*`, `MCP_*` or `OTEL_*` that is the gateway's placeholder.
Behind a wall, the run is also refused when the image has `/etc/claude-code`, where Claude
Code reads managed settings, and when the harness home is inside the checkout: compose it
outside with `--home`.

These checks guard against what the run's image and the repository's files leave in place: a
configuration folder, managed settings or variables that change what Claude Code does. They
also refuse a harness home that the machine's settings put inside the checkout, where the
repository decides what is there. They do not hold against a repository that acts against
them. The image's programs, and the commands the repository's configuration runs, run as the
agent's user in the agent's container, so they can change what Claude Code reads after qory
checked it, such as through a program they leave running. Against a hostile repository, the
boundary is the wall: the container reaches out only through the gateway, the credentials
stay at the gateway, outside the container, and the gateway starts the tools `gateway.tools`
defines, outside the container too; Claude Code's shell and edit tools run inside it. The
run's record is written outside the container, and what Forager and the gateway record, the
run's start and end and its egress, cannot be changed from inside it. The hook events come
from inside the container, so a program left running there can send hook events Claude Code
did not make.

### A machine with a separate access key, behind a gateway

A machine that holds its node's access key, and reaches Qory Apiary only through a
gateway, names that gateway in `gateway.server.via`. qory then registers each run itself,
through the gateway, and the run goes through the gateway its run key names, as a run its
starter registered does. Such a machine holds an access key, so it runs walled only: an
unwalled run is refused, `key_needs_wall`.

```yaml
# ~/.config/qory/forager.yaml, on a machine with a separate access key, behind a gateway
apiVersion: qory.dev/v1alpha1
gateway:
  server:
    url: https://apiary.example
    access_key_id: ak_0123456789abcdef
    apiary_public_key:
      - {alg: ed25519, public_key: mptNqtgGKgLhLZxmOGfpBQkdeBNH7QN3Qs9ETNumy8Q}
    via:                                   # optional: reach the server through this gateway
      url: https://gateway.example:8443
      ca_file: gateway-ca.pem              # optional
      certificate_sha256: 47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=   # optional
wall:
  adapter: docker
  image: qory-agent
```

| Key | What it is |
|---|---|
| `gateway.server.via` | The gateway this machine reaches the server through. Without it, the server is reached directly. |
| `gateway.server.via.url` | The gateway: an https URL of a host and an optional port, with nothing after. Required in `gateway.server.via`. |
| `gateway.server.via.ca_file` | A PEM file of the authorities the gateway's certificate chains to, in place of the system's roots. qory reads it relative to forager.yaml's directory, unless it is absolute. |
| `gateway.server.via.certificate_sha256` | Optional: the SHA-256 of the gateway certificate's public key, in standard base64. With it, only a certificate with that key is accepted, and its chain is still checked. |

`qory config` lists each value; without `gateway.server.via` it shows
`gateway.server.via.url  (none: the server is reached directly)  default`.

Enrol the machine's key through the gateway with `--via`:

```sh
qory access-key enrol --via https://gateway.example:8443 https://apiary.example qecn_…
```

The enrolment goes through the gateway, and enrol writes `gateway.server.via` beside the key:
`wrote gateway.server.url, gateway.server.access_key_id, gateway.server.apiary_public_key
and gateway.server.via.url to <path>`. `--via-ca-file` and `--via-certificate-sha256` write
`ca_file`, as an absolute path, and `certificate_sha256`, and the line names each one written,
such as `wrote gateway.server.url, gateway.server.access_key_id,
gateway.server.apiary_public_key, gateway.server.via.url and gateway.server.via.ca_file to
<path>`. Without `--via`, enrol, `--replace` included, goes through the gateway
`gateway.server.via` names, when the file names one.

Each run reads the server's configuration through the gateway, prints `qory run: node <id>,
instance <id>`, and registers with its labels, the checkout's forge and repository among
them, and what it is about. The server's answer carries the run key. qory keeps it in
`run-keys/<run-id>` beside `forager.yaml`, mode 0600, in a directory of mode 0700: outside
the run's record, and out of every wall's mounts. Then it says `qory run: through the
gateway <host>, run <id>`. qory removes the kept key once nothing of the run is left to
send, or once the gateway says the run has ended; until then, `qory run resend <run-id>`
reads it from there, with no `QORY_RUN_KEY`. A run key that cannot be kept stops the run:
`the run <id> is registered, and its run key could not be kept beside forager.yaml, so the
run does not start: <err>`.

Such a machine starts no gateway itself, so the rest of the `gateway` section does
nothing there: `qory run` refuses each key of it beside `gateway.server`, such as
`gateway.egress`, `gateway.credentials`, `gateway.integrations`, `gateway.listen` and
`gateway.tls`, before anything starts or registers, exit 2, one line per key in
alphabetical order: `<file>: gateway.<key> does nothing on this machine: gateway.server.via
sends its runs through a gateway, which holds their policy, credentials, tools and
integrations; remove gateway.<key>, or remove gateway.server.via`. `qory run --local`
starts a gateway itself, which reads their policy, credentials, tools and integrations, so
it is not refused.

When the server refuses the registration, qory says what the refusal means and what to do,
then Forager's words and the code:

| Code | What it means |
| --- | --- |
| `node_is_direct` | this machine's node is set to "Direct", and `gateway.server.via` names a gateway: remove `gateway.server.via`, or choose a gateway for the node in Qory Apiary |
| `node_uses_gateway` | on a machine without `gateway.server.via`: this machine's node goes through a gateway; set `gateway.server.via.url` to the gateway's address, or set the node to "Direct" in Qory Apiary |
| `no_default_gateway`, `gateway_not_serving`, `gateway_without_key` | as for [`qory run register`](#for-a-starter), said of this machine's node |

### What is refused

Before anything starts, `qory run` given a run key refuses:

- `--local runs with a gateway the run starts itself and no server, and a run key is given, by
  QORY_RUN_KEY or --run-key-fd: the run goes through the gateway its run key names; give no
  run key to run --local`
- `--policy is a run's policy file for a gateway the run starts itself, and a run key is
  given, by QORY_RUN_KEY or --run-key-fd: the gateway its run key names takes the run's
  policy from the server`
- `<file> exists, and a run key is given, by QORY_RUN_KEY or --run-key-fd: a machine whose
  runs a starter registers holds no access key; remove the file`, for `access-key-secret`
  or a key file beside it
- `<variable> is set, and a run key is given, by QORY_RUN_KEY or --run-key-fd: a machine
  whose runs a starter registers holds no access key; unset <variable>`, for
  `QORY_ACCESS_KEY_ID`, `QORY_ACCESS_KEY_SECRET` and `QORY_APIARY_PUBLIC_KEY`
- `--access-key-secret-fd and a run key are both given: a machine whose runs a starter
  registers holds no access key; leave out --access-key-secret-fd`
- `<file>: gateway: a run key is given, by QORY_RUN_KEY or --run-key-fd, so the run goes
  through the gateway its run key names, which holds the server, the policy and the
  credentials, and this machine runs no gateway: remove the gateway section, or give no
  run key`
- `--label`, `--kind`, `--title`, `--subject` and `--details`, as [The run key](#the-run-key)
  says
- an unwalled run with `QORY_RUN_KEY` set, `run_key_needs_wall`
- `qory run: <variable> is the model's credential, which a walled run given a run key takes
  only through the gateway; remove it from --env`, or `...; remove it from wall.env in
  forager.yaml`, for a variable the runtime reads its model key from: `ANTHROPIC_API_KEY`,
  `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_AUTH_TOKEN` for Claude Code
- `--run-key-fd <n>: the standard output and error carry no run key; name 0 for stdin, or
  a descriptor of 3 or above`, `--run-key-fd <n>: more than a run key`, and `--run-key-fd
  <n>: <err>` for a descriptor qory cannot read

A machine with a separate access key, behind a gateway, registers each run itself, so a run key given to
it is refused by the same lines.

A run that ended before its runtime started says `✗ the run did not start: it has ended
already`.

### How a run ends there

The run's starter ends the run with `qory run end` while it runs, with an outcome and a
reason, or with neither:

```sh
qory run end "$run_id" --outcome completed --reason all_checks_passed
qory run end "$run_id" --outcome failed --reason checks_failed
qory run end "$run_id"                                   # cancelled, with no outcome given
```

`--outcome` is `completed`, `failed` or `cancelled`. `--reason` is a code of yours, such as
`checks_failed`: a lower-case letter, then lower-case letters, digits and `_`, at most 64,
and none of Forager's reasons. It needs `--outcome`. The request is signed with the
access key of the node pool that registered the run, read as `qory run register` reads it,
and goes through `gateway.server.via` when that is set. The server records the outcome, and
a run that is still running ends: its runtime is stopped. A run has one outcome: the same
one again gets the same answer, and a different one is refused. qory says what the server
records:

```text
qory run end: node np_f1xt0re000000000, instance 0192f0c1-7d4e-7a2b-8c3d-4e5f6a7b8c9d
✓ the server records the run 0192f0c1-7d4e-7a2b-8c3d-4e5f6a7b8c9d as completed: all checks passed
```

Without `--outcome`, it says `as cancelled, with no outcome given`. A gateway's access key
is refused before anything is sent: `this access key is a gateway's, and a gateway gives no
run's outcome: use the key of the node pool that registered the run`. When the server
refuses, qory says:

- `the run <id> has another outcome already, and a run has one`, then Forager's line with
  `outcome_given`;
- `the server knows no run <id> of this key's node: it registered none of that id, or no
  longer keeps its record`, for a run the server does not know;
- `an answer of the server does not verify under the pinned apiary_public_key, so qory
  cannot tell whether the outcome was recorded: check gateway.server.url and the pin, then
  run the same command again`.

On the runner, `qory run` says the outcome, then the reason: the starter's as it gave it,
with spaces for underscores. The exit status is 0 when the run completed, and 1 otherwise:

- `✓ the run completed: all checks passed, and <runtime> was stopped`;
- `✗ the run failed: <reason>, and <runtime> was stopped`;
- `✗ the run was cancelled: <reason>, and <runtime> was stopped`;
- without a reason, `: <reason>` is left out, such as `✓ the run completed, and
  <runtime> was stopped`;
- without an outcome, `✗ the run was cancelled, with no outcome given, and <runtime> was
  stopped`.

### Resending through the gateway

`qory run resend <run-id>` sends the gateway what it did not accept of the run's record:
after a session that died, or a gateway that was out of reach. It reaches the gateway with
the run's run key: from `--run-key-fd` or `QORY_RUN_KEY`, else, on a machine that
registered the run itself, the run key qory kept for it. `QORY_RUN_KEY` needs no wall here:
resend starts no agent. The run directory records what the gateway accepted, and only the
rest is sent. It doesn't close the record: the gateway ends a run whose session it lost. It
removes the containers and networks the run's wall left, as without a gateway.

- `<n> events were accepted; nothing is left to send to the gateway`: exit 0.
- `the gateway never opened run <id>, so there is nothing to send; its record stays in
  <dir>`: exit 0, for a run the gateway never opened, one it refused at its start, say.
- `<n> events were accepted and <m> were not; <dir>/undelivered contains them`: the
  gateway didn't accept them within `--wait`.
- `the run has ended (<outcome>[: <reason>]), so no more of its events are taken; they
  stay in <dir>`, such as `the run has ended (completed: all checks passed), so no more
  of its events are taken; they stay in <dir>`: the outcome is `completed`, `failed`,
  `cancelled` or `lost`, and the reason is said as `qory run` says it.

Each but the first two is exit 1, and the events stay in the run directory. Without a run
key, Forager says `resend <dir>: the run key is needed to send to its gateway; set
QORY_RUN_KEY`, exit 2. A run that started its gateway itself on this machine is refused with a run
key given: `the run <id> started its gateway itself on this machine, so its
record goes to the server, not to a gateway: resend it without a run key`. With
`gateway.server.via` set, such a run is refused too: qory sends a record to the server
through no gateway, so resend it with a `forager.yaml` without `gateway.server.via`.

## Runs started by another system

A system that starts runs sets their id and labels, and says what each is
about. Start these runs with a wall: see [When to run without a
wall](#when-to-run-without-a-wall).

The system passes each run its policy:

```sh
qory run --run-id "$uuid" --label job=1234 --label issue=77 \
  --policy /etc/qory/policy.yaml --timeout 5h30m --stop-grace 30s -- -p "$prompt"
```

- `--run-id` is a new UUID, in lower case, for each attempt, a retry included. A caller
  that has only a key from its system for the work, such as `job` here, passes it as a
  label, never as the run id.
- The labels go into `dev.qory.run.started`, and into the run's registration.
  There, a server ties the run to records it keeps and chooses its policy.

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
| `--details` | a JSON object of yours, from a file or stdin             | 8192 bytes compacted, 4 levels deep |

Each is optional. An empty `--kind`, `--title` or `--details` is none. They go into
`about` on `dev.qory.run.started`, and no other event repeats them. The run's
registration carries them too, but a server never chooses the run's policy by them. qory
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

### A run's policy file

`--policy` passes one run's policy file. It is in the Forager contract's format. Keep it
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

`gateway.egress.paths` lists the paths a run may request on a host, as a policy's
`egress.paths`. They apply to every run on the machine. With no server and no `--policy`,
they are the run's path rules. A run's policy from `--policy` or from the server keeps its
path rules, and a request to a host either side lists must match an entry of each side
that lists it; the run's mode decides what a miss does. Path rules need the proxy to read
the requests, so with `gateway.egress.paths` set, every run on the machine needs a wall.

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
repository. A policy defines no credential.

```yaml
# ~/.config/qory/forager.yaml
gateway:
  credentials:
    model:                                  # a model key from qory run's environment
      env: CLAUDE_CODE_OAUTH_TOKEN
      format: anthropic-messages
      endpoint: https://api.anthropic.com
      auth: {scheme: bearer, oauth: true}
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
- Its proxy sets the token on the requests to the hosts the token is for. A model key's
  host is its endpoint's host.
- Where a program wants a credential set, the container gets a placeholder. It never gets
  the token.

Without a wall, the run gets the same placeholders, and the proxy sets the same tokens.
Without a wall, qory and the agent run as the same user. So the agent can read the run's
credentials: an `env` credential from the environment qory started with on macOS, or on
Linux from the starting environment of the program that started qory; a `file` credential
from its file; and an adapter's credential by running the adapter. It can also read every
other credential this machine holds, and a program that ignores the proxy reaches the
hosts without the token. If you run without a wall, run the gateway on another machine, as
in [Through a separate gateway](#through-a-separate-gateway), on a machine that holds no
other credential.

### Adapters

An **adapter** is a program of yours, written for one kind of host, such as a source code
host. It runs outside the container. It prints:

- the token,
- the token's expiry,
- the hosts, the scheme and the paths the token is for.

So `qory` defines no host itself. `argument` is the pattern the policy's argument must
match whole, and the gateway puts the argument where the adapter's words say
`${argument}`. `hosts` and `paths` on an adapter's entry are the most its answer may claim.
An [integration](#integrations) is an adapter that describes itself.

### The model key

A **model key** is the key of a model API. Claude Code reads it from one of three
variables, chosen by how the key is set:

| `auth`                                | Claude Code's variable                                       |
| ------------------------------------- | ------------------------------------------------------------ |
| `{scheme: header, header: x-api-key}` | `ANTHROPIC_API_KEY`                                          |
| `{scheme: bearer}`                    | `ANTHROPIC_AUTH_TOKEN`                                       |
| `{scheme: bearer, oauth: true}`       | `CLAUDE_CODE_OAUTH_TOKEN`, a token from `claude setup-token` |

A walled run gets it one of two ways:

- **Passed in.** `wall.env` or `--env` names the variable, and the agent holds the
  credential inside the container. A run given a run key refuses this: it takes the
  credential only through the gateway its run key names. See [What is
  refused](#what-is-refused).
- **Kept outside.** A model key in `forager.yaml`, which the run's policy selects:

```yaml
# ~/.config/qory/forager.yaml
gateway:
  credentials:
    model:
      env: CLAUDE_CODE_OAUTH_TOKEN          # read once at run start
      format: anthropic-messages            # the API the endpoint serves
      endpoint: https://api.anthropic.com
      auth: {scheme: bearer, oauth: true}   # for ANTHROPIC_API_KEY: {scheme: header, header: x-api-key}
      key_prefixes: [sk-ant-]               # optional
```

- `format` is one of `anthropic-messages`, `openai-responses`, `openai-chat` and
  `gemini`.
- `endpoint` is the base URL of the model API: `https`, a host, an optional port and path,
  and no user, query or fragment. The proxy sets the key on every path of the endpoint's
  host, and on no other host. Under `enforce`, the run's allow list must cover that host.
- `auth` is `{scheme: bearer}`, `{scheme: header, header: …}` or
  `{scheme: basic, username: …}`. Add `oauth: true` to a bearer for an OAuth token.
- `key_prefixes`, optional, are 1 to 4 prefixes, each 1 to 32 printable ASCII characters
  without a space. If the key starts with none of them, the run does not start, and the
  message does not contain the key.
- A model key has no `hosts`, `paths` or `placeholders`.

What the run gets:

- Inside the container, the variable for the key's format and `auth` holds a placeholder,
  such as `qory-model-sets-the-credential-outside-the-enclosure` for the key `model`.
  `ANTHROPIC_BASE_URL` holds the endpoint. Claude Code's other key variables are unset
  there.
- A run that passes a value for the key's variable does not start. If the run is walled
  and the variable is the one `env` reads, such as `CLAUDE_CODE_OAUTH_TOKEN` in the
  example, the code is `variable_reserved`; otherwise it is `placeholder_conflict`. So
  once a model key is kept outside, `wall.env` and `--env` no longer name its variable.
- A credential without `format` whose `placeholders` list one of Claude Code's key
  variables stops the run, `run_configuration_invalid`. Write it as a model key.

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

### Keep the gateway's memory out of swap

On Linux, `gateway.lock_memory: true` in `forager.yaml` keeps every page of the gateway's
process in RAM, so that no key the gateway holds is written to swap. A `qory run` with no
separate gateway starts the gateway in the `qory run` process, so the lock covers that whole
process. Through a separate gateway, the lock is set by that gateway's `forager.yaml`: see
[Through a separate gateway](#through-a-separate-gateway). The default is `false`.

1. Set the locked-memory limit of the process to unlimited: `LimitMEMLOCK=infinity` in a
   systemd unit, `ulimit -l unlimited` in a shell, or `--ulimit memlock=-1` in Docker. Or
   give the process the capability `CAP_IPC_LOCK`, which lifts the limit.
2. Set `gateway.lock_memory: true`.

If the lock fails, the gateway does not start, and the error starts with the fix, such as
`raise RLIMIT_MEMLOCK to unlimited: LimitMEMLOCK=infinity in a systemd unit, or ulimit -l
unlimited`. On any other system, `true` stops the gateway: `set gateway.lock_memory to
false: Forager locks memory only on Linux, and this system is darwin`.

### TLS on credential hosts

For the hosts of the credentials and tools the policy selects and the hosts with path
rules, and no other, the proxy ends the container's TLS itself. It uses an authority made
for the run. The authority's key never leaves the gateway.

The container receives one bundle to trust: its image's authorities, and the run's
certificate. The bundle goes in through these variables:

- `SSL_CERT_FILE`
- `GIT_SSL_CAINFO`
- `NODE_EXTRA_CA_CERTS`
- `REQUESTS_CA_BUNDLE`
- `CURL_CA_BUNDLE`
- `AWS_CA_BUNDLE`

Or through the variables `wall.ca_env` lists.

A run without a wall gets the same variables, `wall.ca_env` included. They point at
`ca-bundle.pem` in the run's directory: this machine's authorities and the run's
certificate. Forager reads the authorities from the first of
`/etc/ssl/certs/ca-certificates.crt`, `/etc/pki/tls/certs/ca-bundle.crt`,
`/etc/ssl/ca-bundle.pem` and `/etc/ssl/cert.pem` that this machine has. With none of them,
the run stops.

The record lists the terminated hosts. For each request to one, it lists the method, the
path and `credential`, the name of the credential whose token it carried.

Every other host stays a tunnel that nobody reads.

## Tools the agent reaches

A **tool** is a program of yours that serves hosts, for what a run reaches that needs more
than a token in a header: a request signed with a key that stays outside the container, or
a service only this machine has. `forager.yaml` defines the tools the machine has, under
`gateway.tools`, each by its name. A run's policy selects among them by name, with an
argument, as it selects credentials. A policy defines no tool.

```yaml
# ~/.config/qory/forager.yaml
gateway:
  tools:
    files:
      command: [/opt/tools/files, --prefix, "${argument}"]
      argument: '[a-z0-9-]+/[a-z0-9-]+'
      serves: [files.tools.internal]
```

```yaml
# the run's policy, passed with --policy
version: 1
egress:
  mode: enforce
  allow: [files.tools.internal]
tools:
  - {name: files, argument: acme/shop}
```

- `command` is the program, by its absolute path, and its arguments. The gateway puts the
  policy's argument where an argument says `${argument}`.
- `argument` is the pattern the policy's argument must match whole. Without it, a policy
  passes none.
- `serves` are the hosts whose requests go to the tool, at least one. A host need not
  exist: the proxy never dials it, so a name under `.internal` serves.
- Each of `placeholders` is a variable that holds a placeholder inside the container.

How it works:

- Before the agent starts, Forager's gateway starts each tool the policy selects, outside
  the container. The tool listens on the Unix socket `QORY_TOOL_LISTEN` names.
- The proxy ends the container's TLS for the hosts the tool serves, decides the host and
  the path by the policy, and hands the tool each request it allows.
- The record lists each request the proxy hands to a tool in `dev.qory.run.egress`, with
  `tool`, the tool's name.

A run whose policy selects a tool needs a wall; without one it does not start. Under
`enforce`, the run's allow list must cover each host the tool serves, or the run does not
start.

With a server, the server's run configuration selects the tools: through a server, a tool
is reached only once the server's run configuration selects it. Until then, a run reaches a
tool through `--policy`, on a machine without a server or with `--local`.

`qory config` lists each tool by its program and the hosts it serves. What a tool receives
is in the [Forager
contract](https://github.com/qoryai/forager/blob/v0.7.0/contracts/forager/v1/README.md#tools).

## Integrations

An **integration** is a program that connects a run to an outside system, and describes
itself. It gives a run a credential through its credential role: a token minted for the
run, which the agent must not hold. The program runs on your machine, outside the wall.
The agent never holds its secrets, or the token it mints.

- Qory publishes integrations, each in a separate repository, such as
  [`qory-github`](https://github.com/qoryai/qory-github/tree/v0.2.0). Its integration
  is named `github`.
- A program of yours starts from the
  [integration template](https://github.com/qoryai/integration-template/tree/v0.1.0).

Both are declared and checked the same way. The rules a program follows are the
[integration
contract](https://github.com/qoryai/integrations/tree/v0.3.0/contracts/integration/v1).

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
| `program_version` | the program's version, a string. `qory run` and `qory config` show it |
| `settings` | a JSON Schema, draft 2020-12, of the settings the program takes. A property marked `writeOnly` is a secret: `qory` refuses its value, and takes `<name>_file` in its place, the path of a file that holds it |
| `roles` | the roles it plays, each under its role's name, such as `credential`. `qory` expands the `credential` role and leaves the others as they are. An integration that plays no role `qory` expands is refused |

The **credential** role mints or fetches a token for the run's argument. `argument`,
required, is the RE2 pattern the policy's argument must match whole. `hosts` are the most
hosts the token may be set on.

The description of `github`, shortened. Its argument is one repository or several of one
owner, separated by commas:

```json
{"version": 1, "name": "github", "title": "GitHub", "program_version": "0.2.0",
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

`qory run` prints the program it found, and its version, on a separate line:

```
qory run: integration github: /usr/local/bin/qory-github 0.2.0
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

- `qory run` and `qory config` print a separate line that says so, such as:

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
| Forager | the policy's argument does not match the credential's `argument` whole |
| Forager | two credentials claim the same host |
| Forager | under `enforce`, a host of a credential that the allow list does not cover |
| Forager | the adapter does not answer within a minute, fails, or prints a document the Forager contract refuses |
| Forager | the adapter's answer claims a host or a path above the entry's `hosts` and `paths` |
| Forager | the run passes a value for a placeholder, `placeholder_conflict` |

`qory run` prints why the run did not start. The
[Forager contract](https://github.com/qoryai/forager/tree/v0.7.0/contracts/forager/v1) has
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
happened before it. Behind a gateway, it sends the record to that gateway, with the run key:
see [Resending through the gateway](#resending-through-the-gateway).

The run is selected by its id: its folder in this checkout's run records, as [The
record](#the-record) names it. The
server's configuration is fetched first, signed. It defines where the events go.

- The run directory records what the server accepted. Only the rest is sent, in order.
  Nothing the server accepted is sent again.
- A server may still see an event twice. It discards the copy by the event's id.
- After a Forager process that died, it first closes the record, unless the server never
  opened the run: `dev.qory.run.exited` with `reason: gateway_lost`, and it says `the
  record had no end, and now ends as lost: its end was never recorded`. It also removes
  the containers and networks the run's wall left.
- A run the server never opened, whose registration it never accepted, or a run that
  had no server, is sent nothing, and its record stays as it is: `the server never
  opened run <id>, so there is nothing to send; its record stays in <dir>`, exit 0.
- It refuses a run that is running.
- It keeps sending until the server accepts, or `--wait` is over. The wait is two
  minutes unless set.

The exit status is 0 when the server has everything, or never opened the run. It is 1
when events remain, or when the server said stop. Events the server did not accept within
`--wait` stay under the run directory's `undelivered`; after the server's stop, they stay
in the run directory.

The formats are in the
[Forager contract](https://github.com/qoryai/forager/tree/v0.7.0/contracts/forager/v1).

## The wall

The proxy sees only programs that honour it. A **wall** makes the rest fail.

Turn it on with a `wall` section, or with `--wall docker --image <image>` for one run.
`--wall none` runs one run without the section's wall; on a machine that holds an access
key, such a run is refused (see [An access key needs a wall](#an-access-key-needs-a-wall)).
Run without a wall, by `--wall none` or with no `wall` section in `forager.yaml`, only for
an agent you start yourself on your machine: see [When to run without a
wall](#when-to-run-without-a-wall).

With a wall:

- The runtime starts in a container, on a network with no route out.
- It reaches the proxy, and nothing else, through a relay.
- The container sees the checkout and the composed home, at the same paths as on
  your machine, and the run's record directory, read-only. A run of Claude Code given a
  run key also sees a folder that holds the names of the variables its agent keeps,
  read-only. It sees nothing else of your machine.
- Of your environment, the container gets the ones `wall.env` or `--env` lists, and
  nothing else. The harness's launch variables, fixed and defaults, reach the agent in
  the container as they do outside it. See [A run's variables](#a-runs-variables).
- Forager, the policy, the record, the access key's secret and the run key stay outside.

A wall needs the `docker` command, and an engine behind it. It also needs an image that
contains the runtime: Qory's, or yours FROM it. See [Qory's images](#qorys-images).
`wall.images` defines several, and a run's policy selects one: see [The agent's
images](#the-agents-images).

What to know:

- **The model key.** `wall.env` or `--env` passes it in, and the agent holds it.
  With a run key, such a run does not start: see [What is refused](#what-is-refused).
  A model key in `forager.yaml` keeps it outside: see [The model key](#the-model-key). A
  run that then passes a value for its variable does not start: `variable_reserved` when
  the key's `env` reads that variable, and `placeholder_conflict` otherwise.
  A subscription login kept in a Mac's Keychain does not reach a container. Use an API
  key, or a token from `claude setup-token`.
- **The helper on a Mac.** Inside the container, the relay, the hook forwarder and what
  starts nested Docker are `qory`'s Linux build, mounted read-only. On Linux, that is the binary you run. On a Mac, download the
  Linux archive of the same release, for your engine's architecture. Set `wall.helper` to
  that binary.
- **What the container sees.** The checkout it was started in, the run's record directory,
  read-only, and for a run of Claude Code given a run key, the folder of the names of the
  variables its agent keeps, read-only; no other directory. `--mount <path>[:ro]` or `wall.mounts` shows it another one, at the same path as on your machine, such as a
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
  - one of Forager's program and temporary files, such as a program it starts
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
  other writes there, and changes it where its place is writable: the two can clash
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
- **Your machine.** Behind a wall, the proxy reaches your machine, or a private network
  address, only for a host that `gateway.egress.allow` of your `forager.yaml` lists by
  name, in either mode. Through a separate gateway, the proxy runs on the gateway's
  machine: the rule covers that machine, and the list that counts is in the gateway's
  `forager.yaml`. See [The gateway's machine and private
  addresses](gateway.md#the-gateways-machine-and-private-addresses). It never reaches a
  link-local or cloud metadata address. For a local model endpoint or MCP server, list your machine's host name, and
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
- **Nothing runs by itself.** It has no entry point. Claude Code and `gh` neither update
  themselves nor ask for input, and Claude Code sends no telemetry or error reports,
  which a run under `enforce` would otherwise show as denied connections.
- **Docker.** `agent-docker` holds the daemon for nested Docker, under a
  runtime such as Sysbox. The image starts nothing: define it in `wall.images` with
  `runtime: sysbox-runc` and `docker: true`, and `qory run nest` starts the daemon. See
  [Nested Docker](#nested-docker).
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
  is setuid or setgid or has file capabilities.
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
    go-docker:              # experimental: nested Docker
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
- A run whose `--policy` selects an image needs no default, unless `wall.images_allow`
  is set. A machine that reports to a server sets `wall.image`: the server's run
  configuration arrives once the run starts, and may select none.
- `wall.images_allow` lists where a run's image may come from when the run's policy
  names it by a reference instead of a name of `wall.images`: the prefixes of the
  references the machine runs, such as `registry.example/agents/`. The reference must
  be pinned by digest, `NAME@sha256:` and 64 lower-case hexadecimal digits. The list
  needs a default, `wall.image` or `--image`, whose runtime such an image starts under.
  Without the list, a run whose policy names an image by reference is refused.
- `qory config` lists the images, and shows whether `wall.image` is a name of
  `wall.images` or a reference. A mistyped name shows as a reference.

Before the run starts, `qory run` refuses a `--policy` that selects an image by a name
`wall.images` does not define, or by a reference not pinned by digest, or one for a run
without a wall. A `--policy` that names a reference pinned by digest goes to the run: a
reference under none of `wall.images_allow` stops the run as it starts,
`image_not_allowed`, and the run is recorded. A server's run configuration that selects
an image `wall.images` does not define, or a reference under none of the prefixes, stops
the run as it starts. Every command that
reads `forager.yaml` refuses `docker: true` without a `runtime`.

`dev.qory.run.started` names the image the run started in: `image`, the reference. For an
image of `wall.images` it also has `image_name`, and `container_runtime` and
`docker: true` when they apply. `dev.qory.run.policy_applied` records `image` when the
policy selects one.

### Nested Docker

*Experimental: it may change.*

With `docker: true`, the agent gets nested Docker: a Docker daemon inside the wall,
separate from the machine's. Its tests can start a database. It can build and run images.

Define an image with `docker: true` only where every run may get one: any policy can
select it.

It needs Docker Engine 28 or later, with [Sysbox](https://github.com/nestybox/sysbox)
registered as `sysbox-runc`, and `runtime: sysbox-runc` on the image; with an older
engine, the run is refused before it starts. Sysbox gives the container a separate root,
a user of your machine that is not root. A Mac's engine has no Sysbox: run these
images on a Linux machine that has it.

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
  decided and recorded like the agent's traffic.
- The daemon pulls through the proxy, so allow the registries it pulls from. For Docker
  Hub: `registry-1.docker.io`, `auth.docker.io`, and the hosts it sends the layers from,
  such as `production.cloudfront.docker.com`. The record lists the ones it was denied.
- The containers the agent starts do not trust the run's authority unless the agent mounts
  its bundle into them.
- Mount nothing your machine's root must protect: the container's root may reach the
  mounts as your machine's root.
