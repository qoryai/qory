## qory run

Start a runtime on the composed harness, observed and recorded

### Synopsis

Start a runtime on the composed harness, the way qory harness launch would, inside
the session runner: every connection the runtime makes goes through a proxy on this
machine and is recorded, and the session's output, the runner's observations and the
runtime's own reports are written as events. The runtime is the one the harness is
composed for, or named as the first argument when it is composed for several;
arguments after -- go to the runtime after the launch template's own, so
qory run claude -- -p 'say hello' runs one headless turn.

What the runner does on this machine is runner.yaml in the configuration
directory, ~/.config/qory, and nowhere else: a repository cannot set it. Its egress
section is the policy, which can only narrow what the runtime reaches; no section means
every connection is allowed and recorded, and a file that does not read means no run.
When the harness declares egress, the hosts its modules and the runtime declare in the
report, the runtime reaches the declared hosts the policy covers and nothing else; a
harness that declares nothing leaves the policy's list as it is. --policy names one
run's own policy, a file in the runner contract's policy format kept outside the
checkout, for a machine that serves runs of different kinds. It narrows only: under a
section in mode enforce the run reaches the file's hosts the section covers, and with
no section, or one in mode observe, the file stands as it is. Its webhook section
posts every event somewhere as well; when one is configured the runner pings it first
and does not start unless it answers. --local runs with the files alone, webhook or
not. A descriptor override, <runtime>.yaml under runtimes in the same
directory, replaces the built-in description of how the runtime's output and hooks map
to events.

A wall starts the runtime in a container with no route out except to that proxy, so a
program that ignores the proxy reaches nothing instead of going unseen: --wall docker,
or a wall section in runner.yaml, and --wall none for one run without the
section's. --image, or wall.image, names the container's image, which holds the runtime
and the project's toolchain; qory builds none. The container sees the checkout and the
composed home, at their own paths, and nothing else of this machine; of the environment
it gets the launch template's variables and the ones --env or wall.env names, the model
credential say, and nothing else. --mount, or wall.mounts, shows it more of this machine
at its own path, a sibling checkout say, with :ro after the path for what it must not
change; never a socket. --cpus, --memory, --pids-limit and --shm-size, or the keys of
the same names under wall, limit what it uses; a browser wants more /dev/shm than an
engine gives by default. Inside, the relay and the hook forwarder are qory's
own Linux build, mounted read-only: this binary on Linux, wall.helper elsewhere. With
the engine in a virtual machine, on a Mac, the runtime's hooks do not reach the runner.

At a terminal the session runs on a pseudo-terminal, so the runtime's own interface
works and its bytes are still captured; --headless, or no terminal, runs it on pipes and
reads its structured output. Either way the record is .qory/runs/<id>/ in the checkout:
events.jsonl, one event per line, and output.log, the session's bytes. The exit status
is the runtime's.

A caller that starts runs for a system of its own names them: --run-id gives the run
the id the caller already holds, a UUID in lower case, and --label key=value, repeatable,
puts the caller's own names, a key in a queue, a repository, an issue, into
ai.qory.run.started, where a receiver finds them. --timeout stops a runtime that still
runs after that long, 5h30m say: ai.qory.run.exited says the limit was the reason, and
the exit status is 124, as timeout(1) has it. Stopped at the limit or by a signal to
qory run, the runtime gets SIGTERM and, --stop-grace later, 10s unless named, SIGKILL:
the time a session needs to close what it has open. run.timeout and run.stop_grace in
runner.yaml set both for every run on the machine; --timeout 0 lifts the file's.

--verbose adds nothing here.

```
qory run [runtime] [-- argument...] [flags]
```

### Options

```
      --cpus string           how many processors' worth of time the container gets (runner.yaml: wall.cpus)
      --env stringArray       a variable of this environment that goes into the container under a wall, by name; repeatable (runner.yaml: wall.env)
      --headless              run on pipes even at a terminal, and read the runtime's structured output
  -h, --help                  help for run
      --home string           where the harness is composed: a directory outside the checkout, one home per checkout under it, or .qory/harness (qory.yaml: harness.home)
      --image string          the container's image under a wall (runner.yaml: wall.image)
      --label stringArray     the caller's own name for the run, key=value, reported in ai.qory.run.started; repeatable
      --local                 record to files only, even when a webhook is configured
      --memory string         the most memory the container gets, 8g say (runner.yaml: wall.memory)
      --mount stringArray     a file or directory of this machine the container sees as well, at its own path, with :ro after it for one it cannot change; repeatable (runner.yaml: wall.mounts)
      --pids-limit int        the most processes and threads in the container (runner.yaml: wall.pids_limit)
      --policy string         this run's own policy, a file outside the checkout in the runner contract's policy format; it narrows the egress section of runner.yaml and never widens it
      --run-id string         the run's id when the caller already holds one: a UUID in lower case (default a new one)
      --shm-size string       the size of /dev/shm in the container, 2g say (runner.yaml: wall.shm_size)
      --stop-grace duration   how long the runtime gets between SIGTERM and SIGKILL when the runner stops it (default 10s; runner.yaml: run.stop_grace)
      --timeout duration      stop a runtime that still runs after this long, 5h30m say, and exit 124 (default no limit; runner.yaml: run.timeout)
      --wall string           start the runtime in a container with no route out except to the proxy: docker, or none (runner.yaml: wall.adapter)
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help says what
```

### SEE ALSO

* [qory](qory.md)	 - Compose the harness a runtime loads from modules

