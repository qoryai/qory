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
harness that declares nothing leaves the policy's list as it is. Its webhook section
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
credential say, and nothing else. Inside, the relay and the hook forwarder are qory's
own Linux build, mounted read-only: this binary on Linux, wall.helper elsewhere. With
the engine in a virtual machine, on a Mac, the runtime's hooks do not reach the runner.

At a terminal the session runs on a pseudo-terminal, so the runtime's own interface
works and its bytes are still captured; --headless, or no terminal, runs it on pipes and
reads its structured output. Either way the record is .qory/runs/<id>/ in the checkout:
events.jsonl, one event per line, and output.log, the session's bytes. The exit status
is the runtime's.

--verbose adds nothing here.

```
qory run [runtime] [-- argument...] [flags]
```

### Options

```
      --env stringArray   a variable of this environment that goes into the container under a wall, by name; repeatable (runner.yaml: wall.env)
      --headless          run on pipes even at a terminal, and read the runtime's structured output
  -h, --help              help for run
      --home string       where the harness is composed: a directory outside the checkout, one home per checkout under it, or .qory/harness (qory.yaml: harness.home)
      --image string      the container's image under a wall (runner.yaml: wall.image)
      --local             record to files only, even when a webhook is configured
      --wall string       start the runtime in a container with no route out except to the proxy: docker, or none (runner.yaml: wall.adapter)
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help says what
```

### SEE ALSO

* [qory](qory.md)	 - Compose the harness a runtime loads from modules

