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

Each run is recorded in `.qory/runs/<id>/`:

- `events.jsonl`: one event per line.
- `output.log`: the session's bytes.

`qory run resend` sends a finished run's record to the server again: after a runner that
died, or a server that was away. See [Resending a run's record](#resending-a-runs-record).

## Try it: the hello example

This walkthrough uses the hello example from the [README](../README.md#try-it). Run it in
that directory. It goes in three steps: observed, then behind a wall, then with the token
kept outside.

### Observed

```sh
qory run -- -p "/hello"         # one headless turn, observed
cat .qory/runs/*/events.jsonl   # what it reached, what it printed, how it ended
```

### Behind a wall

Without a wall, the proxy sees only programs that honour it. A **wall** starts the agent
in a container. The container's one route out is the proxy.

A wall needs the `docker` command. It also needs an image that contains the agent. Qory
publishes one with each release, `ghcr.io/qoryai/agent`, tagged with the version `qory
version` prints. See [The agent's image](#the-agents-image).

```sh
export CLAUDE_CODE_OAUTH_TOKEN=...       # from `claude setup-token`; or ANTHROPIC_API_KEY
qory run --wall docker --image ghcr.io/qoryai/agent:0.13.0 --env CLAUDE_CODE_OAUTH_TOKEN -- -p "/hello"
```

On a Mac, first set `wall.helper` to the Linux build of the same `qory` release. See
[The wall](#the-wall).

This run passes the token into the container. The last step keeps it outside.

### With the token kept outside

Define what this machine has, and what the agent may reach, in
`~/.config/qory/runner.yaml`:

```yaml
apiVersion: qory.dev/v1alpha1
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
  image: ghcr.io/qoryai/agent:0.13.0
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
qory run --policy ~/hello-policy.yaml -- -p "/hello"
```

The agent greets you as before. What changed:

- Inside the container, `CLAUDE_CODE_OAUTH_TOKEN` is a placeholder.
- The proxy sets the real token on each request to `api.anthropic.com`.
- The record lists those requests with the credential's name. It never lists the
  token's value.
- Everything else the agent tries to reach is denied and recorded.

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
  url: https://qory.example             # a scheme and a host, nothing after
  access_key: ak_f1xt0re000000000       # the key the server issued this machine
  secret: fixture-secret-not-a-real-one # or QORY_SERVER_SECRET in the environment
wall:                    # start the runtime in a container; optional
  adapter: docker
  image: ghcr.io/qoryai/agent:0.13.0@sha256:…  # Qory's, or yours FROM it
  env: [ANTHROPIC_API_KEY]              # names; nothing else of your environment goes in
  memory: 14g                           # at most 14 GB of memory; also cpus, pids_limit, shm_size; optional
run:                     # optional
  timeout: 5h30m         # stop a runtime that runs this long
  stop_signal: SIGINT    # requests it to stop; the runtime's descriptor's, else SIGTERM
  stop_grace: 30s        # between that signal and SIGKILL; 10s
```

Without an `egress` section, every connection is allowed and recorded. A `runner.yaml`
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
- which signal asks it to stop.

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

## The server

With a server configured, the runner starts by fetching the server's configuration. It
signs that fetch with the access key and the secret. It does not start unless the server
answers.

The configuration defines where the events go. When it selects a run configuration, that
is the run's policy. The runner fetches it with the run's labels, the checkout's forge
and repository among them. It reloads it when the server reports it changed.

`--local` runs with the files alone and the machine's policy. The server is not
contacted.

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
outside the checkout. It is for a machine without a server, one that serves runs of
different kinds.

It only narrows. The `egress` section of the machine's `runner.yaml` decides how:

| `egress` in `runner.yaml`     | What the run reaches                            |
| ----------------------------- | ----------------------------------------------- |
| mode `enforce`                | the policy file's hosts that the section covers |
| mode `observe`, or no section | the policy file, as it is                       |

The `deny` lists of both apply either way.

With a server configured, the server's run configuration is the policy, and `--policy` is
refused. `--local` keeps `--policy`.

The server's secret stays the runner's. `QORY_SERVER_SECRET` is taken out of the
session's environment.

## Credentials the agent never has

Behind a wall, a run needs no credential inside the container. `runner.yaml` defines the
credentials the machine has. A credential's token comes from one of three places:

- a variable of `qory run`'s environment, `env`,
- a file, `file`,
- an adapter, `adapter` (see [Adapters](#adapters)).

A run's policy selects among them by name, with an argument for an adapter, such as a
repository. A policy defines no credential of its own.

```yaml
# ~/.config/qory/runner.yaml
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

- The runner keeps each token outside the container.
- Its proxy sets the token on the requests to the hosts the token is for.
- Where a program wants a credential set, the container gets a placeholder. It never gets
  the token.

### Adapters

An **adapter** is a program of yours, written for one kind of host, such as a source code
host. It runs outside the container. It prints:

- the token,
- the token's expiry,
- the hosts, the scheme and the paths the token is for.

So `qory` defines no host of its own.

### Paths

An adapter's paths are where the token goes on its hosts. Under `enforce`, they are also
the run's whole reach on those hosts. The runner refuses every other path there, another
organization's repository included.

`egress.paths` in a policy limits a host to paths as well, with or without a credential.

Under `enforce`:

- A path outside the adapter's paths is refused.
- On a host with both the adapter's paths and `egress.paths`, a path passes only when it
  matches both lists.

Under `observe`:

- A path outside the adapter's paths is sent on without the token, and recorded.
- On a host with both lists, the rest is sent on and recorded. The token goes only where
  the adapter's paths match.

In either mode, a path that could be read two ways is refused on these hosts, such as one
with an encoded slash.

The [runner's contract](https://github.com/qoryai/runner/tree/main/contracts/runner/v1#credentials)
has the adapter's document and the rules.

### Integrations

An **integration** is an adapter published on its own, that describes itself. It is
either:

- Qory's own `qory-<name>`, each in a repository of its own, such as
  [`qory-github`](https://github.com/qoryai/qory-github), or
- a program of yours, under a name of your own, started from the
  [integration template](https://github.com/qoryai/integration-template).

Declare it, and `qory` writes the definition:

```yaml
# ~/.config/qory/runner.yaml
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

#### What `qory` does with a declaration

Before a run, `qory` runs `<program> describe`. The program prints:

- its description,
- the settings it takes, as a JSON Schema,
- the roles it plays.

`qory` checks the settings against that schema.

The credential role defines a credential. Its name is the declaration's key. It is as if
the file contained:

```yaml
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

`qory run` prints the program it found, on a line of its own.

#### Where a program may live

`qory` runs only a program the run cannot write. The program must be:

- outside the checkout, and
- outside every mount the wall makes read-write in the container: `wall.mounts` and
  `--mount` without `:ro`.

`qory` judges a program by where its links lead. A link on a `PATH` entry the checkout
controls, which resolves outside the checkout, is judged by where it resolves.

The rule applies to each run as it starts. A program written into a directory while that
directory was mounted read-write is judged by where it is, on every run that follows. So
keep a program's directory out of the read-write mounts.

#### Who may own and write it

One rule covers:

- the resolved file,
- every directory above it, up to `/`,
- every directory above each link on the way, the `PATH` directory among them.

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

The settings go on the adapter's command line. Other processes on the machine can read
it. So a secret is refused there:

- The description marks a secret. The mark is a property of the settings themselves.
- The settings define the path of the file that contains the secret: `private_key_file`,
  never `private_key`.

The settings are compact JSON:

- the keys are in the file's order,
- `<`, `>`, `&`, U+2028 and U+2029 are escaped,
- every `$` is written `\u0024`.

This is how the integration contract defines a declaration. So the adapter's
`${argument}` is the policy's argument alone.

#### A name the `credentials` section defines too

A name the `credentials` section defines itself belongs to that section. Then:

- `qory run` and `qory config` print this, on a line of their own,
- `qory config` describes the integration and lists it as shadowed,
- a run leaves it undescribed.

#### Which integrations are described

- A run whose policy is on this machine describes the integrations its `credentials`
  select.
- A run whose policy the server supplies describes every one.
- `qory config` describes every integration, and lists what each defines.

#### What stops a run

Each of these stops the run before it starts:

- a program that does not answer within 10 seconds,
- a description the [integration
  contract](https://github.com/qoryai/integrations/tree/main/contracts/integration/v1)
  refuses,
- settings the description refuses,
- an integration that plays no role `qory` expands.

### TLS on credential hosts

For the hosts a credential is for, and no other, the proxy ends the container's TLS
itself. It uses an authority made for the run. The authority's key never leaves the
runner.

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
path and the credential's name.

A host that a policy's `egress.paths` limits to paths is terminated too, with or without a
credential. Every other host stays a tunnel that nobody reads.

## Resending a run's record

`qory run resend <run-id>` sends a run's record to the server `runner.yaml` defines. It
is for a run whose runner died, or whose server was away. End a job with it, whatever
happened before it.

The run is selected by its id: the directory under `.qory/runs` in this checkout. The
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
- The container sees the checkout and the composed home, at their own paths, and nothing
  else of your machine.
- Of your environment, the container gets the launch template's variables and the ones
  `wall.env` or `--env` lists. Nothing else.
- The runner, the policy, the record and the server's secret stay outside.

A wall needs the `docker` command, and an engine behind it. It also needs an image that
contains the runtime: one Qory publishes, or yours FROM it. See [The agent's
image](#the-agents-image). `wall.images` defines several, and a run's policy selects
one: see [The agent's images](#the-agents-images).

What to know:

- **The model credential.** It goes in by name, with `wall.env` or `--env`. Then it is
  the agent's. A subscription login kept in a Mac's Keychain does not reach a container.
  Use an API key, or a token from `claude setup-token`.
- **The helper on a Mac.** Inside the container, the relay, the hook forwarder and what
  starts an image's own Docker are `qory`'s own Linux build, mounted read-only. On Linux, that is the binary you run. On a Mac, download the
  Linux archive of the same release, for your engine's architecture. Set `wall.helper` to
  that binary.
- **What the container sees.** The checkout it was started in, and no other directory.
  `--mount <path>[:ro]` or `wall.mounts` shows it another one, at its own path, such as a
  sibling checkout the session reads. A socket is never mounted.
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

### The agent's image

Qory publishes four images with each release, for `linux/amd64` and `linux/arm64`,
tagged with the release's version. The release's `images.txt` names each one by digest.

| Image | What it holds |
|---|---|
| `ghcr.io/qoryai/agent` | Claude Code at the version the runner's descriptor is written against, `git`, `gh`, Node, the system's authorities |
| `ghcr.io/qoryai/agent-docker` | `agent`, and Docker's daemon and command |
| `ghcr.io/qoryai/agent-go` | `agent`, and Go |
| `ghcr.io/qoryai/agent-go-docker` | `agent-docker`, and Go |

Name the image by digest in `wall.image`, so every run starts the same one:

```yaml
wall:
  adapter: docker
  image: ghcr.io/qoryai/agent:0.13.0@sha256:…   # a line of images.txt
```

What each of them does:

- **Any user.** The container runs as your user, whom the image does not know. `HOME`
  is `/home/agent`, which every user may write.
- **No setuid program.** None is needed, and none is there.
- **Nothing on its own.** It has no entry point. Claude Code and `gh` neither update
  themselves nor ask for input, and Claude Code sends no telemetry or error reports,
  which a run under `enforce` would otherwise show as denied connections.
- **Docker.** `agent-docker` holds the daemon for a Docker of the agent's own, under a
  runtime such as Sysbox. The image starts nothing: the runner starts the daemon.
  `qory run` does not start one yet.
- **Go.** `agent-go` has Go at the version the image pins, with `GOTOOLCHAIN=local`, so a
  `go.mod` that asks for a newer one fails instead of downloading it. `GOPATH` and the
  caches are under `HOME`. It has no C compiler, so cgo is off.

To add what your agents need, build yours FROM one of them, and check it:

```dockerfile
FROM ghcr.io/qoryai/agent:0.13.0@sha256:…
RUN apt-get update && apt-get install -y --no-install-recommends python3 \
 && rm -rf /var/lib/apt/lists/*
```

```sh
docker build -t my-agent:1 .
qory image check my-agent:1
```

`qory image check` checks an image against what the wall needs of it. With no image, it
checks `wall.image`. It prints a line per check, and exits 1 when one fails.

- From outside: the engine holds the image, for the platform the engine runs; the image
  sets `HOME`; the reference is pinned by digest or not, which fails nothing.
- From inside: it starts the image the way the wall starts the agent, as a user the image
  does not know, with no capability and no network. `qory`'s Linux build then checks
  that `HOME` takes a file from that user, the authorities are where the wall reads
  them, `/bin/sh` is there for the runtime's hooks, `claude` is the version the runner's
  descriptor is written against, `git` and `gh` run, and no file is setuid or setgid. It
  reports whether `dockerd` is in a system directory, and checks what it needs beside it.

The Linux build is the one the wall uses: on a Mac, `wall.helper`, for the image's
architecture.

To build the images from a checkout of `qory`:

```sh
docker build -t qory-agent images/agent
docker build -t qory-agent-docker --build-arg BASE=qory-agent images/agent-docker
docker build -t qory-agent-go --build-arg BASE=qory-agent images/agent-go
docker build -t qory-agent-go-docker --build-arg BASE=qory-agent-docker images/agent-go
```

## The agent's images

A machine that serves several kinds of work defines several images in `runner.yaml`,
each by a name. A run's policy selects one by that name, as it selects credentials. A
repository never names an image.

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
  `docker:dind` has them there.
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
