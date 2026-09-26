# 🐝 qory

A harness is what your coding agent reads before it works: instructions, skills, agents,
commands and settings. `qory` builds the harness of a repository from modules. Each module
is one piece of harness: the piece every repository of your team shares, the piece for
apps built on your framework, the piece this repository alone needs. The repository
commits the list of modules, not a copy of their files.

## The problem

Your coding agent reads its harness from the repository. You have many repositories, and
more than one agent. Claude Code reads `.claude`, Codex reads `.codex`, Gemini CLI reads
`.gemini`. So every repository holds one copy per tool. When the shared part improves, the
copies drift, and nothing tells you which version a checkout runs with.

<p align="center">
  <img src="docs/assets/slogan.png" alt="Don't worry, use Qory" width="720">
</p>

## What qory does

A stack lists modules in order: the harness your team shares, the harness for apps built
on the framework, the harness this repository alone needs. `qory` composes them into one
tree and writes that tree the way each tool reads it. The tree is linked into the checkout
and kept out of git. A report says which module every entry came from.

A module is written once and serves Claude Code, Codex CLI, Gemini CLI, OpenCode, Cursor,
GitHub Copilot CLI, Amp, Goose, and every tool that reads `AGENTS.md` and `.agents/skills`.
A checkout can serve two tools at once, and both read the same harness.

The model is Docker's: a module is an image, the stack is the Compose file, and the
composed tree is what runs. One rule differs on purpose. When two modules provide the same
entry, `qory` refuses to compose until the stack says which one to keep. Nothing wins by
coming last.

## Three steps

1. Write `qory.yaml` in the repository, or let `qory setup repo` write it:

   ```yaml
   apiVersion: qory.dev/v1alpha1
   harness:
     target:
       runtime: claude          # or both at once: [claude, codex]
       model: opus
     modules:
       - name: core             # what every repository of yours gets, pinned to a tag
         source: {git: https://github.com/acme/harness, ref: v2.4.0, path: core}
       - name: nextjs           # the harness for Next.js apps
         source: {path: ../harness/nextjs}
       - name: app              # this repository's own, committed with it
         source: {path: ./harness}
   ```

   Every module has a `qory-module.yaml` that names it. A repository can also take a
   stack someone else delivers, a `qory-stack.yaml`: it names that stack under `extends`,
   says what it composes it for under `target`, and adds its own modules. The delivered
   modules cannot be changed. A runner that holds the stack tree names the base with
   `qory harness compose -f <stack>` instead, and the repository's file then names no
   version, ref or URL of it. The harness repository states the qory it needs once, in
   its own `qory.yaml`, `qory: ">=0.5.0"`, and every stack it delivers is held to it.

   ```yaml
   apiVersion: qory.dev/v1alpha1
   harness:
     extends: {git: https://github.com/acme/harness, ref: v2.4.0, stack: nextjs}
     target: {runtime: claude, model: opus}
     modules:
       - name: marketing          # the harness repository exports it too
         source: {git: https://github.com/acme/harness, ref: v2.4.0, module: marketing}
       - name: app
         source: {path: ./harness}
   ```

   `stack` and `module` name what the harness repository publishes in the `exports`
   section of its own `qory.yaml`, so nobody outside it depends on its directories:

   ```yaml
   exports:
     dir: ./harness               # where stacks/ and modules/ are; default: the root
     stacks: [nextjs]
     modules: [core, nextjs, marketing]
   ```

2. Compose it:

   ```sh
   qory harness compose
   ```

3. Start your agent in the checkout. It reads the composed tree through the files it
   already looks for.

## Install

With Homebrew:

```sh
brew install qoryai/tap/qory
```

With the install script, which puts the release binary in `~/.local/bin`:

```sh
curl -fsSL https://raw.githubusercontent.com/qoryai/qory/main/install.sh | sh
```

From source, with Go 1.27 or later:

```sh
go install github.com/qoryai/qory@latest
```

To update, run `qory update`: it installs the newest release the way this qory was
installed, with `brew`, with `go install`, or by replacing the release binary in place
after checking it against the release's checksums. Every command looks for a newer release
when it runs on a terminal, asking GitHub at most once an hour, and says so after its
output when the newest release is ahead of its version. A build from `main` between
releases is told only when it is behind a release. Set `QORY_NO_UPDATE_CHECK=1` to turn
that off; it is off when `CI` is set.

## Try it

```sh
mkdir hello && cd hello
qory setup example       # writes a stack and two modules
qory harness compose     # or: qory hc; for Codex: qory hc --runtime codex
claude                   # type /hello
qory harness remove      # or: qory hr
```

Both modules ship a greet skill. The README that `qory setup example` writes says which
line to delete to watch `qory` refuse the collision. For a real repository, run
`qory setup repo` instead.

### The same example, observed, then behind a wall

`qory run` starts the agent on the composed harness inside the session runner: every
connection it makes goes through a proxy on your machine, and the session is recorded.

```sh
qory run -- -p "/hello"         # one headless turn, observed
cat .qory/runs/*/events.jsonl   # what it reached, what it said, how it ended
```

Without a wall the proxy sees only programs that honour it. A **wall** starts the agent
in a container whose one route out is that proxy. It needs the `docker` command and an
image of yours that holds the agent; `qory` builds none. A minimal one for Claude Code:

```dockerfile
FROM node:22-slim
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates \
 && rm -rf /var/lib/apt/lists/* && npm install -g @anthropic-ai/claude-code
# The container runs as your user, who has no home in the image.
ENV HOME=/tmp
```

```sh
docker build -t hello-agent:1 .
export CLAUDE_CODE_OAUTH_TOKEN=...       # from `claude setup-token`; or ANTHROPIC_API_KEY
qory run --wall docker --image hello-agent:1 --env CLAUDE_CODE_OAUTH_TOKEN -- -p "/hello"
```

On a Mac, name the Linux build of the same `qory` release as `wall.helper` first; see
[Running a session](#running-a-session). That run hands the token to the container. The
last step keeps it outside: say in `~/.config/qory/runner.yaml` what this machine has
and what the agent may reach,

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
  image: hello-agent:1
```

and give the run a policy that selects the credential, kept outside the checkout:

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

The agent greets you as before. Inside the container `CLAUDE_CODE_OAUTH_TOKEN` is a
placeholder; the proxy sets the real token on each request to `api.anthropic.com`, which
the record lists with the credential's name and never its value, and everything else the
agent tries to reach is denied and recorded.

## Worktrees

One branch per worktree, beside the main checkout. `qory` prepares the worktree the way
the repository's `qory.yaml` says and composes the harness into it:

```yaml
# qory.yaml, committed, beside the harness section
apiVersion: qory.dev/v1alpha1
worktree:
  base: main                     # the base branch, read on the remote; default: the remote's HEAD
  link: [.env, .env.local]       # linked from the main checkout into the worktree
  run:
    add: [pnpm install]          # run in the new worktree
```

```sh
qory worktree add feature        # ../wt-feature on branch feature, pushing to origin/feature
qory worktree add feature --base v1.2.0   # a branch that exists is moved onto the base, after a question
qory worktree add feature --offline       # without the fetch every add starts with
qory worktree add --branch feature   # attach to the remote's feature: fetched, tracked, refused when the remote lacks it
qory worktree add --pr 7             # attach to pull request 7: its branch, found among the remote's refs
qory worktree add review --pr 7      # the same, in ../wt-review
qory worktree remove             # the worktree you stand in, and its branch
qory setup shell                 # make your shell cd into a new worktree, and back on remove
```

Every add fetches the remote first, so a new branch starts at the remote's tip and a
branch pushed from another machine is tracked instead of cut anew; a fetch that fails
stops the add, and `--offline` goes on with the refs already there. The branch goes with
the worktree on remove: quietly when every commit of it is on the remote, in the main
checkout or on the base it was cut from, and after a question otherwise, with push,
keep, delete anyway and stop as the answers. `--keep-branch` keeps it, and
`worktree.branch: keep` makes that the default. `-v` on either verb prints each git
command as it runs, and what the configured commands print.

`--pr` asks no hosting API: the pull request's head is one of the refs the remote
publishes, `refs/pull/<n>/head` on GitHub and Forgejo, `refs/merge-requests/<n>/head` on
GitLab, `refs/pull-requests/<n>/from` on Bitbucket Server. A host with another layout
names it as `worktree.pr: refs/.../{n}/...` in the repository's `qory.yaml`; one that
publishes none, Bitbucket Cloud, takes `--branch` with the pull request's branch.

Where a worktree goes and what it is called is your choice, not the repository's:
`worktree.dir`, `worktree.name` and `worktree.branch` in your own `qory.yaml`, which
`qory setup machine` writes. A file the repository must not name goes there too:

```yaml
# ~/.config/qory/qory.yaml
worktree:
  link: [{from: ~/secrets/app.env, to: .env}]   # this machine's path, linked as .env
```

## Commands

```sh
qory setup repo          # write the repository's qory.yaml: its stack, module and worktree settings
qory setup example       # write the hello example into the current directory
qory setup machine       # write your qory.yaml in ~/.config/qory: how qory runs here
qory setup shell         # completions, and a shell that follows worktree add and remove
qory harness compose     # compose the stack into the checkout you stand in   (qory hc)
qory harness inspect     # the report: every entry and the module it came from (qory hi)
qory harness remove      # remove the composed tree and its links               (qory hr)
qory harness launch      # the command that starts a runtime on the tree          (qory hl)
qory run                 # start a runtime on the tree, observed and recorded
qory worktree add        # add a worktree for a branch and prepare it            (qory wa)
qory worktree remove     # remove a worktree and its branch                      (qory wr)
qory worktree list       # every worktree with its branch                        (qory wl)
qory config              # every setting, its value and the file it came from
qory update              # install the newest release; --check only says whether one exists
```

Flags worth knowing on `compose`:

```
-f <stack>             compose this stack; as the base of the repository's document when that extends one
--dry-run              print the report and write nothing
--runtime claude,codex render for these runtimes instead of the document's
--model opus           write this model instead of the document's
--force                replace a tracked, unmodified file where a link goes
--update               fetch every git source again
--check                exit 6 when the composed tree is behind the stack and modules
--home <dir>           compose under a directory outside the checkout, and write nothing into it
--no-links             keep the checkout untouched; the tree goes under .qory
```

Two `qory.yaml` files are read. Every setting has a default, so both are optional. The
repository's file is committed and holds what the repository decides: its stack, and what
a worktree needs; it may be named `harness.yaml` instead, for a file that says nothing of
the tool that reads it. Your file, in `~/.config/qory`, holds how `qory` runs on this machine
for every repository: the runtime to compose for, where worktrees go, the git timeout.
The repository's file is read on top of yours. `qory config` shows every setting and the
file it came from. The reference, one page per command, is under
[docs/commands](docs/commands/qory.md).

## What you get

- The files your tool reads, linked to a composed tree under `.qory` and kept out of git.
  Your own `settings.local.json` stays yours.
- Settings merged from every module, in the tool's own format. Permission lists and hooks
  join. A value set twice to different things is a collision, never a silent override.
- MCP servers, one file each in a module, written where every tool reads them.
- Modules from a directory beside the repository, or from a git repository at a tag,
  pinned by commit in the report. A repository that publishes stacks and modules lists
  them under `exports`, and a consumer names them, never their directories.
- One instruction file, joined from the modules in order, under the name each tool wants.
- A report that names the module of every entry. When two modules provide the same entry,
  a refusal with the lines that resolve it.
- Part of a module, when that is all you want. `exclude` leaves entries, the instruction
  section, settings fragments or variables out. `only` takes the named things, plus what
  they need as the module's manifest declares it, and nothing else: `only: {skills:
  [deploy]}` is the deploy skill, the command it runs and the agent it calls, from a
  module full of other things. A required entry you take from another module instead is
  one `exclude` line beside the `only`.

## The composed tree

The home, `.qory/harness`, holds one directory per runtime. Most of what is in it is a
symlink to a file in a module: every skill, agent, command and hook. Beside the links
are the generated copies: `AGENTS.md` at the root of the home, the instruction file each
runtime wants, its settings and its MCP file, each joined from the modules' fragments.

Editing a module's file is live through the link. Editing a module's instruction section
or a settings fragment is not: the merged copy is generated, and it stays as it was until
the next compose. `qory harness compose --check` compares the home with what the stack
and modules say now and exits 6 when a merged copy is behind, which is the check a CI
job runs.

What the checkout holds is links too. `.claude` is a real directory with one symlink per
entry, so Claude Code's own `settings.local.json` stays beside them; `.mcp.json` and a
root `AGENTS.md` are symlinks. A tool that walks the tree has to follow them:

- `find` needs `-L`.
- `grep -R` given a symlink as its operand without a trailing slash reads nothing with
  BSD grep on macOS and follows the link with GNU grep on Linux, and neither reports an
  error. Give it the directory with the slash, `grep -R pattern .claude/`, or use `-R -L`.
- `rg` needs `--follow`.

`git status` does not show the tree. Every path `qory` writes is listed in
`.git/info/exclude`, which every worktree of a repository shares.


### A home outside the checkout

The tree can live outside the checkout, owned by whatever composes it, a workflow or a
launcher, and the checkout then carries nothing of it: no link, no `.qory`, no exclude
line, so a tracked `harness/` directory is no collision and a pull is an ordinary pull.
`--home <dir>`, or `harness.home` in your own `qory.yaml`, names the directory, and
every checkout and worktree gets its own home under it. Each tool takes such a home
from its own command line or environment, and `qory harness launch` prints the command:

```sh
qory harness compose --home ~/.cache/qory/homes
eval "$(qory harness launch --runtime claude --home ~/.cache/qory/homes)"
```

For Claude Code that is `--plugin-dir` for a plugin the compose renders, `--settings`
for the permissions, hooks, environment and model, `--mcp-config` for the servers,
`--append-system-prompt-file` for the instructions, and `--setting-sources user` so no
`.claude` of the checkout or of a directory above it is read. Cursor takes the same
plugin, Codex takes the tree as its `CODEX_HOME`, OpenCode as its `OPENCODE_CONFIG_DIR`,
Copilot the skills and agents through `--add-dir`, Amp and Gemini their settings file.
Every line is the tool's own template, and `harness.launch.<runtime>` in your
`qory.yaml` changes the command, the arguments or the variables when a tool's flags
move. `--json` prints the same as one object. The contract says what each tool takes
from outside and what it still reads from the checkout.

## Running a session

`qory run` starts the runtime on the composed tree inside the session runner of
[qoryai/runner](https://github.com/qoryai/runner): the same launch as `qory harness launch`,
with every connection the runtime makes going through a proxy on your machine and
recorded, and the session written as events beside its output.

```sh
qory run                              # the composed runtime, at your terminal
qory run claude -- -p "Reply pong"    # one headless turn; arguments after -- go to the runtime
```

The record is `.qory/runs/<id>/`: `events.jsonl`, one event per line, and `output.log`,
the session's bytes. One optional file, `~/.config/qory/runner.yaml`, says what the
runner does on this machine. It lives beside your `qory.yaml` and nowhere else, so a
repository cannot set it:

```yaml
# ~/.config/qory/runner.yaml
apiVersion: qory.dev/v1alpha1
egress:                  # what the runtime may reach; enforce denies the rest
  mode: enforce          # or observe: record everything, deny only what deny names
  allow: [api.anthropic.com, "*.github.com"]
  deny: [gist.github.com]                # denied in either mode, whatever allow says
server:                  # the server every run reports to; optional
  url: https://qory.example             # a scheme and a host, nothing after
  access_key: ak_f1xt0re000000000       # the key the server issued this machine
  secret: fixture-secret-not-a-real-one # or QORY_SERVER_SECRET in the environment
wall:                    # start the runtime in a container; optional
  adapter: docker
  image: example.com/agent:1            # yours: the runtime and your toolchain
  env: [ANTHROPIC_API_KEY]              # names; nothing else of your environment goes in
  mounts: [/srv/odoo:ro]                # what else of your machine it sees; optional
  memory: 14g                           # and cpus, pids_limit, shm_size; optional
run:                     # optional
  timeout: 5h30m         # stop a runtime that still runs then
  stop_signal: SIGINT    # what asks it to leave; the runtime's descriptor's, else SIGTERM
  stop_grace: 30s        # between that signal and SIGKILL; 10s
```

`qory run` runs whichever runtime the harness is composed for, and none of the above is
particular to one. What the runner knows of a runtime is a descriptor, a file of data in
the [runner contract](https://github.com/qoryai/runner/blob/main/contracts/runner/v1/README.md#the-runtime)'s
format: how its hooks are installed, what its output means as events, which signal asks
it to leave. The runner ships Claude Code's. `~/.config/qory/runtimes/<runtime>.yaml`
describes another, or replaces the one shipped; and a runtime nothing describes runs all
the same, the run, its log and its egress recorded and the session's own events not.

A module declares the hosts it reaches under `egress` in its manifest, and the compose
unions them into the report. The runner reports them as `harness_hosts` in
`dev.qory.run.policy_applied`, for a receiver to compare with the policy; they decide
nothing. The policy alone says what the runtime reaches.

With a server configured the runner starts by fetching the server's configuration,
signed with the access key and the secret, and does not start unless the server
answers: the events go where the configuration says, and when it names a run
configuration, that is the run's policy, fetched for the checkout's forge and repository
and reloaded when the server says it changed. `--local` runs with the files alone and
the machine's policy; the server is not contacted. A denied connection is recorded and
the session goes on; only a time limit you set ends a session: `--timeout 5h30m`, or
`run.timeout`, stops the runtime then, `dev.qory.run.exited` says the limit was the
reason, and `qory run` exits 124.

A system that starts runs of its own names them and brings each its policy:

```sh
qory run --run-id "$uuid" --label run_key=1234 --label issue=77 \
  --policy /etc/factory/shop-policy.yaml --timeout 5h30m --stop-grace 30s -- -p "$prompt"
```

`--run-id` is the id the caller already holds, a UUID in lower case, and the labels go
into `dev.qory.run.started` and onto the run configuration request, where a server ties
the run to what it knows and chooses its policy. Two labels
come from the checkout's origin remote unless `--label` names them: `forge`, the
remote's host, and `repository`, its path without the leading slash and `.git`, so
`git@github.com:acme/shop.git` is `github.com` and `acme/shop`; a checkout with no
remote, or one on this machine, carries neither. `--policy` is one run's own policy, in
the runner contract's format, kept outside the checkout, for a machine without a server.
It narrows only: under an `egress` section in mode `enforce` the run reaches the file's
hosts the section covers; with no section, or one in mode `observe`, the file stands as
it is; the `deny` lists of both hold either way. With a server configured the server's
run configuration is the policy and `--policy` is refused; `--local` keeps it. The
server's secret stays the runner's:
`QORY_SERVER_SECRET` is taken out of the session's environment.

### Credentials the agent never holds

Behind a wall a run needs no credential inside the container. `runner.yaml` defines what
the machine has, and a run's policy selects among it by name:

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
# the run's policy, given with --policy
version: 1
egress:
  mode: enforce
  allow: [api.anthropic.com, git.example.com, api.git.example.com]
credentials:
  - name: model
  - {name: product, argument: acme/shop}
```

The runner keeps each token outside the container, and its proxy sets it on the requests
to the hosts it is for. The container gets a placeholder where a program wants a
credential set, and never the token. An **adapter** is a program of yours that knows one
kind of host, a source code host say: it runs outside the container and prints the
token, its expiry, and the hosts, the scheme and the paths the token is for, so `qory`
names no host of its own. Its paths are the run's whole reach on those hosts: a
credential for `acme/shop` opens no other organization's repository, and a path the
adapter leaves out, the host's GraphQL endpoint say, is not reached. `egress.paths` in a
policy holds a host to paths the same way with no credential. The
[runner's contract](https://github.com/qoryai/runner/tree/main/contracts/runner/v1#credentials)
has the adapter's document and the rules.

An **integration** is an adapter published apart that describes itself: Qory's own
`qory-<name>`, such as `qory-github` from
[qoryai/integrations](https://github.com/qoryai/integrations), or a program of yours
under a name of your own. Declare it and `qory` writes the definition:

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

Before a run `qory` runs `<program> describe`. The program prints its description, the
settings it takes as a JSON Schema and the roles it plays, and `qory` checks the
settings against it. The credential role defines the credential named by the key, as if
the file said

```yaml
credentials:
  github:
    adapter: [/usr/local/bin/qory-github, credential, --settings, '{"app_id":123456,"private_key_file":"/etc/qory/github-app.pem","permissions":{"contents":"write","pull_requests":"write"}}', --, "${argument}"]
    argument: '[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}(,[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100})*'
    hosts: [github.com, api.github.com]
```

and a policy selects it by the key, `{name: github, argument: acme/shop}`. `program`
names the program by its absolute path or by a name on the `PATH`; without it the
program is `qory-<key>` on the `PATH`. On a machine whose `PATH` is not its owner's
alone, name each program by its absolute path with `program`. `qory` runs a program the
run cannot write: one outside the checkout and outside every mount the wall gives the
container read-write, `wall.mounts` and `--mount` without `:ro`, judged by where its
links lead: a link on a `PATH` entry the checkout controls that resolves outside the
checkout is judged by where it resolves. The rule holds for each run as it starts, so a
program written into a directory while that directory was mounted read-write is judged
by where it is on every later run; keep a program's directory out of the read-write
mounts. The resolved file, every directory above it up to `/`, and every directory above
each link on the way, the `PATH` directory among them, keep one rule: root or the user
running `qory` owns each, and each link on the way; other users may write none; and a
group may write one when the group is root's, gid 0, `wheel` or `admin`, or the owner's
primary group, named as the owner is. A directory root owns with the sticky bit set,
`/tmp` or `/nix/store`, keeps the rule. A default Homebrew install and a `~/go/bin` of a
user's private group keep it. `qory run` names the program it found on a line of its
own. The settings go on the adapter's command line, which other processes of the machine
can read, so a secret is refused there: the description marks it, a property of the
settings themselves, and the settings give the file that holds it, `private_key_file`
and never `private_key`. The settings are compact JSON as Go's `encoding/json` writes
it, `<`, `>`, `&`, U+2028 and U+2029 escaped, and every `$` in them is written `\u0024`,
as the integration contract defines for a declaration, so the adapter's `${argument}` is
the policy's argument alone. A name the `credentials`
section defines itself is the section's: `qory run` and `qory config` say so on a line
of their own, `qory config` describes the integration and lists it as shadowed, and a
run leaves it undescribed. A run whose policy is on this machine describes the
integrations its `credentials` select; a run whose policy the server supplies describes
every one. A program that does not answer within 10 seconds, a description the
[integration
contract](https://github.com/qoryai/integrations/tree/main/contracts/integration/v1)
refuses, settings the description refuses, and an integration that plays no role `qory`
knows stop the run before it starts. `qory config` describes every integration and lists
what each defines.

For those hosts, and no other, the proxy ends the container's TLS itself, with an
authority made for the run whose key never leaves the runner. The container is given
one bundle to trust, its image's own authorities and the run's certificate, through
`SSL_CERT_FILE`, `GIT_SSL_CAINFO`, `NODE_EXTRA_CA_CERTS`, `REQUESTS_CA_BUNDLE` and
`CURL_CA_BUNDLE`, or the variables `wall.ca_env` names. The record lists the terminated
hosts and, for each request to one, the method, the path and the credential's name.

A job ends with `qory run resend <run-id>`, whatever happened before it. It sends the
server what it has not accepted of the run's record, and nothing twice. After a runner
that died it first closes the record, `dev.qory.run.exited` with `reason: runner_lost`,
and removes the containers and networks the run's wall left. It refuses a run that is
still going, and exits 1 when the server still has not taken everything after
`--wait`, two minutes unless named. The formats are in the runner's
[contract](https://github.com/qoryai/runner/tree/main/contracts/runner/v1).

The proxy sees only programs that honour it. A **wall** makes the rest fail: with a
`wall` section, or `--wall docker --image <image>` for one run, the runtime starts in a
container on a network with no route out, and reaches the proxy, and nothing else,
through a relay. The container sees the checkout and the composed home and nothing else
of your machine; the runner, the policy, the record and the server's secret stay
outside. It needs the `docker` command and an engine behind it, and an image of yours
that holds the runtime; qory builds none. What to know:

- The model credential goes in by name, `wall.env` or `--env`, and is then the
  agent's. A subscription login kept in a Mac's Keychain does not reach a container;
  use an API key, or a token from `claude setup-token`.
- Inside the container the relay and the hook forwarder are qory's own Linux build. On
  Linux that is the binary you run. On a Mac, download the Linux archive of the same
  release for your engine's architecture and name the binary as `wall.helper`.
- The container sees the checkout it was started in and no other directory, unless
  `--mount <path>[:ro]` or `wall.mounts` shows it one, at its own path: a sibling
  checkout the session reads, say. A socket is never mounted. In a git worktree the
  repository's data lives in the main checkout, outside it, so git inside the container
  works there only with that directory mounted; a clone works as it is.
- `--cpus`, `--memory`, `--pids-limit` and `--shm-size`, or the keys of those names
  under `wall`, limit what the container uses. A headless browser wants `--shm-size 2g`:
  an engine's default `/dev/shm` is 64 MB.
- Behind a wall the proxy reaches your own machine only for a host `egress.allow`
  names itself, in either mode, and never the cloud metadata address. For a local model
  endpoint or MCP server, list your machine's host name, and point the harness at that
  name: `localhost` inside the container is the container.
- With the engine in a virtual machine, as on a Mac, the runtime's hooks do not reach
  the runner, so a walled run there has no hook events; the log, the egress record and
  the structured output are there. On a Linux host they cross.

## The format

The stack, the module manifest, `qory.yaml`, the composition rules and the runtimes are
specified in [contracts/harness/v1](contracts/harness/v1/README.md), with JSON schemas and
the fixtures the test suite runs.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Contributions are made under the agreement in
[CLA.md](CLA.md).

## Licence

Apache License 2.0. See `LICENSE`. Qory™ is a trademark of 8wonders GmbH;
`TRADEMARKS.md` says what you may do with the name.
