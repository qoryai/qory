# Running a session

`qory run` composes your agent's harness, then starts the agent on it, inside the
session of [Forager](https://github.com/qoryai/forager). Every connection the agent makes
goes through a proxy on your machine, Forager's gateway. The session is recorded.

## What `qory run` does

It composes the harness as `qory harness compose` does, then starts the same launch as
`qory harness launch` (see [the harness](harness.md)). Two things are added:

- Every connection the runtime makes goes through a proxy on your machine, and is
  recorded.
- The session is written as events, beside its output. The events hold the session's
  output, what Forager observes, and what the runtime reports itself.

```sh
qory run                              # the stack's runtime, at your terminal
qory run claude -- -p "Reply pong"    # one headless turn; arguments after -- go to the runtime
```

`qory run` opens the run at its gateway, then composes into the home `qory harness
compose` writes, before the agent starts, and prints one line on stderr when it is done,
such as `qory run: composed 42 entries from 3 modules for claude`. `-f`/`--file`,
`--model`, `--effort`, `--tier` and `--tier-effort` mean what they mean on `qory harness compose`, and so do
`qory.yaml` and `harness.yaml`. A compose that fails stops the run before the agent
starts, with the exit status of `qory harness compose`. The run's record shows that the
run failed with no session started (`no_session`), and qory prints the path of the run's
record after the error line. Four stops of a run from a run code, right after the
compose, close with their reason in place of `no_session`: `workflow_not_in_harness` (see
[What the run takes from the response](#what-the-run-takes-from-the-response)), or
`read_needs_wall`, `read_needs_setup_outside` or `read_needs_no_nested_docker` (see [Read
workflows](#read-workflows)). A read run whose setup container was not removed stops after
the setup and closes with `read_needs_setup_removed`. In a folder that is not a git
working tree, `qory run` starts only with the home outside the folder, as `qory harness
compose` composes only then.

A walled run, and any run with a run key, writes no harness into the folder it runs in. The harness goes into
a home under `qory/homes` in your cache directory: `~/.cache/qory/homes/<name>-<hash>` on
Linux, or `$XDG_CACHE_HOME/qory/homes/<name>-<hash>` when that is set, and
`~/Library/Caches/qory/homes/<name>-<hash>` on macOS. A run with a run key composes into that
folder. A walled run with no run key, `--home` or `harness.home` composes into
`runs/<name>-<hash>/<run id>` under `qory/homes`, a home for that run alone. A second run of the
checkout leaves the first run's home as it is, and so do `qory harness compose`, `qory run` and
`qory harness remove` with `--home` set to `qory/homes`, which use `<name>-<hash>` there. `qory`
removes that home when the run ends, also after a
stop and when the run's copy stays. Its harness report stays per checkout, at
`runs/<name>-<hash>-report.json`, so a module keeps its pin between runs: the first run takes each
module's commit, and later runs read the report. `--home`, or `harness.home` in your
`qory.yaml`, sets another folder outside, and that home stays after the run. The compose writes nothing into the folder: no
`.qory/harness`, no link and no line in `.git/info/exclude`, in a folder that is not a
git working tree as well. A home inside the folder stops the run with exit 2, behind a
wall or not, before the run opens and before anything is written:

```text
set --home to a folder outside /work/app: /work/app/.qory/harness is inside /work/app, and a walled run or a run with a run key writes no harness into the folder it runs in
```

A walled run gets no link in the folder, whatever `harness.links` sets, so
`harness.links: checkout` does not stop it. A run with no wall keeps its links. Without the
links, some programs lose part of the harness, since they read that part from the checkout alone:

| Runtime    | What does not reach a walled session                                       |
| ---------- | -------------------------------------------------------------------------- |
| `gemini`   | the skills, the agents, the commands, the instructions, the `files` entries |
| `amp`      | the skills, the instructions, the `files` entries                          |
| `opencode` | the instructions                                                           |
| `cursor`   | the instructions, the `files` entries                                      |
| `copilot`  | the hooks, the instructions, the `files` entries                           |

A walled run with no run key on one of these runtimes prints one line on stderr before the agent
starts, with what the compose wrote of that list, and nothing when it wrote none of it:

```text
qory run: run with --wall none for gemini's skills, agents, commands, instructions and files: a walled run has the harness in a home outside the checkout, and gemini reads those from the checkout.
```

A run with `--home`, `harness.home` or `harness.links: none` has no links with `--wall none`
either, so its line starts with what to remove:

```text
qory run: remove --home, harness.home and harness.links: none, and run with --wall none for gemini's skills, agents, commands, instructions and files: a walled run has the harness in a home outside the checkout, and gemini reads those from the checkout.
```

A run with a run key prints no such line. `codex` gets all of it from the home, which is its
`CODEX_HOME`. `claude` gets the skills, agents, commands, output styles, hooks, MCP servers and
instructions from the home. A `files` entry `claude/<path>` does not reach a walled session on
`claude`, and no line prints: the home has it at `claude/<path>`, a session reads it through the
link `.claude/<path>`, and the launch has `--setting-sources user`.

A walled run from a run code stops with exit 2, before the agent starts, when the agent's
container would get a harness report, `<home>-report.json`, which contains the harness's
addresses. For a mount or the home, this happens before the harness is fetched and before
the run writes its inputs, home or report. For the checkout and the working folder of a read
run, it happens once the fetched stack shows that the run reads. This applies
when `harness_hidden` is `false` too: through `qory` in your cache directory, such a run
would read the reports of the runs on this machine whose harness is hidden. The
container gets each mount of `wall.mounts` and `--mount` and the home, read-only, and a
read run also gets the checkout and the working folder. A read run with a setup gets them
from a copy: see [Read workflows](#read-workflows). The run stops when a mount of
`wall.mounts` or `--mount`, or the checkout or working folder of a read run:

- is, contains or lies inside `qory` in your cache directory, where qory writes the home
  and the harness report of each run with a run key or a run code that has no `--home`
  or `harness.home`;
- is or contains the harness report of this run, beside the home that `--home` or
  `harness.home` sets.

It stops as well when the home is or contains `qory` in your cache directory. A home
that lies inside that folder, as the default under `qory/homes` does, passes, and so
does a home elsewhere: this run's report is beside the home, not inside. qory compares
each path through links, and in any letter case on a disk that ignores case, as macOS
does by default. The code is used, so the run needs a new one:

```text
remove /home/dev/.cache from --mount: it contains /home/dev/.cache/qory, where qory writes the harness report of each run with a run key or a run code that has no --home or harness.home, and the agent's container has every mount; the code is used
remove /srv/qory-homes from --mount: it contains /srv/qory-homes/app-1a2b3c4d-report.json, the harness report of this run, and the agent's container has every mount; the code is used
start the run in a checkout that does not contain /home/dev/.cache/qory: the checkout /home/dev is mounted into the agent's container, and qory writes the harness report of each run with a run key or a run code that has no --home or harness.home under /home/dev/.cache/qory; the code is used
run qory from a folder that does not contain /home/dev/.cache/qory: the working folder /home/dev is mounted into the agent's container, and qory writes the harness report of each run with a run key or a run code that has no --home or harness.home under /home/dev/.cache/qory; the code is used
```

The other lines of this stop start:

- `remove <path> from wall.mounts in forager.yaml: …`, for a mount of `wall.mounts`,
  with the rest as for `--mount`;
- `start the run in a checkout outside <folder>: …` and `run qory from a folder outside
  <folder>: …`, for a checkout or a working folder that is or lies inside `qory` in
  your cache directory;
- `start the run in a checkout that does not contain <report>: the checkout <folder> is
  mounted into the agent's container, and <report> is the harness report of this run;
  the code is used`, and `run qory from a folder that does not contain <report>: the
  working folder <folder> is mounted …`, for one that contains the report. For either,
  this takes another path to the same folder, such as one in another letter case or
  through a bind mount: a home inside the checkout or the working folder, by its path or
  its links, stops the run earlier, with `set --home to a folder outside <folder>: …`;
- `set --home or harness.home to a folder that does not contain <folder>: the home
  <home> is mounted read-only into the agent's container, and qory writes the harness
  report of each run with a run key or a run code that has no --home or harness.home
  under <folder>; the code is used`, and `set --home or harness.home to a folder other
  than <folder>: …` for a home that is the folder.

The working folder lies outside the checkout when `--home` names a composed home whose
report lists another checkout.

Given a run key, `qory run` composes under four rules, so that a branch of the checkout
does not pull files of this machine into the harness or set the machine's settings of the
compose:

- A module or a stack that a file in the checkout lists by a path is read only from inside
  the checkout, with every link resolved, the directory that `exports` sets for an export
  included. Give a module outside the checkout an https git source instead. Every file of
  such a module, its `qory-module.yaml` among them, resolves inside the checkout too. The
  checkout's document and the `qory.yaml` at the checkout's root are read only from inside
  the checkout too: a `qory.yaml` that is a link to a file outside the checkout stops the
  run.
- Every git source of the harness is fetched over https only, through the run's stack
  proxy on the run's gateway, which passes only the hosts of the two lists in [Hosts of
  the stack proxy](#hosts-of-the-stack-proxy) and sets only the run configuration's
  harness credentials, never a credential of the run's work. The fetch reads
  none of this machine's git configuration or credentials, and the fetched sources go
  into the run's directory, which no other run reads. A local path, a `file://`,
  `ssh://` or `git://` URL, and the scp form `host:path` stop the run before git runs,
  exit 2. A URL with a user part or an `@` between `://` and the first `/` after it, a
  host with a character outside ASCII, or a URL that qory cannot read stops it too, exit
  2, before the source is fetched.
- `qory run` reads no `git` or `env` key of the checkout's `qory.yaml`, and no machine key
  under `harness` such as `model` or `launch`, as under `extends`.
- The harness report of an earlier compose, such as `.qory/harness-report.json`, is not
  read: the compose pins no git source to a commit that the report lists, removes no link
  that it lists, and adds none of its replaced paths to the new report.

When the run configuration lists repositories, `qory run` fetches them on this machine
once the run is open, before the compose: the run's repository into the working folder,
which is an empty folder or an empty git working tree, and each further one under
`.qory/repositories`. Each fetch is at depth one, over https only, through the run's host
proxy on the run's gateway, which adds the credential the run configuration has for the
host, with git's hooks off and no credential helper, so no token is on this machine. qory
closes the host proxy once the repositories are fetched. The compose then fetches the harness's git
sources through the run's stack proxy, which gets none of those credentials. `qory run`
prints one line for the repositories, with `and 1 further repository` when there are
two, and then one line for each git source of the compose:

```text
qory run: fetched example-org/shop at 0d62793fbf11 in 3.2s
qory run: fetched https://github.com/example-org/harness#main at 1dfbefcbbe51 in 0.8s
qory run: composed 12 entries from 1 module for claude
```

A run from a run code whose documents set `target_version` fetches the target's
repository at that commit: see [What the run takes from the response](#what-the-run-takes-from-the-response).

A fetch that fails stops the run before the agent starts, and the run's record shows it
as failed with no session, `no_session`. The line starts with the fix: add a credential
for a repository the host does not let the run read, check that the run's policy allows a
host the gateway does not reach, or check a git source's URL and ref, that a list of the
stack proxy contains its host, and, for a private repository, that the run configuration
has a harness credential for it. qory exits 2 for a ref that the remote does not have
and for a git source that is not https, and 1 for any other failed fetch:

- `check that <url> has the branch, tag or commit <ref> and that gateway.stack_hosts in
  the gateway's forager.yaml or the run configuration's stack_hosts lists <host>, and,
  for a private repository, that the run configuration has a harness credential whose
  hosts and paths include the repository. The fetch through the run's stack proxy
  failed: <cause>`, for a git source of the harness
- `run again: the run's stack proxy, through which qory fetches the run's harness, did
  not open: <error>`, exit 1

A starter gives the run an empty folder or an empty git working tree that git init made.
In an empty folder, qory runs `git init` with no template. The run stops with exit 1 before
git fetches anything if the working folder:

- has any entry and no `.git`, such as a file or only `.qory`;
- is a link to an empty folder;
- is empty and, on the same file system, inside a git repository or below a `.git` on which
  git fails, such as a `.git` file that leads nowhere or a bad config;
- has a `.git` and anything else but `.qory`, with or without a commit;
- has a `.git` that is a file or a link, or that has no `HEAD`, `objects` or `refs`;
- is not the top of its git working tree;
- has a repository with a commit or a remote.

Before the fetch, qory also checks the working folder's `.git`. The run stops in the same way
if:

- its configuration has a key git init does not write, such as a filter,
  `remote.origin.proxy` or `http.sslVerify`;
- `.git` has a link anywhere under it, such as a linked `objects` folder, or
  `objects/info/alternates` or `objects/info/http-alternates`;
- `.git` has a `commondir` file or `info/grafts`;
- `.git/config` or `.git/config.worktree` is not a regular file;
- a file under `.git`, outside `.git/objects`, or under `.qory` has a second name, a hard
  link, so that a write to it in place would reach the file of the other name. git writes
  each object to a new file, so the hard links of `git clone --local` in `.git/objects`
  do not stop the run.

Start the run in a new empty folder or a new empty git working tree.

A source that breaks a rule stops the run with exit 2 before the agent starts. A stack
given to `-f` from outside the checkout is not a file in the checkout: its paths resolve
as they do on `qory harness compose`, and its git sources are fetched through the run's
stack proxy, as every git source of the harness is.

`-f` takes a stack by reference too, `<https URL>#<ref>:<folder>`, as on `qory harness
compose`. Given a run key, the run fetches the folder through the run's stack proxy,
with a `qory run: fetched` line, and every git source of the fetched stack the same way.
Its modules listed by a path are read from the fetched tree at the same commit. A
reference whose URL has a user part or an `@` between `://` and the first `/` after it
stops the run with exit 2 before anything is fetched: `remove the user name and password
from the URL in -f: the run's stack proxy sets the harness credentials of the run
configuration, so a source's URL needs none`. One whose host has a character outside
ASCII stops it the same way: `write the host of the URL in -f in ASCII, …`. This run
composes the hello example at a commit of the qory repository:

```sh
qory run claude -f https://github.com/qoryai/qory#<commit>:examples/hello --wall docker -- -p "/hello"
```

The runtime is the one in the stack's target. A first argument selects another, and the
run composes for it instead; name one when the target has several. Arguments after `--`
go to the runtime, after the launch template's arguments.

The report fixes no variable and adds no mount. Behind a wall, `harness.home` comes from
the `qory.yaml` in qory's configuration directory alone: another `qory.yaml` that moves
the home refuses a walled run.
A walled run from a run code with push checks, whose home `--home` or `harness.home` sets
outside `qory` in your cache directory, stops at the compose, before the home is written,
with exit 2, when its `wall.mounts` or `--mount` has a read-write mount at or inside the
home: `Mount <path> read-only, or move it out of the harness home, for a walled run with
push checks. The agent could change the push checks through it; the code is used`. Any
other walled run stops on such a mount before the run opens: a run from a run code whose
home is under `qory` in your cache directory with `remove <path> from --mount: …`, see
[What `qory run` does](#what-qory-run-does), and a run with no run code, unless the mount
is the checkout's, with `make the mount <path> read-only, …`, see [A run that may write
works in a copy](#a-run-that-may-write-works-in-a-copy). A walled run whose home has a
`push-checks.json` that qory cannot read stops before the setup and the agent, with exit
2: `Compose the harness again, and run again. The push checks in <path> could not be read:
<cause>.` It stops there with exit 2 too when the push checks would run on another engine
than the run's containers, `Switch the engine back to the one the run started on, and run
again. The push checks would run on the engine <id>, and the run's containers are on the
engine <id>.`, and when the run API of a run from a control plane fails, `Open the run
through a gateway that sends the run API address, and run again. The push checks read the
artifacts through the run API, which failed: <cause>.`, or when the run API address is not
an http or https URL: `Update the run's gateway, and run again. The run API address that
the gateway sent is not one the push checks can use: <cause>.` When Forager cannot turn on
the check of the POSTs from behind the wall, the run stops there with exit 1: `Run again.
Turning on the push checks for the POSTs from behind the wall failed, so the run stopped
before the setup: <cause>.` A home without push checks, and a run with no control plane,
start as before, with no check turned on.

A run from a run code stops at the same points with the same exit status. The code is used
by then, so the fix in each line is to take the work item in again:

```text
Take the work item in again in Qory Apiary, so that the new run composes the harness again: the push checks in <path> could not be read: <cause>; the code is used
Take the work item in again in Qory Apiary, and do not switch the engine while that run starts: the engine changed after this run opened, so the push checks would run on the engine <id>, and the run's containers are on the engine <id>; the code is used
Update the run's gateway so that it sends the run API address, then take the work item in again in Qory Apiary: the push checks read the artifacts through the run API, which failed: <cause>; the code is used
Update the run's gateway, then take the work item in again in Qory Apiary: the run API address that the gateway sent is not one the push checks can use: <cause>; the code is used
Take the work item in again in Qory Apiary: turning on the push checks for the POSTs from behind the wall failed, so the run stopped before the setup: <cause>; the code is used
```

When the run ends, qory says how, and its exit status goes with it:

- `✓ <runtime> exited 0` or `✗ <runtime> exited <n>`: the runtime exited by itself, and
  the exit status is the runtime's.
- `✗ <runtime> ended on the signal <signal>`, such as `SIGKILL` from elsewhere: exit 1.
- `✗ the run was cancelled: qory run got SIGTERM, and <runtime> was stopped`, or
  `SIGINT`, when the signal came while the runtime ran: exit 1. A signal that comes after
  the runtime exited changes neither its line nor the exit status.
- `✗ the run was cancelled: it reached the time limit of <d>, and <runtime> was
  stopped`: exit 124 (see [Time limits](#time-limits)).
- `✗ the run was cancelled: qory run got SIGTERM before <runtime> started`, or
  `SIGINT`, when the signal came before the runtime started, such as during the compose:
  exit 1. The line contains `the agent` in place of `<runtime>` when qory had not read the
  runtime yet: when the signal came late in the run's open or during the fetch of the
  run's repositories, and when the compose failed after the signal.
- `✗ give the run a longer --timeout: the run was cancelled at its time limit of <d>,
  before <runtime> started`: the limit came before the runtime started, exit 124. The
  line contains `the agent` in place of `<runtime>` when the limit came during the fetch
  of the run's repositories, and when the compose failed after the limit.
  A run from a run code, which takes no `--timeout`, prints `✗ set a longer
  time_limit_minutes in Qory Apiary, or a longer workflows.<workflow>.time_limit in the
  base of the workspace's harness: the run was cancelled at its time limit of <d>, before
  <runtime> started` instead. The line contains `the workspace's harness` for a harness
  that defines a stack, and `the checkout's harness or the base it extends` for a run
  whose documents contain no harness (see [What the run takes from the
  response](#what-the-run-takes-from-the-response)).
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

### Secret values in qory's lines

`qory run` keeps a mask list of the run's secret values. As it reads each value, it adds
the value to the list:

- the value of each `--env` variable and of each `wall.env` variable;
- the run code and the run key, from `--run-code-file`, `--run-key-fd`, `QORY_RUN_KEY`,
  the answer to the run code, or the registration through `gateway.server.via`;
- the values of `QORY_ACCESS_KEY_SECRET`, `QORY_SERVER_SECRET` and `QORY_RUN_KEY`, which
  qory reads as it starts, in every run;
- the access key's secret from `--access-key-secret-fd`, in every run, and from the
  `access-key-secret` file when qory signs its requests to the server with it;
- the password of the run's host proxy, of its stack proxy and of
  `wall.engine_proxy_file`;
- for a run given a run key or a run code, the query values of each harness source's URL,
  before qory fetches the source through the stack proxy;
- for a run whose gateway runs in the `qory run` process, the value of each credential of
  `gateway.credentials` whose `env` or `file` qory can read.

qory adds each line of a value with several lines too, without the spaces around it. It
prints `***` in place of:

- each value of the mask list, as it is, percent-encoded or JSON-escaped;
- the value of a URL query parameter named `X-Amz-Signature`, `X-Amz-Security-Token`,
  `Signature`, `sig`, `token` or `access_token`, its ASCII letters in any case;
- a whole key of a shape that Forager's mask package lists, such as a GitHub token that
  starts with `ghp_`, an Anthropic key that starts with `sk-ant-`, an AWS access key id
  that starts with `AKIA`, or the token after `Bearer `.

It does so in qory's lines and errors, in the last error line, in the lines of the fetch
and of the compose, and in the lines that the fetch and the compose write into the run's
record. A value split across two writes is replaced whole: qory keeps back the end of
each write in which a value could start, until the next write or the end of the stream.
`qory run prepare`, `qory run prebuild-step` and `qory
run pool` mask their output too. Their mask lists contain no value of a run: there qory
replaces a URL's signature or token and a key of a listed shape, and in `qory run pool`
also the access key's secret.

`qory run resend`, `qory gateway`, `qory run register` and `qory run end` mask every line
that they print, on stdout and on stderr, Forager's lines among them, in the same way. The
mask list of each contains the values of `QORY_ACCESS_KEY_SECRET`, `QORY_SERVER_SECRET`
and `QORY_RUN_KEY`, the access key's secret from `--access-key-secret-fd`, and from the
`access-key-secret` file when qory signs its requests to the server with it, and the
values that the command reads:

- `qory run resend`: the run key from `--run-key-fd`, and the run key that qory kept for
  a run registered through `gateway.server.via`;
- `qory gateway`: the enrolment code from `gateway.server.enrolment_code_file`, and the
  value of each credential of `gateway.credentials` whose `env` or `file` qory can read;
- `qory run register`: the run key in the server's response.

qory run gives the list to Forager's session, which masks with it the agent's output, on
pipes and on a terminal, the output of the setup commands, the run's record,
`output.log` and `events.jsonl`, its events and its lines. The session masks each of
the agent's streams with a separate writer, so a value split across two writes of the
agent is replaced whole, also when one of qory's lines comes between them. qory passes
the session stdout and stderr as they are, so the agent's output is masked once. The
session adds the server's variables and the passwords of the run's forwarders to the
list, so qory masks those in its lines too; a forwarder's password prints as
`qory-proxy-password`.

A value of 8 bytes or more is masked wherever it appears, also where it is not secret:
with `--env DEPLOY_TARGET` set to `production`, the agent's line `deploying to
production` prints as `deploying to ***`.

These are not masked:

- a value that neither qory nor the session reads: a credential that an adapter or an
  integration supplies, and the credentials of a separate gateway, which a run given a
  run key goes through, unless the key has a listed shape;
- a token or a key that the agent, a setup command or a build step reads from a file or
  the network and then prints;
- a value printed in another encoding, such as base64;
- a value shorter than 8 bytes, since `***` would also replace ordinary text that contains
  it, a value longer than 4096 bytes, and every value past the first 1024;
- the parts of the run's record that Forager writes as they were given: the run's labels,
  the policy applied, a build's images and commit, the reason of the runtime's exit, the
  names of a refusal, and the setup script in the session's folder.

## The record

Each run is recorded on your machine, outside the checkout, in qory's state directory:
`~/.local/state/qory/runs/<checkout>-<hash>/<id>/`, or under `$XDG_STATE_HOME/qory` when
that is set; qory refuses a relative `XDG_STATE_HOME`. `<checkout>` is the checkout's
directory name, and `<hash>` the first 12 hex digits of the SHA-256 of its full path, with
links resolved. Each checkout has a separate folder. The state directory, its `runs`
directory and the checkout's folder are mode 0700: only you can open them.

A walled run whose read-write mount is your home folder or qory's state directory, or a
folder that contains either, such as `/`, stops before anything starts, and `qory run`
exits 2. The line starts `make the mount <folder> read-only, with :ro on --mount or in
wall.mounts`. A read-only mount is not compared. A walled run takes no read-write mount
at all: see [A run that may write works in a copy](#a-run-that-may-write-works-in-a-copy).

qory names the run's folder when the run ends:

```
qory run: the record is in /home/you/.local/state/qory/runs/app-3f9a1c0b7d2e/0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5e6f
```

It holds:

- `events.jsonl`: one event per line.
- `output.log`: the lines of the run's steps and the session's bytes.

The record has `qory run`'s work before the agent starts as steps, each with its lines and
how it ended:

1. For a run from a run code given `--machine-steps-file <file>`, which a machine's start
   script passes, the step `start machine`, of kind `machine`, right after the run opens:
   one line per entry of the file, such as `start Docker  3.2 s`, and it ends `succeeded`.
   With a bad file the run has no such step, qory prints one line, and the run goes on.
2. When the run configuration lists repositories, the fetch, named after the run's
   repository, such as `fetch github.com/example-org/shop`, with `and 1 further
   repository` when there are two.
3. The compose, `compose the harness for claude`, or `compose the harness` when you name
   no runtime. The step ends once `qory run` has read the agent's launch from the
   composed home.
4. For a walled run whose policy names its image by a reference pinned by digest, the
   image step, named after the reference. Forager first requests the image's manifest
   from its registry through the run's proxy, with the run's credentials: a `HEAD` of
   `/v2/<repository>/manifests/<digest>`, over https, or over http for a registry on
   loopback. Then the engine runs `docker pull` of the image, also when it holds the
   image by that digest, and Forager checks that the image's user is not root. The
   run's policy must allow the registry's host; for a registry on your machine or a
   private address, `gateway.egress.allow` of `forager.yaml` must list the host by name
   too. A host the run's proxy denies fails the step with Forager's line `Allow <host>
   in the node's policy, and in the server's policy for a run from a server.` With
   `wall.engine_proxy_file`, the pull goes through the run's proxy: see [The
   wall](#the-wall). The setup and the agent then start from that image.

Forager's steps of the session's start come next, such as `join session 1`. Each line
the fetch or the compose prints on stderr is in that step's lines too. A fetch or a
compose that fails ends `failed` with the exit status of `qory run`, and its last line is
the error line. An image step that fails ends `failed` with -1, and its last line is
Forager's error. A step that fails once the run is stopped, or out of time, ends
`cancelled`.

After the agent, a walled run that may write in a repository has the step `copy work out`,
of kind `copy`, around the git session: see [A run that may write works in a
copy](#a-run-that-may-write-works-in-a-copy). Forager's steps of the git session, such as
`join session 2`, start and end inside it. It ends `succeeded` with 0 when the git session
exits 0 within its limit. It ends `cancelled` with -1 when the run is stopped or out of time
during the session. Otherwise it ends `failed`, also when the session reaches its limit of 5
minutes, with the git session's exit status, or with -1 when the session has none to show,
such as one that did not start or one that exits 0 at that limit. Unless it ends `succeeded`,
its last line is the error line, with `the run's folder` in place of the path on the machine.

An image step that fails ends the run before the agent starts, and `qory run` exits 1
with one line, the fix first. An image its registry does not have closes the run
`failed` with the reason `image_missing`, and an image whose user is root, or a user
that cannot be checked for root, with `image_user_root`:

```
qory run: push the image to its registry, or select another image in the run's policy: the image <reference> is not in its registry (image_missing)
qory run: set USER in the image to a user other than root that its /etc/passwd lists, or select another image in the run's policy: <Forager's reason> (image_user_root)
```

Any other failure closes the run `failed` with `no_session`:

```
qory run: run again: the fetch of the run's image failed: <Forager's error>
```

For a run from a run code, the code is used by then. Each refusal line ends with `; the
code is used`, and any other failure says:

```
qory run: take the work item in again in Qory Apiary: the fetch of the run's image failed: <Forager's error>; the code is used
```

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
- The harness is in `~/.cache/qory/homes/runs/<name>-<hash>/<run id>` on Linux, or
  `~/Library/Caches/qory/homes/runs/<name>-<hash>/<run id>` on macOS, and the container has that folder
  read-only. `qory` removes that folder when the run ends. Your checkout gets no `.qory/harness`.

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
container. `gateway.listen`, `gateway.tls` and `gateway.server.enrolment_code_file` are
`qory gateway`'s: `qory run` does not use them, and stops with an error when
`gateway.server.enrolment_code_file` is set. See [the gateway as a service](gateway.md).

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
  stack_hosts: [git.example.com]          # the hosts each run's stack proxy reaches; optional
  lock_memory: true        # Linux only: keep the gateway's memory out of swap; optional
session:
  instance:                # optional
    name: build-01         # how the server shows this machine; the host name by default
  run:                     # optional
    timeout: 5h30m         # stop a run that runs this long, compose included
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

`machine`, beside `apiVersion`, is the adapter that made this machine, such as `aws-ec2`,
or `none`: 1 to 64 of a-z, 0-9 and -, the first a letter. `qory run` passes it to Forager,
which reports it as `machine` in `dev.qory.run.started`; without it, `run.started` has no
`machine`. Any other value stops every command that reads `forager.yaml`. `qory config`
lists it.

## Runtimes

`qory run` runs whichever runtime it composes the harness for. Nothing in `forager.yaml`
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

Set it with `--timeout 5h30m`, or with `session.run.timeout`. The limit counts from the
run's open, so it covers the compose and the session. When the limit is reached:

- the runtime is stopped, or does not start when the compose took the time,
- `dev.qory.run.exited` records the limit as the reason,
- `qory run` exits 124, as `timeout(1)` does.

A compose that is running when the limit is reached runs to its end; the runtime does
not start after it.

`--timeout 0` lifts the limit `forager.yaml` sets.

A run from a run code takes its limit from elsewhere, in this order:
`run_settings.time_limit_minutes` in Qory Apiary's response to the code, then
`workflows.<name>.time_limit` of the run's workflow in the stack that the harness
defines, or in the `qory-stack.yaml` of the stack that it extends, then 3 hours.
`--timeout` stops it with exit 2, and `session.run.timeout` does not
apply to it (see [The run key from a run code](#the-run-key-from-a-run-code)).

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

## Hosts of the stack proxy

`gateway.stack_hosts` in `forager.yaml` lists the hosts that each run's stack proxy
reaches. The stack proxy is a proxy of each run on the gateway, apart from the run's
proxy, and is open until the run's session starts. A run with a run key or a run code
fetches every git source of its harness through it: the stack, its modules, and the
modules of the checkout's file.

```yaml
# ~/.config/qory/forager.yaml
gateway:
  stack_hosts: [git.example.com, "*.example.org"]
```

- Each entry is a lower-case host name, an IPv4 address or a `*.` suffix, as in
  `gateway.egress.allow`, with no port, path or scheme. The stack proxy reaches a listed
  host on any port.
- Empty or left out, the stack proxy reaches no host.
- The stack proxy allows a listed host without the run's policy: a listed host that the
  policy denies is allowed too.
- The stack proxy sets no credential of `gateway.credentials` and no credential of the
  run's work. It sets only the run configuration's harness credentials, those with
  `purpose: harness`, on their hosts and paths, and passes the hosts' other paths with
  no credential.
- Behind a wall or through a separate gateway, the stack proxy reaches the gateway's
  machine or a private network address only for a host that `gateway.stack_hosts` lists
  by name and the allow list of the gateway's policy lists by name: `gateway.egress.allow`,
  or for `qory run --policy`, that file's allow list as `gateway.egress` narrows it. A
  `*.` pattern in either list opens no such address. The stack proxy never reaches a
  link-local or cloud metadata address.
- The stack proxy counts its connections and requests at the limits of
  `gateway.run_limits`, apart from the run's proxy.
- An entry of another form stops every qory command that reads `forager.yaml`, with the
  fix, such as `set gateway.stack_hosts[0] to a lower-case host name, an IPv4 address or
  a *. suffix, such as git.example.com; no port, path or scheme`.
- For each request the stack proxy allows or denies, the gateway prints one line on
  standard error, and the record has a `dev.qory.run.egress` with `channel: stack`.
  When the run configuration sets `harness_hidden`, those records have no `path` and no
  `path_rule`. Of the requests denied at a run limit, the gateway reports at most one a
  second per limit.

`qory gateway` applies the list of its `forager.yaml` to each run it serves. A `qory run`
through a separate gateway gets that gateway's list.

The run configuration that the gateway's server sends can list more hosts, in its
`stack_hosts`, of the same form. The stack proxy passes those hosts as well, for that run
alone, when the gateway's policy allows the host and has no path rule for it. A fetch
through the stack proxy to a host that neither list contains, or that the policy stops,
gets `403`.

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

A run goes through a separate gateway when it is given a run key, by `--run-key-fd`,
`QORY_RUN_KEY` or a run code that `--run-code-file` redeems, or when `forager.yaml` sets
`gateway.server.via` on a machine with a separate access key. qory then starts no gateway
itself. The run key names the gateway; no setting does.

The gateway is `qory gateway` on another machine, or a service on this one: see [the
gateway as a service](gateway.md). The session reaches it over TLS 1.3, at the address the
run key names, and every request carries the run key. The proxy, the policy, the
credentials and the access key are the gateway's, on its machine, and the gateway reports
the run to Qory Apiary. Without a run key or `gateway.server.via`, nothing changes: `qory
run` starts a gateway itself for each run.

The run's first line names the gateway and the run:

```text
qory run: through the gateway gateway.example:8443, run 0192…
```

### The run key

A run's starter gets the run key from Qory Apiary with `qory run register`, and gives it to
the job: see [For a starter](#for-a-starter). It names the run and the gateway. `qory run`
takes it from the descriptor `--run-key-fd` names, or from `QORY_RUN_KEY`, or redeems a run
code for it ([The run key from a run code](#the-run-key-from-a-run-code)):

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
--title go into the run's registration, and a run key is given, by QORY_RUN_KEY,
--run-key-fd or --run-code-file: its starter registered the run already, so give them to
qory run register`.
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
Behind a wall, the run also stops when the image has `/etc/claude-code`, where Claude
Code reads managed settings.

These checks guard against what the run's image and the repository's files leave in place: a
configuration folder, managed settings or variables that change what Claude Code does. A
harness home inside the folder the run runs in, where the repository decides what is
there, stops every walled run and every run with a run key before the compose, see [What `qory run`
does](#what-qory-run-does).
They do not hold against a repository that acts against
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

### The run key from a run code

A machine that a provisioner creates for one run gets a run code, `qecr_…`, and the
gateway's URL in its start data, in place of the run key: a cloud may keep start data
readable for the machine's life. A run code is valid for 15 minutes and is used once. Give
`qory run` the code in a file, and the gateway:

```sh
qory run --run-code-file /run/qory/run-code --gateway https://gateway.example:8443
```

- `--run-code-file <file>`: read a run code (qecr_…) from <file>, redeem it through
  --gateway for the run's key and documents, and run the workflow they set; the key
  stays in memory.
- `--gateway <https URL>`: the gateway that --run-code-file redeems the code through; the
  run key must be for this gateway.

Each flag needs the other, and neither goes with `--run-key-fd` or `QORY_RUN_KEY`. The file
contains the code, with at most one newline after it.

To give the code on stdin, set `<file>` to `/dev/stdin`, `/dev/fd/0` or `/proc/self/fd/0`.
qory then reads stdin to its end and closes it, as with `--run-key-fd 0`, and the agent's
stdin is empty. Stdin may be a pipe or a regular file. qory reads stdin in the same way
for any other path that qory can open to the file, pipe or device that stdin is, such as
a link to `/dev/stdin`.

qory makes a key pair in memory and sends the code, with a proof under that key, through
the gateway's relay to Qory Apiary, which returns the run's run key, signed. qory verifies
the answer under the server key whose fingerprint is first in the code, and the run key as
the gateway verifies it. The run key must be for the `--gateway` URL: the same host, in
any letter case, and the same port, 443 when the URL has none. The run then goes through
the gateway as a run given `--run-key-fd` does. The run key stays in qory's memory: qory
writes the code, the key pair and the run key to no file, log or record, and no program it
starts receives them. As with `--run-key-fd`, an unwalled run is not stopped for the run
key. `qory run resend` takes no run code: the run key is gone once `qory run` exits.

qory redeems the code last before the run opens, after every check of the flags, of
`forager.yaml` and of the wall: a run that one of these checks stops leaves the code
unused. A `--run-id` is compared with the run key's run when the run opens, after the
redemption, so a `--run-id` that differs uses the code; leave `--run-id` out with
`--run-code-file`. The compose of the harness and the checks of the launch, such as a model
credential in `--env`, come after the run opens, so a failure there comes after the code
is used. When a send of the code
fails in transport, so that an answer may have been lost on its way back, qory sends the
same request with the same key pair once more: within the code's 15 minutes, Qory Apiary
returns the same run key to it. qory sends it no third time.

Each of these errors stops the run before it starts, exit 2:

- `add --gateway <https URL>: --run-code-file redeems the run code through that gateway`
- `add --run-code-file <file>, or leave out --gateway: --gateway is the gateway that a run
  code is redeemed through`
- `leave out --run-key-fd or --run-code-file: a run takes its run key from one of them`,
  and `unset QORY_RUN_KEY, or leave out --run-code-file: …`
- `leave out --gateway: it goes with --run-code-file alone, and this run takes its run key
  from --run-key-fd`, or `… from QORY_RUN_KEY`
- `set --gateway to the gateway's https URL, a host and an optional port: <reason>`
- `set --gateway to a gateway that qory can reach: <error>`, when Forager's check of the
  URL or of its TLS settings fails
- `give --run-code-file a file that qory can read: <file>: <error>`
- `give --run-code-file a regular file: <file> is not one`, for a named pipe, a device or
  a folder, which qory opens without waiting, unless it is the pipe or device that stdin is
- `write the run code into <file>: the file is empty`
- `write only the run code into <file>, with at most one newline after it: the file
  contains more`
- `write a run code (qecr_…) into <file>: the file does not contain an enrolment code of
  Qory Apiary`, or `…: the file contains the enrolment code of an access key of kind
  <kind>`
- `ask for a new run code: the redemption returned 401, so the code was used, has expired
  or was cancelled, or it was redeemed with another key first (unauthorized)`
- `ask for a new run code, and give qory run the gateway that the run key is for: the run
  key is for the gateway <host:port>, and --gateway is <host:port>; the code is used`
- Forager's lines that start with `use another run code: `, for a signed response that
  used the code and that qory does not run with: a run key that does not verify, or the
  response of an access key's code
- `update qory, then ask for a new run code: the response to the redemption is of a
  version that this qory does not read; the code is used`

These stop it with exit 1. The code may be unused, so each fix runs qory again with it:

- `run qory run again within the run code's 15 minutes, and ask for a new run code if the
  redemption then returns 401: the redemption through the gateway <host:port> failed
  twice: <error>`, when both sends failed in transport, other than by a TLS certificate
  that does not verify
- `check that --gateway is the run's gateway, then run qory run again within the run
  code's 15 minutes, and ask for a new run code if the redemption then returns 401: the
  redemption through the gateway <host:port> failed twice: <error>`, when the gateway's
  TLS certificate does not verify: its authority is not trusted, it is for another host
  name, it has expired or is not yet valid, or its key usage does not allow a server
- `check that --gateway is the run's gateway, then run qory run again within the run
  code's 15 minutes, and ask for a new run code if the redemption then returns 401: the
  response to the redemption (HTTP <status>) does not verify under the server key whose
  fingerprint is first in the code (answer_unsigned)`
- `run qory run again within the run code's 15 minutes: Qory Apiary returned 409 to the key
  pair that qory made for the redemption (<code>)`
- `run qory run again later, within the run code's 15 minutes: Qory Apiary returned 429 to
  the redemption, too many attempts with this code (rate_limited)`
- `update qory, then run qory run again within the run code's 15 minutes: Qory Apiary
  returned a signed refusal whose code this qory does not read: <error>`
- `run qory run again within the run code's 15 minutes, and ask for a new run code if the
  redemption then returns 401: qory run stopped before the redemption completed: <cause>`

No error contains the code or the run key.

A starter that shows qory's lines apart from the agent's output sets `QORY_STATUS_FD` to
an open file descriptor above 2, as in `QORY_STATUS_FD=3 qory run … 3>&2 2>&1`. `qory run`
then also writes each of its lines there, one line per write, masked as
[Secret values in qory's lines](#secret-values-in-qorys-lines) describes: the lines that
start with `✓`, `✗` or `⚠` and their continuations; every `qory run:` line, Forager's
lines among them; and the last error line, an error of the flags included. Each of these
lines goes there only when qory prints it on stderr too. The output of the programs that
qory starts, the agent and git among them, does not go there, and neither do the lines of
a prebuild, which contain the output of its build. As it starts, before it opens any
file, every qory command removes `QORY_STATUS_FD` from its environment and marks the
descriptor close-on-exec, so no program that it starts receives either. A value that is not a number, is 0, 1 or 2, or is not an
open descriptor gets one line on stderr from `qory run`, which then writes its lines to
stderr only. After a write to the descriptor fails, qory prints one line on stderr and
writes nothing more there; the run goes on. The descriptor works on Linux and macOS only.

#### What the run takes from the response

Qory Apiary's signed response to the code contains the run's documents beside the run key:
the step, the domain and the inputs; for the step explore, the work item, the run
settings, the harness, the workflow and `harness_hidden`; for the step prebuild, the target
and the places of its images at a registry. qory runs the steps explore and prebuild.
qory checks the documents as the run contract's `documents.schema.json`, and, for the
step explore, checks that `inputs.domain` and `inputs.work_item.id` match the domain and
the work item. Then a run of the step explore:

- composes the harness that the documents contain, or, when they contain none, the
  checkout's `qory.yaml` or `harness.yaml`, through the run's stack proxy (see [The run's
  harness](#the-runs-harness));
- when `harness_hidden` is `true`, composes a home that contains no link, and has
  Forager mask text that matches the home's files in the session's log and events
  (see [A hidden harness](#a-hidden-harness));
- runs the runtime of `run_settings.runtime`, `claude` when the response sets no run
  settings, composed with the model, the effort and the tiers the run settings set, as
  `--model`, `--effort`, `--tier` and `--tier-effort` set them;
- writes the inputs, unchanged, to `inputs.json`, mode 0600, in a folder `in/`, mode
  0700, of a new temporary folder, which qory removes when the run ends. Behind a wall,
  `in/` is mounted read-only at the same path;
- starts the runtime with its headless start: the arguments that run one prompt with no
  terminal, write the session's output to stdout and exit. The prompt invokes the
  first of these that the runtime can start: the skill `workflow-<workflow>`, the command
  `workflow-<workflow>`, the skill `<workflow>`, the command `<workflow>`. A plain name
  works, and when a harness ships both names, the prefixed one runs. The prompt uses the
  name the runtime registers the entry under, with `<in>/inputs.json` after it. Each
  runtime's start, after its launch's arguments:

  | Runtime | Headless start | Invokes |
  |---|---|---|
  | `claude` | `--output-format stream-json --verbose --add-dir <in> -p "/<name> <in>/inputs.json"`, where `<name>` is such as `harness:hello` | a skill or a command |
  | `codex` | `exec --json --add-dir <in> "$<name> <in>/inputs.json"` | a skill |
  | `gemini` | `--output-format stream-json --include-directories <in> -p "/<name> <in>/inputs.json"` | a command |
  | `opencode` | `run --format json --command <name> <in>/inputs.json` | a command |
  | `cursor` | `--print --output-format stream-json --add-dir <in> "/<name> <in>/inputs.json"` | a skill |

  `amp`, `copilot`, `goose` and `any` have no headless start, so a run from a run code
  does not start on them;
- takes its time limit as [Time limits](#time-limits) says.

So the command line sets none of these. After the redemption, qory prints `qory run: the
run <run id> starts the workflow <workflow> of the workspace's harness on claude` for a
run whose documents contain a harness, or `… of the checkout's harness on claude` for one
whose documents contain none.

When the composed harness has no skill or command under either name, the run stops
before the agent starts, exit 1, and qory closes the run `failed` with the reason
`workflow_not_in_harness`, in place of `no_session`:

```
add a <kind> workflow-<workflow> to <harness>, or set another workflow in Qory Apiary: the composed harness has no skill or command workflow-<workflow> or <workflow> (workflow_not_in_harness); the code is used
```

`<kind>` is `command` on `gemini` and `opencode`, and `skill` on `claude`, `codex` and
`cursor`. `<harness>` is `the workspace's harness` for a run whose documents contain a
harness. For one whose documents contain none, it is `the checkout's harness`, with the target's
`path` after it when the documents have a target: `the checkout's harness in the
repository <path>` when the target's `system` is `QORY_RUN_SYSTEM`, and `the checkout's
harness for the target <path>` on any other system. A workflow whose skill or command
the runtime runs by no name from a headless start closes the run with `no_session`.

A run sees the checkout read-only when `access` in its documents is `read`, or when
`workflows` of the stack declares its workflow's access as `read`. Such a run with no
wall, with `wall.setup: inside` or on an image with nested Docker stops after the compose,
exit 1, and qory closes it `failed` with `read_needs_wall`, `read_needs_setup_outside` or
`read_needs_no_nested_docker`. A writable mount that contains the checkout, lies inside it
or links to it also stops the run, later, and the agent does not start. qory removes the
setup container of such a run before the agent starts, and closes the run `failed` with
`read_needs_setup_removed` when the removal fails. See [Read workflows](#read-workflows).

The documents of a run with a target may set `target_version`: the version at which Qory
Apiary checked the target for a harness file. When the target's `system` is
`QORY_RUN_SYSTEM`, the target is the run's repository: Qory Apiary lists the target's
`path` first in `QORY_RUN_REPOSITORIES`, compared in any letter case on github.com. qory
fetches that repository at the version, which must be a full commit id, by its id, in
place of the head of its default branch. The fetch goes through the run's gateway at
depth one, and the working folder is on a local branch with the default branch's name,
at that commit, with no upstream, so a bare `git push` or `git pull` has no upstream to
use. The fetch line shows the commit: `qory run: fetched example-org/shop at
4b8e2f1a9c7d in 1.4s`. When the documents contain no harness document, the compose reads
the checkout's `qory.yaml` or `harness.yaml` as that commit has it. The further
repositories are fetched at the head of their default branches.

When the target is on another system than `QORY_RUN_SYSTEM`, or `QORY_RUN_SYSTEM` is
not set, qory does not use `target_version`.

A run of a work item at a `target_version` of a target on `QORY_RUN_SYSTEM` works on the
branch `qory/<work item id>`. qory reads the work item and its artifacts through the run API
before the fetch, and picks one version of the artifact type `branch`:

1. The newest version of the artifact named `main`, if the work item's stage wrote it.
2. Else, the version that a gate approved, of any artifact name, if the stage lists
   `branch` as an input.
3. Else, the newest version of the artifact named `main`.
4. With no version, the run creates the branch at `target_version`.

qory composes the harness at `target_version` and with the further repositories at their
default branches, so a branch does not change the harness of its work item's runs. The
fetch step also fetches each repository's commit by id, at depth 1. After the compose,
qory checks out the branch with no request to a remote: at the commit in the version, or
where the fetch left a repository with no entry in the version. The working folder then
has the commit's files. qory leaves the ref `refs/qory/target-version` at the target
version, so a file that the branch removed stays out when the run starts from an image's
copy.

A run stops before the agent starts when:

- the version does not match the [branch format](../contracts/harness/v1/README.md#the-branch-format),
  or its `ref` is another work item's;
- a commit is not on its repository;
- the target's commit has another `.qory` than `target_version`;
- git does not check out a commit.

An instance without the feature `workflow_model` returns 404 with no error code to the run
API. The run then works on `target_version` with no branch.

A write run on the branch pushes what the agent left to it: see [The push step of a write
run](#the-push-step-of-a-write-run).

Each of these stops the run before the compose, exit 1, and the run's record shows the
run as failed with no session. `<commit>` is the commit's first 12 characters:

- `report this run to the instance admin of Qory Apiary: the run's documents set the
  target <path>, but the run configuration lists it after another repository; the target
  must be the first repository of the run configuration; the code is used`, before any
  fetch
- `report this run to the instance admin of Qory Apiary: the run's documents set the
  target <path>, but the run configuration does not list it; the target must be the
  first repository of the run configuration; the code is used`, before any fetch, also
  when the run configuration lists no repository
- `ask Qory Apiary's instance admin to check the adapter of the target: the run's
  documents set the target version <version>, and qory fetches a target at a version
  only when the version is a commit id of 40 or 64 hex digits; the code is used`, before
  any fetch
- `take the work item in again in Qory Apiary: the commit <commit> that the run's
  documents set is not on <path> on <host>. The fetch through the run's gateway failed:
  <git's line>; the code is used`
- `let <host> serve a fetch of a commit by its id, such as with git's
  uploadpack.allowReachableSHA1InWant, then take the work item in again in Qory Apiary:
  <host> does not serve the commit <commit> of <path> by its id, or does not have it. …`,
  from a server that git reaches over git's protocol version 0 or 1 and that does not
  allow a fetch of a commit it does not list. Over protocol version 2, which git uses by
  default, git's server needs no such setting.

A run of the step prebuild builds the image that the runs of its target start from. It
runs behind the wall of its run, in the image of `wall.images` that `wall.prebuild_image`
is set to, with no agent:

1. qory opens the run at its gateway, with the wall. When the run's policy is not the
   one of the run configuration Qory Apiary serves for the workspace, qory stops here with
   `set a run configuration for the run's workspace in Qory Apiary: a prebuild runs under
   its policy alone, and the run's policy is <source>`, where `<source>` is `the gateway's
   machine's`, `none` or `not recorded`.
2. qory fetches the run's repositories through the run's host proxy into an empty working
   folder under `pool/runs/<run id>` in qory's state directory. git runs on this machine
   for the fetch, outside the wall.
3. One session joins the run, with qory's helper, `qory run prebuild-step`, as its
   command, the working folder as its folder and a writable `out` folder beside it. The
   session gets none of this machine's variables and no model key.
4. Behind the wall, the helper builds the development container configuration on the
   wall's nested Docker, in two steps: the environment image, tagged
   `<environment_destination>:<run id>`, then the target image from it, with the agent
   added, tagged `<image_destination>:<run id>`. The build keeps the environment image
   apart only when the image depends on the configuration's bytes alone. When the
   documents set `environment_image`, the build starts the target image from that image
   and builds no environment image. The build's steps get the run's proxy as Docker's
   predefined build arguments, `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY`, which the
   image does not keep. The configuration's commands get the proxy and the run's
   authorities from a folder bound read-only at `/qory/proxy`, never from the
   container's environment.
5. The helper pushes the environment image, when the build kept one apart, then the
   target image, with `docker push` through the run's proxy and a docker configuration
   that has no login. It writes what it pushed to `out/result.json`.
6. qory reads `out/result.json`: a regular file of at most 1 MiB, opened without
   following a link, whose images are under `image_destination` and
   `environment_destination`. Then qory writes the run's `dev.qory.run.built`: the target
   image by digest, its size, the commit and the inputs' `environment_ref`. The event has
   the environment image by digest when the helper pushed one, or the build started from
   `environment_image`. It has the base image by digest when the build resolved the
   image its target step starts from, and none when the build started from
   `environment_image`, from scratch, or from an image it cannot resolve before it
   builds. Then qory closes the run as succeeded, and exits 0.

The build takes the agent image and the user from `wall.image`, pinned by its digest, and
`wall.user` in `forager.yaml`. The wall's helper is `wall.helper`, or on Linux the `qory`
you run. When `forager.yaml` sets `pool.registry`, qory stops a run whose
`image_destination` or `environment_destination` is not under it, before anything is
made, with `set pool.registry to the registry of Qory Apiary's QORY_REGISTRY, or ask its
instance admin: the run's <member> is not under <registry>`.

The nested Docker's images, its build cache and the container the configuration's
commands ran in go with the wall when the run closes. This machine's Docker engine
builds and keeps none of them, and its registry logins are not read. Each prebuild pulls
and builds from the start.

What the instance admin installs on the machine:

- Linux, with [Sysbox](https://github.com/nestybox/sysbox) registered as `sysbox-runc`;
- Docker Engine 28 or later;
- a file with the machine's certificate authorities, which the wall joins with the run's
  authorities;
- [Qory's](#qorys-images) `prebuild` image, or one built FROM it, defined in
  `wall.images` with `runtime: sysbox-runc` and `docker: true`, and named in
  `wall.prebuild_image`.

The nested containers reach the outside only through the run's proxy, so these build
steps fail behind the wall:

- `ssh`, `git://` and anything over UDP;
- a pull of a base image, or of the `docker/dockerfile` frontend, from a host that is not
  in the run's egress.

After the redemption, qory prints `qory run: through the gateway <host>, run <run id>`,
with `:<port>` after the host when the gateway's URL has a port. Then it prints each line
of the fetch, and of the session behind the wall, after `qory run: <run id>: `. When the
registry of `environment_image` has no such image, qory closes the run as failed with the
reason `image_missing`. A fetch past its 15 minutes closes the run as cancelled with the
reason `timeout`, a signal as cancelled with the reason `interrupted`, and any other
failure as failed. In each case qory exits 1. When events of the run did not reach the
gateway at the close, qory keeps the run's folder under `pool/runs/<run id>`, the record
and those events in it. After a prebuild that succeeded, qory exits 1 with `check that
this machine reaches the run's gateway: <n> events of the run did not reach it;
<record>/undelivered contains them`, with `1 event` for one. After a failure, qory prints
that line as `qory run: <run id>: check that this machine reaches the run's gateway: …`,
and exits 1 with the failure's error. qory removes the run's folder in every other case.

Each of these errors stops the run before the code is sent, exit 2:

- `leave out -f: a run from a run code composes the harness in Qory Apiary's response to
  the code, or the checkout's qory.yaml or harness.yaml`
- `leave out the runtime <name>: a run from a run code takes its runtime from Qory
  Apiary's response to the code`
- `leave out --model, --effort, --tier and --tier-effort: a run from a run code takes them
  from Qory Apiary's response to the code`
- `leave out the arguments after --: a run from a run code starts its workflow with the
  arguments qory sets`
- `leave out --timeout: a run from a run code takes its time limit from Qory Apiary's
  response to the code, else from its workflow`

These stop it after the redemption, exit 1. The code is used, so each needs a new one:

- `update qory, or ask Qory Apiary's instance admin: the response to the run code does not
  pass qory's checks: <reason>; the code is used`, where `<reason>` is a check that failed,
  such as `it breaks documents.schema.json`, and contains no value of the response
  but a member's name, an id, a revision or a byte count
- `update qory: the response to the run code is for the step <step>, and qory runs a run
  code's run for the steps explore and prebuild only; the code is used`
- `set wall.image, pinned by its digest, wall.user and wall.prebuild_image in
  forager.yaml: a prebuild from a run code builds with them; the code is used`
- `run the prebuild on a machine without an access key: this machine has an access key in
  <path>, and a prebuild fetches the target's code with git on this machine, outside the
  wall; the code is used`, when a file with an access key's secret appeared beside `forager.yaml` while
  the code was redeemed
- `set a workflow for the workspace in Qory Apiary, then take the work item in again: the
  response to the run code has no workflow; the code is used`
- `set another runtime for the workspace in Qory Apiary, then take the work item in
  again: qory starts a run code's workflow with a runtime's headless start, and has none
  for <runtime>; the code is used`, before anything is fetched
- `add a <kind> workflow-<workflow> to <harness>, or set another workflow in Qory
  Apiary: the composed harness has no skill or command workflow-<workflow> or <workflow>
  (workflow_not_in_harness); the code is used`, after the compose, which closes the run
  with the reason `workflow_not_in_harness`; [What the run takes from the
  response](#what-the-run-takes-from-the-response) lists the values of `<kind>` and
  `<harness>`
- `run this workflow with a wall: this run of workflow <workflow> only reads the checkout,
  and a run with no wall mounts nothing read-only (read_needs_wall); the code is used`,
  after the compose, for a read run with no wall, which closes the run with the reason
  `read_needs_wall`
- `set wall.setup to outside in forager.yaml, or remove it: this run of workflow
  <workflow> only reads the checkout, and setup inside runs in the agent's container,
  which sees the checkout read-only (read_needs_setup_outside); the code is used`, after
  the compose, for a read run with `wall.setup: inside`, which closes the run with the
  reason `read_needs_setup_outside`
- `use an image without nested Docker: this run of workflow <workflow> only reads the
  checkout, and image <image> has nested Docker, so the checkout cannot be made read-only
  (read_needs_no_nested_docker); the code is used`, after the compose, for a read run on
  an image with `docker: true`, which closes the run with the reason
  `read_needs_no_nested_docker`; see [Read workflows](#read-workflows)
- `start the run again from the work item in Qory Apiary: the setup container of this
  read run was not removed, so a process in the container can write the checkout: <error>
  (read_needs_setup_removed); the code is used`, after the setup, for a read run whose
  setup container was not removed, which closes the run with the reason
  `read_needs_setup_removed`; see [Read workflows](#read-workflows)
- `add a command workflow-<workflow> to <harness>, or set another runtime in Qory Apiary:
  <runtime> runs no skill by its name from a headless start, and the composed harness has
  no command workflow-<workflow> or <workflow>; the code is used`, after the compose, for
  a workflow that is only a skill on `gemini` or `opencode`
- `add a skill workflow-<workflow> to <harness>, or set another runtime in Qory Apiary:
  <runtime> runs no command by its name from a headless start, and the composed harness
  has no skill workflow-<workflow> or <workflow>; the code is used`, after the compose,
  for a workflow that is only a command on `codex` or `cursor`
- `take the work item in again in Qory Apiary: the run's host proxy, through which qory
  fetches the run's repositories, did not open: <error>; the code is used`, where a run
  without a run code prints `run again: the run's host proxy, through which qory fetches
  the run's code, did not open: <error>`

#### The run's harness

The documents contain the harness, a harness document like a checkout's `harness.yaml`,
when the workspace in Qory Apiary sets one. The harness extends a stack, under
`extends`, or defines one: with no `extends`, it lists at least one module and may list
`workflows`, as a `qory-stack.yaml` does. It has no `target`: qory composes a stack it
defines for the run's runtime, `run_settings.runtime`, or `claude` when the documents
have no run settings. Its `extends` and the source of each of its modules are git
sources, which `documents.schema.json` requires. A source with no `git` address, a
harness with no `extends` and no module, or `workflows` beside `extends` fails that
schema, and the run stops after the redemption with exit 1 and the line `update qory, or
ask Qory Apiary's instance admin: …`.

When the documents contain a harness, qory writes it to `harness.yaml`, mode 0600, in
the run's folder, and composes that file in place of the checkout's `qory.yaml` or
`harness.yaml`, with `target.runtime` set to the run's runtime for a harness that
defines a stack. qory fetches the stack that the harness extends, and every module that
the harness or the stack's file lists from another repository, through the run's stack
proxy. The stack proxy is a proxy of the run's gateway that passes the hosts of
`gateway.stack_hosts` in the gateway's `forager.yaml` and of the run configuration's
`stack_hosts` alone, with no rule of the run's policy. It sets the run configuration's
harness credentials and no credential of the run's work (see [Hosts of the stack
proxy](#hosts-of-the-stack-proxy)). So list the host of each of those repositories in
one of the two lists, and for a private one, give the run configuration a harness
credential that covers it. qory opens the stack proxy for the compose and
closes it before the session joins.

When the documents contain no harness, qory composes the checkout's `qory.yaml` or
`harness.yaml`, as a run without a run code does, and fetches the stack it extends and
every module from another repository through the run's stack proxy too. The run's host
proxy, with the run's credentials, fetches only the run's repositories. When the checkout
has neither file, the run stops with exit 1, before anything is fetched:

- `take the work item in again in Qory Apiary: the run's documents contain no harness and
  the checkout has no qory.yaml or harness.yaml; the code is used`

These stop a run whose documents contain a harness after the redemption, exit 2, before
the run opens and before anything is fetched. `<source>` is `extends`, `the module
<module>`, or `modules[<n>]` for a module listed without a name:

- `remove the user name and password from the URL of <source> of the harness in the
  workspace's settings in Qory Apiary: the run's stack proxy sets the harness
  credentials of the run configuration, so a source's URL needs none; the code is
  used`, for a URL that has a user part or an `@` between `://` and the first `/`
  after it, whatever comes before the `@`
- `correct the URL of <source> of the harness in the workspace's settings in Qory Apiary:
  qory could not read it as a URL; the code is used`
- `write the host in the URL of <source> of the harness in the workspace's settings in
  Qory Apiary in ASCII, with the xn-- form for an international name: the host has a
  character outside ASCII, which git may map to another, such as a fullwidth @ to @; the
  code is used`, for a host that has a character outside ASCII once its
  percent-encodings are decoded
- `set <source> of the harness in the workspace's settings in Qory Apiary to an https
  source: <url> is not an https URL with a host, and qory fetches a harness only over
  https; the code is used`, with `its URL` in place of `<url>` when `harness_hidden` is
  `true`
- `set the path of <source> of the harness in the workspace's settings in Qory Apiary to
  a folder inside its repository: the path is absolute, the repository's root, or leads
  out of the repository; the code is used`

These stop it during the compose, each with the source as the lines show it. The first
five also stop a run from a run code whose documents contain no harness, and a run given
a run key. Such a run shows `the checkout's harness` or `the run's harness` in place of
`the workspace's harness`, and a module of the checkout's file `in the checkout's
harness` or `in the run's harness`. A run given a run key has no `harness_hidden` and
shows its addresses, so the fifth line applies to it. In place of the sixth, it prints
`run again: the run's stack proxy, through which qory fetches the run's harness, did not
open: <error>`:

- `remove the user name and password from the URL of the base of the checkout's harness:
  the run's stack proxy sets the harness credentials of the run configuration, so a
  source's URL needs none`, exit 2, before the base is fetched, for a checkout's file
  whose `extends` has a URL with a user part or an `@` between `://` and the first `/`
  after it; `correct the URL of the base of …: qory could not read it as a URL` and
  `write the host in the URL of the base of … in ASCII, …` for the other two checks of a
  module source below
- `remove the user name and password from the source of the module <module> in the base
  of the workspace's harness: the run's stack proxy sets the harness credentials of the
  run configuration, so a source's URL needs none`, exit 2, before any module is
  fetched, for a module source in the stack's file whose URL has a user part or an `@`
  between `://` and the first `/` after it, and `the source of modules[<n>]` for a
  module listed without a name
- `correct the URL in the source of the module <module> in the base of the workspace's
  harness: qory could not read it as a URL`, exit 2, before any module is fetched
- `write the host in the source of the module <module> in the base of the workspace's
  harness in ASCII, with the xn-- form for an international name: the host has a
  character outside ASCII, which git may map to another, such as a fullwidth @ to @`,
  exit 2, before any module is fetched
- `check that <url> has the branch, tag or commit <ref> and that gateway.stack_hosts in
  the gateway's forager.yaml or the run configuration's stack_hosts lists <host>, and,
  for a private repository, that the run configuration has a harness credential whose
  hosts and paths include the repository. The fetch through the run's stack proxy
  failed: <cause>`, when `harness_hidden` is
  `false`: exit 2 when the repository has no branch or tag `<ref>`, with `<url> has no
  branch or tag <ref>` as the cause, and exit 1 when the fetch failed, as it does for a
  commit that the repository does not have
- `take the work item in again in Qory Apiary: the run's stack proxy, through which qory
  fetches <harness>, did not open: <error>; the code is used`, exit 1, where `<harness>`
  is `the workspace's harness` or `the checkout's harness`
- `check the run's folder <folder>: qory run could not write the workspace's harness
  there: <error>; the code is used`, exit 1

The stack file of a git base, the `qory.yaml` of the base's repository, and the
`qory.yaml` of a repository that lists the exports a source selects may be links that
the repository contains. When such a link leads outside the clone, qory stops with
exit 2 before it reads the file, for a run from a run code and for any other run:

- `check the folder of <base>: its qory-stack.yaml links outside its repository`, where
  `<base>` is `the base of the workspace's harness` or `the base of the checkout's
  harness`, and `extends <url>#<ref>:<path>: qory-stack.yaml links outside the
  repository` when the harness is shown
- `check <base>: the qory.yaml of its repository links outside its repository`, and
  `extends <url>#<ref>:<path>: qory.yaml at the root of its repository links outside the
  repository` when the harness is shown
- `the qory.yaml of the repository of <source> links outside its repository`, and
  `qory.yaml in <url> at <ref> links outside the repository` when the harness is shown

When `harness_hidden` is `true`, every harness source counts as hidden: the documents'
harness, the checkout's file, the stack either one extends, and every module they list.
Each line, each event of the run's record and each error then shows the source by a
phrase and the module by its name, in place of a URL, a ref and a folder:

- `qory run: fetched the base of <harness> at <commit> in <n>s`, where `<harness>` is
  `the workspace's harness` or `the checkout's harness`, and `qory run: fetched the module
  <module> at <commit> in <n>s` for a module from another repository;
- `harness "base of <harness>" at <commit>` and `harness "module <module>" at <commit>` in
  Forager's line `session <n> starts on this machine: …`;
- a file of the stack as its path from the stack's folder, such as `README.md in the base
  of the workspace's harness`, a file of a module as `<path> in the module <module>`, and
  no path in `<run>/sources`;
- none of git's output, which can contain the URL.

When the stack that the harness extends has no `name`, the compose report records the
base as `base`, not by its folder. The clones are in folders of `<run>/sources` named by a
hash of the URL. The addresses are still in files on the machine that qory's user can
read: in each clone's `.git/config` and `FETCH_HEAD`, in the run's `harness.yaml`, and in
the compose report beside the home, `<home>-report.json`. Behind a wall, the agent's
container gets none of them: Forager stops a run with a mount that is, contains or lies
inside qory's state directory, which contains the run's folder, before the agent starts,
and qory stops a run whose mounts would include a harness report before the harness is
fetched, as [What `qory run` does](#what-qory-run-does) describes.

These stop a run whose harness is hidden during the compose, each with the source by its
phrase:

- `add the host of <source> to gateway.stack_hosts in the gateway's forager.yaml or to
  the stack_hosts of the run configuration that Qory Apiary sends, and, for a private
  repository, a harness credential for <source> to that run configuration, then take
  the work item in again: the fetch of <source> through the run's stack proxy failed:
  <cause>; the code is used`, exit 1, where `<cause>` is the status the stack proxy or
  the host returned, such as `the stack proxy returned 403, as it does for a
  host that neither list of stack hosts contains, and for a host of the run
  configuration's stack_hosts that the gateway's policy denies or has path rules for`,
  or git's exit status
- `take the work item in again in Qory Apiary: the run's stack proxy returned 407 Proxy
  Authentication Required to the password Forager set for the run, so the fetch of
  <source> failed; the code is used`, exit 1
- `set the ref of <source> to a branch, tag or commit its repository has: the repository
  of <source> has no branch or tag by the ref it is set to`, exit 2
- `use an https URL for <source>: a run from a control plane fetches git sources only
  over https`, exit 2
- `<source> exports <export> at a folder that is not there`, exit 2
- `module <module>: source.path is not a directory inside the repository`, exit 2, for a
  module of the stack's file or of the checkout's file whose path is absolute, the
  repository's root, or leads out of it
- `qory.yaml in <source>: exports.dir <stacks or modules> is not a directory inside the
  repository`, exit 2, for an `exports.dir` that is absolute or leads out of its
  repository

When `harness_hidden` is `false`, the lines and errors show the addresses, as for any
harness.

#### A hidden harness

When the documents set `harness_hidden` to `true`, the home contains copies of every
module's files in place of links, as the home of a walled run does: `modules/<name>`,
each skill, agent, command and hook, and each `files` entry. qory also sets Forager's
`HarnessHidden` for the run.

The copy follows these rules:

- A link inside a module is copied as the file or directory it leads to.
- A link that leads out of the module stops the run, exit 1, before the session starts,
  with one of these errors:
  - `module <name>: Remove the link <path> or point it inside the module. It links to
    <target>, outside the module, and qory copies only the module's files into the
    home.`, for a link to a file or directory outside the module;
  - `module <name>: Remove the link <path> or point it inside the module. It points out
    of the module, and qory copies only the module's files into the home.`, for a link
    out of the module that leads to nothing.
- Without a wall, the copy contains the files that the stack's `only` or `exclude`
  leave out, as a link to the module shows them. Behind a wall, the copy leaves them
  out.

Forager reads every regular file of the home as the session starts, before its join
ends. Then, in each `dev.qory.run.log` chunk of the session, the lines of its setup
steps among them, and in each event that the runtime's descriptor maps from an output
or hook record, Forager replaces text with `[harness text: <path>]` when the text
matches either of these:

- at least 3 consecutive lines of one file, each line compared without the whitespace
  at its start and its end, and the lines that are empty without it skipped;
- at least 200 consecutive characters of one file, line breaks included.

`<path>` is the file's path in the home, such as `skills/greet/SKILL.md`. A shorter
match stays as it is. Forager masks none of these:

- the command and arguments in `session.started`;
- the lines of the host steps, such as the fetch and the compose;
- the lines of a setup that runs outside a session;
- the copy of the runtime's output that `qory run` prints to its stdout and stderr;
- the failure line of a join that fails before Forager reads the home, and the error
  of the join.

Forager masks no text of a file that contains a zero byte, and `qory run` prints
`qory run: the text of the harness file <path> is not masked: it contains a zero byte`.

The join fails, before the runtime starts, when the home is over one of Forager's
limits. Each error starts with its fix:

- more than 10,000 regular files: `make the harness home at most 10000 files: <path> is
  file 10001`;
- a file of more than 1 MiB (1,048,576 bytes): `make each harness file at most 1048576
  bytes: <path> has <size>`;
- more than 16 MiB (16,777,216 bytes) in all, not counting the files that contain a
  zero byte: `make the harness files at most 16777216 bytes in all: with <path> they
  have <size>`.

When `harness_hidden` is `false`, the home of a run without a wall keeps its links to
the modules, as the home of a run started without a run code does.

### The push step of a write run

A write run of a work item ends with a push step: qory commits what the agent left, pushes
it to the branch `qory/<work item id>`, and stores a new version of the artifact `branch`.
The next run in the same stage starts at that commit. A run of another stage starts at the
version a gate approved when its stage lists `branch` as an input, and at that commit
otherwise. The step runs after the agent exits and before the run closes. A setup container
is gone by then: qory removes it before the step, in the wall step `remove the setup
container`, so no process in it writes the working folder after the check. Its kind is `push` and its name is `push qory/<work item
id> to <system>/<path>`, followed by ` and 1 further repository` or ` and <n> further
repositories` for a run with more repositories.

The step runs when the run works on the branch of its work item, its documents set `access`
to `write`, `workflows` of the stack declares the workflow's access as `write`, and an agent
session ran, however it ended. A workflow that the stack does not declare counts as `read`.
A session that failed, timed out or was cancelled gets the same step and the same checks as
one that succeeded. The commit keeps its subject and gets the body line `The run ended
<state>: <reason>.`, and the version's note is the same line. `<reason>` is `exit <code>`
for a session with no reason. A session that succeeded has neither.

A run has no step in these cases. Nothing reaches the branch: in a walled run that may write,
what the agent left stays in the run's copy or lands on `qory/run-<run id>` in the checkout on
that machine, and with no wall it stays in the working folder.

- A read run.
- A run whose session did not start, or stopped with an error of the session itself.
- A run that Qory Apiary ended during its session. What the agent left stays on the machine
  and is deleted with it, so nothing reaches the branch.
- A run on a machine that is lost: nothing is left to run the step.

The step does this, in order:

1. A walled run works in a copy: see [A run that may write works in a
   copy](#a-run-that-may-write-works-in-a-copy). qory starts a second session on the run,
   the git session, in the step `copy work out`, before the push step. The git session
   commits what the agent left in each repository of the copy, and writes the run's branch
   to a bundle per repository. The commit has the message `Changes left by run <run number>`
   and the author and committer `qory <qory@localhost>`. A new file is in the commit unless
   git ignores it through `.gitignore`. `.qory` and the links into it stay out, also when
   the agent staged them. When a commit of the agent has them,
   the git session's commit puts them back as they were at the start of the run. A
   repository inside a repository stays out, as its files and as a gitlink. A path that
   the setup changed stays out while it is as the setup left it: after a setup outside the
   agent's container, qory records each changed path and its content, and the commit has a
   path only if the agent changed it again. With `wall.setup: inside`, the setup runs in the
   agent's container, so what it changed counts as what the agent left. No hook runs.
2. Host git fetches each bundle into a new quarantine repository, with git's object checks
   on. A commit of the run that adds `.git` or `.qory` at the top of a repository stops
   the step here, and no commit is pushed. qory prints `qory run: the run's commits in
   <folder> change these qory settings, links or repository folders: <keys>. Check them before you check out the
   branch.` for a repository whose commits change a setting that qory acts on.
3. An agent with no wall has no copy, and qory works in the working folder. It checks each
   repository, since the agent could write each `.git`, and commits what the agent left, as
   in step 1. A `.git/index.lock` fails the step, since the stop of such an agent is not
   proven.
4. qory pushes each repository whose head is past the commit the run started at to
   `refs/heads/qory/<work item id>`, through the run's host proxy, with no force. For a
   walled run, git runs in the quarantine repository and never in the folder the agent
   wrote. It
   prints `qory run: pushed qory/<work item id> to <path> at <commit>`, where `<commit>`
   is the commit's first 12 characters. With no new commit in any repository, it prints
   `qory run: no new commits to push`, and the step ends with no push and no version.
5. qory stores a new version of the artifact `main` of the type `branch`, with one file,
   `branch.json`, in the [branch format](../contracts/harness/v1/README.md#the-branch-format):
   the version the run read, with the commit of each pushed repository replaced, or added
   when the version has no entry for it. After a 429, qory waits for `Retry-After`, at
   most 60 seconds, and sends the request again, at most 5 requests in all. After a request
   with no answer, or with a status of 500 or above, qory reads the work item's artifacts
   first, and sends the request again only when no version of this run has the file.

A run's policy with path rules must allow `/<path>.git/git-receive-pack` for the push.

The step counts toward the run's time limit, and has 5 minutes when the run has less left,
also after the session used up the limit. A run can pass its time limit by up to 5
minutes. A first signal, before or during the step, cuts the step to at most 15 seconds. A
second signal ends the step at once. A step that one of these ended has the state
`cancelled`. The git session counts toward the same time, and its limit is also 5 minutes, so
a git session that uses all of it leaves the push nothing, and that work does not come back.
On an image that runs nested Docker, starting the engine can use up to 120 seconds of that
time. A git session that ends past that time, or that fails, brings nothing back: the step pushes nothing, the run's
copy goes, and the step prints `take the work item in again in Qory Apiary` with the cause.

The run closes as its session ended, whatever the step did: a session that timed out closes
`cancelled` with the reason `timeout`, exit 124, and a session that failed closes `failed`.
The line of a failed step is printed after the run's end. After a session that succeeded,
the step sets the close:

- A failed step closes the run `failed`, exit 1.
- A step stopped at the time limit closes the run `cancelled` with the reason `timeout`,
  exit 124.
- A step stopped after a signal closes the run `cancelled` with the reason `interrupted`,
  exit 1.

`qory run pool` stops a run with SIGTERM, and with SIGKILL 30 seconds later. The step gets
its 15 seconds when the agent's runtime stops within the 10 seconds of the default
`--stop-grace`. A longer `--stop-grace` takes time from the step, and SIGKILL ends it.

One repository can be pushed before another fails, and a push can succeed before its version
is stored. The branch is then past every version of the artifact, and the next run's push
fails as a branch that moved. Each line for that state gives both ways out: add a version
with the head of the branch, or delete the branch.

On a run that can push, each of these lines stops the run after the setup, before the
agent starts, and closes it `failed` with `no_session`:

- `check that git can read the copy <folder>: qory lists what the setup changed there, and
  git failed: <error>`

When a push fails, the step's log has each `remote:` line of the forge as `qory run:
remote: <line>`, and each commit that did not reach the branch as `qory run: not pushed
from <path>: <commit> <subject>`, at most 50 of each, then `qory run: <n> further lines
left out`. A URL's login and password and the run's secret values are hidden.

Each of these lines fails the step. `<commit>` is the commit's first 12 characters:

- `take the work item in again in Qory Apiary: after the agent exited, the check of
  repository <path> failed, so qory committed and pushed nothing: <cause>; the code is
  used`, before any commit
- `take the work item in again in Qory Apiary: the commit of what the agent left in <path>
  failed: <error>; the code is used`
- `take the work item in again in Qory Apiary: the check that HEAD of <path> descends from
  commit <commit> failed: <error>; the code is used`
- `take the work item in again in Qory Apiary, and keep the run's commits on top of commit
  <commit>: HEAD of <path> is at <commit>, which does not descend from it, so a push would
  not be a fast-forward; the code is used`, when the agent replaced the commit the run
  started at
- `add a new version of the artifact branch in Qory Apiary with the head of qory/<work item
  id> on <path>, or delete that branch on <system>, then take the work item in again: the
  branch moved since the run fetched it, and the push failed: <error>; the code is used`
- `change the rule on <system> that stops a push to qory/<work item id> of <path>, then take
  the work item in again: the push failed: <error>; the code is used`, when a rule or a
  hook on the system stops the update
- `add /<path>.git/git-receive-pack to the path rules of the run's policy for <system>, and
  give the run's credential for <system> write access to <path>, then take the work item in
  again: the push through the run's gateway returned 403: <error>; the code is used`
- `give the run's credential for <system> write access to <path>, then take the work item in
  again: the push through the run's gateway failed: <error>; the code is used`
- `check that the run's policy allows <system> and that <system> is reachable, then take
  the work item in again: the push of <path> through the run's gateway failed: <error>;
  the code is used`
- `take the work item in again in Qory Apiary: the run's host proxy, through which qory
  pushes the run's commits, did not open: <error>; the code is used`
- `take the work item in again in Qory Apiary: the run API, which stores the version of the
  artifact branch, is not reachable from this run: <error>; the code is used`
- `Define the artifact type branch in the workspace's Artifact types, then add a new version
  of the artifact branch in Qory Apiary with the head of qory/<work item id> on <path>, or
  delete that branch on <system>, then run the round again`, when the workspace has no
  artifact type `branch`
- `add a new version of the artifact branch in Qory Apiary with the head of qory/<work item
  id> on <path>, or delete that branch on <system>, then take the work item in again: qory
  pushed <branch>, and the version of the artifact branch was not stored: <error>; the code
  is used`
- `check the artifact branch of the work item in Qory Apiary; with no version of this run,
  add a new version of the artifact branch in Qory Apiary with the head of qory/<work item
  id> on <path>, or delete that branch on <system>; then take the work item in again: qory
  pushed <branch>, and the request that stores its version ended with no response, so the
  version may or may not be stored: <error>; the code is used`
- `add a new version of the artifact branch in Qory Apiary with the head of qory/<work item
  id> on <path>, or delete that branch on <system>: qory pushed <path> and stored no version,
  because the push of the next repository failed: <line of that failure>`

A removal of the setup container that failed runs no step: qory commits nothing and pushes
nothing, and the run ends as a failed step ends it, with this line:

- `take the work item in again in Qory Apiary: the setup container of this run was not
  removed, so a process in it could write the working folder, and qory pushed nothing:
  <error>; the code is used`

A step that the time limit or a signal stopped prints one of these, where `<cause>` is `qory
run got a second signal`, `qory run got <signal>, and the step reached its limit of 15s`,
`the step reached its limit of 5m0s`, or `the run reached its time limit of <limit>`:

- `take the work item in again in Qory Apiary: <cause> during the push step, and the run's
  commits may not have reached the branch; the code is used`
- `check the artifact branch of the work item in Qory Apiary; with no version of this run,
  add a new version of the artifact branch in Qory Apiary with the head of qory/<work item
  id> on <path>, or delete that branch on <system>; then take the work item in again: qory
  pushed <branch>, and <cause>, so the version may or may not be stored; the code is used`

### Runs of a node pool

`qory run pool` runs a node pool's runs on this machine. It starts each run in `work/` of
the run's folder, `pool/runs/<dispatch id>` in qory's state directory, with `qory run
--run-code-file /dev/fd/3 --gateway <gateway_url> --home <the run's folder>/home`. The run
composes its harness under `home/` of that folder, outside `work/`. Once the run ends,
the pool removes the run's folder and the harness home in it, so no later run on the
machine reads that harness.

Each run's `HOME` is `home-user/` of the run's folder, mode `0700`, which the pool makes
empty and removes with the folder. `XDG_CONFIG_HOME` stays `pool.run_config`. So a run
reads none of these files under the `HOME` of the user the pool runs as:

- the registry logins of `~/.docker/config.json`;
- the Docker contexts of `~/.docker`: set `DOCKER_HOST` for an engine other than the
  default;
- the git configuration of `~/.gitconfig`.

A run still reads `pool.run_config`, and the files that its `forager.yaml` names, also
when they are under that `HOME`.

The pool stops before its first claim when the runs' `forager.yaml` sets `wall.mounts`,
since every run would see the paths it lists, with exit 2:

```text
remove wall.mounts from <pool.run_config>/qory/forager.yaml: every run of the pool would see the paths it lists, so the runs would share them
```

With `pool.runs` above 1, runs share the machine's processors, memory, disk and network
at once. The pool stops before its first claim, with exit 2, unless the runs'
`forager.yaml` sets both `wall.pids_limit` and `wall.memory` (see Limits in [The
wall](#the-wall)):

```text
set wall.pids_limit and wall.memory in <pool.run_config>/qory/forager.yaml: with pool.runs above 1, runs share this machine, and a run without both limits can take the processes and memory the others need
```

The pool also stops before its first claim, with exit 2, when the runs' `forager.yaml`
sets no `wall.engine_proxy_file`, so that the engine pulls each run's image through that
run's gateway (see The engine proxy in [The wall](#the-wall)):

```text
set wall.engine_proxy_file in <pool.run_config>/qory/forager.yaml, as The engine proxy in docs/run.md shows: without it, the engine pulls each run's image over this machine's network, not through the run's gateway
```

Before its first claim, `qory run pool` checks that a container on the engine's default
bridge network cannot reach `169.254.169.254` or `fd00:ec2::254`, the addresses of a cloud
metadata service. It runs one container, removed when it ends, from `wall.image` of the
runs' `forager.yaml`, on the default bridge network, as `wall.user`, with
`--cap-drop ALL`, `--security-opt no-new-privileges` and `--pull never`, and its
`docker` gets an empty `HOME`, as a run's does. The container
tries a TCP connection to `169.254.169.254:80` and one to `[fd00:ec2::254]:80`, and gives
up each after 2 seconds. The connection to `[fd00:ec2::254]:80` can succeed only when the
Docker daemon enables IPv6 on the default bridge network; without IPv6, it fails whatever
the host's rules are. If either connection succeeds within its 2 seconds, the pool exits 1
and claims no run:

```text
drop 169.254.169.254 and fd00:ec2::254 in the raw table before Docker starts: a container on the default bridge network connected to 169.254.169.254:80, the cloud metadata service, so the pool claims no run
```

Add these rules on the host before Docker starts, such as from a unit that runs before
`docker.service`:

```sh
iptables -w -t raw -I PREROUTING -d 169.254.169.254 -j DROP
iptables -w -t raw -I OUTPUT -d 169.254.169.254 -j DROP
ip6tables -w -t raw -I PREROUTING -d fd00:ec2::254 -j DROP
ip6tables -w -t raw -I OUTPUT -d fd00:ec2::254 -j DROP
```

The raw table runs before the nat and filter rules that Docker writes. The PREROUTING
rules match the packets that arrive from containers on a Docker bridge, and the OUTPUT
rules match the packets that the host's processes send. The check runs once Docker runs,
so it does not show whether the rules were in place before Docker started.

If the check cannot run, the pool exits 1 and claims no run, with one of these errors:

- `pull <wall.image> onto this machine, and check that the container engine runs: the
  container that checks the cloud metadata service exited <status>: <docker's last
  line>`. The check never pulls the image, so pull `wall.image` before the pool starts.
- `install <program>, or set wall.command in the runs' forager.yaml: the start of
  <program> for the check of the cloud metadata service failed: <error>`, where
  `<program>` is `wall.command`, or `docker` when that is not set.
- `check that the container engine runs: the check of the cloud metadata service did not
  end within 2m0s`.

#### What the runs of a pool share

The runs of a pool on one machine share these:

- **The kernel and the Docker engine.** Every run's containers, and a prebuild's nested
  Docker inside its container, run on this machine's kernel and its Docker engine. An
  escape from a container to the kernel or to the engine reaches every run on the
  machine.
- **The user.** Every run's `qory run` runs as the user the pool runs as, with a separate
  `HOME`, `home-user/` of the run's folder under `pool/runs/<dispatch id>`. Every session
  of a run runs in its container as `wall.user`.
- **The machine's resources.** With `pool.runs` above 1, runs at the same time share the
  processors, memory, disk and network bandwidth. `wall.pids_limit` and `wall.memory`
  limit the processes and memory of the container each session of a run runs in. No
  setting limits a run's disk or bandwidth, or the size of a prebuild's nested Docker.

The runs of a pool share none of these:

- **The run's folder**: its checkout, state, harness home and `HOME`. The pool removes the
  folder when the run ends.
- **A registry login.** A prebuild pushes its images from behind its wall, with `docker
  push` through the run's proxy and a Docker configuration that has no login. When the
  run's policy has a credential for the registry, the run's gateway sets it on the
  requests the credential covers. No run reads `~/.docker/config.json` of the user the
  pool runs as.
- **A prebuild's nested Docker**: its images, its build cache and the container the
  configuration's commands ran in. They go with the wall when the run closes, and this
  machine's Docker engine holds none of them. The images a prebuild pushed stay in the
  registry; qory deletes none of them.

[Sysbox](https://github.com/nestybox/sysbox) runs on Linux only, and nested Docker needs
Docker Engine 28 or later. A pool needs Sysbox for its prebuilds, which run in the image
that `wall.prebuild_image` is set to, and for a run whose image sets `docker: true`. A pool
whose runs use neither needs no Sysbox.

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

Before the agent starts, `qory run` given a run key stops the run with one of these errors:

- `--local runs with a gateway the run starts itself and no server, and a run key is given, by
  QORY_RUN_KEY, --run-key-fd or --run-code-file: the run goes through the gateway that its
  run key points to; give no run key to run --local`
- `--policy is a run's policy file for a gateway the run starts itself, and a run key is
  given, by QORY_RUN_KEY, --run-key-fd or --run-code-file: the gateway that its run key
  points to takes the run's policy from the server`
- `<file> exists, and a run key is given, by QORY_RUN_KEY, --run-key-fd or --run-code-file:
  a machine whose runs a starter registers holds no access key; remove the file`, for
  `access-key-secret` or a key file beside it
- `<variable> is set, and a run key is given, by QORY_RUN_KEY, --run-key-fd or
  --run-code-file: a machine whose runs a starter registers holds no access key; unset
  <variable>`, for `QORY_ACCESS_KEY_ID`, `QORY_ACCESS_KEY_SECRET` and
  `QORY_APIARY_PUBLIC_KEY`
- `--access-key-secret-fd and a run key are both given: a machine whose runs a starter
  registers holds no access key; leave out --access-key-secret-fd`
- `<file>: gateway: a run key is given, by QORY_RUN_KEY, --run-key-fd or --run-code-file, so
  the run goes through the gateway that its run key points to, which holds the server, the
  policy, the credentials and the tools, and this machine runs no gateway: remove the
  gateway section, or give no run key`
- `--label`, `--kind`, `--title`, `--subject` and `--details`, as [The run key](#the-run-key)
  says
- an unwalled run with `QORY_RUN_KEY` set, `run_key_needs_wall`
- `remove <variable> from --env: a walled run given a run key takes the model's credential
  only through the gateway`, or `remove <variable> from wall.env in forager.yaml: ...`, for
  a variable the runtime reads its model key from: `ANTHROPIC_API_KEY`,
  `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_AUTH_TOKEN` for Claude Code
- `remove <variable> from --env: a walled run given a run key takes a forge token only
  through the gateway`, or `remove <variable> from wall.env in forager.yaml: ...`, for a
  forge token's variable of Forager's deny list, such as `GH_TOKEN`, `GITHUB_TOKEN`,
  `GITLAB_TOKEN` or `BITBUCKET_TOKEN`
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

## Commands of a work item

A skill reads its work item and stores what it made with four commands. Each command
works in a run and on a laptop. A wall mounts `qory` at `/qory/qory` and does not add it to
`PATH`, so a skill calls `/qory/qory` in a run with a wall and `qory` on a laptop.

| Command | In a run | Outside a run |
| ------- | -------- | ------------- |
| `qory work-item show` | Prints the work item as JSON. | Prints `.qory/work-item.json`. With no file, exits 3. |
| `qory artifact pull [<type>] [--version <N>]` | Fetches artifacts into the store, under `<type>/<name>/v<N>/`, writes `index.json` there, and prints the store's directory. | Fetches nothing, and says so. |
| `qory artifact push <type> <file>... [--name <name>] [--note <text>]` | Stores the files as one new version, keeps a copy in the store, and prints the version. | Keeps the files in `.qory/artifacts`, and sends nothing. |
| `qory run verdict passed\|changes-requested\|escalate [--gaps <file>]` | Stores the verdict. Qory Apiary acts on it when the run ends. | Prints the verdict and keeps it in `.qory/artifacts/verdict/`. Nothing acts on it. |

In a run, the types `questions` and `review-comments` take only the name `main`.

A run with a control plane sets `QORY_RUN_API` to
`http://run-api.qory.invalid/v1/runs/<run id>`. The commands send their requests there
through `HTTP_PROXY`. The gateway signs them, so the run has no key. With the variable
absent, the commands work on the checkout's `.qory` directory.

The store is the folder of a run's artifact files. It is `.qory/artifacts` in the checkout.
In a run that cannot write its checkout, such as a [read run](#read-workflows), it is
`/tmp/qory-artifacts` in the agent's container, and it ends with the container. `qory
artifact pull` and `qory artifact push` check whether they can write the checkout, so no
setting is needed. In a wall they find the checkout from the folder the run started in, so
a `cd` does not change the store. On an image with nested Docker, and with no wall, they
find it from the working folder. In a run, read the files from the directory that `qory
artifact pull` prints, which is a whole path. Outside a run the store is `.qory/artifacts`.

`qory artifact pull` fetches the newest version of each output of the stage, and the
version of each input that its gate approved. A type that is both counts as an output. For
any other artifact it fetches the approved version, or the newest when none is approved.
`<type>` limits the fetch to one type. `--version` needs `<type>`.

`-` as a file of `qory artifact push` reads one file from standard input. Its name is
`stdin` with the extension of its content, such as `stdin.md`. One version is at most
32 MiB encoded through the run API, about 24 MiB of files, whatever the type's size limit.

`qory artifact push` exits 4, with the type and the limit, for a type that the workspace
does not have, for too many files, and for files over a limit. One version has at most 100
files through the run API, whatever the type's maximum.

In a walled run whose modules declare a push check for the type, the check runs before the
push reaches Qory Apiary (see Push checks in `contracts/harness/v1/README.md`). A check
that does not pass stops the push with 422 `push_check_failed`, and `qory artifact push`
exits 4. `qory artifact push` prints the fix line, such as `Fix each finding below and push
again. The check for spec failed:`, then each finding of the check on a separate line:

```text
✗ Fix each finding below and push again. The check for spec failed:
  spec.md: the section Goal is missing
```

Fix the files and push again. A check that did not finish stops the push with 503
`push_check_unavailable`, and `qory artifact push` exits 1 with `Run the push again: the
check before the push did not finish.` qory run writes the cause on its stderr, as `qory
run: the check before a push did not finish, and the push was not stored (503
push_check_unavailable): <cause>`. Neither push stores a version or keeps a copy in the
store. The push step of a write run runs the check of the type `branch` before it stores
its version. A check that does not pass fails the step as when the version is not stored:
the step's line, which ends `the version did not pass the check for branch; the code is
used`, comes first, then `The check for branch failed:` and the findings, each a line of
qory run.
`qory run verdict` exits 4 for a request over its limit too. `qory work-item show` exits 3
with no work item. These numbers mean a collision and a foreign path on `qory harness`.

`.qory/artifacts/.gitignore` keeps the artifacts out of git, also in a run's checkout,
which has no exclude line for `.qory`. `/tmp/qory-artifacts` has no `.gitignore`, because
it lies outside the checkout. qory makes that folder with mode 700 as the run's user. If
another user owns a folder at that path, or its mode has a group or other bit,
`qory artifact pull` and `qory artifact push` stop with exit 1, and the push sends nothing.
Remove the folder to run the command again. A directory link at `.qory`,
`.qory/artifacts` or `/tmp/qory-artifacts` stops `qory artifact push` with exit 1 before it
sends anything. A directory link further
down stops `qory artifact pull` and a push outside a run with exit 1. In a run, a push has
stored the version by that point, so it keeps no local copy, prints a line on standard
error, and exits 0. A write replaces a link at a file's path, and never follows it.

With a home in the checkout, `qory harness remove` deletes the directory with the rest of
`.qory`. `--runtime` does the same when it removes the last composed runtime.

The client sends a POST once. If the response is lost, the version may be stored: read the
artifact index with `qory artifact pull` before you push again.

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
- The container sees a copy of the checkout, or the checkout read-only for a read run,
  and the composed home, and the run's record directory, read-only. A run of Claude Code given a
  run key also sees a folder that holds the names of the variables its agent keeps,
  read-only. It sees nothing else of your machine.
- The composed home contains copies of the module files that the stack selects, in place
  of links: a module's folder outside the checkout is not mounted in the container.
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
- **What the container sees.** The copy of the checkout it was started in, the run's
  record directory, read-only, and for a run of Claude Code given a run key, the folder of the names of the
  variables its agent keeps, read-only; no other directory. `--mount <path>[:ro]` or `wall.mounts` shows it another one, at the same path as on your machine, such as a
  sibling checkout the session reads. A socket is never mounted. A read-write mount stops
  a run that may write: see [A run that may write works in a
  copy](#a-run-that-may-write-works-in-a-copy). A read-write mount that
  is or contains your home folder or qory's state directory stops any run first, as [The
  record](#the-record) describes. A mount is refused before the agent starts,
  `mount_contains_forager_files`, when it is, contains or lies inside one of these:
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
  instead. A read-write mount that is or contains the state directory stops first, with
  `make the mount <path> read-only, …`, so for a read-write mount this line appears only when the
  mount lies inside the state directory.

  For a link on the way to the state directory, to a file of the configuration
  directory or to a `<name>_file` setting's file, it names the link, which leads to
  them:

  ```
  qory run: the mount <host path> contains <link>, which leads to one of Forager's files; the agent could point it elsewhere, so the run does not start. Mount a narrower path
  qory run: the mount <host path> contains <link>, which leads to qory's run records, so the run does not start. Mount a narrower path
  ```

  For a read-only mount, it leaves out `; the agent could point it elsewhere`. The second
  line appears only for a read-only mount: a read-write mount that contains a link on the
  way to the state directory stops first, with `make the mount <path> read-only, …`.

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
- **The engine proxy.** `wall.engine_proxy_file` is the absolute path of a file that
  contains the container engine's proxy setting. The machine's start unit writes it as
  one line, `{"version": 1, "address": "127.0.0.1:3129", "password": "<43 characters of
  base64url>"}`, and the machine's Docker daemon has the same proxy and password in
  `daemon.json`. With the key, the image step of a walled run pulls the image through the
  run's proxy, under the run's policy; without it, the engine pulls over the machine's
  network. `qory run` reads the file before the run opens, and stops with exit 2 unless
  the file is a regular file and not a link, belongs to the user `qory run` runs as, has
  mode `0400` or `0600`, has at most 4 KiB, and contains exactly those three members:
  `version` 1, `address` a loopback IP address and a port, and `password` 43 characters
  of base64url with no padding. No line, event or record contains the password, and
  `qory config` lists the path alone. A walled run's image step holds an exclusive lock
  of the file, so one run at a time pulls through the engine's proxy; replace the file
  only while no run is going, since a run that opened the old file does not exclude a run
  that opens the new one.

  Pull every image the machine defines on the host before a run, and before you set the
  engine's proxy: the agent's image in `wall.images`, the image that Forager's relay
  starts from, which is the machine's default image, and the image of
  `wall.prebuild_image`. With `wall.engine_proxy_file` set, a run's wall creates the
  agent's and the relay's containers with `--pull=never`, so the engine pulls only in a
  run's image step. A run whose agent or relay image the engine does not have fails with
  Forager's line `Pull the image on this machine before you set the engine proxy: the
  engine has no image <image>, and under an engine proxy the wall pulls only in a run's
  image step.` A prebuild whose image the engine does not have fails with the engine's
  error for the pull, since the engine sends every pull to a run's proxy, which listens
  only during that run's image step.
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

### A run that may write works in a copy

A walled run that may write never binds your repository, so it has no write access to your
`.git`, and git on your machine never opens a folder the run could write. qory clones your
repository to `<user cache>/qory/runs/<run id>/work`, `~/Library/Caches` on macOS and
`$XDG_CACHE_HOME` or `~/.cache` on Linux, and binds that clone in place of the checkout. Each
repository under `.qory/repositories` is cloned too. The harness goes into
`<user cache>/qory/homes/runs/<name>-<hash>/<run id>`, outside the clone, and the container has that
folder read-only. `qory` removes that folder when the run ends. A run with a run key has
`qory/homes/<name>-<hash>` as its home, and a home from `--home` or `harness.home` stays. The clone gets no
link to the harness, so a program that reads part of it from the checkout does not get that part:
see [What `qory run` does](#what-qory-run-does). A read run with no setup binds your checkout
read-only instead, and a read run with a setup gets the clone read-only: see
[Read workflows](#read-workflows).

The run starts from your commit at `HEAD`. It does not see your uncommitted, untracked or
ignored files, such as `.env`. A working folder that the commit at `HEAD` does not have
stops the run: commit it, or run qory in a folder that commit has. A repository with no commit stops the run
too, with one exception. A run from a run code or a run key that lists no repositories, in a folder where
`git init` ran and that has no commit, works like a run in a folder that is not a repository, with `.git` left
out of the copy. A filter in your global git configuration, such as Git LFS, and the submodules of each
repository apply before the run starts. A required filter that cannot run stops the run.

The clone has your `origin` URL with any credentials removed, your `origin/*` refs, and the
`user.name` and `user.email` of your repository's configuration. With no `origin` in your
repository, or an address that could still have a credential in it after that, it has none.

Four cases stop the run with their fix first:

- A branch named as a parent of the run's branch, such as `qory`: git keeps no branch below a
  branch, so rename yours with `git branch -m`.
- A partial clone: the clone has only the objects your repository has. The line has the
  commands that fetch the rest, such as `git fetch --refetch origin`.
- A folder that is not a repository, and that is your home folder or qory's state directory or
  contains one, with exit 2: the copy would have every file there, such as `~/.ssh`. Start the run
  from a folder that does not contain them. A home folder that is a repository runs when
  `--home` or `harness.home` sets a harness home outside it: its copy has only the files of the
  commit at `HEAD`. The default home is under your cache directory, which on macOS, and on Linux
  with no `XDG_CACHE_HOME` outside it, is inside your home folder.
- A folder of a run's copy under `qory/runs` in your cache folder: a run could have written
  it, so `qory run` and `qory harness compose` also stop there. Run qory in your checkout.

When the agent has ended, however it ended, a second session starts on the same run: the git
session. It runs no agent and gets no model key and no run code. It never binds your
checkout, and of what you have, only the copy is writable. Its other binds are a new folder
for its bundles and the folder of the hook forwarder's socket, both writable, and `qory`'s
Linux build and the session's folder in the run's record, both read-only. A run whose gateway
answered the open with an authority also binds its bundle of authorities read-only, at
`/qory/ca-bundle.pem`. A run with no authority in that answer has no such bind, whatever its proxy
does with TLS. It commits what
the agent left, with no setting from the copy's `.git` and no filter, and writes the run's
branch to a git bundle. With a branch
checked out that descends from your commit, the bundle has that branch, and its commits keep
their messages. With `HEAD` at your commit and the run's branch past it, with `HEAD` on a branch with
no commit, on a commit older than yours, or on a commit other than the run's branch's that does not
descend from yours, the bundle has the run's branch as it is. The session then makes no commit, since
a commit of that working tree would undo the branch's files. After an amended commit or
`git pull --rebase`, with `HEAD` and the run's branch at one commit, the session commits as usual.
Your `.gitignore`, `.git/info/exclude` and global excludes file keep their files out of
the commit. The image needs git 2.25 or later, `cat`, `comm`, `cp`, `find`, `grep`, `mkdir`, `mv`,
`readlink`, `sh`, `sort` and `tr` in `/usr/local/bin`,
`/usr/bin` or `/bin`: `qory image check` tests them. A write run that lists repositories tests them
too, in a first setup step named `Check tools`, in the setup container or, with `wall.setup: inside`,
in the agent's container. The step prints nothing when they are there. When one is missing in the
setup container, the run closes as a failed setup before the agent starts, with one line per missing
program, such as:

```text
qory run: install comm in /usr/local/bin, /usr/bin, /bin: the git session of a walled run runs comm from these folders, to compare the run's refs and commits with what the bundle has
```

If the step exits 127 with no such line, which is what docker does for an image with no `/bin/sh`,
qory prints the line for `sh`. Any other failure with no such line prints
`Fix Check tools: setup step <n> exited with <n>.`, also exit 126, where docker could not run the
command for another cause, such as the image's architecture.

With `wall.setup: inside`, you see each line once as the image's shell printed it, with no
`qory run:` in front, then a line such as `✗ claude exited 1`, though no agent started. On a run
with no push step, the git session then runs on the same image, qory keeps the copy, and you see a
second line.

A run that lists no repositories has no setup, so it has no such step, and a read run has no git
session. The run's record shows the session as a second session.

Part of the run's work can miss the bundle:

- A change in the working tree when the session makes no commit.
- A commit on a branch that the bundle does not have, a stash, a tag the run made, or a commit made
  with `HEAD` detached. A commit that a remote-tracking ref or a ref of your repository has does not
  count. Some ordinary commands count too: `git fetch --tags` with a new tag off any branch, a
  fetched pull request ref checked out as a local branch, `git notes add`, and a feature branch
  squash-merged and deleted.
- With nothing past your commit, a commit that a reset took off the branch.
- A sparse checkout in the copy: the session makes no commit, since a commit would delete the
  files that the checkout took out.
- A commit or a change in a submodule, other than a file the setup left there, or a repository the
  run made. An ignore rule that covers a repository's folder keeps it out of this list. The session
  prints a line with its path.
- A file or a folder that `git add` cannot read, such as one of mode 000. The rest of the work comes
  back.
- A setup's file that the agent commits as it is, under a filter set only in the copy's git settings,
  `core.autocrlf=true` with a CRLF file, or `core.fileMode=false` with an executable file: the git that
  compares and commits has none of the copy's git settings, so a local run keeps the copy with no work
  lost.

Other lines of `git add` print too, such as its warning about a link at `.gitignore`. They keep
nothing behind.

A local run then keeps the copy and exits 1:

```text
qory run: part of the run's work did not come back: the copy has changes not committed, commits on another branch or in a submodule, a detached commit, a commit a reset took off the branch, a stash, a tag, a sparse checkout, a nested repository, or files that git add could not read.
qory run: read the run's files in <folder>, run no git there, then remove the folder: it has the work that did not come back
```

A run with the push step keeps no copy: the copy goes, and the run continues to the push of what
came back. Its line has the fix:

```text
qory run: take the work item in again in Qory Apiary: part of the run's work did not come back: the copy has changes not committed, ...
```

Git LFS: because the session runs no filter, an LFS file the agent changes goes into the
commit as its full content, in place of a pointer.

A program the agent planted in the copy can run again in this session, under the run's egress
policy, so the session has at most 5 minutes. A first signal to `qory run` leaves it that time,
and a second signal ends it. qory prints how long the return of the work may take:

```text
qory run: got SIGTERM: bringing the run's work back, for at most 30m0s. Send a second signal to stop that.
```

A run from a run code, as a pool starts it, prints no such line. It has 15 seconds after a signal,
since a pool kills it 30 seconds after its SIGTERM. Past the session's time, or if the session
exits with another status than 0, the run's commits do not come back, and qory keeps the copy:

```text
qory run: read the run's files in <folder>, and run no git there: the git session ran past its limit of 5m0s, so the run's commits did not come back
```

A run with the push step keeps no copy: the copy goes, and the step prints
`take the work item in again in Qory Apiary` with the cause.

Once the run's containers are gone, qory checks the bundle, fetches it into a new repository
with git's object checks on, and fetches the run's branch into your repository as
`qory/run-<run id>`. qory never checks that branch out, so the files in your checkout stay
as they are:

```text
qory run: the run's commits are on qory/run-0193 in /path/to/repo.
```

When the commits change a setting that qory acts on, such as `harness.launch` or `env` in
`qory.yaml`, or add a link, the line continues with what they change:

```text
qory run: the run's commits are on qory/run-0193 in /path/to/repo. They change these qory settings, links or repository folders: harness.launch.claude.command. Check them before you check out the branch.
```

With no commit past `HEAD`, qory prints `qory run: no action needed: the run made no commit
past its base commit`. Then it deletes the copy. When anything stops the return, qory
prints the cause and `qory run: read the run's files in <folder>, run no git there, then
remove the folder: it has the work that did not come back`, and `qory run` exits 1 for a session
that succeeded. On the push path, qory instead pushes nothing, prints
`take the work item in again in Qory Apiary` with the cause, and deletes the copy. For the exit
status, see
[The push step of a write run](#the-push-step-of-a-write-run).

When removing a part of the run's wall failed at any point of the run, even if a later removal
succeeded, a process may have written the copy after its session ended. Nothing comes back,
qory keeps the copy, and `qory run` exits 1:

```text
qory run: read the run's files in <folder>, and run no git there: removing a part of the run's wall failed during the run, so a process may have written the copy after its session ended, and nothing came back from the run
```

On the push path, the result depends on when the removal failed:

- Before the push step, for the agent's container or the setup container: the run has no push
  step. qory pushes nothing, prints `take the work item in again in Qory Apiary` with the cause,
  and deletes the copy.
- At the close, which comes after the push step: what the step pushed stays on the branch, and the
  run's exit status does not change. qory prints `removing the wall:` with the cause, and deletes
  the copy.

In a copy that stays, qory renames each `.git` to `.git-from-run`, mode 0700, and the `HEAD` of
a folder that git would take as a repository. Run no git there: after a close that failed, a
container of the run can still write a new `.git` in the copy. Read the files, and remove the
folder when you are done.

A commit of the run that adds `.git` or `.qory` at the top of a repository brings nothing
back. A write run of a work item pushes from the checked repository instead of fetching
into your repository: see [The push step of a write run](#the-push-step-of-a-write-run). In
such a run, a line that stops the return starts `take the work item in again in Qory Apiary`. It
has no path, and qory deletes the copy.

**Artifacts.** Outside a control plane, `qory artifact push` keeps versions in
`.qory/artifacts`, which the commits leave out. Once the run's containers are gone, with or
without a commit and also past the limit of the git session, qory copies the regular files
of that folder from the copy to your `.qory/artifacts`, by the same rules as a folder that
is not a repository. A file you already have with other content stays, qory lists it, and
the copy stays too with the run's version.

Before the run starts, qory copies your `.qory/artifacts` and `.qory/work-item.json` into the
clone by the same rules. A run in a repository reads your versions and your work item with
`qory artifact pull` and `qory work-item show`, and numbers its versions after yours. If
`.qory/artifacts` has more than 100000 files or 8589934592 bytes, the run stops: remove old
versions first.

**A folder that is not a repository.** qory copies the folder and, once the containers are
gone, copies back the regular files that the run added or changed, and deletes those it
deleted. It leaves out links, every `.git` and `.qory`, and a qory file whose settings
the run changed, and it keeps your version of a file you both changed. It prints what it left
out and what you both changed. When you both changed a file, or the run changed a qory
setting, the copy stays with the run's version of each, and qory prints its folder. Otherwise
it deletes the copy.

**Mounts.** A read-write `--mount` or `wall.mounts` entry stops a walled run, since it
would give the run write access outside the copy or the read-only checkout. Read-only mounts
stay:

```text
make the mount /srv/data read-only, with :ro on --mount or in wall.mounts: a walled run reads the checkout or works in a copy of it, and a read-write mount lets it write your files directly
```

### Read workflows

Two values set what a run of a workflow from a run code may do on the checkout:

- The workflow's access in the stack, `workflows.<name>.access`: `read` or `write`. A
  workflow that sets no access, and one the stack does not declare, is `read` (see
  `workflows` in `contracts/harness/v1/README.md`).
- `access` in the run's documents, from Qory Apiary: `read` or `write`. Documents with no
  target have no `access`, and the workflow's access applies alone.

A read run is a run where either value is `read`. A workflow declared `write` with
`access: read` in the documents gives a read run, and so does a workflow declared `read`
with `access: write`. A read run sees the checkout read-only:

- Forager binds the workspace read-only in the agent's container, and the checkout's
  mount is read-only. A write into the checkout inside fails with `Read-only file
  system`.
- `qory artifact pull` and `qory artifact push` keep their files in `/tmp/qory-artifacts`,
  the store of a run that cannot write its checkout. `qory artifact pull` prints that
  directory. See
  [Commands of a work item](#commands-of-a-work-item).
- On Claude Code, the run's settings deny `NotebookEdit` and every edit under the
  checkout, with `Edit(//<checkout>/**)` and `Edit(//workspace/**)`. They allow
  `Bash(/qory/qory artifact *)`, `Bash(/qory/qory run verdict *)`,
  `Bash(/qory/qory work-item *)`, `Read(//tmp/qory-artifacts/**)` and
  `Edit(//tmp/qory-artifacts/**)`, add `/tmp/qory-artifacts` to
  `permissions.additionalDirectories`, and set `permissions.defaultMode` to `dontAsk`,
  beside what the composed settings contain. Inside the wall, `qory` is at `/qory/qory` and
  not on `PATH`. On the other runtimes, the read-only mount is the only change.

  `permissions.additionalDirectories` keeps the store readable when the composed settings
  set `permissions.blockReadsOutsideWorkingDirectories`. A folder on that list must exist
  when Claude Code starts, so qory makes `/tmp/qory-artifacts` in the container before the
  agent starts. If the image has something at that path, the run stops before the agent
  starts, and the error line starts with the fix.

  So the agent reads what it pulled and makes what it pushes in `/tmp/qory-artifacts`, with
  Claude Code's `Read` and `Write` tools:

  ```
  /qory/qory artifact push review /tmp/qory-artifacts/review.md
  /qory/qory run verdict changes-requested --gaps /tmp/qory-artifacts/gaps.md
  ```

  Every other write stops at `dontAsk`, apart from Claude Code's memory folder,
  `/tmp/qory-agent-home/projects/<project>/memory/`, which Claude Code lets the agent write
  with no rule. That folder stays in the container and does not reach the checkout. An
  `Edit` or `Write` allow in the composed settings opens the rest of the container, and the
  deny rules and the read-only mount still stop a write to the checkout.
- A run with no setup binds your checkout read-only. A run with a setup gets a copy, as in
  [A run that may write works in a copy](#a-run-that-may-write-works-in-a-copy): the setup
  works in the copy, so it cannot write your `.git`, the agent has the copy read-only, and
  nothing comes back from it. The fetch of a run's repository still writes the working folder.
- qory removes the setup container after the setup and before the agent starts, in the
  wall step `remove the setup container`, so every process in that container has ended
  when the agent starts. A read run's agent cannot reach a service that the setup started:
  the name `qory-setup` does not resolve, so a connection to `qory-setup:5432` fails. A run
  that gets the push step keeps the setup container until its session ended, and qory
  removes it before the step. Any other run keeps it until the run closes. If a workflow's
  agent needs such a service, declare the workflow `write`, and check that `access` in the
  run's documents is not `read`.

A read run with no wall, with `wall.setup: inside` or on an image with nested Docker
cannot see the checkout read-only. It stops after the compose, before the image's fetch,
the setup and the agent, exit 1. qory closes the run `failed` with the
reason in the line, in place of `no_session`:

- With no wall, as a run with `--run-code-file` and `--gateway` and no `wall` section
  has, `read_needs_wall`:

  ```
  run this workflow with a wall: this run of workflow <workflow> only reads the checkout, and a run with no wall mounts nothing read-only (read_needs_wall); the code is used
  ```

- With `wall.setup: inside`, `read_needs_setup_outside`:

  ```
  set wall.setup to outside in forager.yaml, or remove it: this run of workflow <workflow> only reads the checkout, and setup inside runs in the agent's container, which sees the checkout read-only (read_needs_setup_outside); the code is used
  ```

- On an image of `wall.images` with `docker: true`, `read_needs_no_nested_docker`:

  ```
  use an image without nested Docker: this run of workflow <workflow> only reads the checkout, and image <image> has nested Docker, so the checkout cannot be made read-only (read_needs_no_nested_docker); the code is used
  ```

  qory checks the image chosen at the run's open, whichever way it was selected: by the
  run's policy, which with a server is the run configuration's, or by `--image` or
  `wall.image` when the policy selects none. A policy that selects an entry with
  `docker: true` stops the run even when `wall.image` is a pinned reference. An image
  selected by its reference runs no nested Docker.

A read run whose setup container was not removed stops before the agent starts, exit 1,
because a process in the container can still write the checkout. qory closes the run
`failed` with the reason `read_needs_setup_removed`:

```
start the run again from the work item in Qory Apiary: the setup container of this read run was not removed, so a process in the container can write the checkout: <error> (read_needs_setup_removed); the code is used
```

A time limit or a signal that ends the removal closes the run as it closes a setup that
it cuts short: `cancelled` with `timeout`, exit 124, or `cancelled` with `interrupted`,
exit 1.

A read-write mount from `--mount` or `wall.mounts` stops a read run after the compose, exit 2,
with a line that starts `make the mount <folder> read-only, with :ro on --mount or in
wall.mounts`. A mount at the checkout's exact path stops nothing: qory makes it read-only.

A read run with no setup whose checkout or working folder is your home folder or qory's state
directory, or contains one, stops after the compose, exit 2, since the agent could read every
file there:

```text
start the run from a folder that does not contain your home folder or qory's state directory: the folder <folder> is your home folder, and a read run binds it into the agent's container; the code is used
```

A run of a workflow from a run code where neither value is `read`, a run given a run key
with no run code, and a run you start yourself see the checkout writable.

### Qory's images

`qory`'s repository defines four images for the agent under `images/`, and one a prebuild
runs in, for `linux/amd64` and `linux/arm64`. They are not published for you to pull: you build them, by hand as
below, and a workflow of the repository builds and checks them on a change. Build them
from a checkout of `qory` at the commit `qory version` prints, since each commit pins the
runtime at the version the Forager it builds against is written for:

```sh
git clone https://github.com/qoryai/qory && cd qory
git checkout <commit>                    # the commit qory version prints
docker build -t qory-agent images/agent
docker build -t qory-agent-docker --build-arg BASE=qory-agent images/agent-docker
docker build -t qory-agent-go --build-arg BASE=qory-agent images/agent-go
docker build -t qory-agent-go-docker --build-arg BASE=qory-agent-docker \
  --build-arg TITLE=agent-go-docker images/agent-go
docker build -t qory-prebuild --build-arg BASE=qory-agent-docker images/prebuild
```

| Image | What it holds |
|---|---|
| `agent` | Claude Code at the version Forager's descriptor sets, `git`, `gh`, poppler's `pdftoppm`, Node, the system's authorities |
| `agent-docker` | `agent`, and Docker's daemon and command |
| `agent-go` | `agent`, and Go |
| `agent-go-docker` | `agent-docker`, and Go |
| `prebuild` | `agent-docker`, the `devcontainer` command `@devcontainers/cli` 0.89.0, and Docker's `buildx` plugin: the image a prebuild runs in behind its wall |

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
  each runtime of Forager's runtime list, `runtimes.json`, is on the `PATH` at the
  version its descriptor sets, and `git` and `gh` run. It reports whether
  `dockerd` is in a system directory, and checks that what the daemon runs is there too.

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
