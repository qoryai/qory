## qory run

Run the agent on its harness, observed and recorded

### Synopsis

Compose the harness, then run the agent on it, inside Forager's session.

Every connection goes through a proxy on this machine and is recorded. The record,
events.jsonl and output.log, goes to a folder of the checkout's under
~/.local/state/qory/runs ($XDG_STATE_HOME/qory/runs when that is set; qory refuses a
relative XDG_STATE_HOME), and qory names it when the run ends. The exit status is the
agent's when it exits by itself. When the run ends in another way, or its starter says
it went otherwise, the exit status is 0 if the run completed, 124 at the time limit,
and 1 otherwise.

qory run opens the run, then composes the harness, as qory harness compose does, into
the same home, and prints the result on stderr. -f, --model, --effort, --tier and
--tier-effort mean what they mean there. With a run key, the home is outside the folder
the run runs in, under qory/homes in the cache directory, unless --home or harness.home
sets another, and a home inside the folder stops the run.

The agent is the runtime of the stack's target. Name another first, and the run composes
for it instead; name one when the target has several. Arguments after -- go to the
agent. At a terminal the agent runs with its interface; --headless, no terminal, or a
headless argument such as -p runs it on pipes.

forager.yaml in ~/.config/qory sets what Forager does on this machine. A repository
cannot set it:

  gateway.egress        the hosts the agent may reach: enforce or observe, allow and deny
  wall                  run the agent in a container whose one way out is the proxy
  gateway.credentials   tokens the proxy sets on requests; behind a wall the agent never has them
  gateway.integrations  programs that supply such tokens, such as qory-github
  gateway.server        the server every run reports to; --local skips it
  session.run           a time limit, and how the agent is stopped

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md

```
qory run [runtime] [-- argument...] [flags]
```

### Examples

```
  qory run                                          # the agent, at your terminal
  qory run claude -- -p "Reply pong"                # one headless turn
  qory run --model claude-opus-5-5 -- -p "$prompt"  # composed with this model
  qory run --wall docker --image agent:1            # in a container
  qory run --image go-docker                        # in an image forager.yaml defines
  qory run --policy ~/policy.yaml -- -p "$prompt"   # with this run's policy file
  qory run --timeout 5h30m -- -p "$prompt"          # stop it after five and a half hours
  qory run --subject type=ticket,ref=7              # say which ticket the run works on
```

### Options

```
      --access-key-secret-fd int   read the access key's secret from this file descriptor, 3 or above; it wins over QORY_ACCESS_KEY_SECRET and the access-key-secret file
      --cpus string                how many CPUs the container gets, such as 1.5 (forager.yaml: wall.cpus)
      --details string             a JSON object of the run's details, read from this file, or - for stdin when stdin is not a terminal; at most 8192 bytes compacted, 4 levels deep
      --effort string              run the main agent at this effort value, one of the values the runtime's descriptor lists, such as high (qory.yaml: harness.effort)
      --env stringArray            a variable of this shell to pass to the agent, by name, with a wall or without; it wins over wall.env of forager.yaml; repeatable
  -f, --file string                the qory-stack.yaml, qory.yaml or harness.yaml to compose instead of the one found, or a stack's folder in a git repository as <https URL>#<ref>:<folder>; when the checkout's document extends a stack, this one is its base
      --gateway string             the gateway that --run-code-file redeems the code through; the run key must be for this gateway
      --headless                   run on pipes even at a terminal; -p for claude implies it
  -h, --help                       help for run
      --home string                where the harness is composed: a directory outside the checkout, one home per checkout under it, or .qory/harness (qory.yaml: harness.home)
      --image string               the container's image unless the run's policy selects one: a name of wall.images, or a reference (forager.yaml: wall.image)
      --kind string                what kind of run it is, such as review or fix, reported when the run starts
      --label stringArray          a key=value name for the run, reported in its events; repeatable (forge and repository come from the origin remote)
      --local                      run without the server: record to files, under the machine's policy
      --memory string              the most memory the container gets, such as 8g (forager.yaml: wall.memory)
      --model string               write this model instead of target.model (qory.yaml: model)
      --mount stringArray          a path of this machine the container sees too, :ro for read-only; repeatable (forager.yaml: wall.mounts)
      --pids-limit int             the most processes and threads in the container (forager.yaml: wall.pids_limit)
      --policy string              this run's policy file, kept outside the checkout; it narrows gateway.egress of forager.yaml, never widens it (with a server: needs --local)
      --relabel                    run although the checkout's origin remote gives other labels (forge, repository) than at its first run against the server, and keep the new ones
      --run-code-file string       read a run code (qecr_…) from <file>, redeem it through --gateway for the run's key and documents, and run the workflow they set; the key stays in memory
      --run-id string              the run's id, a UUID in lower case (default a new one)
      --run-key-fd int             read the run key from this file descriptor, 0 for stdin, in place of QORY_RUN_KEY, which an unwalled agent may read
      --shm-size string            the size of /dev/shm in the container, such as 2g (forager.yaml: wall.shm_size)
      --stop-grace duration        the time between the stop signal and SIGKILL (default 10s; forager.yaml: session.run.stop_grace)
      --stop-signal string         the signal that stops the agent: SIGTERM, SIGINT, SIGHUP, SIGQUIT, SIGUSR1 or SIGUSR2 (default SIGTERM; forager.yaml: session.run.stop_signal)
      --subject stringArray        what the run works on, type=<type>,ref=<ref>[,url=<url>][,title=<title>], such as type=ticket,ref=7; title takes the rest of the value, commas too; repeatable
      --tier stringArray           map a tier to a model id for each agent whose model is ${qory:<tier>}, as <tier>=<model id>; repeat it for each tier (tiers: light, fast, thinking; qory.yaml: harness.tiers)
      --tier-effort stringArray    set the effort value for each agent whose model is ${qory:<tier>}, as <tier>=<effort>, beside --tier <tier>=<model id>; repeat it for each tier
      --timeout duration           stop the run after this long, counted from its open, compose included, such as 5h30m, and exit 124 (default no limit; forager.yaml: session.run.timeout)
      --title string               the run's title, for a person to read, reported when the run starts
      --wall string                run the agent in a container whose one way out is the proxy: docker, or none (forager.yaml: wall.adapter)
```

### Options inherited from parent commands

```
  -v, --verbose   print more of what the command does; each command's help lists what
```

### SEE ALSO

* [qory](qory.md)	 - Get your coding agent ready to work, and keep it in check
* [qory run end](qory_run_end.md)	 - End a run a starter registered, and say how it went
* [qory run register](qory_run_register.md)	 - Register a run with the server, and write its run key for the job to a descriptor
* [qory run resend](qory_run_resend.md)	 - Send a finished run's record to the server again

