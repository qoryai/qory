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
      --headless      run on pipes even at a terminal, and read the runtime's structured output
  -h, --help          help for run
      --home string   where the harness is composed: a directory outside the checkout, one home per checkout under it, or .qory/harness (qory.yaml: harness.home)
      --local         record to files only, even when a webhook is configured
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help says what
```

### SEE ALSO

* [qory](qory.md)	 - Compose the harness a runtime loads from modules

