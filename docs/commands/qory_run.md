## qory run

Start a runtime on the composed harness, observed and recorded

### Synopsis

Start a runtime on the composed harness, the way qory harness launch does, inside
the session runner: every connection the runtime makes goes through a proxy on this
machine and is recorded, and the session's output, the runner's observations and the
runtime's own reports are written as events. The runtime is the one the harness is
composed for, or the one the first argument selects when it is composed for several;
arguments after -- go to the runtime after the launch template's own, so
qory run claude -- -p 'say hello' runs one headless turn.

What the runner does on this machine is runner.yaml in the configuration
directory, ~/.config/qory, and nowhere else: a repository cannot set it. Its egress
section is the policy, which can only narrow what the runtime reaches: its allow list,
and its deny list, whose hosts are denied in either mode, under observe as under
enforce, whatever allow lists. No section means every connection is allowed and
recorded, and a file that does not read means no run.
The hosts the harness declares, its modules' and the runtime's in the report, are
reported beside the policy as harness_hosts and narrow nothing; the policy alone defines
what the runtime reaches. --policy selects one run's own policy, a file in the runner
contract's policy format kept outside the checkout, for a machine without a server
that serves runs of different kinds. It narrows only: under a section in mode enforce
the run reaches the file's hosts the section covers, and with no section, or one in
mode observe, the file stands as it is; the deny lists of both apply either way. The
file's server section defines the server
every run reports to, with the access key and the secret the server issued this
machine: the runner fetches the server's configuration first, signed, and does not
start unless the server answers; the events go where the configuration defines, and when
it defines a run configuration that is the run's policy, fetched with the run's labels,
the checkout's forge and repository among them, and reloaded when the server reports it
changed, so --policy is refused.
--local runs with the files alone and the machine's policy; the server is not
contacted.

Any runtime the harness is composed for runs this way. What qory run knows of one, how
its hooks are installed, what its output means and which signal requests it to stop, is
a descriptor in the runner contract's format: the runner's own, Claude Code's, or
<runtime>.yaml under runtimes in the same directory, which describes a
runtime the runner ships nothing for or replaces what it ships. A runtime with neither
runs all the same: the run, its log and its egress are recorded, the events of the
session inside it are not.

A wall starts the runtime in a container with no route out except to that proxy, so a
program that ignores the proxy reaches nothing instead of going unseen: --wall docker,
or a wall section in runner.yaml, and --wall none for one run without the
section's. --image, or wall.image, sets the container's image, which contains the runtime
and the project's toolchain; qory builds none. The container sees the checkout and the
composed home, at their own paths, and nothing else of this machine; of the environment
it gets the launch template's variables and the ones --env or wall.env lists, such as the
model credential, and nothing else. --mount, or wall.mounts, shows it more of this
machine at its own path, such as a sibling checkout, with :ro after the path for what it
must not change; never a socket. --cpus, --memory, --pids-limit and --shm-size, or the
keys of the same names under wall, limit what it uses; a browser needs more /dev/shm
than an engine's default. Inside, the relay and the hook forwarder are qory's
own Linux build, mounted read-only: this binary on Linux, wall.helper elsewhere. With
the engine in a virtual machine, on a Mac, the runtime's hooks do not reach the runner.

At a terminal the session runs on a pseudo-terminal, so the runtime's own interface
works and its bytes are still captured; --headless, or no terminal, runs it on pipes and
reads its structured output. An argument the runtime's descriptor lists as headless,
-p for Claude Code, runs it on pipes as well, since with it the runtime has no interface
whoever started it: qory run claude -- -p '…' needs no flag. Either way the record is
.qory/runs/<id>/ in the checkout:
events.jsonl, one event per line, and output.log, the session's bytes. The exit status
is the runtime's. qory run resend sends a finished run's record to the server again,
after a runner that died or a server that was away.

A run has no credential it can be spared. The credentials section of runner.yaml
defines what this machine has: a token from a variable of qory's environment, from a
file, or from an adapter, a program of yours that knows one kind of host, such as a
source code host, and prints the token with the hosts, the scheme and the paths it is
for. A run's policy selects credentials by name, with an argument for an adapter, such
as a repository, and defines none. Behind a wall the runner keeps each outside the
container and its proxy sets it on the requests to the hosts it is for, ending the
container's TLS for those hosts alone with an authority made for the run, which the
container trusts beside its image's own. Of those hosts the run reaches the paths the
credential lists and no other, not another organization's repositories, and every
other host stays a tunnel nobody reads. The policy's egress.paths limits a host to
paths the same way with no credential.

An integration is an adapter published apart that describes itself: Qory's own
qory-<name>, such as qory-github, or a program of yours. The integrations section of
runner.yaml declares each under a key with its settings, and defines its
program when it is not qory-<key> on the PATH; where the PATH is not the machine owner's
alone, program defines it by its absolute path. qory runs a program the run cannot
write: one outside the checkout and outside every read-write mount of the wall's
container, judged by where its links lead. The program and every directory above it up
to /, and above each link on the way, belong to root or to the user running qory, and
so does each link; other users may write none of them, and a group may write one when
it is root's, wheel, admin, or the owner's primary group when its name is the owner's.
A directory root owns with the sticky bit set keeps the rule. qory run prints the program
it found on a line of its own. Before a run qory runs <program> describe for each
integration the run's policy selects, every one when the server supplies the policy,
checks the settings against the description, and defines the credential whose name is
the key, with the adapter <program> credential --settings <json> -- ${argument}. A
policy selects it by the key like any other. The settings go on that command line, so
a secret among them is refused and set as the file that contains it. A name the
credentials section defines itself is the section's, a line states this, and the run
describes that integration no further. An integration that does not describe, or whose
settings its description refuses, means no run.

A caller that starts runs for a system of its own identifies them: --run-id sets the
run's id to the one the caller already has, a UUID in lower case, and --label
key=value, repeatable, puts the caller's own names, a key in a queue, a repository, an
issue, into dev.qory.run.started and onto the run configuration request, where a server
finds them. Two come from the checkout's origin remote unless --label sets them: forge,
the remote's host, and repository, its path without the leading slash and .git, such as
github.com and acme/shop; a checkout with no remote, or one on this machine, has neither.
--timeout stops a runtime that still runs after that long, such as 5h30m:
dev.qory.run.exited records the limit as the reason, and the exit status is 124,
as timeout(1) has it. Stopped at the limit or by a signal to qory run, the runtime gets
--stop-signal, SIGTERM unless set or the runtime's descriptor sets one, and, after
--stop-grace, 10s unless set, SIGKILL: the time a session needs to close what it has
open. Runtimes differ in what a signal means, one closes its session on SIGINT and drops
it on SIGTERM, so the signal is yours to choose: SIGTERM, SIGINT, SIGHUP, SIGQUIT,
SIGUSR1 or SIGUSR2.
run.timeout, run.stop_signal and run.stop_grace in runner.yaml set them for
every run on the machine; --timeout 0 lifts the file's.

--verbose adds nothing here.

```
qory run [runtime] [-- argument...] [flags]
```

### Options

```
      --cpus string           how many processors' worth of time the container gets (runner.yaml: wall.cpus)
      --env stringArray       a variable of this environment that goes into the container under a wall, by name; repeatable (runner.yaml: wall.env)
      --headless              run on pipes even at a terminal, and read the runtime's structured output; implied by an argument the runtime's descriptor lists as headless, -p for claude
  -h, --help                  help for run
      --home string           where the harness is composed: a directory outside the checkout, one home per checkout under it, or .qory/harness (qory.yaml: harness.home)
      --image string          the container's image under a wall (runner.yaml: wall.image)
      --label stringArray     the caller's own name for the run, key=value, reported in dev.qory.run.started; repeatable. forge and repository come from the origin remote unless set
      --local                 record to files only and run under the machine's policy, even when a server is configured; the server is not contacted
      --memory string         the most memory the container gets, such as 8g (runner.yaml: wall.memory)
      --mount stringArray     a file or directory of this machine the container sees as well, at its own path, with :ro after it for one it cannot change; repeatable (runner.yaml: wall.mounts)
      --pids-limit int        the most processes and threads in the container (runner.yaml: wall.pids_limit)
      --policy string         this run's own policy, a file outside the checkout in the runner contract's policy format; it narrows the egress section of runner.yaml and never widens it, and is refused with a server configured unless --local
      --run-id string         the run's id when the caller already has one: a UUID in lower case (default a new one)
      --shm-size string       the size of /dev/shm in the container, such as 2g (runner.yaml: wall.shm_size)
      --stop-grace duration   how long the runtime gets between the stop signal and SIGKILL when the runner stops it (default 10s; runner.yaml: run.stop_grace)
      --stop-signal string    the signal that requests the runtime to stop when the runner stops it: SIGTERM, SIGINT, SIGHUP, SIGQUIT, SIGUSR1 or SIGUSR2 (default SIGTERM; runner.yaml: run.stop_signal)
      --timeout duration      stop a runtime that still runs after this long, such as 5h30m, and exit 124 (default no limit; runner.yaml: run.timeout)
      --wall string           start the runtime in a container with no route out except to the proxy: docker, or none (runner.yaml: wall.adapter)
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory](qory.md)	 - Compose the harness a runtime loads from modules
* [qory run resend](qory_run_resend.md)	 - Send a finished run's record to the server again, completing it first

