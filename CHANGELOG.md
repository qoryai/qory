# Changelog

Every release of qory, newest first, in the shape of [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
The version numbers follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html); before 1.0 a minor
release may change what an existing document does, and states it under Upgrading.

## [Unreleased]

### Added

- The `integrations` section of `runner.yaml` declares the integrations a machine uses,
  each a program that speaks the integration contract of qoryai/integrations, under a key:
  `program`, an absolute path or a name on the `PATH`, `qory-<key>` when absent, and
  `settings`. `qory` runs `<program> describe`, checks the settings against the
  description, and defines the credential whose name is the key, whose adapter is
  `<program> credential --settings <json> -- ${argument}` and whose `argument` and `hosts`
  are the description's. The settings word is compact JSON, every `$` written `\u0024`, as
  the integration contract defines for a declaration. `qory` contains no table of
  integrations: Qory's own `qory-github` and a program of yours are found and set up the
  same way. `qory run` describes the integrations the run's policy selects, every one when
  the server supplies the policy, and prints each program it found; `qory config`
  describes every one. A program runs from outside the checkout and outside every mount
  the wall makes read-write in the container, judged by where its links lead. The resolved
  program, every directory above it up to `/`, and every directory above each link on the
  way belong to root or the user running `qory`, and so does each link; other users may
  write none of them; and a group may write one when it is root's, gid 0, `wheel`,
  `admin`, or the owner's primary group when that group has the owner's name. A directory
  root owns with the sticky bit set keeps the rule, so `/tmp`, `/nix/store`, a default
  Homebrew install and a user private group's `~/go/bin` keep it. On a machine whose
  `PATH` is not its owner's alone, set `program` to each program's absolute path. `describe`
  runs in `/`, in a process group of its own that is stopped with it. A settings schema is
  draft 2020-12 and marks a secret `writeOnly` on a property of the settings themselves; a
  value of a secret is refused, since the settings go on a command line, and its
  `<name>_file` defines the path of the file that contains it. A name the `credentials`
  section defines itself is the section's: `qory run` and `qory config` print this, and
  `qory config` alone describes that integration. A program that does not describe within
  10 seconds, settings its description refuses, and an integration that plays no role
  `qory` expands stop the run before it starts, with the program's own line.

## [0.11.0] - 2026-09-24

### Upgrading

- A `qory.yaml` or `harness.yaml` holding a YAML alias, a `*name` or a `<<: *name`
  merge, is refused with the alias's line and name, exit 2. Other readers of the file
  already refuse an alias, since a few hundred bytes of them chained can expand to
  gigabytes, and they read such a file as unreadable without saying so to its author;
  qory now fails on it where it is written. An anchor that no alias uses is still read.
  Write the value out in full where the alias stood.
- Every event the runner reports is named under `dev.qory`, where it was `ai.qory`:
  `dev.qory.run.started`, `dev.qory.run.egress`, `dev.qory.ping` and the rest. The prefix
  is the reverse-DNS name of qory.dev, the domain of the contract's identifiers, like its
  schema URLs. A receiver of your own that matched on `ai.qory.*` matches `dev.qory.*`.
  `qory run resend` of a run recorded before this release posts its events under the new
  names; the record on disk stays as it was written. The wall's containers and networks
  are labelled `dev.qory.run`, and the ones an older runner left are still reaped.
- Needs `github.com/qoryai/runner` 0.5.1, contract `v1` revision 1 as amended in 0.5.0
  and 0.5.1.

### Added

- A swarm of bees under `qory worktree add`, `qory worktree remove` and
  `qory harness compose` while they work with nothing to print, over git, a fetch or a
  configured command: three bees circling in braille on the line under the last one
  printed, once the output has been quiet for 250 ms. Every write clears it first, so a
  fast command never shows it, and the cursor is hidden while it is drawn and shown again
  when it is cleared, when the command ends and on an interrupt. It does not run off a
  terminal, with `TERM=dumb`, in CI, or under `run` and `launch`, which hand the terminal
  to another program.

### Changed

- The run configuration request carries every label of the run, the ones `--label`
  names and the two from the origin remote, where it carried `forge` and `repository`
  alone: the server decides which of them name what the run works on. A server that
  reads only those two finds them as before.

- `qory worktree remove` lets a branch go without asking when its pull request was
  merged by squash or by rebase. Such a merge writes new commits on the base, so the
  branch's own were on no remote branch once GitHub deleted it, and remove asked about
  them as if they were unpushed. The base is fetched first, and the branch's commits, or
  its whole change as one, are found on it by patch; the branch row names the commit it
  landed as. A branch with commits made after the merge is still asked about.
  `--offline` skips the fetch.

## [0.10.0] - 2026-09-21

### Upgrading

- The `webhook` section of `runner.yaml` is gone, and a file that still has it is
  refused with a message that says so. Put a `server` section in its place: `url`, the
  server's scheme and host with nothing after, `access_key`, the key the control plane
  issued this machine, and `secret`, in the file or in `QORY_SERVER_SECRET`;
  `QORY_WEBHOOK_SECRET` is not read any more, and `wall.env` and `--env` refuse the new
  name as they refused the old. At start the runner fetches the server's configuration,
  signed with the key and the secret, posts the events where it says, and takes the
  server's run configuration as the run's policy when it names one. A receiver of your
  own implements the same contract, the runner's `contracts/runner/v1` at revision 1:
  the discovery document and the events endpoint.
- `qory run --policy` is refused when a server is configured, unless `--local` keeps
  the run to the files: with a server the server's run configuration is the policy.
- `qory config` lists `runner.server.url` and `runner.server.access_key` where it listed
  `runner.webhook.*`; the secret is never listed.
- The hosts a harness declares no longer narrow the policy. The runner reports them as
  `harness_hosts` in `ai.qory.run.policy_applied`, renamed from `declared`, and `allow`
  is the policy's own list. A machine that relied on the narrowing puts the hosts in
  `egress.allow`.
- Needs `github.com/qoryai/runner` 0.4.1, which replaces the webhook with the server.

### Added

- `egress.deny` in `runner.yaml`: hosts the runtime may not reach, in `allow`'s
  grammar, denied in either mode, under `observe` as under `enforce`, whatever
  `allow` says of them. The list goes to the runner as written, `qory config` lists
  it as `runner.egress.deny`, and a run's own `--policy` keeps the file's deny list
  beside its own whatever the modes. Observe records every connection and denies
  only what `deny` names.
- The `server` section of `runner.yaml`: `url`, `access_key` and `secret`, the runner
  contract's server document, read and refused in the file's voice, and described by
  `runner.schema.json`.
- Two labels from the checkout's origin remote, unless `--label` names them: `forge`,
  the remote's host, and `repository`, its path without the leading slash and `.git`,
  so `git@github.com:acme/shop.git` is `github.com` and `acme/shop`. They go into
  `ai.qory.run.started` as every label does, and the runner asks the server for the run
  configuration by them. A checkout with no remote, or one on this machine, carries
  neither, and nothing else is read from the remote.
- What the runner reports at contract revision 1: `outcome` on every
  `ai.qory.run.egress`, `connected`, `dial_failed` or `refused`; `harness_hosts`,
  `source: fetched` with the `url` and the server's `run_configuration` digest on
  `ai.qory.run.policy_applied`, and a second `policy_applied` when the server's run
  configuration changes during a run; `contract_version` in the ping.

### Changed

- `qory run claude -- -p '…'` at a terminal runs on pipes and is recorded as not
  interactive, with no flag to say so: an argument the runtime's descriptor names as
  headless, `-p` and `--print` for Claude Code, means the runtime runs without an
  interface whoever started it, and the runner takes it as `--headless`. The
  descriptor names the arguments, not the command, since runtimes differ in how they
  say it; a runtime whose descriptor names none is on the pseudo-terminal at a terminal
  as before, and `--headless` still says so by hand. Needs `github.com/qoryai/runner`
  0.4.1.
- `qory run resend` sends the record to the server: it fetches the server's
  configuration first and posts where it says.
- The help of `qory run` and `qory run resend`, the README, the contract's runner file
  section and the security policy say server where they said webhook.

## [0.9.0] - 2026-09-19

### Upgrading

- A session no longer finds `QORY_WEBHOOK_SECRET` in its environment, and `wall.env` or
  `--env` naming it is refused. Nothing that runs in a session should have read it; a
  configuration that passed it into a container on purpose has to stop. Keep the secret
  in the environment `qory run` starts in, or in `runner.yaml`, as before.
- A runtime the runner ships no descriptor for used not to start under `qory run`. It
  now runs, recorded without the session's own events. Nothing changes for Claude Code.
- A run that uses none of what this release adds behaves as before: with no credential
  selected and no path rule, the proxy terminates no TLS and the container gets no
  authority to trust.

### Security

- `qory run` no longer passes `QORY_WEBHOOK_SECRET` into the session. Without a wall the
  session's environment was qory's own, the secret included when it was given that way,
  and a session holding it could sign batches of its own to the receiver. The variable
  is taken out, and `wall.env` and `--env` refuse its name. Behind a wall it never went
  in. GHSA-ff27-pg8g-j94q.

### Added

- `qory run --policy <file>`: one run's own policy, in the runner contract's policy
  format, for a machine that serves runs of different kinds. It narrows the `egress`
  section of `runner.yaml` and never widens it, and is refused inside the checkout or
  anything the container may write.
- `qory run --mount <path>[:ro]` and `wall.mounts`: more of the machine for a walled
  run, at its own path. A socket is refused.
- `qory run --cpus`, `--memory`, `--pids-limit`, `--shm-size`, and `wall.cpus`,
  `wall.memory`, `wall.pids_limit`, `wall.shm_size`: what the container may use.
- `qory run --run-id` and `--label key=value`: the caller's id for the run, a UUID, and
  its own names for it, reported in `ai.qory.run.started`.
- `qory run` runs any runtime the harness is composed for, not Claude Code alone. What
  it knows of one is a descriptor in the runner contract's format: the runner's own,
  Claude Code's today, or `<runtime>.yaml` under `runtimes` in the user's configuration
  directory, which describes a runtime the runner ships nothing for: what its output
  means, and in a `stop` section which signal asks it to leave. A runtime with neither
  runs bare: the run, its log and its egress are recorded, the session's events are not.
  Before, a runtime without a descriptor did not start.
- `qory run --timeout`, `--stop-signal` and `--stop-grace`, and `run.timeout`,
  `run.stop_signal` and `run.stop_grace` in `runner.yaml`: a runtime still running at the
  limit is stopped, `ai.qory.run.exited` carries `reason: timeout`, and the exit status
  is 124. Whenever the runner stops the runtime it sends the stop signal, SIGTERM unless
  named, and SIGKILL after the grace, 10s unless named. A runtime may close its session
  on one signal and drop it on another, so the signal is one of SIGTERM, SIGINT, SIGHUP,
  SIGQUIT, SIGUSR1 and SIGUSR2.
- Credentials the agent never holds. The `credentials` section of `runner.yaml` defines
  what the machine has: a token from `env`, from a `file`, or from an `adapter`, a
  program of yours that knows one kind of host and prints the token with the hosts, the
  scheme and the paths it is for. A run's policy selects among them by name, with an
  argument for an adapter. Behind a wall the runner keeps each outside the container,
  its proxy sets it on the requests to its hosts, and the container gets placeholders.
  Of a host with paths the run reaches those and no other; `egress.paths` in a policy
  does the same with no credential. `wall.ca_env` names the variables that point the
  container at the authorities it trusts.
- `qory run resend <run-id>`: a job's last step. It sends the webhook what it has not
  accepted of a finished run's record, closes a record a runner that died left without
  `ai.qory.run.exited`, with `reason: runner_lost`, and removes the containers and
  networks that run's wall left. A run still going is refused.
- Needs `github.com/qoryai/runner` 0.3.0, which adds the limit, the labels, the
  container's limits, the policy's narrowing, credentials held outside the container,
  path rules, the resend and the runtime interface.

## [0.8.0] - 2026-09-17

### Upgrading

- `worktree.base` and `--base` naming a branch the remote holds now start new work from
  the remote's copy, `<remote>/<branch>`, where they read the local branch before. A
  branch recorded from here on has `branch.<name>.qory-base` set to that ref. Write
  `heads/<branch>` to keep starting from the local branch.

### Added

- The report carries the repository's base branch as `worktree.base`: the `branch` a
  pull request targets, the `ref` a diff or a merge reads, and the `source` that named
  it, the file that set `worktree.base`, `remote HEAD` or `checkout`. It is resolved
  from the refs already fetched, so a compose reaches no remote for it, and
  `qory harness inspect` prints it as `base branch`. `worktree.base` is documented as
  the repository's base branch, with `qory worktree add` as one reader of it.

### Changed

- `qory update` of a `go install` build fetches the release's module first, and the
  finished step stays on the screen as `downloaded qory <version>`, above `built qory
  <version>`. It was one step, which landed as built alone.

### Fixed

- `worktree.base` naming a branch that was never checked out, a base branch other than
  the remote's HEAD branch in a fresh clone, was refused as `not a branch, tag or commit
  of this repository`. It is read on the remote now, the way the default base is.

## [0.7.0] - 2026-09-17

### Added

- `qory run --wall docker --image <image>` starts the runtime in a container with no
  route out except to the runner's proxy, so a program that ignores the proxy reaches
  nothing instead of going unseen. The container sees the checkout and the composed
  home, at their own paths, and of the environment only the launch template's variables
  and the ones `--env` names. `--wall none` runs once without a configured wall.
- The `wall` section of `runner.yaml`: `adapter`, `image`, `env`, `user`, `command` and
  `helper`, with the same meaning as the flags, for every run on the machine;
  `qory config` lists it under `runner.wall.`, and `runner.schema.json` describes it.
- Inside the container the relay and the hook forwarder are qory's own static Linux
  build, mounted read-only: the running binary on Linux, `wall.helper` elsewhere. The
  hidden `qory run relay` is the mode the relay's container runs.

### Upgrading

- Needs `github.com/qoryai/runner` 0.2.0, which adds the wall. A run without a wall does
  what it did.

## [0.6.0] - 2026-09-17

### Added

- `qory update` shows a progress indicator while it downloads or builds the release.

### Fixed

- `qory update` no longer cuts a release download off after three seconds.

### Changed

- `extensions`, in a `qory-stack.yaml` and in a checkout's `harness` section, takes a
  value of any shape under each key: a scalar, a list, or a map of any depth. It was one
  map per namespace, so `sweep_floor: 40` directly under `extensions` failed to decode.
  The report carries the block as written, and `qory harness inspect` prints one row per
  key, a map's keys one level down as `key.sub`, which is what a namespaced block printed
  before. A key with no value is carried as null and no longer refused. Both schemas
  drop the object constraint on the values.
- A checkout that extends a stack may set an extension key the base sets: its value
  replaces the base's whole, and the keys it leaves alone stay the base's. It was
  refused with `extensions.<namespace> is the base stack's`. Nothing under a key is
  merged.

### Upgrading

- Nothing to change: a block written one map per namespace reads, reports and prints as
  it did. A reader of the report's `extensions` that assumed every value is an object
  now meets whatever the stack wrote there.

## [0.5.0] - 2026-09-16

### Added

- `qory run` starts a runtime on the composed harness through the session runner of
  [`github.com/qoryai/runner`](https://github.com/qoryai/runner): the launch spec is the
  one `qory harness launch` prints, with the arguments after `--` appended; every
  connection the runtime makes goes through a loopback proxy and is recorded. What the
  runner does on a machine is `runner.yaml` beside the user's `qory.yaml`, and nowhere
  else: its `egress` section is the policy, which in enforce mode decides what is denied,
  and its `webhook` section is where every event is posted as well, the secret there or
  in `QORY_WEBHOOK_SECRET`. The session's bytes, the runner's observations and the
  runtime's own reports are written as CloudEvents to `.qory/runs/<id>/events.jsonl`
  beside `output.log`; `--local` keeps to the files when a webhook is configured. `qory
  config` lists the file's values under `runner.`. At a terminal the session runs on a pseudo-terminal;
  `--headless` or no terminal runs it on pipes. The exit status is the runtime's. The
  hidden `qory run forward` is the hook command the runner installs into a copy of the
  runtime's settings, so a session needs nothing on the machine beyond `qory`.
- A module declares the hosts its skills, hooks and servers reach under `egress` in
  `qory-module.yaml`, a lower-case name or a `*.` suffix, in the grammar the runner
  contract gives a policy's allow list. The compose unions the declarations into the
  report's `egress`, each host with the modules that declared it, and adds the runtime's
  own endpoint under the runtime's name when any module declares; `qory harness inspect`
  shows the union. `qory run` hands it to the runner, which keeps the declared hosts the
  policy covers, so the policy is the ceiling and a declaration only lowers it. A harness
  in which no module declares hands over nothing and the policy's list stands. `egress`
  is a new manifest key, refused by a 0.4.4 binary as unknown; a stack shipping a module
  that declares states `qory: ">=0.5.0"`.

- `extending.target` in a stack: the runtimes and the models the stack is written for,
  `runtime` and `model`, each one name or a list, one of the two at least. The target a
  checkout resolves, whichever of the document, the configuration and the flags set it,
  is held to it under `extends` and `-f` alike, and before `--dry-run` stops: a runtime
  outside the list is refused, `runtime <r> is not one the base stack <name>@<pin> is
  written for; runtimes: <list>`, and where models are listed the target names one of
  them, or is refused with `the target names no model, and the base stack <name>@<pin>
  is written for one of these; models: <list>`. A stack without the block accepts every
  target.

### Changed

- A `qory-stack.yaml` carries no `target`, and one that does is refused: `target is not
  a stack's; a stack is delivered to be extended, and the checkout extending it sets
  target under harness, beside extends`. The `harness` section of a checkout's
  `qory.yaml` sets `target` beside `extends`, the runtime and model it composes the
  base for, where before the base's target was the checkout's and a `target` beside
  `extends` was refused.
- The target of a compose on a base is the document's, then the machine's
  `harness.runtime` and `harness.model` over it, then `--runtime` and `--model` over
  those. The two base checks are gone, the one refusing a runtime the base did not
  render for and the one refusing a model beside the base's; what bounds the target now
  is the base's `extending.target`, when it states one. A compose on a base with no
  runtime from the document, the configuration or `--runtime` is refused with
  `target.runtime is required; the base stack <name>@<pin> carries no target, so the
  document sets one beside extends, or the configuration or --runtime does`.
- `qory harness compose -f <stack>` in a checkout whose root holds a document keeps the
  document's `target`, as `extends` does. Before, a document that set one was taken for
  the repository's own stack and refused beside `-f`.
- A stack's `qory` range is joined with the `qory` key of the `qory.yaml`, or
  `harness.yaml`, at the root of the repository the stack sits in, read whenever the
  stack file is read: under `extends`, under `-f` and alone. A repository delivering
  stacks states its floor once, and a stack naming no key is held to the repository's.

### Upgrading

- A 0.4.x qory refuses `target` beside `extends`, and 0.5.0 refuses `target` on a
  stack, so the three land together: the stack edit that drops its `target`, one
  `target` line in every repository extending it, and the runner's upgrade. A stack
  delivered for 0.5.0 states `qory: ">=0.5.0"`, so an earlier qory is told which it
  needs instead of refusing the file.
- A stack that states `extending.target.model` makes the model required: a checkout
  naming none is refused, since it would run whatever the runtime defaults to. A stack
  that means to keep a fleet on one model lists it; one that leaves the model to the
  checkout names `runtime` alone, or no block.
- A repository delivering stacks may state `qory: ">=0.5.0"` once, at its root, and
  drop the key from each stack; every stack it delivers is held to the root's range
  beside its own.
- A self-test that composed a stack with `-f` into a checkout holding no document, and
  took the runtime from the stack's target, now passes `--runtime` and `--model`, or
  composes once per runtime the stack allows.

## [0.4.4] - 2026-09-16

### Added

- A document names an entry it dispatches by a reference, `${qory:agents/<name>}`,
  `${qory:skills/<name>}` or `${qory:commands/<name>}`, and the compose resolves it to
  the name the session registers the entry under, which the module cannot know: a
  program that loads the harness as a plugin puts its own name before every agent,
  skill and command, `harness:reviewer`, where the checkout's links and most launch
  paths register the name as written. A reference is read in an agent, a command, an
  output style, `AGENTS.md` and every `.md` file under a skill. It is a requirement: an
  entry needs no `requires` line for what its documents reference, an `only` follows it
  within the module, and a reference nothing composed answers to fails the compose,
  `module core: skill deploy references agent coder, which no module ships and the stack
  does not bind`. A reference resolves within the stack's composed entries and its
  bindings alone; it never brings in a module the stack did not list.
- A reference may name a role, a name no entry has, and the stack binds it under `bind`
  to the entry that fills it: `agents/coder: rails-coder`. One core module then serves
  every product stack, each binding the role to its own agent. A binding to an entry
  that is not composed, and a role whose name an entry has, fail the compose; every
  failure of a binding, a requirement or a reference comes at once, one per line, so
  one compose names every site there is to fix. A checkout extending a stack adds to
  the base's bindings and may rebind a role of the base's. The report carries the
  bindings and each entry's references, and `qory harness inspect` prints them.
- The composed tree resolves each reference for the path it stands on. An entry with
  none is a link as before; one with a reference is a written copy, a skill a real
  directory holding a link per file without a reference, so the module's files stay live
  wherever nothing had to change. The shared root, every checkout link and every launch
  path but one resolve to the bare name; the `claude` plugin resolves to
  `harness:<name>`.
- The instructions a launch path reads end with the names the session registers, per
  kind, and each bound role as the entry it is bound to, derived from the same resolver
  as the documents, so a session that meets an unmarked name in prose knows the
  registered one. For claude they are written to `claude/launch/CLAUDE.md`, which the
  launch line appends to the system prompt, while the checkout's `.claude/CLAUDE.md`
  keeps the bare names its `.claude/agents` register; for codex they end the home's
  `AGENTS.md`, which says the names are the modules'. A compose with no agent, skill or
  command says nothing.
- `qory harness launch --json` carries `addresses`, the registered names per kind for
  that launch, roles included, and `--address <kind>/<name>` prints one of them alone,
  so a launcher builds its first prompt from an entry point whichever runtime it
  starts.

### Upgrading

- `${qory:` followed by anything but `agents/<name>`, `skills/<name>` or
  `commands/<name>` and `}` now fails the compose in a document; a module that carried
  the text for another reason renames it.
- The claude launch line appends `${dir}/launch/CLAUDE.md` in place of
  `${dir}/CLAUDE.md`. A `harness.launch.claude` override naming the old file keeps
  working and gets the bare names without the roster; drop the override, or name the
  new file, for the plugin's.
- `bind` is a new stack key, refused by a 0.4.3 binary as unknown. A stack that binds a
  role states `qory: ">=0.4.4"`.

## [0.4.3] - 2026-09-16

### Fixed

- A session started from the claude launch line had every skill and no agent. The
  plugin the compose renders at `claude/plugin` linked its agents the way it links
  everything else, and Claude Code passes over a link in a plugin's `agents/` where it
  follows one in a plugin's `skills/` and in a checkout's `.claude/agents`. The agents
  are now copied into the plugin and register as `harness:<name>`; the launch line, the
  layout and the checkout's links are as they were. A stack delivered for launching
  claude states `qory: ">=0.4.3"`, so an earlier qory refuses it instead of composing
  a roster the session never sees.

## [0.4.2] - 2026-09-15

### Added

- The harness can be composed outside the checkout, into a directory the process that
  composes it owns: `qory harness compose --home <dir>`, or `harness.home` in the
  machine's `qory.yaml`, puts the tree and its report under the directory, one home per
  checkout and per worktree, and writes nothing into the checkout: no link, no `.qory`,
  no exclude line. A tracked path at a link's name is then no collision, `git clean -x`
  deletes nothing of qory's, and a pull is an ordinary pull. `inspect`, `remove` and
  `--check` take `--home` too and find the checkout through the report, so they run from
  either side. `harness.links: none`, or `--no-links`, keeps the checkout untouched with
  the home under `.qory` as well.
- `qory harness launch --runtime <name>` prints the command that starts a runtime's
  program on the composed home, wherever it is, from a launch template each runtime
  ships: for claude `--plugin-dir` for a plugin in Claude Code's own layout that the
  compose renders at `claude/plugin`, `--settings` for the permissions, hooks,
  environment and model, `--mcp-config` for the servers, `--append-system-prompt-file`
  for the instructions, and `--setting-sources user` so no `.claude` of the checkout or
  of a directory above it is read; for cursor the same plugin with the hooks and servers
  copied in; for copilot `--add-dir` on a rendered workspace and
  `--additional-mcp-config`; for codex `CODEX_HOME`, with the skills and instructions
  rendered into the directory; for opencode `OPENCODE_CONFIG_DIR`; for amp
  `--settings-file`; for gemini `GEMINI_CLI_SYSTEM_SETTINGS_PATH`. A launcher evals the
  line and knows nothing of the layout; `--json` prints the command, arguments and
  variables as one object. `harness.launch.<runtime>` in `qory.yaml` puts a command,
  arguments or variables in place of the runtime's own, field by field, so a program
  whose flags move is followed without a new qory. goose and any read their harness
  through the links alone, and `launch` says so.
- copilot has a place for MCP servers now, `copilot/mcp.json` for a launch, so the kind
  is no longer skipped for it.
- `qory worktree add` composes a worktree into its own home under `harness.home`, and
  `qory worktree remove` removes that home with the worktree; `qory worktree list` finds
  the report there.

## [0.4.1] - 2026-09-14

### Added

- `qory update` installs the newest release the way this qory was installed: `brew
  upgrade` for the cask, `go install` for a GOBIN build, and for a release binary the
  release's archive for this platform, checked against the release's checksums and
  renamed over the running binary. `--check` reports and installs nothing. A build ahead
  of the newest release is offered the release and asked; `--release` answers yes in a
  script.
- Every command looks for a newer release when its error output is a terminal and says
  so after its own output when the newest release is ahead of its version. GitHub is
  asked at most once an hour; between asks the answer is read from a file under the
  user's cache directory. `QORY_NO_UPDATE_CHECK=1` and `CI` turn the look off. A build
  from `main` between releases is told only when it is behind a release.

### Fixed

- A document declaring a retired `apiVersion`, one an earlier qory wrote for the same
  format, was refused with exit 2. It is now read as the current version, in every
  document, and the compose prints a `retired` row per document naming the version it
  declares and the line to write. A later major release stops reading it.

## [0.4.0] - 2026-09-12

A harness repository publishes its stacks and modules by name, and a consumer names them
instead of their directories. A checkout's committed file carries only what is the
repository's own, its modules and its extensions, and a runner supplies the base from the
stack tree it holds, so the file names no version, ref or URL of the base, and may say
nothing of the tool that reads it.

### Upgrading

- The API group is `qory.dev`, the domain of the open format: `apiVersion:
  qory.dev/v1alpha1`. `qory setup repo` writes it, `qory version` reports it, and the
  schemas name it.
- A `qory.yaml` with an `exports` section, and a source with a `stack` or `module` key,
  are refused by a 0.3.x binary as unknown keys, exit 2. A repository that exports states
  `qory: ">=0.4.0"` on its stacks, so a 0.3.x consumer is told which qory it needs.
- A stack in a repository whose `qory.yaml` sets `exports.dir` reads a module named
  without a source under that modules directory, `<dir>/modules/<name>`, instead of
  `modules/<name>` at the root. A repository without the key reads as before.
- `qory worktree remove` deletes the branch by default. `--keep-branch` keeps it for one
  remove, and `worktree.branch: keep` in your `qory.yaml` keeps it always. `--force` now
  covers uncommitted changes alone; a branch with commits nothing else holds is asked
  about, or answered by `--keep-branch` or `--delete-branch`.
- `qory harness compose -f <stack>` in a checkout whose `qory.yaml` holds a document, a
  `harness` section naming modules, a stack to extend or extensions, composes the
  document on that stack as its base. Before, the flag composed the stack alone and the
  document was dropped without a word. A document holding its own stack, a `target`, is
  refused beside `-f` with exit 2; compose it without the flag.
- `qory worktree add` fetches the remote before every add, so a new branch starts at the
  remote's tip and a branch pushed from another machine is tracked instead of cut anew.
  `--fetch` is gone; `--offline` skips the fetch and goes on with the refs already there.
  A fetch that fails stops the add and names `--offline`; `git.timeout` bounds it.
- `-v, --verbose` is a flag of every command, in place of the one `qory harness compose`
  owned. `qory harness compose -v` reads as before.
- What a `worktree.run.add` or `worktree.run.remove` command prints is shown with
  `--verbose`, and otherwise only when the command fails, as part of the error.

### Added

- An `exports` section in a repository's `qory.yaml`: the `stacks` and `modules` it
  publishes, each a name that is one directory under `stacks/` or `modules/`, and `dir`,
  where those two directories are, as one directory holding both or a map naming each.
  `qory config` prints it, and every command that reads the configuration refuses a
  listed export whose directory holds no document, naming it.
- `stack` on `extends` and `module` on a module's source, beside `git` and `ref` or the
  `path` of the repository on disk: the export's directory is read from the
  repository's `exports` section, so the publisher's layout is its own. An export the
  repository does not list is refused with what it does export. The report writes an
  export as `<url>#<ref> stack <name>` or `<url>#<ref> module <name>`.
- `--base` on `qory worktree add` moves a branch that already exists: a branch with no
  commits of its own is reset onto the base, one with commits has them rebased onto it,
  after a question or at once with `--rebase`. The base a branch was cut from is recorded
  in its git config as `branch.<name>.qory-base`.
- `--branch` and `--pr` on `qory worktree add` attach the worktree to a branch of the
  remote or to a pull request: the branch is fetched, checked out under its own name and
  set to track the remote's, and a name given beside the flag names the worktree. A
  pull request's head is found among the refs the remote publishes, with no hosting API:
  GitHub, Forgejo, GitLab and Bitbucket Server out of the box, any other host through
  `worktree.pr` in `qory.yaml`. A head that no branch of the remote holds, a fork's, is
  checked out as `pr-<n>` and pulled from its ref.
- `worktree.branch` in `qory.yaml`, `delete` or `keep`: what a remove does with the
  branch. `qory config` shows it and `qory setup machine` writes it.
- `qory worktree remove` tells a branch that holds nothing of its own, which goes
  quietly, from one holding commits no remote branch, the main checkout or its base
  holds, which is asked about: push it and delete, keep it, delete it anyway, or stop.
  Without a terminal the question is an error naming the flags.
- `-f <stack>` names the base of the checkout's own document, as if the document had
  named the stack's directory under `extends`: the base's modules come first and closed,
  the document's modules append, extensions merge by namespace, and the collision rules
  are those of `extends`. What the document itself names under `extends` is not read and
  not fetched. The compose prints `base <file>  (named by -f)`, or `(named by -f, in
  place of extends <source>)`, and the report records the base with the stack
  directory's path relative to the document and the pin `working-tree`. A checkout
  without a document composes the stack alone, as before, and `-f` naming a `qory.yaml`
  or `harness.yaml` still composes that document itself. `qory worktree add -f <stack>`
  composes the new worktree the same way, so a runner adds and composes in one call.
- `harness.yaml` as a second name for `qory.yaml`, read exactly as `qory.yaml` at every
  level: the user's directory, the ancestor directories, the checkout root; by the
  configuration walk, by stack discovery, and by `-f`. A directory holds one of the two
  names, never both, and both is refused. `qory setup repo` writes under the name the
  directory already uses, and an unknown key is reported under the file's own name.
- `apiVersion` is optional in `qory.yaml` and `harness.yaml`: a file leaving it out is
  read as `qory.dev/v1alpha1`, the newest format this qory reads, and a wrong value is
  still refused. A delivered file, `qory-stack.yaml` or `qory-module.yaml`, carries it
  as before.
- A document may leave `extends` out when `-f` supplies the base, and a document that
  extends a stack may name no modules, carrying its extensions alone. A document with
  neither `target` nor `extends`, composed with no base from `-f`, is refused with exit 2
  and the message says what the section holds instead. A `harness` section with only
  the machine's keys is still not a document.
- `{from: <path>, to: <path>}` as an entry of `worktree.link` and `worktree.copy`, beside
  a path inside the checkout: `from` anywhere on the machine, absolute or under `~`, `to`
  the path in the worktree. Meant for the user's `qory.yaml` in `~/.config/qory`, so a
  machine's own path never lands in the committed file. A `from` that is not there is
  reported as missing; a destination already in the worktree, a dangling link too, is kept.
- `--verbose` on `qory worktree add` and `remove` prints each git command as it runs,
  and remove prints how the branch's own commits were counted; `qory worktree list -v`
  adds the path of each composed worktree's report.
- `QORY_BASE`, the branch's recorded base, in the environment of `worktree.run.add` and
  `worktree.run.remove`, beside `QORY_WORKTREE`, `QORY_MAIN` and `QORY_BRANCH`.

### Changed

- `qory worktree add` on a worktree already there says `already there` instead of
  `reused`, and `you are in it` on the path row when you stand in that worktree.
- The message for a checkout with nothing to compose names both file names: no
  `qory-stack.yaml`, and no `qory.yaml` or `harness.yaml` whose harness section names
  modules or a stack to extend.

### Fixed

- `qory worktree add --base <ref>` on a branch that already existed silently ignored the
  base, even one that named nothing. The base is checked before anything else, on every
  path.
- `worktree.branch` was missing from `config.schema.json` and the configuration reference.

## [0.3.0] - 2026-09-11

A document states the qory it is written for, a compose can be checked against the home
it wrote, and a stack may take part of a module, with what every entry requires declared
by the module that ships it.

### Upgrading

- A stack or a `qory.yaml` with a `qory` key is refused by a 0.2.x binary as an unknown
  key, exit 2. Upgrade every machine before a delivered stack gains the key.
- A `qory-stack.yaml` at a repository root without an `extending` block is refused. Move
  its target and modules under `harness` in `qory.yaml`; `qory setup repo` writes one.
- `qory version` prints the number without a `v` from every kind of build. A script that
  read the `v` reads `qory version --json` instead.
- The instructions land at `.claude/CLAUDE.md` for Claude Code and at `AGENTS.md` in the
  home. A root `AGENTS.md` exists only for a runtime that reads one; add `any` to
  `target.runtime` to get the link at the root.

### Added

- A `qory` key on `qory-stack.yaml` and on `qory.yaml`: the qory versions the document
  is written for, as comparators such as `>=0.3.0 <0.4.0`. Compose and worktree add check
  every document they read before anything is fetched or written and exit 5 outside the
  range, naming the file, the version and the range. A delivered stack states its minimum
  once, and every repository extending it inherits the range, so upgrading qory on the
  runners changes no consumer's file. A build from source between tags has no version to
  compare; it composes and prints one row saying the range was not checked.
- `qory version --json`: version, commit, dirty, source (`release` for a release build,
  `source` for a `go install` or `go build`), the harness format and the report version.
  The `source` field is how an installer leaves a developer's own build alone.
- The report records the qory that wrote it, `version`, `commit` and `source`, so a
  runner's report and a laptop's can be compared.
- `qory harness compose --check` renders again into a scratch directory, compares it
  with the home, and exits 6 naming each path that differs, or saying nothing is
  composed. It writes nothing. The merged outputs are generated copies, so an edit to a
  module's instruction section leaves the composed one stale until the next compose, and
  a CI job runs `--check` as the gate for that.
- `only` on a stack module, and `exclude` extended to the same keys: the seven entry
  kinds with names, `instructions: true`, and `settings` and `env` as `true` or a list.
  Under `exclude` a named thing is left out. Under `only` a named thing is composed,
  together with what it requires from the module, and nothing else:

  ```yaml
  - name: ops
    only: {skills: [deploy]}          # deploy, the command it runs, the agent it calls
    exclude: {agents: [reviewer]}     # that agent comes from another module instead
  ```

  The report lists every entry and part a block left out, and marks each pulled entry
  with the entry that required it. Two modules contributing one entry stays a collision.
- `requires` in the module manifest, one statement per entry, declares what an entry
  needs composed beside it:

  ```yaml
  requires:
    - skill: deploy
      commands: [ship]
      agents: [reviewer]
  ```

  A composed entry whose requirement is not composed is refused: `module ops: skill
  deploy requires command ship, which module ops leaves out`, or `which no module ships`.
- Exit statuses 5, a document's `qory` key excludes the running qory, and 6, `--check`
  found the home behind the stack and modules.
- A README section on the composed tree and the tools that have to follow its links:
  `find -L`, BSD versus GNU `grep -R`, `rg --follow`.
- This changelog, and a release workflow that takes a release's section from it as the
  release body and refuses a tag whose version has no section.

### Changed

- A root `qory-stack.yaml` with no `extending` block is refused with the place a
  repository's own stack goes. The same file through `-f`, in an ancestor directory, or
  through `extends` reads as before. The hello example is a `qory.yaml` now, with the
  worktree section shown as a comment.
- `qory version` prints `0.3.0`, without the `v`, from a release and from a source build
  alike.

### Fixed

- A `+dirty` suffix on a version Go stamped into a source build was not recognised and
  printed as a version.

## [0.2.1] - 2026-09-11

### Fixed

- Every worktree of a repository shares one `.git/info/exclude`, and `qory harness
  remove` in one worktree dropped the lines every sibling's composed tree still relied
  on, which showed up as hundreds of untracked files. A line is kept while another
  worktree still has something at the path it names.

## [0.2.0] - 2026-09-10

### Added

- `qory.yaml`: the repository's own document at its root, holding its stack under
  `harness` or the stack it extends, and the machine's in `~/.config/qory` and the
  checkout's ancestors.
- A closed base stack with an `extending` block that says what an appended module may
  add; `extends` in a checkout's `qory.yaml`.
- Git sources pinned by commit in the report, the `mcp` kind with one server per file,
  real runtime directories with one link per entry, `--force`, module links, exported
  variables, and `extensions` carried into the report verbatim.
- `qory worktree add`, `remove` and `list`, and `qory setup shell`.
- `qory harness init` makes a git repository where there is none.

### Changed

- A layer is a module and a profile is a stack. The compose document is `qory-stack.yaml`
  for a delivered stack and the `harness` section of `qory.yaml` for a repository's own.

## [0.1.0] - 2026-09-10

The first release: a stack of modules composed into one tree, linked into the checkout
and kept out of git, with a report naming the module of every entry and a refusal when
two modules provide the same one.

[Unreleased]: https://github.com/qoryai/qory/compare/v0.11.0...HEAD
[0.11.0]: https://github.com/qoryai/qory/compare/v0.10.0...v0.11.0
[0.10.0]: https://github.com/qoryai/qory/compare/v0.9.0...v0.10.0
[0.9.0]: https://github.com/qoryai/qory/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/qoryai/qory/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/qoryai/qory/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/qoryai/qory/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/qoryai/qory/compare/v0.4.4...v0.5.0
[0.4.4]: https://github.com/qoryai/qory/compare/v0.4.3...v0.4.4
[0.4.3]: https://github.com/qoryai/qory/compare/v0.4.2...v0.4.3
[0.4.2]: https://github.com/qoryai/qory/compare/v0.4.1...v0.4.2
[0.4.1]: https://github.com/qoryai/qory/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/qoryai/qory/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/qoryai/qory/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/qoryai/qory/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/qoryai/qory/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/qoryai/qory/releases/tag/v0.1.0
