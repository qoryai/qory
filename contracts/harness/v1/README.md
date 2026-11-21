# Harness compose format, v1alpha1

What `qory harness compose` reads and what it writes.

## Thirteen files

| File | Lives in | Defines |
|---|---|---|
| `qory-stack.yaml` | a directory that `extends` or `-f` selects; an ancestor directory covering several repositories; the root of the harness repository that delivers it | a stack delivered to be extended: the ordered modules, the excludes, what a checkout extending it may add and the runtimes and models it is written for |
| `qory-module.yaml` | the root of a module | the module: its name, its variants per runtime, the variables it exports |
| `process.yaml` | optional; the folder root of a module that `exports.modules` lists, which makes the module a factory | a process template: its name, summary and description, the workspace type, the exported stack its workflows run on, its `qory.process/v1` process, and optionally the artifact types its workspaces use and the submit step of a run. Every command that reads the configuration checks it; the compose does not read it (§Exports) |
| a role contract | optional; a file of a module that declares an agents role, at the path that `roles.agents.<role>.contract` in the module's `qory-module.yaml` is set to | the parts an agent needs to fill the role: level-2 headings, front-matter keys, tools, a tier, and the form of its report (§The role contract). The compose checks every agent that fills the role against it; the harness's checks read the report's form |
| `qory.yaml`, or `harness.yaml` | the repository root, committed; the user's configuration directory and the checkout's ancestor directories, for the machine | the repository's document and the machine's: under `harness`, this repository's stack, its target and modules, or the stack it extends, the target it composes it for, the modules it appends and the workflow hooks it adds, and the runtime and model this machine composes for; under `worktree`, what a worktree of the repository needs and where the machine puts one; under `exports`, the stacks and modules this repository publishes for others, by name (§Exports); `git` and `env` (§The configuration) |
| a branch document | the file of a version of a structured artifact type `branch` | a work item's branch and the commit in each repository (§The branch format). No compose reads it |
| an acceptance criteria document | the file of a version of a structured artifact type for a work item's acceptance criteria | a summary of what the work item must achieve and the criteria that say when it is achieved, each with its check (§The acceptance criteria format). No compose reads it |
| a verification document | the file of a version of a structured artifact type for a work item's verification | the result of checking the criteria against one version of the work (§The verification format). No compose reads it |
| a questions document | the file of a version of the artifact type `questions` | a work item's questions, their answers and the decisions of its runs (§The questions format). No compose reads it |
| a review comments document | the file of a version of the artifact type `review-comments` | a work item's review comments on its artifact versions and the replies to them (§The review comments format). No compose reads it |
| a proposal sources document | the file `sources.json` of a version of the artifact type `proposal` | every source a run used to write the proposal, with the reason for each (§The proposal sources format). No compose reads it |
| a verdict document | the file of a version of the artifact type `verdict`, which `qory run verdict` stores | a run's verdict, and its points: for `escalate` the points a person acts on, and for `changes-requested` what the next round fixes (§The verdict format). No compose reads it |
| a review report | `report.json`, which one reviewer of a review round writes | the reviewer's findings on the reviewed commit, each blocking or non-blocking (§The review report format). qory's review round reads it; no compose reads it |

Every module has a `qory-module.yaml`; it defines the module's name, and a directory
without one is not a module. `qory.yaml` is optional: every setting has a default. It has
a second name, `harness.yaml`, read exactly as `qory.yaml` at every level, for a
repository whose committed file does not mention the tool that reads it. Each file
name is fixed, so a repository with several stacks has one directory per stack, each
with its `qory-stack.yaml`. A directory contains `qory-stack.yaml` or a `qory.yaml` or
`harness.yaml` whose `harness` section defines modules or a stack to extend, never both,
and one of the two names, never both; the file name defines which document a file
contains, and no document has a kind.

A `qory-stack.yaml`, a `qory-module.yaml` and a `process.yaml` contain `apiVersion`;
they are delivered files. A `qory.yaml` or `harness.yaml` may leave it out, and is then read as the newest
format this qory reads, `qory.dev/v1alpha1`. A reader refuses a version it does not read
and lists the versions it does. `v1alpha1` marks the format as subject to change.

The group is `qory.dev`, the domain of the open format and its documentation. A
retired version of the same format, `qory.ai/v1alpha1`, is read as the current one, and a
compose prints a `retired` row with the file and the line to write.

## Discovery

What to compose, in order: the `-f <file>` flag; `qory-stack.yaml` in the checkout root,
or the checkout's `qory.yaml` or `harness.yaml` when its `harness` section contains a
document, modules, a stack to extend or extensions; the nearest of either in an ancestor
directory that the current user owns. An ancestor's file owned by another user is not
read. A `qory-stack.yaml` found in the checkout root must declare an `extending` block:
it is a stack delivered to be extended, and one nothing can extend is a repository's
stack in the wrong file, refused with the place it goes. The same file selected with `-f`,
in an ancestor directory, or reached through `extends` is read as it is. When nothing is
found, the message lists the root and every file it looked for.

`-f` also takes a stack reference, `<https URL>#<ref>:<folder>`, such as
`https://github.com/example-org/harness#v1.2.0:stacks/web`: a folder of a git repository
at a ref, which contains a `qory-stack.yaml`. A value with `://` is a reference; any other
value is a file. The URL is https with a host, and the ref and the folder are required.
The folder is a path from the repository's root, and `.` is the root. An absolute folder,
a folder with a `..` segment, a folder the repository does not have at the ref, and a
folder with no `qory-stack.yaml` stop the compose with exit 2. The folder is fetched as a
git module is, and its `qory-stack.yaml` is then the file `-f` selects, under the rules
that follow. Each module that stack lists by a path or by a name alone becomes a git
source of the same URL and ref, with its path inside the repository, and resolves on the
commit the reference resolved to; a module path outside the repository stops the compose
with exit 2. Under a run key, the folder and every git source of the stack are fetched
through the run's gateway, over https only. When the checkout has a document, the `base`
row shows the reference, and the report records the base by the reference and its commit.

`-f` dispatches on the file's name. Selecting a `qory.yaml` or `harness.yaml` composes
that file's document. Selecting a stack, any other file, in a checkout whose root
contains a document, makes the stack that document's base: the compose reads as if the
document had defined the stack's directory under `extends`, the base's modules first and
closed, the document's modules appended, the extensions merged by key, under the rules of
§Extending a stack. What the document itself defines under `extends`, if anything, is
not read and not fetched. The compose prints a row, `base <file>  (set by -f)`, or `base
<file>  (set by -f, in place of extends <source>)` when the document defines one. A
checkout whose root contains no document composes the stack alone. A document that sets
a `target` keeps it: the base has none, and the target is what the document composes
the base for, as under `extends` (§Extending a stack). This is how a runner that has
the stack tree composes a fleet of checkouts: each repository's file contains only what
belongs to the repository, its target, its modules and its extensions, and defines no
version, ref or URL of the publisher's.

The configuration is every `qory.yaml` or `harness.yaml` found, applied in this order,
each overriding the one before it: `$XDG_CONFIG_HOME/qory/qory.yaml`, else
`~/.config/qory/qory.yaml`, each under either name; the files of the checkout's
ancestor directories that the current user owns, the farthest first; the file in the
checkout root. A directory containing both names is refused. A compose flag overrides every
file.

## The stack

```yaml
apiVersion: qory.dev/v1alpha1
name: nextjs-app                 # optional; default: owner/name from the origin remote
description: The web app's harness   # optional; copied into the report
modules:
  - name: core                   # read from modules/core at this repository's root
    exclude:
      skills: [test]             # the nextjs module ships this repository's test skill
      agents: [reviewer]
      instructions: true         # the module's AGENTS.md is left out too
  - source: {git: https://github.com/acme/harness, ref: v2.4.0, path: modules/nextjs}
    variant: claude              # optional; forces a variant of the module
  - name: ops
    only:
      skills: [deploy]           # the deploy skill and what its manifest lists as its requirements; no instructions, settings or variables
    exclude:
      agents: [reviewer]         # optional; one the only brought in, taken from another module instead
  - name: team
    source: {path: ./harness}    # a name and a source: the manifest must declare the name team
    link: harness                # optional; <checkout>/harness -> .qory/harness/modules/team
extending:                       # optional; without it the stack cannot be extended
  kinds: [skills, commands]      # what a module of an extending checkout may ship
  instructions: true
  files: [claude/rules]
  target:                        # optional; the runtimes and models the stack is written for
    runtime: [claude]
    model: [opus, sonnet]        # listed, the checkout's target selects one of them
  hooks: {per_point: 3, per_workflow: 10, timeout: 10m}   # optional; without it a checkout adds no workflow hook
extensions:                      # optional; copied into the report, not read
  required_check: Harness self-tests
  corpus_roots: [scripts]        # a value is any shape: a scalar, a list, a map of any depth
workflows:                       # optional; the settings of the workflows a dispatched run starts
  example:
    access: write                # optional; read, write-no-push, or write, read when left out
    tier: thinking               # optional; light, balanced, or thinking, none when left out
    plan: change-plan            # optional; an artifact type, only with access write, none when left out
    hook_points:                 # optional; where a checkout extending the stack may add workflow hooks
      after-review-round: {at: end, kinds: [check, review]}
runtimes:                        # optional; the version qory run installs of a runtime, its latest when left out
  codex: {version: 0.167.2}      # an exact version: a digit first, never latest
```

A stack has no `target`: it is delivered to be extended, and the checkout extending it
defines what it composes the stack for. A stack file that sets one is refused, `Remove target
from the stack file.`, and on the next line `A stack is delivered to be extended, and the
checkout extending it sets target under harness, beside extends.`

A repository defines its stack in the `harness` section of its `qory.yaml`, with the same
keys, or extends a stack delivered as a `qory-stack.yaml` and appends more modules
there (§Extending a stack):

```yaml
# qory.yaml, defining a stack
apiVersion: qory.dev/v1alpha1
harness:
  target: {runtime: claude}
  modules:
    - name: app
      source: {path: ./harness}
  workflows:                     # optional; the same keys as in a qory-stack.yaml
    example: {access: write}
```

```yaml
# qory.yaml, extending a stack; apiVersion left out, since the file is the repository's
harness:
  extends: {git: git@git.example.com:acme/harness, ref: v2.4.0, stack: nextjs-15}  # or left out for compose -f <stack>
  target: {runtime: claude, model: opus}   # what this checkout composes the base for
  modules:                       # optional under extends; the target or the extensions alone are a document
    - name: app
    - name: marketing
      source: {git: git@git.example.com:acme/harness, ref: v2.4.0, module: marketing}
  hooks:                         # optional; workflow hooks at the base's hook points (§Workflow hooks)
    - {at: after-review-round, kind: check, run: scripts/check-licences.sh}
  runtimes:                      # optional; this pin of codex wins over the base's
    codex: {version: 0.170.0}
```

The section takes `name`, `description`, `modules`, `bind`, `extensions` and `runtimes` as
a stack does, `workflows` when it defines a stack, `hooks` when it extends one (§Workflow hooks),
and `target`, which a stack does not; never `extending`, since a stack to be extended is
a `qory-stack.yaml`. A section extends a
stack or defines one. A section that extends a stack takes the base's `workflows`; one
that also lists `workflows` is refused with status 2, `Remove workflows or extends.` with `A harness section that extends a stack takes the workflows of its base.`
and `Both keys are in qory.yaml.` on the next two lines. A stack it
defines requires `target`: `runtime`, the program that runs the harness, one of the
runtimes in §Runtimes or a list of them to compose for at once, and `model`, optional,
written into the settings of every targeted runtime that has a project-level place for
it. `extends` selects the base: a stack the repository exports, as `{git: <url>,
ref: <ref>, stack: <name>}` or `{path: <repository>, stack: <name>}` (§Exports), or the
directory containing its `qory-stack.yaml`, as `{path: <dir>}` or `{git: <url>, ref: <ref>,
path: <dir>}`. Beside `extends`, `target` is what the checkout composes the base for,
since the base has none; the machine's `harness.runtime` and `harness.model` stand
over it, and `--runtime` and `--model` over those (§Extending a stack). `extends` may be
left out when `qory harness compose -f <stack>` selects the base (§Discovery); a section
with neither `target.runtime` nor `extends`, composed with no base from `-f`, is refused
with status 2, and the message lists what the section contains instead. A document
extending a stack, under `extends` or from `-f`, with no runtime from the document, the
configuration or `--runtime` is refused with `Set target.runtime beside extends, or set
the runtime in the configuration or with --runtime.`, and on the next line
`target.runtime is required, and the base stack <name>@<pin> defines no target.` A section that extends a stack may list no modules and
contain its target or its extensions alone; a stack lists at least one module.
`apiVersion` is optional in this file (§The configuration). A `harness` section containing
only machine harness settings, `runtime`, `model`, `force` and `update`, is not a
document. The
schema is [config.schema.json](config.schema.json).

| Field | Required | Meaning |
|---|---|---|
| `apiVersion` | yes | `qory.dev/v1alpha1`; a retired version is read as it, with a `retired` row |
| `qory` | no | the qory versions the stack is written for: comparators such as `>=0.3.0 <0.4.0`, every one of which has to match. A compose on a qory outside the range is refused with status 5; a build from source between tags, which has no version, composes and prints that the range was not checked. A stack is checked against its range joined with the `qory` key of the `qory.yaml`, or `harness.yaml`, at the root of the repository it sits in, read when the stack file is read, on every path, so a repository delivering stacks states its floor once; a stack that sets no key is checked against the repository's alone, and a checkout extending the stack is checked against the joined range. A comparator both list is kept once |
| `name` | no | the stack's name in the report. Default: `owner/name` from the origin remote, else the directory name |
| `description` | no | what the stack is for, copied into the report and printed by `qory harness inspect` |
| `modules` | yes | ordered, at least one. Order decides the order of the instruction sections |
| `modules[].name` | one of name and source | the module's name, the one its `qory-module.yaml` declares, one path segment. Alone, it is the address too: `<name>` under the modules directory of the repository the stack is in, `modules/` at its root unless the root's `qory.yaml` sets another under `exports.dir` (§Exports) |
| `modules[].source` | one of name and source | `{path: <dir>}`, relative to the stack file; or `{git: <url>, ref: <tag, branch or commit>}` with an optional `path` to the module's directory inside the repository (§Sources); or a module the repository exports, `{git: <url>, ref: <ref>, module: <name>}` or `{path: <repository>, module: <name>}` (§Exports). Alone, the module's name is its manifest's; with a name, the manifest must declare that name |
| `modules[].exclude` | no | what of this module is left out; everything else is composed, or, beside `only`, everything the `only` takes. Entries by kind, `skills`, `agents`, `commands`, `output-styles`, `hooks`, `mcp`, `files`, each a list of names; the three merged parts, `instructions: true` for the module's `AGENTS.md`, `settings` and `env` as `true` for all of it or a list, of `<runtime>/<file>` fragments and of variable names. Not with `only` |
| `modules[].only` | no | the only things of this module that are composed, with the same keys as `exclude`, plus what the listed entries require from this module (`requires` in its manifest, followed transitively). Everything else is left out: a kind not listed contributes no entry beyond those, a part not listed is left out. `only: {skills: [deploy]}` is the deploy skill, what it needs, and nothing else. An `exclude` beside it lists entries the `only` brought in, to leave them out after all; a requirement left out that way has to come from another module (rule 4). It lists no part, and nothing the `only` lists |
| `modules[].variant` | no | forces one of the module's variants instead of the one with the targeted runtime's name |
| `modules[].link` | no | a name at the checkout root, one path segment, linked to the module's directory in the composed tree, so a permission rule or a script refers to the module's files by a checkout-relative path: `harness/scripts/check.sh`. A hard link (§Rendering), each name used once across the modules |
| `bind` | no | the entry that fills each role a document references (§References), by kind and then by role, to the name of one entry of that kind: `bind: {agents: {coder: rails-coder}}`. The kind is `agents`, `skills` or `commands`. A role's value is one name. A group role (§References) takes a list of agents, `bind: {agents: {reviewers: [code-reviewer, python-reviewer]}}`, and only a group role takes one. A flat key, `agents/coder: rails-coder`, is refused with `Write bind as bind.agents.coder: rails-coder.` and `The keys of bind are kinds, and agents/coder is a kind and a role.` and `bind is in qory.yaml.` on the next two lines. A checkout's `bind` sets a role over the base's `bind` and over the modules' `provides` |
| `extensions` | no | a map of keys the author chooses, each a value of any shape, a scalar, a list or a map of any depth; written into the report as it is and printed by `qory harness inspect`; qory reads nothing in it. A run from Qory Apiary, from a run code or a run key, writes the report's `extensions` into the run's `inputs.json` as `qory.harness_extensions` (docs/run.md, §The run key from a run code) |
| `workflows` | no | the settings of each workflow of the stack, by its name: a lower-case letter, then up to 54 lower-case letters, digits, and hyphens, 55 characters at most, so `workflow-<name>` has at most 64 characters. A run that Qory Apiary dispatches from a run code starts the first of these that the runtime can start: the skill `workflow-<name>`, the command `workflow-<name>`, the skill `<name>`, the command `<name>` (docs/run.md, §The run key from a run code). A plain name works, and when a harness ships both names, the prefixed one runs. The compose fails with status 2 for each workflow that no module of the stack composes as a skill or command under either name, after the modules' `exclude` and `only`: `Add skill or command workflow-<name> to a module of <stack>, or remove workflow <name>.`, then `The base stack <stack> declares the workflow <name> in its qory-stack.yaml.` and `No module of <stack> ships a skill or command workflow-<name> or <name>.` on the next two lines, or, for a stack that extends none, `Add skill or command workflow-<name> to a module of the stack in <file>, or remove workflow <name>.`, then `The stack in <file> declares the workflow <name>.` and `No module of the stack in <file> ships …`, where `<file>` is the `qory-stack.yaml` or the `qory.yaml` that defines the stack. For a stack that the harness document of a run from a run code defines, the line has `the workspace's harness in Qory Apiary` in place of `the stack in <file>`. A module that a checkout adds to the stack it extends does not count, and neither does a checkout's exclude: the compose checks what the stack's modules ship before it applies that exclude. The harness section of a `qory.yaml` that defines a stack lists them with the same keys and checks; a section that extends a stack takes the base's and lists none |
| `workflows.<name>.hook_points` | no | the points of the workflow where a checkout extending the stack may add workflow hooks, by the point's name: `{at: end, kinds: [check, review]}`. `at` is where the point sits in the workflow's session, `end`, `start` or `inside`; `kinds` is one or more of `check`, `review` and `enrich`, each once. A point's name has the form of a workflow's name, and is unique in the stack, since a hook selects a point by its name alone. Only a `qory-stack.yaml` declares points: a `qory.yaml` that defines a stack, which nothing extends, is refused with `Remove hook_points from the workflows in qory.yaml.` (§Workflow hooks) |
| `workflows.<name>.access` | no | the access level the workflow declares for what a dispatched run of it works on: `read`, `write-no-push`, or `write`, and `read` when left out; qory stops with an error on any other value: `Set workflows.<name>.access to a valid level.` with `The levels are read, write-no-push, and write.`, `The value is <value>.` and `workflows.<name> is in <file>.` on the next three lines. A run takes the lower of this access and `access` in the run's documents, in the order `read`, `write-no-push`, `write`; documents with no target have no `access`, and this access applies alone. A dispatched run from a run code sees the checkout read-only when that level is `read`, and on Claude Code its settings deny `Edit`, `Write` and `NotebookEdit`; such a run with no wall, with `wall.setup: inside`, or whose image chosen at the run's open has nested Docker, whichever way it was selected, stops before the agent starts (docs/run.md, §Read workflows). A `write-no-push` run has a writable checkout, no push step, no submit, and no version of the artifact `branch`. At `write` the checkout is writable and qory pushes what the agent left |
| `workflows.<name>.tier` | no | the tier that names the main session's model and effort for a dispatched run of the workflow: `light`, `balanced`, or `thinking`; none when left out. qory stops with an error on any other value: `Set workflows.<name>.tier to a valid tier.` with `The tiers are light, balanced, and thinking.`, `The value is <value>.` and `workflows.<name> is in <file>.` on the next three lines. qory passes nothing new to the run: Qory Apiary resolves the session's model and effort from the tier, and sends them as it sends the session's model. `qory run` does not read it |
| `workflows.<name>.plan` | no | the artifact type whose launch version sets what a dispatched run of the workflow may push, such as `build-plan`: lowercase letters and digits in groups joined by single hyphens, 64 bytes at most; none when left out. Only a workflow with `access: write` names one. qory stops with an error on a value that is no artifact type: `Set workflows.<name>.plan to an artifact type.` with `An artifact type is lowercase letters and digits in groups joined by single hyphens, at most 64 bytes.`, `The value is <value>.` and `workflows.<name> is in <file>.` on the next three lines; and on a `plan` in a workflow whose access is `read`, `write-no-push` or left out: `` Remove `plan` from <name>, or set its access to write. `` with `Only a workflow with access write has a plan, and the access of <name> is <access>.` and `workflows.<name> is in <file>.` on the next two lines. The compose stops when no module of the stack declares the scope check for the type in `scope_checks` (§The module manifest): `Add a scope check for <type> to a module's scope_checks.` with `` You can also remove `plan: <type>` from <name> in the stack. `` on the next line, or both on one line when the first line has at most 98 characters, then `No module of the stack declares a scope check for <type>.` and `workflows.<name> is in <file>.` on the next lines. `qory run` does not read `plan` |
| `runtimes` | no | the version `qory run` installs of each runtime, by the runtime's name, `claude` or `codex`: `runtimes: {codex: {version: 0.167.2}}`. A runtime that no layer pins installs its latest version (docs/run.md, §The runtime's program). The harness section of a checkout's `qory.yaml` takes the same key, beside `extends` or in a stack it defines, and its pin of a runtime wins over the base's; a runtime only the base pins keeps the base's pin. qory stops with `Remove <name> from runtimes.` on another runtime's name, then `qory installs and pins only claude and codex.` and `runtimes is in <file>.` on the next two lines. qory stops with an error on `harness.runtimes` in the user's file, and in an ancestor's file that discovery or `-f` selects to compose; in another ancestor's file, qory leaves the pin unread (§The configuration) |
| `runtimes.<runtime>.version` | yes, in an entry | an exact version, as a string: a digit, then up to 31 letters, digits, `.` or `-`, 32 characters at most, such as `2.1.320` or `0.167.2`; `latest` is not one. qory stops with an error on any other value: `Set runtimes.<runtime>.version to an exact version, or remove runtimes.<runtime>.` with `An exact version starts with a digit, followed by up to 31 letters, digits, . or -. A runtime with no pin installs its latest version.`, `The value is <value>.` and `runtimes.<runtime> is in <file>.` on the next three lines. A version that YAML reads as a number or a date, such as an unquoted `2.1` or `2024-01-01`, stops with `Put runtimes.<runtime>.version in quotes, such as version: "2.1.320".`, then `Without quotes, YAML reads the version as a number or a date, and an exact version is a string.` and the same value and file lines. A version that npm, or the registry's runtime cache, does not have stops the run before its session (docs/run.md, §The runtime's program) |
| `extending` | no | what a module of a checkout extending this stack may ship: `kinds`, `instructions`, `settings`, `files` (§Extending a stack). A base without the block cannot be extended |
| `extending.hooks` | no | lets a checkout extending the stack add workflow hooks at the points of its workflows: `per_point`, the most hooks one point takes, 3 when left out; `per_workflow`, the most the points of one workflow take together, 10 when left out; `timeout`, a duration in whole seconds, 1s or more, the longest a hook may run and the timeout of a hook that sets none, 10 minutes when left out. A count below 1 is refused. Without the block a checkout adds no workflow hook (§Workflow hooks) |
| `extending.target` | no | the runtimes and the models the stack is written for, `runtime` and `model`, each one name or a list, one of the two at least. A checkout's resolved target outside either list fails the compose; listed models make the model required, since a target without one runs whatever the runtime defaults to, which is a model the stack did not list. Left out, every target is accepted, a model left out included (§Extending a stack) |

A name is composed once: two entries resolving to modules of one name fail the compose,
since a name is one directory in the composed tree.

The schema is [stack.schema.json](stack.schema.json).

## Extending a stack

A publisher delivers a harness as a stack; a product repository extends it in the
`harness` section of its `qory.yaml` and adds more modules. The base is closed and the
checkout appends:

- The base's modules come first, in the base's order, with the base's excludes, variants
  and links; a relative source of the base resolves inside the base's repository. The
  checkout cannot reorder, re-source or replace a base module, or change its files.
- The checkout may leave entries out of a base module: its `modules` lists the base
  module by the name the base lists it under, its `name` or the `module` its source
  selects, with `exclude` and no other key, `{name: core, exclude: {skills:
  [workflow-explore-a-ticket]}}`. The entry is no module of the composed stack: its exclude
  applies to the base module after the base's exclude or only block, and the report
  lists each entry it leaves out under `excludes`, by the base module's name. The
  exclude lists entries of the kinds in the base's `extending.kinds` and files under its
  `extending.files` prefixes; an instruction section, a settings fragment, a variable, a
  hook or an MCP server of a base module stays. Anything else on that entry fails with
  status 2, and `qory harness compose` prints the path of the `qory.yaml`, then the
  message, which starts with its fix: `List module <module> with its name and exclude
  only.`, `Add an exclude to module <module>, or remove its entry.`, `Remove <part>
  from the exclude of module <module>.`, or `Remove <kind>/<entry> from the exclude of
  module <module>.`, each with its cause on the next line. An entry the base module does
  not compose fails with status 2 and `Remove <kind>/<entry> from the exclude of module
  <module> in qory.yaml.`, then `The base stack <stack>@<pin> composes no <kind>/<entry>
  from module <manifest name>.` on the next line.
- The target is the document's. A delivered stack has none, and the checkout defines
  what it composes the base for under `target` beside `extends`; the runner's
  `harness.runtime` and `harness.model` override the document's, and `--runtime` and
  `--model` override those. A compose with no runtime from any of the three is refused with
  status 2, `Set target.runtime beside extends, or set the runtime in the configuration
  or with --runtime.`, and on the next line `target.runtime is required, and the base
  stack <name>@<pin> defines no target.`
- The base's `extending.target`, when it has one, is the policy the resolved target is
  checked against, whichever of the three set it: a runtime the base does not list is
  refused, `Set the target runtime to one that the base stack lists.`, with `Runtime <r> is
  not one the base stack <name>@<pin> is written for.` and `Runtimes: <list>.` on the next
  lines; where the base lists models, the target selects one of them, or is refused with
  `Set the target model to one that the base stack lists.`, `The target sets no model,
  and the base stack <name>@<pin> is written for one of these.` and `Models: <list>.`, and
  a model outside the list with the same first line, `Model <m> is not one the base stack
  <name>@<pin> is written for.` and `Models: <list>.` A base without the block
  accepts every target. The check runs under `-f` and `extends` alike, and before
  `--dry-run` stops, since the target is resolved before the report is built.
- The machine harness settings of the checkout's `qory.yaml`, `harness.runtime`,
  `harness.model`, `harness.effort`, `force`, `update`, `home`, `links` and `launch`, and
  its `git` and `env` keys, are not read under `extends`, and the compose reports it;
  the document, `target`, `modules`,
  `bind`, `extensions` and `hooks`, the workflow hooks, is. The runner's configuration, in the user's directory or an
  ancestor the runner's user owns, is the only one. The `worktree` section is the
  repository's and is read.
- The base's `extending` block defines what an appended module may ship: `kinds`, from
  `skills`, `agents`, `commands` and `output-styles`, never `hooks`, the runtime hooks,
  or `mcp`, since the runtime executes those without the agent; `instructions: true` for its `AGENTS.md`,
  appended after the base's; `settings`, the dotted key paths a fragment may set, such as
  `permissions.allow`; `files`, the `<runtime>/<path>` prefixes its files may sit under,
  such as `claude/rules/`, matched by path segment, so `claude/rules` covers
  `claude/rules/a/b.md` and not `claude/rules-private/x.md`, and a prefix that is a file's
  path covers that file only. A module shipping anything else fails the compose and the
  message lists it. An appended module's `provides` (§The module manifest) fills roles of
  the `kinds` alone; a module that provides another kind fails with `Remove
  provides.agents from module typescript, or use a base that allows agents.` and `The
  base's extending.kinds is [skills].` on the next line. A base without the block is closed to extension.
- An entry of an appended module that collides with a base entry fails with status 3.
  The message lists that one entry and nothing else of the base. For a base module the
  base lists by `name` or by an export, `qory harness compose` prints `Module <module>
  belongs to the base stack <name>@<pin>.`, and under `Fix` the entry to write in
  `qory.yaml`, under `harness.modules`, or `Replace the entry of module <module> with:`
  when `qory.yaml` lists the module already. The error's text, which the fixtures contain
  (§Composition rules), has the exclude line for the base module and `write the exclude
  for a module of the base stack <name>@<pin> in qory.yaml, under harness.modules`. For a
  base module the base lists by a path or a git source alone, which `qory.yaml` cannot
  list, `qory harness compose` prints `It belongs to the base stack <name>@<pin>; rename
  yours.`, and the error's text starts `Rename your <kind>/<name>.`, then has the lines `<n> modules
  provide <kind>/<name>: <module>@<pin>, …` and `<kind>/<name> belongs to the base stack
  <name>@<pin>.`
- Both stacks' `extensions` go into the report, merged by top-level key: a key the
  checkout sets replaces the base's value whole, and a key it leaves alone stays the
  base's. Nothing under a key is merged.
- The base's `bind` applies, then the `provides` of the modules, then the checkout's
  `bind`, each layer replacing the name a role has from the one before (§References):
  which entry fills a role is a target's choice, like the model. A group role adds the
  modules' agents to the base's list, and the checkout's `bind` replaces the list.
- The report records the base with its source and pin, and marks the base's modules.
- The base may come from `-f` in place of `extends` (§Discovery): `qory harness compose
  -f <stack>` in a checkout whose document extends a stack composes the document on that
  stack, under these same rules, and does not read what the document defines under
  `extends`. The report records the base with `source` as the path of the stack's
  directory relative to the document and the pin `working-tree`.
- A base that cannot be fetched, on a machine without access to its repository, fails
  with status 1. The first line is "Run qory on a machine that can reach the base stack." and a later line is
  "The stack <source> is not reachable from here: <error>".
- A base does not extend another.

The base's runtime hooks and deny rules reach every extending checkout unchanged: the
runtime hooks under `hooks` and the `permissions` concatenate (§Composition rules), and
no runtime hook, MCP server, settings fragment, instruction section or variable of the
base can be excluded. What the kinds list removes is the code that runs without the
agent's involvement; a script inside an appended skill runs when the agent runs it,
under the base's runtime hooks and rules. A workflow hook (§Workflow hooks) is no
runtime hook: the base declares where one may go, and qory or the workflow's session
runs it. A wide `files` prefix, such as `claude/`, is safe for the same reason: the paths a
runtime reads as settings are reserved for every module (§Rendering), so a file cannot
reach them. Keeping a copy of the harness from leaving the machine is the gateway's egress
control, not the format's.

### Workflow hooks

A workflow hook is a step that a repository adds at a hook point of a workflow of the
stack it extends, so the repository extends the workflow without a copy of it. A
workflow hook is not a runtime hook, a module's `hooks/<file>`, which the runtime runs
and an extending module never ships. A workflow hook has one of three kinds:

- a `check` hook runs a program or an agent and reports pass or fail,
- a `review` hook runs an agent that reviews the work,
- an `enrich` hook runs a program or an agent that writes notes for the agents that
  start after it.

The base stack declares the points under `workflows.<name>.hook_points`, with the kinds
each point takes, and the limits under `extending.hooks` (§The stack). The repository
lists its workflow hooks under `harness.hooks` in its `qory.yaml`. Commit a hook's
program before you compose: the hook runner runs the file as committed in git, so the
compose reads the program from the commit at HEAD of the checkout.

```yaml
harness:
  extends: {git: https://github.com/example-org/harness, ref: v1.2.0, stack: software}
  hooks:
    - {at: after-review-round, kind: check, run: scripts/check-licences.sh}
    - {at: after-review-round, kind: review, agent: licence-reviewer, required: false}
```

| Field | Required | Meaning |
|---|---|---|
| `at` | yes | the name of a hook point of the base stack |
| `kind` | yes | `check`, `review` or `enrich`, and one the point takes |
| `run` | one of run and agent | the program, by its path from the directory of `qory.yaml`, alone or followed by its arguments, separated by spaces, with no shell and no quoting. The path is relative, with forward slashes, stays inside the checkout being composed, and leads to a file in the commit at HEAD of that checkout, of mode 100644 or 100755. The path is a name, not a pattern: `scripts/*.sh` is the file of that name. Neither the file nor a folder on its way may be a link, since the hook runner runs the file as committed in git and refuses a link. `hooks.json` has the path as git has it, from the checkout's root |
| `agent` | one of run and agent | the name of an agent that a module of the composed harness ships. A `review` hook runs an agent |
| `name` | no | the hook's name, unique at its point: letters, digits, dots, hyphens, and underscores, 64 at most, starting with a letter or digit. Default: the agent's name, else the file name of the program |
| `required` | no | whether a red result blocks. Default: `true` |
| `timeout` | no | the longest the hook may run, a duration in whole seconds, 1s or more, at most the base's `extending.hooks.timeout`. Default: the base's, else 10 minutes |

`hooks` is read beside `extends` and under `qory harness compose -f <stack>`, where
workflow hooks alone are a document. A key present with an empty value or null, such as
`run: ~`, is refused, as is a key a hook does not have. A `qory.yaml` in a folder above the checkout, one that
covers several repositories, may list workflow hooks; each program is then a file
inside the checkout being composed, so a hook of another repository under the folder
fails the compose of this one.

Each refusal is a line with the fix, of at most 98 characters, so that it has at most 100
under the `✗ ` that qory prints before it, then a line with the
cause, indented by two spaces. A refusal of a rule that needs no base comes as the
document is read, and its cause line has the path of `qory.yaml`, such as:

```
Set hooks[0].timeout to whole seconds, 1s or more, such as 90s or 5m.
  /work/app/qory.yaml sets "500ms".
```

A name or a quoted value of more than 24 characters in a first line that has more than 98
characters is shortened to 20 characters, its first 9 and last 10 with `…` between them;
the keys in front of a dotted name, such as `bind.agents.`, stay whole, and a first line of
at most 98 characters stays as it is. When two shortened names in the line would be equal,
every shortened name in the line keeps one more character at each end, not only the two that
are equal, and the round repeats until no two shortened names are equal: the first 9 and
last 10 characters become the first 10 and last 11, then the first 11 and last 12, and so
on. A name whose kept ends reach its whole length, such as one of two names that differ
only in their middle, stays whole. A last line of the refusal has the whole name of each
shortened name that no line after the first line has: `The full name of <short> is <name>.`

Those rules are: both or neither of `run` and `agent`, a `review` hook with `run`, a
kind other than the three, a missing `at` or `kind`, an empty value or null, a program path
that is absolute or leads out of the checkout, a malformed name, a timeout that is not
whole seconds of 1s or more, and `harness.hooks` in a section that defines a stack. The
compose fails with status 2 for a hook that breaks a rule of the base or the checkout:

- a base without `extending.hooks`:
  ```
  Remove hooks from qory.yaml, or extend a stack that allows them.
    The base stack software@4b8e2f1a9c7d has no extending.hooks.
  ```
- a base whose workflows declare no point:
  ```
  Remove hooks from qory.yaml, or extend a stack that declares hook points.
    The base stack software@4b8e2f1a9c7d declares none.
  ```
- a point the base does not declare:
  ```
  Set hooks[0].at to a hook point of the base stack.
    The base stack software@4b8e2f1a9c7d declares after-plan, after-review-round; after-merge is not one.
  ```
- a kind the point does not take:
  ```
  Set hooks[1].kind to check or review.
    after-review-round allows only check and review, and hooks[1].kind is enrich.
  ```
- more workflow hooks at a point than `per_point`, and in the points of one workflow
  than `per_workflow`:
  ```
  Remove a hook at after-review-round.
    The base stack allows 3, and qory.yaml has 4.
  ```
  ```
  Remove a hook at the points of workflow review-changes.
    The base stack allows 10, and qory.yaml has 11.
  ```
- a timeout over the base's:
  ```
  Set hooks[0].timeout to 10m or less, or leave it out.
    The base stack software@4b8e2f1a9c7d allows at most 10m, and hooks[0].timeout is 15m.
  ```
- an agent no module ships:
  ```
  Ship the agent in a module of qory.yaml, or fix hooks[1].agent.
    No module ships the agent licence-reviewer.
  ```
- a program outside the checkout, the checkout itself, a checkout with no commit or one
  git fails to read, a program not in the commit at HEAD or there in another letter case
  or Unicode form, a link, or an entry that is no file:
  ```
  Move the program into the checkout, or fix hooks[0].run.
    other/check.sh, from the directory of /work/qory.yaml, is outside the checkout /work/app.
  ```
  ```
  Set hooks[0].run to a program file in the checkout.
    hooks[0].run, ., is the checkout itself.
  ```
  ```
  Run qory in a git checkout: a hook runs the file as committed in git.
    The checkout /work/app has no commit yet.
  ```
  For a folder in no git checkout, the cause is `The folder /work/app is in no git
  checkout.`
  When git fails to read the checkout, the refusal has git's first line, such as:
  ```
  Fix what git reports for the checkout: a hook runs the file as committed in git.
    git rev-parse in /work/app failed: fatal: detected dubious ownership in repository at '/work/app'
  ```
  ```
  Commit the program of hooks[0].run: a hook runs the file as committed in git.
    hooks[0].run, scripts/check-licences.sh, has no file in the commit at HEAD of the checkout.
  ```
  An untracked file and an ignored file get this refusal too.
  A path that git has in another letter case or Unicode form, such as an NFD name, which
  a file system that ignores either finds on disk:
  ```
  Write hooks[0].run as committed in git.
    hooks[0].run is Scripts/check-licences.sh, and git has scripts/check-licences.sh.
  ```
  ```
  Point run at the file itself, not a link.
    hooks[0].run, scripts/check-licences.sh, is a link.
  ```
  The compose refuses a file in a linked folder the same way.
  ```
  Point run at the file itself, not a link.
    hooks[0].run passes through the link tools.
  ```
  ```
  Point run at a file.
    hooks[0].run, scripts, is a folder in git.
  ```
  For a submodule, the cause ends in `is a submodule in git.`
- two workflow hooks of one name at a point:
  ```
  Set hooks[1].name to a name that no other hook at its point has.
    hooks[0] at after-review-round has the name check-licences.sh too.
  ```

A stack file is refused the same way, with the fix first, for a point name that two
workflows declare, `Rename one of the two hook points in qory-stack.yaml: a point name is
unique in a stack.`, then `Workflows build and plan both declare after-review-round.`,
and for a point or a limit that breaks the rules of §The stack.

The home contains `hooks.json` with each workflow hook, in the order of `harness.hooks`;
a checkout without workflow hooks has no such file. The workflows' files are the same
with and without workflow hooks: the compose writes no hook into a skill, an agent or a
command.

```json
{
  "hooks": [
    {
      "workflow": "review-changes",
      "at": "after-review-round",
      "point_at": "end",
      "kind": "check",
      "name": "check-licences.sh",
      "run": ["scripts/check-licences.sh"],
      "required": true,
      "timeout_seconds": 600,
      "index": 0
    }
  ]
}
```

`workflow` is the workflow that declares the point, and `point_at` the point's `at`.
`run` is the program and its arguments, the program by its path from the root of the
checkout being composed, with forward slashes, whatever folder the `qory.yaml` that lists
it is in; `agent` stands in its place for an agent. `timeout_seconds` is the hook's
timeout, and `index` the hook's place in `harness.hooks`, from 0.

## Sources

A **path source** reads a directory as it stands. Its pin is `working-tree`, marked dirty in
the report when git sees uncommitted changes under it.

A **git source** reads a repository at a ref: a branch, a tag, or a commit by its full id.
Its commit is fetched, at depth one, into `qory/sources/<url>/<commit>` under the user's
cache directory, the URL made into a directory name with a short hash appended, beside a
`refs/<ref>` file containing whether the ref is a branch or a tag and the commit it last
resolved to; the pin is that commit. A branch follows the remote: each compose takes its
current commit, and the module's row shows when it moved. A tag or a full commit id stays
pinned to its commit. Offline, compose keeps the cached commit and warns. A branch that is
gone from the remote fails; to keep the old harness, set `ref` to a commit id. An edited
ref resolves anew. `qory harness compose --update` re-fetches every git source, tags
included. A path inside the repository that links outside it is refused. `path` defines the module's directory inside the
repository, for a repository that contains several modules. The report and the messages
write a git source as `<url>#<ref>` or `<url>#<ref>:<path>`.

An **export** is a stack or a module selected by the name the repository publishes it
under, `module: <name>` in a module's source and `stack: <name>` in `extends`, beside the
`path` of the repository on disk or its `git` and `ref`; a git source selects an export
or a `path` inside the repository, never both. The repository is resolved as above, and the
export's directory is read from the `exports` section of the `qory.yaml` at its root
(§Exports), so the layout is the publisher's alone and a consumer refers to nothing of it.
The report and the messages write an export as `<url>#<ref> module <name>`, `<url>#<ref>
stack <name>`, or `<path> module <name>`. A base's modules listed without a source are
recorded by the directory they resolved to, `<url>#<ref>:<dir>/<name>`, so the report
records where each module came from at the pinned commit.

## Exports

A repository that delivers stacks or modules to other repositories lists them in the
`exports` section of its `qory.yaml`, or `harness.yaml`, at its root:

```yaml
# qory.yaml of the harness repository
apiVersion: qory.dev/v1alpha1
qory: ">=0.5.0"                  # the range every stack this repository delivers is checked against
exports:
  dir: ./harness                 # optional; where stacks/ and modules/ are
  stacks: [nextjs-15]            # harness/stacks/nextjs-15/qory-stack.yaml
  modules: [core, nextjs, marketing]   # harness/modules/<name>/qory-module.yaml
```

The section is the whole public surface: a consumer can select what it lists and nothing
else, and a stack or a module not listed is the publisher's alone, whatever directory it is
in. Each export is one directory named after it: `stacks/<name>` containing a
`qory-stack.yaml`, `modules/<name>` containing a `qory-module.yaml`. Where those two
directories are is `dir`, relative to the repository root: absent, they are at the root;
as one string, `dir` is the directory containing both, `<dir>/stacks` and `<dir>/modules`;
as a map, `{stacks: <dir>, modules: <dir>}`, each is used as written and the exports sit
directly under it. A publisher moves its directories by changing `dir`, and a consumer
pinned to a ref sees nothing of it. `exports.dir` also decides where a stack of that
repository reads a module it lists without a source (§The stack), so a stack, its modules
and the section agree on one layout.

A name is one path segment, listed once. `qory config` prints the section, and every
command that reads the configuration checks that each export is there: a listed stack
whose directory contains no `qory-stack.yaml`, or a module without its `qory-module.yaml`,
is refused with the directory in the message, so the section cannot go stale unnoticed in
the
repository that owns it. A section elsewhere than the repository root, in the machine's
file or an ancestor's, is read and left out: an export is a repository's. A consumer that
selects an export the repository does not list is refused with the list of what the
repository exports; one that selects an export of a repository without the section is
refused with a message to select its directories with `path`.

### A factory's process.yaml

A module that `exports.modules` lists and that has a `process.yaml` at its folder root is
a factory. The file is a process template: the texts, the workspace type, the stack and
the process that a workspace made from it starts with
([process.schema.json](process.schema.json)). It may also list the artifact types its
workspaces use, and declare the submit step of a run. Every command that reads the
configuration checks the file, once it finds each export. A module without the file is
not checked for it.

```yaml
# harness/modules/support-desk/process.yaml
apiVersion: qory.dev/v1alpha1
name: Support desk               # at most 60 characters
summary: Agents draft answers to support tickets. A person checks each answer.   # at most 200
description: >-                  # at most 1200 characters and 80 words
  Each ticket goes through two stages. In Answer, an agent drafts an answer and
  then checks its tone. In Check, a person reads the draft and sends it or sends it back.
type: qory.software              # the workspace type key
stack: support                   # a stack that exports.stacks of this repository lists
process:                         # a qory.process/v1 document
  schema: qory.process/v1
  stages:
    - id: st_0000000001
      name: Answer
      sequence:
        - workflow: draft-an-answer          # declared under workflows in harness/stacks/support/qory-stack.yaml
        - loop: {rounds: 3, body: [{workflow: check-the-tone}]}
      gate: {kind: none}
    - id: st_0000000002
      name: Check
      sequence: []
      gate: {kind: person, approvers: members}
artifact_types:                  # optional; the artifact types the workspaces use
  - name: screenshot             # a standard type: the name alone
  - name: reply-draft            # a custom type: the full definition
    base_view: document
    size_limit: 65536
    max_files: 1
  - name: customer-facts         # a structured custom type also has a JSON Schema
    base_view: structured
    size_limit: 65536
    max_files: 1
    json_schema:
      type: object
      required: [customer]
      properties:
        customer: {type: string}
```

| Key | Meaning |
|---|---|
| `apiVersion` | `qory.dev/v1alpha1`, required as in every delivered file |
| `name` | the template's name, at most 60 characters |
| `summary` | what a workspace made from the template does, at most 200 characters |
| `description` | how such a workspace works, at most 1200 characters and at most 80 words |
| `type` | the workspace type of a workspace made from the template: a workspace type key, such as `qory.software` |
| `stack` | the stack the workspace's workflows run on, one that `exports.stacks` of the same repository lists |
| `process` | the workspace's process, a `qory.process/v1` document. qory checks that it is an object with `schema: qory.process/v1` and a list of `stages`; that each `workflow` of a stage's `sequence`, a loop's `body` included, is a non-empty string of one line that the stack's `qory-stack.yaml` declares under `workflows`; and that each stage `name` has no line break or other control character. qory checks nothing else in it |
| `artifact_types` | optional; the list of the artifact types the workspaces use, as [Artifact types](#artifact-types) has it. A list with no entries is valid |
| `submit` | optional; the submit step of a run, as [The submit step](#the-submit-step) has it |

Every key but `artifact_types` and `submit` is required, and the file has no other key. A key left out, a text over its
limit, a `stack` that `exports.stacks` does not list, and a `process` that is not such an
object are refused with the file and the fix in the message: the fix is the first line, and the file is on the
last line, so a long path never pushes the fix past 98 characters. Each
workflow that the stack does not declare is refused with a fix line, which has the
workflow, the stack and the stage, a cause line, which has the `process.yaml`, and a last
line with the stack file:

```text
Declare check-the-tone in stack support, or remove it from stage Answer.
  Stage Answer of /src/acme-harness/harness/modules/support-desk/process.yaml runs check-the-tone, and stack support does not declare it under workflows.
  The stack is in harness/stacks/support/qory-stack.yaml.
```

A workflow that is empty, null, not a string, such as `123` or `true`, or has a line
break or another control character, is refused the same way, with the entry's place in the stage, such as `entry
2 in the loop of entry 3`. A stage name and a `stack` with a line break or another
control character are refused too. qory reads the file through the module's folder, as
the compose reads a module's files: a link to a file inside the folder is followed, and
a link to a place outside it is refused without reading the place it leads to.

A character is a Unicode code point, and a word is a run of characters without white
space. The compose does not read the file: in the composed tree it is one of the
module's other files (§The module manifest).

#### Artifact types

An artifact is a typed, versioned result of a work item. Each entry of `artifact_types`
is a standard type, `name` alone or with `withdrawable`, or a custom type, with these keys and no others:

| Key | Meaning |
|---|---|
| `name` | required: lowercase letters and digits in groups joined by single hyphens, 1 to 64 bytes, such as `reply-draft`. Each name is listed once |
| `base_view` | `document`, `image`, `video`, `file` or `structured`, how a version's files are shown |
| `size_limit` | the most bytes of all files of one version, a whole number from 1 to the base view's most: 10 MiB for `document`, 50 MiB for `image`, 2 GiB for `video`, 1 GiB for `file` and 1 MiB for `structured` |
| `max_files` | the most files of one version, a whole number from 1 to 20; exactly 1 for `structured` |
| `json_schema` | the JSON Schema of the file, exactly when `base_view` is `structured` |
| `withdrawable` | optional, `true` or `false`, and `true` when the entry has none: whether a run may withdraw a name of the type with `qory artifact push --withdraw`. A standard type and a custom type may both set it. qory refuses any other value, such as `"no"`, with the fix on the first line and the file on the last |

A custom type has `base_view`, `size_limit` and `max_files`. qory has no list of standard
types: a name alone, or a name with `withdrawable` alone, passes for any name of the right
form.

```yaml
artifact_types:
  - name: acceptance-lock   # a standard type that a run cannot withdraw
    withdrawable: false
```

With no `artifact_types`, a workspace made from the template shows every standard type. A
list, also an empty one, hides each standard type it leaves out in a new workspace, except
`proposal`, `questions`, `review-comments` and `verdict`, which no workspace can hide. A
person can switch a hidden type on in the workspace. A template fails Qory Apiary's check
when a stage has an input or output that is neither in the list nor one of those four.

A `json_schema` is an object that compiles as a JSON Schema. It is draft 2020-12 unless its
`$schema` sets draft-07, the only other draft. A reference to a file or URL outside the
schema fails when the schema reaches it, and qory reads no file or URL for one. The JSON
Schema metaschemas are built in, so a reference to one passes. A `pattern` is a Go
regular expression (RE2). A
`json_schema` has at most 65,536 bytes as compact JSON, 4096 values in all,
1024 entries in a list, 1024 members in an object, 16,384 bytes in a string or a member
name, and 64 lists and objects nested in one another. The patterns of all custom types in
one `process.yaml` are at most 16 MiB by qory's estimate from each pattern's parts: a
group repeated 1000 times counts 1000 times. qory parses each pattern and compiles none.

An entry that breaks a rule stops the command with a refusal that has the file and the
fix, such as:

```text
Add size_limit and max_files to artifact type reply-draft, or leave only its name.
  A custom type has base_view, size_limit, and max_files.
  The type is in /src/acme-harness/harness/modules/support-desk/process.yaml.
```

#### The submit step

`submit` declares the submit step of a run of a workspace made from the template. A
`process.yaml` has at most one `submit`, an object with `type` and `send` and no other
key. Without `submit`, the step stores no submission.

```yaml
submit:
  type: customer-facts           # a structured entry of artifact_types
  send: after_gate               # or end_of_run
```

`type` is the artifact type of the submission: the name of a custom entry of
`artifact_types` in the same file with `base_view: structured`. qory refuses a name that no
entry has, a standard type (a name alone) and a custom type of another base view. `send`
is when the step sends the submission: `end_of_run` sends it at the end of the run;
`after_gate` sends it after the gate of the run's stage passes, and until then the step
stores it and sends nothing. A `submit` that breaks a rule stops the command with a
refusal that starts with the fix, then gives the cause and the file:

```text
Remove the key "command" from submit.
  submit has only type and send.
  submit is in /src/acme-harness/harness/modules/support-desk/process.yaml.
```

## The branch format

A branch document records a work item's branch and the commit in each repository. This
is version 1 of the format ([branch.schema.json](branch.schema.json)):

```json
{"ref": "qory/wi_7k2m9pq4xt3a8b1c",
 "repositories": [{"system": "github.com", "path": "acme/web", "commit": "5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e"}]}
```

| Member | Meaning |
|---|---|
| `ref` | the branch's name, the same in each repository: `qory/` followed by the work item's id |
| `repositories` | each repository with the commit of the branch in it, at least one |
| `system` | the host the repository is on, a lower-case host name of at most 253 characters |
| `path` | the repository's path on the host: at most 200 characters of segments joined by slashes, with no `.`, `..` or `.git` segment |
| `commit` | a full commit id, 40 or 64 lower-case hexadecimal digits |

A document has no other member. A software factory can use the format as the JSON Schema
of a custom artifact type in its `process.yaml`:

```yaml
artifact_types:
  - name: branch
    base_view: structured
    size_limit: 65536
    max_files: 1
    json_schema: {…}             # the content of branch.schema.json
```

## The acceptance criteria format

An acceptance criteria document records what a work item must achieve and the criteria that
say when it is achieved. The format names no kind of work, so a workspace that does not
produce software uses it as it is. This is version 1 of the format
([acceptance-criteria.schema.json](acceptance-criteria.schema.json)):

```json
{"summary": "A customer sees the new price before they pay.",
 "criteria": [{"id": "ac-1", "text": "The price page shows the new price.",
               "check": "Open the price page as a customer. The price is 12.50."},
              {"id": "ac-2", "text": "The old price is gone.",
               "check": "Search the price page for 10.00.",
               "removed": {"reason": "The old price stays for existing customers."}}]}
```

| Member | Meaning |
|---|---|
| `summary` | what the work item must achieve, plain text, 1 to 2000 characters |
| `criteria` | every criterion, at least one, in the order they were written; a removed criterion stays in the list |
| `id` | the criterion's id, unique in the document: `ac-` and a number from 1 to 9999 with no leading zero, such as `ac-3` |
| `text` | the condition that the finished work item meets, plain text, 1 to 2000 characters |
| `check` | how to check that the condition is met, free text, 1 to 2000 characters |
| `removed` | present when the criterion is dropped, and only then: an object with `reason`, why it is dropped, 1 to 2000 characters |

The id form `ac-` and a number from 1 to 9999 with no leading zero is the one form of every
criterion id in the contract: `ac-0`, `ac-01` and `ac-10000` fail the schema, in this
format, in the verification format and in the questions format. A document has no member
other than those in the table. A JSON Schema cannot check that each `id` is unique.

## The verification format

A verification document records the result of checking a work item's acceptance criteria
against one version of its target. The format names no kind of work. This is version 1 of the
format ([verification.schema.json](verification.schema.json)):

```json
{"target_version": "5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e",
 "matrix": [{"criterion": "ac-1", "state": "met", "evidence": "The price page shows 12.50."},
            {"criterion": "ac-2", "state": "not_met", "evidence": "The page still shows 10.00 in the footer."}],
 "not_verified": [{"criterion": "ac-3", "reason": "The payment sandbox was down."}],
 "model_families": ["anthropic", "openai"]}
```

| Member | Meaning |
|---|---|
| `target_version` | the exact version of the target that the verification checked, never a name that can later point to another version; for a repository, a commit. 1 to 255 letters, digits and the characters `._/-`, such as `5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e` or `2026-11-18.3` |
| `matrix` | one row for each criterion that was checked, empty when none was |
| `criterion` | the id of the criterion, in the form of `id` in the acceptance criteria format |
| `state` | `met`: the check shows that the criterion is met; `not_met`: the check shows that it is not |
| `evidence` | what the check found: what was looked at or run, and the result, plain text, 1 to 2000 characters |
| `not_verified` | each criterion that was not checked, empty when every criterion was checked; each has `criterion` and `reason`, why it was not checked, 1 to 2000 characters |
| `model_families` | the families of the models that checked the criteria, at least one, each 1 to 64 characters on one line, none twice |

A criterion is in `matrix` or in `not_verified`, never in both, so `state` has no value for
a criterion that was not checked. A document has no member other than those in the table. A
JSON Schema cannot check that each id is a criterion of the newest acceptance criteria, or
that a criterion is in only one of the two lists.

## The questions format

A questions document records every question of a work item and its answer or a run's
decision, one document per work item and at most 1 MiB. A run adds, decides, withdraws or
reopens questions with `qory artifact push questions`, and the control plane writes the
answers. A question has 0 to
4 options. The run sets `recommended` to `true` on the option it recommends, on at most
one option of a question. An answer has
`option`, the title of the option the member chose, or `null`, and `note`, added detail on
the chosen option, or the member's answer when `option` is `null`. `note` is optional when
the member chose an option and required when `option` is `null`.

A question has the required member `about`, what the question is about. It has one of two
forms:

```json
{"work_item": true}
{"type": "acceptance-criteria", "name": "main", "item": "ac-3"}
```

- `{"work_item": true}`: the work item itself, before any output, such as the ticket. It
  has no other member.
- An output of the work item: `type` is the artifact type's name, as in `artifact` of a
  review comment, and `name` is the artifact's name, 1 to 64 characters on one line, such
  as `main`. `item` is optional: the id of the entry of that artifact that the question
  concerns, 1 to 32 characters on one line, such as `screen-2` for a screen of a mockup.
  When `type` is `acceptance-criteria`, `item` is a criterion id: `ac-` and a number from
  1 to 9999 with no leading zero, such as `ac-3`, so `screen-2`, `ac-0`, `ac-01` and
  `ac-10000` fail the schema. With any other `type`, `ac-3` is an id like any other.

No member of `about` is a work item id: the document is for one work item. A JSON Schema
cannot check that the work item has the artifact or that the artifact has the entry.
`about` replaces the member `criterion`, which a question no longer has.

An option has the optional member `proposal`, the new wording of the entry that `about.item`
names, which the run proposes for a member to edit before sending: plain text, 1 to 2000
characters. Only an option of a question whose `about` has `item` has `proposal`. The schema
refuses `proposal` on an option of a question about the work item, and on one about an
output with no `item`.

A run that chooses an option without asking a member records a decided question: its
`state` is `decided`, so it is not open, and it has `decision`. A decided question has at
least one option, and none has `recommended`. `decision` has four members, all required:

| Member | Meaning |
|---|---|
| `option` | the title of the option the run chose, one of the question's option titles |
| `reason` | why the run chose the option, plain text in 1 or 2 sentences, at most 2000 characters |
| `run` | the run's number, as in `asked` |
| `at` | when the run chose the option, as in `asked` |

`decision` stays on the question when its state changes. A run that withdraws a decided
question keeps `decision`. A member changes a decision by answering the question: its
`state` becomes `answered`, and `decision` stays beside `answer`. A run that reopens that
answer keeps `decision` too. This is version 1 of the format ([questions.schema.json](questions.schema.json)).

## The review comments format

A review comments document records the members' review comments on a work item's artifact
versions, such as a proposal, or on the whole round of a run, and the replies to them, one
document per work item and at most 1 MiB. The control plane writes the members' review
comments and replies. A version that a run pushes only adds the run's reply to any of the
open review comments, at most one reply to each, and closes each review comment it adds a
reply to: it changes no member's text, drops no id and adds no review comment. A closed
review comment has at least one reply by a run. A run's reply has an `outcome`:

| `outcome` | Meaning |
|---|---|
| `changed` | the run changed the artifact for the review comment |
| `answered` | the review comment is a question, and the reply contains the answer |
| `declined` | the review comment is a request for a change that the run did not make, and the reply contains the reason |
| `asked` | the run could not settle the review comment and added a question for a person to the `questions` artifact; the reply's `question` is the id of that question in the newest `questions` version, so the run pushes the questions before the review comments |

A member's reply has no `outcome` and no `question`.

A review comment's `on` has one of two forms:

- On an output, `on` has the artifact type's name in `artifact` and the version's number
  in `version`: the review comment is about that artifact version. For a review comment
  on one screen of a mockup, `on` also has `screen`: the id of a screen in that artifact
  version, 1 to 32 characters on one line.
- On a round, `on` has only `run`, the number of a run, from 1: the review comment is
  about everything that run produced and the round as a whole. `run` is the number of the
  run whose round the member commented on.

A version that a run pushes keeps `on` unchanged.

This is version 1 of the format ([review-comments.schema.json](review-comments.schema.json)).

## The proposal sources format

A proposal sources document is the file `sources.json` of a version of the artifact type
`proposal`, beside `proposal.md`, and at most 1 MiB. It records every source the run used
to write the proposal, with the reason for each, for the agent of the next stage. A source
is anything the run read: a web page, a ticket, a record in another system, a document or
a file in a repository. Each source has a `kind` from an open list, such as `page` or
`record`, a `ref` in the form that its kind needs, and a `why`; `system`, `container` and
`locator` are optional. This is version 1 of the format
([proposal-sources.schema.json](proposal-sources.schema.json)).

## The verdict format

A verdict document records a run's verdict on the work of its round. `qory run verdict`
stores it as a version of the artifact type `verdict`. This is version 1 of the format
([verdict.schema.json](verdict.schema.json)):

```json
{"verdict": "escalate",
 "points": [{"text": "The spec does not say which customers see the new price.", "action": "answer", "question": "q4"},
            {"text": "The checks script failed on the spec's links.", "action": "run_again",
             "fix": "Fix the link to /docs/start in the spec."}]}
```

| Member | Meaning |
|---|---|
| `verdict` | `passed`: the work meets the bar; `changes-requested`: the work needs another round; `escalate`: a person must act |
| `points` | what is wrong or missing, 1 to 20 points: required with `escalate`, optional with `changes-requested`, where each point has the action `run_again`, and absent with `passed` |
| `text` | what is wrong or missing, in one plain sentence for a person on one line, 1 to 500 characters, with no action and no question id, because Qory Apiary adds the action from `action`: it has no control character, so no tab, line feed or carriage return, and the schema's `$defs` `line`, the `line` of the questions format, lists the format characters it cannot have |
| `action` | `answer`: a person answers the question in `question`, only with `escalate`; `run_again`: another round runs, and with `escalate` a person runs it |
| `question` | the id of an open question in the work item's newest `questions` version, in the id form of the questions format: required with `answer`, and absent with `run_again` |
| `fix` | what a person does to fix the point, in one plain sentence on one line, with the rules of `text`: optional with `run_again`, and absent with `answer`. Qory Apiary shows it in place of the sentence of the action `run_again` |
| `track` | the track the run proposes for the work item: optional with a verdict of any kind, an object with the members `id` and `reason` and no other. A verdict with no track proposes none. Qory Apiary returns 422 for a track that the gate of the run's stage does not offer |
| `id` | the id of a track that the gate of the run's stage offers: one lower-case word, or lower-case words joined by hyphens, such as `small-change`, at most 40 characters. Required in a track |
| `reason` | why the run proposes the track, in one plain sentence on one line, 1 to 300 characters, with the rules of `text`. Required in a track |

A `changes-requested` verdict may list what the next round fixes:

```json
{"verdict": "changes-requested",
 "points": [{"text": "The checks script failed on the spec's links.", "action": "run_again"}]}
```

`qory run verdict` sends points only with `escalate`.

A verdict of any kind may propose a track:

```json
{"verdict": "passed", "track": {"id": "small-change", "reason": "The change touches one config file."}}
```

`qory run verdict --track <id> --track-reason <text>` sends the track. It sends `id` and
`reason` together or not at all, and checks both against this schema before it sends or
keeps anything. It exits 2 for an id, a reason or a pair that breaks a rule. It exits 4
when Qory Apiary refuses the verdict with 422 `invalid_artifact`, such as for a track that
the gate does not offer.

A document has no member other than `verdict`, `points` and `track`, a point has no member
other than `text`, `action`, `question` and `fix`, and a track has no member other than
`id` and `reason`. A JSON Schema cannot check that the
question is open, so the run pushes its questions before its verdict. Each of these
documents fails the schema:

```json
{"verdict": "escalate"}
{"verdict": "passed", "points": [{"text": "The checks script failed on the spec's links.", "action": "run_again"}]}
{"verdict": "escalate", "points": []}
{"verdict": "escalate", "points": [{"text": "The spec does not say who sees the price."}]}
{"verdict": "escalate", "points": [{"text": "The spec does not say who sees the price.", "action": "answer"}]}
{"verdict": "escalate", "points": [{"text": "The checks script failed.", "action": "run_again", "question": "q4"}]}
{"verdict": "escalate", "points": [{"text": "The checks script failed.\nThe spec's links are broken.", "action": "run_again"}]}
{"verdict": "changes-requested", "points": [{"text": "The spec does not say who sees the price.", "action": "answer", "question": "q4"}]}
{"verdict": "escalate", "points": [{"text": "The spec does not say who sees the price.", "action": "answer", "question": "q4", "fix": "Answer q4."}]}
{"verdict": "changes-requested", "gaps": "Add the empty cart."}
{"verdict": "passed", "track": {"id": "small-change"}}
{"verdict": "passed", "track": {"id": "Small-Change", "reason": "The change touches one config file."}}
{"verdict": "passed", "track": {"id": "small-change", "reason": "The change touches one config file.", "stage": "build"}}
{"verdict": "passed", "track": {"id": "small-change", "reason": "The change touches one config file.\nIt adds no code."}}
```

## The review report format

A review report records the findings of one reviewer of a review round on the reviewed
commit. The reviewer writes it as `report.json`, and qory's review round reads it, with
the result of the full suite, to decide the round's verdict. This is version 1 of the
format ([review-report.schema.json](review-report.schema.json)):

```json
{"reviewer": "code-reviewer",
 "findings": [{"severity": "blocking", "quote": "if count >= limit {",
               "problem": "A batch of exactly the limit is cut, so the last item of a full page is lost.",
               "fix": "Cut only a batch of more than the limit, and test a batch of the limit.",
               "proof": "failing check: TestBatchAtLimit fails with 99 of 100 items.",
               "point": "A batch of exactly the limit loses its last item.",
               "file": "internal/batch/batch.go", "line": 12}]}
```

| Member | Meaning |
|---|---|
| `reviewer` | the reviewer's agent name: lower-case letters, digits and hyphens, from a letter or a digit, at most 64 characters. Required |
| `findings` | the reviewer's findings, a list that may be empty. Required |
| `id` | the id of a finding of the round before that this finding repeats, `f` and a number from 1 of at most 6 digits. Optional |
| `severity` | `blocking`: the round is not clean while the finding is open; `non-blocking`: the finding is open, and the round can still pass. Required |
| `quote` | the text of the reviewed commit that the finding is about, 1 to 2000 characters. Required |
| `problem` | what is wrong, 1 to 2000 characters. Required |
| `fix` | what the next session changes, 1 to 2000 characters. Required |
| `proof` | `failing check: `, `failing input: ` or `broken rule: `, then the evidence, at most 1000 characters. A blocking finding has a failing check or a failing input. Required |
| `point` | the finding in one line, 1 to 500 characters, with at least one character that is not a space. qory trims the spaces at its ends, and reads a point of spaces and characters that show nothing, such as U+200B, as a report that does not match the format. Required |
| `file` | the path of the file that the finding is about, 1 to 500 characters. Optional |
| `line` | the line of `file`, a whole number from 1. Optional |

A report has no member other than `reviewer` and `findings`, and a finding has no member
other than those of the table. qory reads at most 1 MiB of the file and follows no link.
A report that is missing, is not one JSON object, does not match the schema, or has
another reviewer's name is a blocking finding that qory writes for the reviewer. Each of
these documents fails the schema:

```json
{"reviewer": "code-reviewer"}
{"reviewer": "code-reviewer", "findings": [], "result": "clean"}
{"reviewer": "code-reviewer", "findings": [{"severity": "minor", "quote": "q", "problem": "p", "fix": "f", "proof": "failing input: x", "point": "p"}]}
{"reviewer": "code-reviewer", "findings": [{"severity": "blocking", "quote": "q", "problem": "p", "fix": "f", "proof": "broken rule: r", "point": "p"}]}
{"reviewer": "code-reviewer", "findings": [{"severity": "non-blocking", "quote": "q", "problem": "p", "fix": "f", "proof": "broken rule: r", "point": " "}]}
```

## The module manifest

```yaml
# qory-module.yaml
apiVersion: qory.dev/v1alpha1
name: nextjs                     # the module's name: modules/nextjs in the composed tree
description: Next.js 15 conventions and the e2e skill
variants:
  default: claude                # the variant a runtime gets when no variant is named for it, or `fail`
  claude: {}
  codex: {agents: agents/codex}  # this variant reads its agents from another directory
env:
  HARNESS_HOME: .                # exported as the module's root in the composed tree
  HARNESS_TOOLS: scripts/tools   # a path inside the module
requires:
  - skill: deploy                # this module's deploy skill needs these composed beside it
    commands: [ship]
    agents: [reviewer]
egress:                          # the hosts the module's skills, hooks and servers reach
  - api.example.com
  - "*.github.com"               # every host below github.com, not github.com itself
roles:                           # the roles the module's documents reference
  agents:
    reviewer: one                # one entry fills the role
    reviewers: many              # a group role: several agents fill it
    developer: {fill: one, contract: roles/developer.yaml}  # and the parts its agent needs
provides:                        # the roles the module fills
  agents:
    developer: nextjs-developer  # ${qory:agents/developer} is the agent nextjs-developer
    reviewers: [nextjs-reviewer] # the module adds nextjs-reviewer to the group
push_checks:                     # optional: a check for the files of one artifact type
  - type: spec
    command: [scripts/checks.js, push, spec]  # a program in the module, then its arguments
    reads: [questions]           # optional: the other artifact types the check reads
scope_checks:                    # optional: the checks of the plan of one artifact type
  build-plan: [scripts/checks.js, scope, --plan]   # the type a workflow names in `plan`: one command, a program in the module, then its arguments
```

`description`, optional, describes what the module is for; the report contains it and `qory harness
inspect` prints it beside the module. `name` is the module's identity: the stack refers to the module by it, the composed tree
contains the module at `modules/<name>`, and the report and every message use it. The
directory the module is stored under is not consulted. `env` lists the variables the module exports, each the path of a file or directory inside
the module, `.` for its root. The compose writes each as
`$QORY_HARNESS_HOME/modules/<name>/<path>` and every launch sets it as the module's
absolute path in the home, a default (§Launching), so a script the module ships reads the
module's location from the same variable wherever the home is; a value a run sets for the name
over the defaults wins. Two modules exporting one name with different values
fail the compose unless the configuration's `env` sets it; `QORY_HARNESS_HOME` is qory's.
`requires`, optional, is one item per entry of the module that needs other entries
composed beside it: the entry by its singular kind, `skill`, `agent`, `command`,
`output-style`, `hook`, `mcp` or `file`, and what it needs under the plural kinds an
`exclude` uses. A required entry may come from any module. `mcp` is the same in the singular: a
string selects the server, a list the servers needed, and since a key appears once per
item an item with a server lists no servers. An item that lists no entry or two, a key
that is neither, an empty list, an entry listed twice, and an entry the module does not
ship are refused (§Composition rules).
`egress`, optional, declares the hosts the module's skills, hooks and servers reach: a
lower-case host name, or `*.` followed by a name for every host below it and not the name
itself. No ports, no paths, no schemes. The grammar is the Forager contract's, the
`egress.allow` entry of a run policy at
`https://qory.dev/contracts/forager/v1/policy.schema.json`, copied into the module schema and
kept identical by a test; a host outside the grammar is refused, with the host in the
message. The
declarations of every composed module are unioned into the report's `egress` (§The
report), and `qory run` passes the union to Forager, which reports it as `harness_hosts` in
`dev.qory.run.policy_applied` beside the policy's list, for a receiver to compare the
two: the declaration decides nothing, the policy alone defines what the run reaches. An
`exclude` or an `only` does not touch the declaration; a module composed at all declares.
A module with no `egress` key declares nothing, and a harness in which no module declares
passes Forager no list. `egress: []` is a declaration that the module reaches
nothing.
`roles`, optional, lists the roles the module's documents reference, by kind, `agents`,
`skills` or `commands`, and then by role, each set to `one`, for a role one entry fills,
or `many`, for a group role, which several agents fill (§References). Only an `agents`
role can be `many`: `Set roles.skills.lint of module core to one.` with `Only an agents
role can be a group. The map is in modules/core/qory-module.yaml.` on the next line. Any
other value fails
the compose, `Set roles.agents.developer to one or many.` with `One entry fills a role and
several fill a group, and the value is "two".` and `roles is in
modules/core/qory-module.yaml.` on the next two lines, and so does a list. A module that sets a role to `one` while another
sets it to `many` fails the compose: `Declare agents/reviewers as many in module python,
or remove it.` and `Module software-factory declares it many.` on the next line. An `agents` role may be a map
instead, `{fill: one, contract: roles/developer.yaml}`: `fill` is `one` or `many`, as
above, and `contract`, optional, is the path of a role contract inside the module, with
no `..` part (§The role contract). A map with another key fails, and so do a map without
`fill`, a map for a `skills` or `commands` role, and a `contract` that is not a path
inside the module. Each refusal is a fix line, then a cause line that ends with the
manifest's path:

```
Set roles.agents.developer.contract of module core to a path inside the module.
  The value is "../developer.yaml". The map is in modules/core/qory-module.yaml.
```

Apart from `many` and the contracts, the compose checks nothing else against `roles`.
`provides`, optional, fills roles (§References), by kind and then by role, each to the
name of one entry of that kind, as `bind` does: a technology module `typescript` writes
`provides: {agents: {developer: typescript-developer}}`, and a workflow skill's
`${qory:agents/developer}` resolves to `typescript-developer` with no `bind` in the
checkout. The name is a composed entry of any module. A group role takes a list of the
agents the module adds to the group, `provides: {agents: {reviewers: [python-reviewer]}}`.
A name for a group role fails the compose, `Write provides.agents.reviewers in module
python as a list: it is a group role.`, and so does a list for a role no module declares
`many`: `Write one name for provides.agents.developer in module python.` with `No module
declares agents/developer as many, so one agent fills it.` on the next line. The compose
refuses a list in a `bind` the same way, not when it reads the file, with `Write one name for
bind.agents.developer.`, the cause, and `bind is in <file>.` on the last line. A list for a kind other than
`agents`, an empty list, a flat `agents/developer` key and a kind other than the three are
refused, each with the manifest's path.
`push_checks`, optional, declares checks for the files of an artifact type, one item per
type: `type`, the artifact type, `command`, the program and its arguments, and `reads`,
optional. `reads` lists the other artifact types the check reads. The program is a file in
the module, a relative path with no `..` part, such as an executable script with a
`#!/usr/bin/env node` line. The checks are set per module, and a module without
`push_checks` adds none: its pushes work as before. A type that does not match
`^[a-z0-9]+(-[a-z0-9]+)*$` or is longer than 64 bytes, one type twice in one module, an
empty command, a word of the command that is not a string, and a value of `reads` that is
not a type or is listed twice fail the compose, each with the manifest's path. The compose
also fails for a check of the type `verdict`, `Remove the push check for verdict from
module core.` with `qory run verdict sends a verdict, and push checks do not cover it.` on
the next line; when two modules declare a check for one type, `Remove the push check for
spec from module core or module web.` with `A type has one push check, and both modules
declare one.` (when the first line would have more than 98 characters, it is `Remove the
push check for the type from one of the two modules.`, and the next line has the type and
both module names, whole); when the program is not a file in the module, `Add the program
of the push check to its module, or change its path.` with `Module core has no file at
checks/check.js, the program of the push check for spec.`; when it is a folder or another
thing that is no file, `Set the program of the push check to a file in its module.` with
`checks/check.js of module core is not a file, and it is the program of the push check for
spec.`; and when the program is in an entry an `exclude` or an `only` of its module leaves
out, `Keep the program of the push check in its module, or move it out of the left-out
entry.` with `The program skills/running/checks.js of the push check for spec is in
skills/running, and an exclude or only of module core leaves skills/running out.` In the
errors for a program, line 1 has no module name, type or path. Line 1 of the `verdict`
error has the module name, and line 1 of the error for two modules with one type has the
type and both module names when it has at most 98 characters. So a long value never pushes
line 1 past 98 characters. The home contains
`push-checks.json` with each check, sorted by type, with its `reads` and its program's
absolute path in the home; a harness whose modules declare no check has no such file.

`scope_checks`, optional, declares the checks of the plan of an artifact type, the type that a
workflow of the stack names in `plan`. It is a map from the type to one command, a list:
the program, a file in the module as for `push_checks`, then its arguments. The command is
written to take `--base <start commit>` and `--artifacts <folder>` as its last arguments,
and it exits 0 when the plan holds. The compose declares and checks them, and `qory run` does not run them. Only a module of the run's base stack
counts: a module that the checkout's `qory.yaml` adds to the base declares none that counts, so the
repository under work cannot supply the check of its plan. A `scope_checks` in such a module has no
effect: the compose reads its form, as for any manifest, and uses nothing from it. When the `harness`
section of a `qory.yaml` defines the stack and extends none, every module of the section is a module of
the stack. A key that does not
match `^[a-z0-9]+(-[a-z0-9]+)*$` or is longer than 64 bytes, one type twice in one module, a
value that is not a list or is empty, a word of the command that is not a string, a list of lists
(`Give each plan type of scope_checks one command, a list of strings, not a list of lists.`
with `scope_checks.<type>[<n>] is a list.` on the next line), and a program that is empty,
absolute or has a `..` part fail the compose, each with the manifest's path. The compose also fails when two modules declare
one type, `Remove the scope check for build-plan from module core or web.` with `A
type has one scope check, and both modules declare one.` on the next line (when the first line
would have more than 98 characters, it is `Remove the scope check for the type from one of the
two modules.`, and the next line has the type and both module names, whole); when a program
is no file in the module, `Add the program of the scope check to its module, or change its
path.` with `Module core has no file at checks/check.js, the program of the scope check for
build-plan.`; when it is a folder or another thing that is no file, `Set the program of the
scope check to a file in its module.` with `checks/check.js of module core is not a file, and
it is the program of the scope check for build-plan.`; and when the program is in an entry
an `exclude` or an `only` of its module leaves out, `Keep the program of the scope check in
its module, or move it out of the left-out entry.` with `The program skills/tools/check.js
of the scope check for build-plan is in skills/tools, and an exclude or only of module core
leaves skills/tools out.` In the last three errors, line 1 has no module name, type or path,
so a long value never pushes it past 98 characters; the values are on line 2.

**Push checks.** A push check runs in a run behind a wall, before a push of its type
reaches Qory Apiary: for every push to the run API from inside the wall, through `qory
artifact push` or any other request, and for the version that the push step stores after
the agent. qory runs the program on the engine of the run's wall, in a new container from
the wall's default image, as the run's user, with no network and no capability. The
default image is `--image` or `wall.image` in `forager.yaml`, and not the image that the
run's policy selects, so a change to the repository, such as one to its `.devcontainer/`,
cannot turn the checks off. A run on a person's machine, with no run code, and with
neither `--image` nor `wall.image`, whose policy selects the image, runs the checks in the
run's image. The container runs under the `runtime` of the `wall.images` entry that the run's policy
selects, such as `runsc`, when that entry has one, so a check never runs under a weaker
runtime than the run's container. Otherwise it runs under the `runtime` of the default
image's entry, or the engine's default for an entry with none and for a default that is a
reference. A run from a run code with neither stops before the setup and the agent,
with exit 2: `Set wall.image in forager.yaml, then take the work item in again in Qory Apiary: push
checks run in the wall's default image; the code is used`. A run whose engine does not have the wall's default image stops before the setup and the agent,
with exit 2: `Pull the wall's default image onto this machine, and run again.`, and on the
next line `Push checks run in it, and the engine does not have the image <image>.` A run
whose engine's `docker info` does not list the runtime that the container runs under
stops there with exit 2 too: `Install the container runtime on the engine, and run again.`,
and on the next line `The push checks' container runs under the runtime <runtime>, and
the engine does not have it.` An engine whose `docker info` fails, or does not list
runtimes as Docker does, lets the run go on, and so does a runtime name that
Docker starts as a containerd shim although its list omits it: a name with a `.`, a part
before the first `.`, and no `/`, such as `io.containerd.runsc.v1`. A runtime name with a
`/` stops the run there with exit 2 too: `Fix the runtime name in wall.images, and run
again.`, and on the next line `The push checks' container runs under the runtime
<runtime>, and Docker accepts no runtime name with a "/".` The container has
no variable but the image's and those Docker sets: `HOME`, `HOSTNAME` and `PATH`. It has
the wall's CPU and memory limits of the run's containers, the wall's process limit or 512
processes, and the label of the run's containers, so the clean-up of the run removes a
container that a stopped qory left. The container gets three read-only folders:

- the home, at its path, so the program's absolute path in `push-checks.json` is valid;
- the working folder: the work item's artifact index, `index.json`; the work item's notes,
  `notes.json`, the `notes` array of the work item that `qory work-item show` prints,
  oldest first, or `[]` when the work item has none; and the newest version of each
  artifact of the type and of each `reads` type that the index does not mark
  `cancelled`, in the layout of `qory artifact pull`, `<type>/<name>/v<N>/<files>`; an
  artifact with no such version has no folder;
- the pushed files, under their names, whose paths follow the command's arguments.

In a run from Qory Apiary, from a run code or a run key, the container also mounts a copy
of the run's `inputs.json` read-only at the file's path, mode 0644 in a folder of mode
0755, so the run's user can read it, and the program gets `--inputs <path>` after the
command's arguments, before the pushed files' paths, such as `checks.js push spec --inputs
<in>/inputs.json /qory-push-check/files/spec.md`. Its member `qory.harness_extensions` is
the report's `extensions` (docs/run.md, §The run key from a run code). The file of a run
key has only the member `qory`, since that run has no run documents. The home of such a
run also contains `inputs.json` with the same bytes, mode 0644, which a hook reads as
`$QORY_HARNESS_HOME/inputs.json`. A person's run, also one that registers itself through
`gateway.server.via`, has no `inputs.json`, and its check gets no `--inputs`.

qory makes the working folder, the pushed files' folder and the copy of `inputs.json` in
the run directory, in a folder that only its user can list, and removes them after each
check.

A check that exits 0 lets the push go on with the bytes it checked. Any other exit stops
the push with 422 `push_check_failed` and `Fix each finding below and push again.`, and on
the next line `The check for <type> failed:`, with the findings: the lines of the program's standard output. When
standard output has none, the first finding is `The check exited with <N> and printed
nothing on stdout. Its stderr:`, and the lines of standard error follow it. When neither
has a line, the one finding is `The check exited with <N> and printed nothing.` Each
finding is without control characters, with the run's secrets masked, and at most 1,000
characters; a check has at most 200 findings. qory reads at most 64 KiB of each stream.

A program that does not start stops the push with 422 too: `Set wall.image or --image to
an image with node, such as one built FROM qory-agent, then run again.` and on the next
line `The check for <type> could not start: <error>.` when its `#!` line is for `node`,
else `Install the program of the check, then run again.`, then `The program is <path>.`
and `The check for <type> could not start: <error>.` on the next lines. A
program does not start when it exits with 126 or 127 and the last line of its standard
error is the container's init's line for the program, or ends with `not found`, `No
such file or directory` or `Permission denied` for the program or a word of its `#!`
line, such as `/usr/bin/env: 'node': No such file or directory`. Any other exit with 126 or 127, such as
one for a program that the check runs, is a check that failed. A program still running
after 30 seconds is stopped, with `Make the check for <type> finish within 30 s, and push
again.` and on the next line `It ran longer and was stopped.`

In a run whose home has push checks, a push that the check cannot read gets 422
`push_check_failed` too, before any check runs, whatever its type:

- a body that is not a push in the run API's form: `Send the push as qory artifact push
  sends it, and push again.` and on the next line `The body is not a push: <cause>.`;
- a member twice in one object, or one whose name differs from a member of the push only
  in case: `Send each member of the push once, in lower case, and push again.`;
- of a type with a check, a file's name that is empty, `.` or `..`, longer than 255 bytes,
  or contains a slash, a backslash or a control character, or one that is there twice:
  `Rename each file to a valid name, and push again.` and on the next line `<name> is not
  one: a name has at most 255 bytes, is not . or .., and has no slash, backslash, or
  control character.`, or `Give each file a different name, and push again.` and `<name>
  is the name of two files.`;
- of a type with a check, two files whose names differ only in case or in the form of an
  accent, on a machine whose file system keeps them as one file, such as macOS: `Give each
  file a different name, also when you ignore case and accents, and push again.` and on the
  next line `<a> and <b> are one file on this machine.`

When qory cannot run the check, the push stops with 503 `push_check_unavailable`, and qory
run writes the cause as a line on its stderr: the fetch of the artifacts fails, two files
of a fetched version are one file on this machine, the engine is not reachable or does
not create the container, the engine does not have the image of the checks, or a
temporary folder cannot be made. Node is needed only by a check whose program uses it: Qory's agent
image, and every image built FROM it, has it. A run without a wall, or without a control
plane, runs no check, and outside a run `qory artifact push` keeps the files in
`.qory/artifacts` and runs no check.

A module's tree contains these entry kinds:

```
AGENTS.md                        merged; the instruction section, read by every runtime
skills/<name>/SKILL.md           atomic; the Agent Skills layout, read by every runtime; may sit in folders that group skills, skills/<group>/<name>/SKILL.md
agents/<name>.md                 atomic; frontmatter name, description, paths, and the keys some runtimes read (model, tools, mode); body = system prompt
commands/<name>.md               atomic; frontmatter description; body = prompt, $ARGUMENTS for the arguments
output-styles/<name>.md          atomic; Claude Code only
hooks/<file>                     atomic; scripts that a settings fragment refers to as $QORY_HARNESS_HOME/hooks/<file>; files only
mcp/<name>.json                  atomic; one MCP server: the object a runtime's MCP configuration contains under the server's name
files/<runtime>/<path>           atomic; one file the runtime reads at <path> under its directory, any depth
settings/<runtime>/<file>       merged; one fragment per target file of one runtime, in that runtime's format
process.yaml                     a factory's process template, checked when exports.modules lists the module (§Exports); composed as one of the other files
<anything else>                  the module's other files, reached as $QORY_HARNESS_HOME/modules/<name>/<path>
```

`model` may be a tier, `${qory:light}`, `${qory:balanced}` or `${qory:thinking}`, which the compose replaces with the model id the tier maps to (§ The configuration). A tier with no map leaves the agent without a model, so it runs on the main model, and the compose prints a row.

`paths`, optional, lists the files of a change an agent reads, as globs from the checkout
root: `paths: ["**/*.py", pyproject.toml]`, or one glob as a string. A glob matches a path
from the checkout root with forward slashes: `**` matches any number of directories, `*`
and `?` match within one directory, every other character, `[`, `{` and `\` included, is
literal, and a glob without a slash matches at any depth only through `**/`. An agent without `paths` reads every change. The home's `roles.json`
contains the `paths` of each member of a group role (§References), for a script that
starts the members a change needs. qory leaves the key and its whole value out of every
agent file it writes for a runtime, since not every runtime accepts a key it does not
know. The compose checks the `paths` of every agent, and any other value fails it:
`Set paths to globs, such as ["**/*.py"], or remove it.` as line 1, then `The value is not
a glob or a list of globs.` and `The agent is in agents/python-reviewer.md of module core.`
on the lines after it.

A skill is a note when its frontmatter has `notes_for`: a role, or a list of roles, each
written as in `bind` without the kind, `developer` for `agents/developer`, a group role such
as `reviewers` too. A note also sets `paths`, one glob or a list of globs from the checkout
root, read as an agent's `paths` is read, with the grammar of that key above: `**` matches
any number of directories, `*` and `?` match within one directory, and every other
character, `[`, `{` and `\` included, is literal. A skill with `paths` and no `notes_for`
is no note. Technology modules and the modules of a repository write notes; a script
attaches the ones that match a change to the start of an agent that fills a role. The
compose checks both keys of every note it composes. A skill that an `exclude` or an `only`
leaves out is not checked. Each refusal has the fix as line 1, then the cause:

- a role that no agent fills, a role with no row in `roles.json`: `Set notes_for in
  python-naming of module python to a role the stack fills.` with `The stack fills no
  tester role.` and `The stack fills developer and reviewers.` on the next lines;
- a stack that fills no role: `Remove notes_for from python-naming of module python.` with
  `The stack fills no role.` on the next line;
- no `paths`, an empty list, an empty string, or a value that is no string or list of
  strings: `Add paths to python-naming of module python.` with `A note needs one path or
  more.`, or `A note needs one path or more, and the value is not a glob or a list of
  globs.`, and `The skill is in skills/python-naming/SKILL.md of module python.` on the next
  lines;
- a `notes_for` that is no role or list of roles: `Set notes_for in python-naming of module
  python to a role or roles.`

The home contains `role-notes.json`, always written, and not the `notes.json` of a push
check: `{"notes": [...]}`, one row per note in the order of the report's entries, each
`{name, roles, paths, module, layer}`: `name` is the skill's name, so its body is
`skills/<name>/SKILL.md` in the home; `roles` is `notes_for` as a list, each role as
`roles.json` writes it, `agents/<role>`; `paths` is the globs as written; `module` is the
module that ships the skill; `layer` is the number 0 for a module of the checkout's
`qory.yaml`, or of the stack composed when it extends none, and 1 for a module of the base
stack it extends. With no note the file is `{"notes": []}`. A home that an earlier qory
composed has no `role-notes.json`, and `--check` reports it until the next compose.

Below `skills`, a folder with `SKILL.md` is a skill, and everything below it is that skill's
files, a nested `SKILL.md` included. A real folder with no `SKILL.md` and with a folder below
it is a group of skills, at any depth, read by the same rules. qory writes each skill flat for
every runtime, at `skills/<name>` in the home and at `<name>` in each runtime's skills folder,
so a skill's name is its last folder. A group appears in no name: `modules[].exclude`, `only`,
`requires`, `bind`, `provides` and a reference (§References) all use `<name>`. Loose files in a
group are skipped. A skill refers to another skill by name with `${qory:skills/<name>}`, which
resolves to the registered name and gives no path. A relative path out of a skill's folder
resolves differently by how qory places the skill: as a link to the source folder, or as a copy.

A folder that a variant reads skills from composes when that variant is selected. These stop the
compose with status 2, each with the full path below the module:

- a folder with neither `SKILL.md` nor, apart from dot folders, a folder below it: `Add SKILL.md
  to the folder.` with `skills/review in module core has no SKILL.md and no skill below it.` on the
  next line
- a group with no skill anywhere below it, by its topmost folder: `Add SKILL.md to the
  folder, or put a skill in a folder below it.` with `skills/review in module core has no SKILL.md
  and no skill below it.` on the next line
- a folder named after a runtime, with no `SKILL.md`: `Rename the folder, or read it
  through a variant.` with `skills/tools/codex in module core is named after a runtime, and such a
  folder is not a group.` on the next line
- a folder that another variant reads skills from, with no `SKILL.md`: `Select the variant that
  reads the folder, or change that variant's skills.` with `skills/modern in module core is a
  folder that a variant reads skills from, and such a folder is not a group.` on the next line
- a link to a folder with no `SKILL.md`: `Point the link at a skill, or
  remove it.` with `The link skills/g/docs in module core leads to a folder with no SKILL.md.` on
  the next line
- a link that leads nowhere: `Point the link at a path that
  exists, or remove it.` with `The link skills/g/gone in module core leads nowhere.` on the next
  line
- two skills of one module with one name: `Rename skills/a/review or skills/b/review in module
  core.` with `Each skill goes to skills/<name>, and both are the skill review.` on the next line
- two skills of one module whose names differ only in case: `Rename skills/a/Review or
  skills/b/review in module core.` with `Each skill goes to skills/<name>, and these two names
  differ only in case.` on the next line

The same name in two modules is a collision, status 3, whose `exclude` lists `<name>`.

`AGENTS.md` and skills are read by every runtime. Agents and commands are written in each
runtime's format from the one file above; a runtime without the concept skips the kind and
the compose reports it. Every path an entry, a fragment or `AGENTS.md` resolves to, symlinks
followed, lies inside the module; a link that leaves the module fails the compose, so the
harness reads nothing the report does not list. Settings are runtime-native: `settings/claude/settings.json`,
`settings/codex/config.toml`, `settings/gemini/settings.json`, `settings/cursor/hooks.json`,
and so on, merged per file across modules.

`settings/codex/read-access.rules` is the one file under `settings/` that is not merged
into a target file. It contains Codex execpolicy rules in Starlark, such as
`prefix_rule(pattern=["rm"], decision="forbidden")`, for a run whose access is `read`.
The compose places it in no home. A walled read run on Codex writes it read-only to
`read-access.rules` in the `rules/` folder of the Codex home, and a run with any other
access gets none. When several modules ship the file, the run gets them joined in module
order. The rules stop no command: a command gets
past a prefix rule through `bash -c` or a path such as `/bin/rm`. The read-only mount of
the checkout is what keeps a read run from changing it.
Any other file under `settings/` that is neither `.json` nor `.toml`, another `.rules`
name included, fails the compose with `settings fragments are .json or .toml`.

`files/<runtime>/<path>` is for what a runtime reads from its directory beyond the kinds
above: Claude Code's `.claude/rules/*.md`, Cursor's `.cursor/rules/*.mdc`, Copilot's
`.github/instructions/*.instructions.md`. Each file is one entry, `<runtime>/<path>`,
linked to `<path>` under the runtime's directory in the checkout (§Runtimes), so two modules
shipping one path collide like two skills of one name and an exclude lists the path. A
path the runtime reads as settings, one qory writes, or one a kind is linked or copied at is
reserved and fails the compose, and so does a runtime qory does not render; a file
directly under `files/` fails, since the runtime segment decides where it lands. A
directory under `settings/<runtime>/` fails and the message points here. A file for a
runtime the stack does not target is left out, like a fragment for one.

An MCP server is one JSON object per file, in the shape Claude Code's `.mcp.json` contains under
`mcpServers`: `command`, `args` and `env` for a stdio server, `url` and `headers` for a
remote one, `type` for either, and `description` for a note to the module's readers, which
the compose leaves out of every runtime's file. Exactly one of `command` and `url`, and
no other key; the schema is [mcp.schema.json](mcp.schema.json). Every runtime with a project-level place for
servers gets it there, rewritten where its shape differs (§Runtimes). A server file that fails the check
fails the compose with the fix first, such as `Remove the key "comand" from the MCP server
file.`, then the cause, and `The file is mcp/db.json in module core.` on the last line. A directory under
`hooks/`, or a link to one, fails the compose: a hook is one file, and a script's helpers
live anywhere else in the module, such as under `scripts/`, reached through `modules/<name>` in
the composed tree. The refusal is `Move hooks/scripts out of the hooks directory of
module core.`, with `hooks/scripts is a directory, and a hook is one file.` and `A hook reads
its helpers at $QORY_HARNESS_HOME/modules/core/<path>.` on the next two lines.

The schema is [module.schema.json](module.schema.json).

### The role contract

A module that declares an `agents` role may give it a contract: a file inside the module
that lists the parts an agent needs to fill the role. The contract's path goes in the
role's map in `qory-module.yaml`: `roles: {agents: {developer: {fill: one, contract:
roles/developer.yaml}}}`. A group role takes a contract the same way, with `fill: many`.

```yaml
# roles/developer.yaml
sections: [Guardrails, The work, Stop conditions, Expected output]
front_matter: [name, description, tools, model]
tools: [Read, Edit, Write, Bash]
tier: true
report: {}
```

- `sections` are the level-2 headings the agent's body has, each the heading's text
  without the `##`, in any order. A heading inside a fenced code block does not count.
- `front_matter` are the front-matter keys the agent sets to a value that is not empty.
- `tools` are the tools the agent's `tools` key lists, as a comma-separated string or a
  list, each matched exactly: `Bash(git:*)` is not `Bash`. An agent without a `tools` key
  lacks every tool listed.
- `tier: true` requires the agent's model to be a tier, such as `${qory:balanced}`.
- `report` is the form of the agent's report, which the harness's checks read. The compose
  checks only that it is a map.

Every key is optional, so `{}` checks nothing. The schema is
[role-contract.schema.json](role-contract.schema.json).

After the composition rules hold, the compose checks every agent that fills the role
against each of the role's contracts: the agent the bind or a module's `provides` sets,
the base's agent included, or the agent of the role's name when no layer sets the role.
For a group role, it checks each member, and not a member that the checkout's bind
replaced. An agent that lacks parts fails the compose with one block per agent and
contract, which lists every part the agent lacks:

```
Complete agent python-developer of module python, or bind agents.developer to another agent.
  The contract roles/developer.yaml of module core requires these parts, which agents/python-developer.md lacks:
  - the heading "## Stop conditions"
  - the front-matter key model
  - the tool Bash
  - a tier in model, such as ${qory:balanced}
```

For a member of a group role, the fix line is `Complete agent python-reviewer, or remove
it from provides.agents.reviewers of module python.` The fix line ends with the list that
added the member: `bind.agents.reviewers in qory-stack.yaml` for a bind in the stack you
compose, and `bind.agents.reviewers in qory.yaml` (or `in harness.yaml`) for a bind in a
checkout's file. When the fix line would pass 98 characters, it ends with `remove it
from the list that adds it.`, and `This list adds it:` follows with the list on the next
line. When two or more lists add the member, the fix line is `Complete agent
python-reviewer, or remove it from the lists that add it.`, and `These lists add it:`
follows, with a line per list, before the contract's line:

```
Complete agent python-reviewer, or remove it from the lists that add it.
  These lists add it:
  - provides.agents.reviewers of module software-factory
  - provides.agents.reviewers of module python
  The contract roles/reviewer.yaml of module software-factory requires these parts, which agents/python-reviewer.md of module python lacks:
  - the heading "## Stop conditions"
```

A checkout does not edit the bind of the base stack it extends, nor the `provides` of a
base module. When one such list alone adds the member and a module of the checkout's
`qory.yaml` ships the agent, the fix line is `Complete agent python-reviewer in module
python.`, and the cause line names the list. When a base module ships the agent, the
block offers an exclude in `qory.yaml`, or asks the base stack's maintainer, and the
cause line names the list, the base stack, its pin and the contract. The fix line names
the agent's module when the line has at most 98 characters, and the cause line names it
otherwise:

```
Exclude agents/python-reviewer in qory.yaml, or ask the base stack's maintainer to complete it.
  The base stack software@4b8e2f1a9c7d binds python-reviewer of module factory to agents/reviewers, and the agent lacks these parts of the contract roles/reviewer.yaml of module factory:
  - the tool Read
```

When the line without the module still passes 98 characters, the fix line is `Exclude
the agent in qory.yaml, or ask the base stack's maintainer to complete it.`, and the
cause line names `agents/python-reviewer` and its module. A base module is named as the
checkout's `qory.yaml` lists it, by the name of the export when the base lists an export
whose manifest declares another name. The exclude is offered only when `qory.yaml` can
list it: the base's `extending.kinds` lists `agents`, and the checkout can name the
module. Otherwise the fix line is `Ask the base stack's maintainer to complete agent
python-reviewer.` For a base module's `provides`, the cause line starts `Module factory
of the base stack software@4b8e2f1a9c7d provides python-reviewer`. When such a list and
another both add the member, the fix line offers the exclude, or the change to the agent
where the checkout can make it, and the lists follow the missing parts:

```
Exclude agents/python-reviewer in qory.yaml, or complete it in module python.
  The contract roles/reviewer.yaml of module factory requires these parts, which agents/python-reviewer.md of module python lacks:
  - the tool Read
  These lists add it:
  - bind.agents.reviewers in the base stack
  - provides.agents.reviewers of module python
```

The compose reads each contract whether or not an agent fills the role. A contract file
that is missing fails it:

```
Add roles/developer.yaml to module core, or remove contract from roles.agents.developer.
  qory-module.yaml sets roles.agents.developer.contract to roles/developer.yaml, and the module has no file at that path.
```

So do a directory at that path, a link that leads out of the module, a path that passes
through more than 40 links, and a file that is not a contract. An absolute link fails
too, unless the home contains copies of the module's entries (`copied` in the report,
§The report) and the link stays inside the module. A file is not a contract when it is
not a map, or when it has a key other than the five, a list key that is not a list of
names on one line each with no name twice, a section that starts with `#`, a `tier` that
is not `true` or `false`, or a `report` that is not a map. Each refusal starts with its
fix, such as `Remove key paths from roles/developer.yaml of module core.`, and has its
cause on the next line.

## References

A document that dispatches another composed entry refers to it by a reference, and the
compose resolves the reference to the name the session registers the entry under, which
the module does not define: a program that loads the harness as a plugin puts the plugin's name
before every agent, skill and command, `harness:reviewer`, where the checkout's links and
most launch paths register the name as written. A reference is `${qory:<kind>/<name>}`,
the kind `agents`, `skills` or `commands`, the name one path segment:

```markdown
1. Dispatch ${qory:agents/coder} for the code.
2. Run ${qory:skills/test}, then dispatch ${qory:agents/reviewer}.
```

The compose reads a reference in an agent, a command, an output style, `AGENTS.md`, and
every `.md` file under a skill's directory at any depth. A reference to a skill in a group
is `${qory:skills/<name>}`, with its last folder alone. Anything else between `${qory:`
and `}` fails the compose, with the file and the reference in the message. Prose that
mentions an entry in passing stays prose; the marker identifies the strings that are
dispatched. The reference
becomes the registered name and nothing around it, so a document that wants the slash a
program puts before a skill writes `/${qory:skills/review}`.

`${qory:<tier>}`, with no slash, is a tier, not a reference. It is allowed only as the whole value of an agent's frontmatter `model`. Anywhere else, and with a name that is not a tier, it fails the compose.

A reference selects an entry of that name, or a role. A role is a name no entry has, set
to the entry that fills it: core writes `${qory:agents/coder}`, one stack binds `bind:
{agents: {coder: rails-coder}}` and another binds a different agent, and the same core
module serves both. A role's name is set in three layers, each replacing the name of the
one before: the `bind` of the stack a checkout extends, then the `provides` of the
composed modules (§The module manifest), then the `bind` of the checkout's `qory.yaml`,
or of the stack composed when it extends none. Every module's manifest is read before
the compose reads any module's entries, so an `only` of one module follows a role that
a later module provides, when the role has one name while the `only` blocks are
followed. While the `only` blocks are followed, a provided entry that the providing
module's `exclude` lists, or the exclude that a checkout's `qory.yaml` lists for it,
provides nothing. One that the providing module's `only` leaves out still counts: it
fills its role, or, when another module provides a different name, the role keeps the
name of the layer below, and the `only` blocks follow neither provider. To drop a
provider, list its entry under `exclude`. After the last module, a provided entry that
an `exclude` or an `only` left out provides nothing. Two modules that provide one role with different names fail the
compose, unless the checkout's `bind` sets the role: `Bind agents.developer in qory.yaml,
or compose one of typescript and elixir.` with `Both modules provide agents/developer:
typescript-developer, elixir-developer.` on the next line. A reference resolves within the stack's
composed entries and its roles, and nowhere else: it never brings in a module the stack
did not list, and never an entry a module's `only` or `exclude` left out. A reference is a requirement
(rule 4), so an entry needs no `requires` line for what its documents reference, and an
`only` follows references the way it follows `requires`.

A group role is an `agents` role that several agents fill, its members. A module declares
it with `many` under `roles` (§The module manifest), and each module adds its members
with a list under `provides`; a role such as `developer` that no module declares `many`
keeps one agent. The members are the base's `bind` list, then each module's `provides`
list in module order, each name once. The checkout's `bind` list replaces them. A member
that an `exclude` or an `only` leaves out is no member. With `software-factory` providing
`reviewers: [code-reviewer, test-reviewer]` and `python` providing `reviewers:
[python-reviewer]`, `Start ${qory:agents/reviewers}.` resolves to `Start code-reviewer,
test-reviewer, python-reviewer.`: the members' names at the path's address, joined by
`, `, in the order of the group's rows in `roles.json`. A reference expands anywhere in a
document's body, in a code span and in a fenced code block too. A reference to a group
resolves only in a document's body. In front matter it fails the compose: `Remove
${qory:agents/reviewers} from the front matter.`
with `A group resolves to a list.` and `The reference is in the front matter of
skills/review/SKILL.md of module core.` on the next two lines. A group with no member fails the compose
when a document references it: `Fill agents/reviewers, or remove the reference to it.` with `The group has no member.` and
`The reference is in skills/review/SKILL.md of module core.` on the next two lines. When the document is in a module of the base stack a checkout extends,
which the checkout does not edit, the fix is `Add a module to qory.yaml that provides
agents/reviewers, or ask the base stack's maintainer.`, with `skills/review/SKILL.md of
module core references agents/reviewers, and the group has no member.` on the next line.
When the base's `extending.kinds` does not list `agents`, a module of the checkout cannot
provide members, and the fix is `Ask the base stack's maintainer to fill
agents/reviewers.` The home contains
`roles.json`, written when the harness has a role: `{"roles": [...]}`, one row per role
and per member of a group, in the report's order, each `{role, agent, from}` as the
report has it, with `group: true` for a member and the member's `paths` (§The module
manifest) when its agent file sets them. A member the checkout's `bind` replaced is not in
it.

Each place a renderer puts a document in front of a program has an address, the name
the program registers an entry under when it reads it there, and the document is
placed with its references resolved at that address (§Rendering). A launch path whose
program renames the entries also gets the names written into the instructions it reads,
under a separate heading after the modules' text, so a session that meets an unmarked
name in prose finds the registered one; `qory harness launch --json` prints the same
names under `addresses` (§Launching). A module's text is never rewritten for one
runtime: the resolution happens in the composed tree, per path.

## Composition rules

1. **One flat tree, one entry per name.** Every atomic entry links into `<kind>/<name>` from
   the module that provides it, a file into `<runtime>/<path>`.
2. **Excludes first, then the collision check.** After each module's `exclude` or `only`
   is applied, an atomic name provided by more than one module fails the compose. The error lists every
   module and the excludes that resolve it; this is its text, which the fixtures contain, and
   `qory harness compose` prints the same as a table under a `Fix` heading with the
   stack lines to paste:

   ```
   Keep skills/test in one module, and exclude it from the others.
     3 modules provide skills/test: core@working-tree, nextjs@working-tree, team@working-tree
     For example:
       core:   exclude: {skills: [test]}
       nextjs: exclude: {skills: [test]}
   ```

   There is no last-wins and no rename.
3. **An exclude or only that selects nothing fails**, so a module that stops shipping an
   entry, an instruction section, a fragment or a variable is noticed rather than silently
   composed. The fix line has the name, the block and the module, such as `Remove
   skills/nope from the exclude of module core.`, and the next line shows what the module
   lacks. An `only` brings in
   what its listed entries require from the module, transitively, and an `exclude` beside
   it may list only those; one listing anything else fails the same way.
4. **A requirement that is not composed fails.** After the excludes and the collision
   check, every composed entry's `requires` items from its manifest and the references in
   its documents (§References) are checked against the composed entries, a reference to
   a role against the entry that fills it. One that is missing fails the compose
   with `Keep command ship in module core, or remove it from the requires of skill
   deploy.` and `The skill deploy of module core requires command ship, and module core
   leaves it out.` on the next line when an `exclude` or an `only` dropped it, with the
   module whose block did, and with `Ship command ship in a module, or remove it from the
   requires of skill deploy.` and `The skill deploy of module core requires command ship,
   and no module ships it.` otherwise. For a reference that neither an entry nor a role
   matches, the fix is `Ship agent coder, bind agents.coder in qory.yaml, or remove the
   reference from skill deploy.`, with `The skill deploy of module core references agent
   coder. No module ships or provides it, and the stack does not bind it.` on the next
   line. When a module provides the role with an entry that an `exclude` or an `only`
   left out, the message has one refusal per such module: `Keep agent core-developer in
   module core, or bind agents.developer to another agent.`, then `The skill deploy of
   module core references agent developer, which module web provides as agent
   core-developer.` and `Module core leaves out agent core-developer.` on the next two
   lines. When the exclude a checkout's `qory.yaml` lists for a base module dropped it,
   the fix is `Remove commands/ship from the exclude of module core, or ship it in a
   module of the repository.`, then `The skill deploy of module core requires command
   ship.` and `The exclude of module core in qory.yaml leaves command ship out.` on the
   next two lines, and the fix is `Remove <kind>/<name> from the exclude of module <m>.` with
   `You can also ship it in a module of the repository.` on the next line when the first
   line would pass 98 characters; for a provided entry, the middle line ends `which module ts provides
   as agent ts-developer.` A binding is checked first: one that selects an entry that is
   not composed fails with `Ship agent rails-coder in a module, or change bind
   agents.coder.` and `The bind agents.coder selects agent rails-coder, and no module
   ships it.` on the next line, or `Keep agent rails-coder in module rails, or change
   bind agents.coder.` and `The bind agents.coder selects agent rails-coder, and module
   rails leaves it out.`, and a role whose name an entry has fails with `Rename role
   agents.reviewer in bind, or rename agent reviewer in module core.` and `The bind
   defines role agents.reviewer, and module core ships agent reviewer: a role and an
   entry cannot share a name.` on the next line, since such a role
   cannot be rebound. A role a module provides is checked the same way, with `Ship agent
   typescript-developer in a module, or remove provides.agents.developer from module
   typescript.` and `No module ships agent typescript-developer.` on the next line, and `Rename role
   provides.agents.reviewer in module typescript.` with `Module core ships agent
   reviewer, and a role's name is no entry's.` on the next line. Each member of a group role is checked the
   same way, with `Ship agent python-reviewer, or remove it from
   provides.agents.reviewers of module python.` and `No module ships agent
   python-reviewer.` on the next line, the fix ending with the list that added the member
   as in §The role contract. When two or more lists add it, the fix is `Ship agent
   python-reviewer, or remove it from the lists that add it.`, then `No module ships
   agent python-reviewer, and these lists add it:` and a line per list, and a single list
   that would make the fix line pass 98 characters goes on the next line after `No
   module ships agent python-reviewer, and this list adds it:`. When the bind of the base
   stack a checkout extends or the `provides` of a base module adds the member, which the
   checkout does not edit, the fix is `Ship agent python-reviewer in a module of
   qory.yaml, or ask the base stack's maintainer to ship it.`, or `Ship the agent in a
   module of qory.yaml, or ask the base stack's maintainer to ship it.` when that line
   would pass 98 characters, since an agent that a module of `qory.yaml` ships fills the
   member, with the lists on the lines after `No module ships agent python-reviewer, and
   this list adds it:`. When the base's `extending.kinds` does not list `agents`, the fix
   is `Ask the base stack's maintainer to ship agent python-reviewer.` A member that only
   a bind adds and that a module leaves out fails with `Compose agent python-reviewer, or
   remove it from bind.agents.reviewers in qory-stack.yaml.` and `Module python leaves
   out agent python-reviewer.` on the next line. Every failure of this rule comes at
   once, each on its line, or with its cause on indented lines after the fix: the
   workflow blocks first (the next paragraph), then the roles, then the entries in their
   sorted order, so one compose lists every site there is to fix. An entry that was left
   out has no requirements to meet. A manifest whose `requires` lists an entry the module
   does not ship fails as the module is read: `Ship skill release in module core, or
   remove it from the requires of the module.` with `The requires of module core lists
   skill release, and the module does not ship it.` on the next line.

   A run starts a workflow of the stack at its skill or command (§The stack), and that
   entry starts each agent its documents reference. A reference of that entry to an agent
   that no module ships or provides, that the stack does not bind and that no `exclude` or
   `only` left out fails the compose with the role and the workflow. Each such pair of an
   entry and a reference is a block of two lines, which replaces this rule's refusal for
   the pair; the blocks come first among this rule's failures:

   ```
   Add a module that fills agents/demo-author, or drop capture-demos from the workflows and process.
     The base stack software@working-tree declares the workflow capture-demos. Its skill workflow-capture-demos of module software-factory starts agents/demo-author, and no module ships or provides that agent, and the stack does not bind it.
   ```

   Every workflow of a factory's process is a workflow of its stack (§A factory's
   process.yaml), so this covers what the process lists. A stack that has no module to fill
   such a role excludes the workflow's skill and lists the workflow in neither its
   `workflows` nor its process.
5. **Merged kinds join, and a value is set once.** A settings target file merges across
   the modules that ship a fragment for it, JSON or TOML by extension: objects deep-merge,
   `permissions` lists concatenate and deduplicate, `hooks` arrays concatenate. Every
   other list and every scalar is set by one module; another module may repeat the value and
   may not change it. Two fragments setting one key path to different values fail the
   compose with `Set <dotted.key.path> to one value in both modules.` and `Modules <a> and <b> set
   <dotted.key.path> to different values.` and `<dotted.key.path> is in settings/<runtime>/<file>.` on
   the next two lines. A top-level `env` key the configuration sets takes the
   configuration's value and never collides; an `env` key a fragment sets and a module
   manifest exports with a different value fails with `settings/<runtime>/<file>:
   env.<name> is set by module <a> and exported by module <b> with different values`,
   unless the configuration sets it. `AGENTS.md` is the concatenation of the modules' files in module order, separated
   by a blank line, the one place the order of `modules` decides anything. An MCP server is
   atomic and follows rules 1 to 4; where a settings fragment also defines servers, the
   composed servers are written on top.
6. **Variants resolve per module.** The forced `variant`, else the one with
   `target.runtime`'s name, else the manifest's `default`, else the module root when the manifest
   declares no variants. A manifest with variants, none for the runtime and no default, or
   `default: fail`, fails the compose.
7. **Everything is reported.** The report lists every module with its pin, every entry with
   its module and, for one an `only` brought in, the entry that required it, the
   references each entry's documents contain, the roles with the layer that set each, every entry
   and part a block left out, as `<kind>/<name>`, `instructions/AGENTS.md`,
   `settings/<runtime>/<file>` or `env/<NAME>`, and every variant chosen. `qory harness
   inspect` prints it.
8. **Nothing composed is committed.** The link in the checkout is excluded through the
   clone-local exclude file, never the repository's ignore file.

## Rendering

The composed tree, the home, is written into the checkout at `.qory/harness`, beside its
report `.qory/harness-report.json`, and `.qory` is excluded through the clone-local
exclude file. `.qory` must be a real directory or absent: a symlink or a file there,
which a repository could commit, is refused, because everything qory writes and removes
goes through it. A home inside the checkout needs a git working tree. A folder that is not
one composes only with the home outside it.

The home can stand outside the checkout instead: `harness.home` in the configuration, or
`--home <dir>` on `compose`, `inspect`, `remove` and `launch`, sets a directory outside
the checkout, and the tree goes under it at `<dir>/<name>-<digest>`, the checkout's base
name and eight hex digits of the SHA-256 of its real path, one home per checkout and per
worktree, with the report beside the tree as `<home>-report.json`. Such a compose writes
nothing into the checkout: no link, no `.qory`, no exclude line, and a tracked path at a
link's name is no collision, since no link is written. A runtime reads such a home
through its launch spec (§Launching). A relative value is under the checkout root, and
the one value allowed inside the checkout is `.qory/harness`. `harness.links: none`, or
`--no-links`, keeps the checkout untouched with the home inside it too: `.qory` is
written and excluded, and nothing else; a compose with links off removes the links a
previous compose wrote. `harness.links: checkout`, the default inside, is refused with a
home outside the checkout. The report records the checkout the home was composed for, so
`inspect`, `remove` and `--check` find the pair from either side: standing in the
checkout, or anywhere with `--home` set to the tree. `qory worktree add` composes each
worktree into a separate home under the directory, and `qory worktree remove` removes it
with the worktree.

The tree contains the runtime-agnostic parts once, `AGENTS.md`, `skills/` and `hooks/`, one
link per module at `modules/<name>` to the module's directory, and one directory per
runtime with that runtime's files. In a home outside the checkout, `modules/<name>` of a
module whose directory lies in the checkout, as written or resolved, is a copy of the
directory without `.git`, under the same rules as the entries below. Every atomic entry is
a symlink into its module, with two exceptions. In a home outside the checkout, every
entry of a module whose directory lies in the checkout, as written or resolved, is a copy:
files and directories, each link inside followed within the module. A copy has the
original's permission bits without setuid, setgid, sticky and write for group and others,
and a directory also has read, write and search for its owner.
The other exception: an entry whose documents contain a reference (§References) is
placed with the references resolved for the path it stands on, a single file written, a
skill a real directory containing a link per file and directory without a reference and a
written copy of each `.md` file with one, so the module's files stay live wherever nothing
had to change, and an edit to a file with a reference reaches the tree on the next
compose, which `--check` reports. The shared root, every checkout link and every launch path but
one resolve to the bare name, the name as the module wrote it; the `claude` plugin
resolves to `harness:<name>`, the name Claude Code registers a plugin's entries under,
and the plugin's copies of the same documents differ from the checkout's in that alone.
Every
`$QORY_HARNESS_HOME`, braced or not, in a settings fragment or an MCP server becomes the
tree's absolute path, so `$QORY_HARNESS_HOME/modules/core/scripts/check.sh` runs the script
the core module ships. The links survive a moved checkout; the absolute paths written into
settings do not, so a checkout that moves is composed again.

A tree contains every runtime the checkout is composed for, so one checkout serves two agents
at once and a person can stop working in one and continue in the other. A compose renders
the targeted runtimes and every runtime already in the tree, because the tree is replaced
whole, and each runtime's links keep resolving. A module whose variants differ between two
targeted runtimes cannot be rendered for both, since the tree contains one copy of each
entry: the compose refuses and lists both runtimes.

The checkout gets relative links into the tree, listed per runtime below, each excluded
through the clone-local exclude file, so every path a runtime checks resolves inside the
project. A link to a file, such as `AGENTS.md`, is one
symlink. A link to a directory, such as `.claude`, is a real directory in the checkout
containing one symlink per entry of the tree's directory, `.claude/skills` and
`.claude/settings.json` alike, so the checkout's files there stay: Claude Code's
`.claude/settings.local.json` survives every compose, and a repository may keep its
agents under `.github/agents` beside the linked ones. `.claude/rules` is a second
directory link of this kind, so the rules a repository keeps there stay beside
`qory-harness.md` and the rules the modules ship. Any other directory a module's files make
in the tree is one such symlink, like `.claude/skills`: a repository that keeps files there
moves them into a module, under `extends` into a module it appends, with that directory in
the base's `files` list. A compose also removes what a
previous one linked and this one does not. `qory harness remove --runtime <name>` takes one
runtime's links and leaves a link another composed runtime shares, such as `.agents/skills`.
A link is written, pruned or removed only where the checkout is: a directory on the way
that is itself a symlink, such as a committed `.github` link, is passed over for a soft link
and refused for a hard one, and nothing behind it is touched. An exclude line goes with
its link, and the `.qory` line with the directory, so the exclude file hides no path the
repository adds there; worktrees of one repository share one exclude file, so a remove in
one worktree drops lines the links of another use; that worktree's next compose writes
them again.

A module's `link` is a hard link at the checkout root to `modules/<name>` in the tree,
written after the runtimes' links, excluded, pruned when the stack drops it, and
refused when it is a path a runtime links or `.qory`. With links off it is not
written, and the compose reports it; a script that reaches the module through the
environment the module exports, such as `HARNESS_HOME`, finds it wherever the home is, since
every launch sets the variable as the module's absolute path in the home. Hard, because the permission
rules and scripts of the harness depend on that path: a file or a foreign link there
fails the compose with status 4, and `--force` replaces it when git can restore it.
`qory harness remove` takes every link at the checkout root into `modules/`, with or
without a report, so a `.qory` deleted by hand leaves no link behind.

The links listed below are the full set; a link to a file the compose did not produce is
left out, `AGENTS.md`, `AGENTS.override.md` and `GEMINI.md` without instructions,
`.claude/rules` without instructions and without a module's `files/claude/rules/` entry,
`.mcp.json` without a server or a `settings/claude/mcp.json` fragment, `opencode.json`
without a model, a server or a fragment.

A path qory did not write is never replaced. Where a hard link goes, such as
`.claude/settings.json`, it fails the compose; where a soft link goes, such as `AGENTS.md`,
it is skipped and reported, so a repository's instructions stay as they are. With
`--force`, a file or directory that git tracks and that is unmodified is removed for the
link, either kind, and listed in the report under `replaced`; `git checkout --` brings it
back, and `qory harness remove` prints that command. Anything untracked or modified, and a
directory containing an untracked or ignored file, is refused, because git cannot
restore it. A compose that fails after replacing something has written the report already,
so the hint is not lost.

## Runtimes

| `target.runtime` | Program | Links in the checkout | Model written to | MCP servers written to | A fragment's variables, set in the launch | Files land in | Kinds with no place |
|---|---|---|---|---|---|---|---|
| `claude` | Claude Code | `.claude`, `.claude/rules`, `.mcp.json` | `.claude/settings.json` `model` | `.mcp.json` `mcpServers` | `settings/claude/settings.json` `env` | `.claude/<path>` | none |
| `codex` | Codex CLI | `.codex`, `.agents/skills`, `AGENTS.override.md` | `.codex/config.toml` `model` | `.codex/config.toml` `mcp_servers` | `settings/codex/config.toml` `shell_environment_policy.set` | `.codex/<path>` | commands, output-styles |
| `gemini` | Gemini CLI | `.gemini`, `GEMINI.md` | `.gemini/settings.json` `model.name` | `.gemini/settings.json` `mcpServers` | none | `.gemini/<path>` | output-styles |
| `opencode` | OpenCode | `.opencode`, `.agents/skills`, `opencode.json`, `AGENTS.md` | `opencode.json` `model` | `opencode.json` `mcp`, in OpenCode's shape | none | `.opencode/<path>` | output-styles |
| `cursor` | Cursor, Cursor CLI | `.cursor`, `.agents/skills`, `AGENTS.md` | not written; a global CLI setting | `.cursor/mcp.json` `mcpServers` | none | `.cursor/<path>` | commands, output-styles |
| `copilot` | GitHub Copilot CLI | `.github/agents`, `.github/hooks`, `.agents/skills`, `AGENTS.md` | not written; a user setting | `copilot/mcp.json` `mcpServers`, for a launch (§Launching); not linked, a user file otherwise | none | `.github/<path>`, one soft link per file | commands, output-styles |
| `amp` | Amp | `.amp`, `.agents/skills`, `AGENTS.md` | not written; Amp picks by mode | `.amp/settings.json` `amp.mcpServers` | none | `.amp/<path>` | agents, commands, output-styles |
| `goose` | Goose | `.agents/skills`, `.agents/agents`, `AGENTS.md` | not written; no project file | not written; a user file | none; no launch | nowhere | commands, output-styles, mcp, files |
| `any` | any program that reads `AGENTS.md` and `.agents/skills` | `.agents/skills`, `AGENTS.md` | not written | not written | none; no launch | nowhere | agents, commands, output-styles, hooks, mcp, files |

No file a runtime reads holds the harness's environment; the launch sets it (§Launching).
The variables a runtime's settings fragments set, in the column above, come first, the
variables the modules export over them, and the configuration's `env` over both.
`QORY_HARNESS_HOME`, the tree's absolute path, is apart from all of them. A runtime without
a launch, `goose` and `any`, gets none of them.

A `files/<runtime>/<path>` entry may not use a path the runtime reserves under its
directory, whatever stack composes it: the files qory writes, the directories a kind is
linked or copied at, and every file the program reads as settings, which a fragment under
`settings/<runtime>/` sets instead. A directory reserves what is below it, by path
segment. The refusal lists the module, the entry and the reason, with status 2:

| `target.runtime` | qory writes | The program reads as settings | A kind is linked or copied at | Runs without the agent |
|---|---|---|---|---|
| `claude` | `rules/qory-harness.md`, `settings.json`, `mcp.json`, `plugin`, `launch`; `rules` as a file, since qory links that directory one file at a time | `settings.local.json` | `skills`, `agents`, `commands`, `hooks`, `output-styles` | |
| `codex` | `config.toml`, `AGENTS.md` | | `agents`; `skills`, copied as folders and regular files | |
| `gemini` | `settings.json` | | `skills`, `hooks`, `agents`, `commands` | |
| `opencode` | `opencode.json` | | `commands`, `hooks`, `agents`, `skills` | |
| `cursor` | `plugin` | `mcp.json`, `hooks.json`, `cli.json`, `environment.json` | `agents`, `hooks` | |
| `copilot` | | | `agents`, `hooks` | `workflows` |
| `amp` | | `settings.json` | | |
| `goose` | | | `agents` | |
| `any` | | | | |

`claude` also reserves `CLAUDE.md` and `AGENTS.md`. qory does not write them under
`.claude`, and a module ships its instructions as `AGENTS.md` at the module root.

`codex` reserves `auth.json`, Codex's login file, which no home of qory's has. The refusal
is ``Remove `files/codex/auth.json` from module <name>.`` and the next line ``A Codex run
takes its model key from the run, not from a file.``

`codex` also reserves `hooks.json`, `.env`, `environments.toml`, `AGENTS.override.md` and
`rules/read-access.rules`: compose refuses a module that ships one of them under
`files/codex/`. qory writes `hooks.json`, `.env`, `environments.toml` and
`AGENTS.override.md` in the Codex home of every walled Codex run, and the refusal is
``Remove `files/codex/<path>` from module <name>.`` and the next line ``qory writes it for
every walled Codex run.`` qory writes `rules/read-access.rules` in the Codex home of a
walled read run whose harness ships `settings/codex/read-access.rules`, and the refusal is
``Remove `files/codex/rules/read-access.rules` from module <name>.`` and the next line
``qory writes it for a walled Codex run that only reads, from the harness's
`settings/codex/read-access.rules`.`` Other files under `rules/` stay allowed. Each skill
under `codex/skills/` is copied within the limits of a copy of one module entry and leaves
out `.git`. A walled Codex run's copy of `rules/` and `agents/` takes at most 10,000
files, directories and links, 1 GiB in all and 64 MiB a file.

Per runtime, the files written into its directory:

- **claude**: links for every kind, `settings.json` with the model and without `env`,
  whose variables are the launch's, `mcp.json` with the servers, linked as `.mcp.json` at the checkout root, and
  `rules/qory-harness.md`, a relative symlink to the tree's `AGENTS.md`, or a copy of it in
  the tree of a hidden harness, linked as `.claude/rules/qory-harness.md`. qory writes no `CLAUDE.md`. Claude Code loads a rule
  without `paths` frontmatter at launch, beside a repository's `CLAUDE.md`, and a rule
  does not hide a repository's `AGENTS.md`, as a `.claude/CLAUDE.md` does. The rule is a
  link with no `@AGENTS.md` import, because Claude Code resolves a link to its real path
  and prompts about an import found through one on every start. `plugin/` is the launch
  spec, a plugin in Claude Code's layout, and `launch/AGENTS.md` the instructions a launch
  reads, with the plugin's names and the names the session registers after the modules'
  text (§Launching); neither is linked into the checkout, whose
  `.claude/rules/qory-harness.md` and `.claude/agents` register the bare names.
- **codex**: `config.toml` with the model, the servers as `[mcp_servers.<name>]` tables,
  `features.unified_exec_tty = false`, `web_search = "disabled"`, a `hooks.state."<file>:<event>:<group>:<hook>"`
  table with the `trusted_hash` of each `command` hook of `hooks.json` and `config.toml`
  that has no U+2028 or U+2029 in its command, status message or matcher, `<file>` being
  the file in the home's `codex` folder with links resolved and each name in the case on
  disk, and any other `settings/codex/` file, without `shell_environment_policy.set`,
  whose variables are the launch's, one `agents/<name>.toml` per agent with the body as
  `developer_instructions`, with the tier as `model` and `model_reasoning_effort` in a
  home composed for codex alone, with `[features]` `shell_tool = false` when the agent's
  `tools` key lists no `Bash` or `PowerShell` (an agent with no `tools` key keeps the
  shell; Codex 0.149.0 or later reads this setting), and for a launch the skills copied
  under `skills/` as folders and regular files, since Codex 0.167.2 skips a `SKILL.md`
  that is a link, and the instructions as `AGENTS.md`, neither linked into the checkout
  (§Launching). Codex passes its environment to every command it runs. Codex 0.76.0 or
  later filters no name by default. An older Codex drops the names that contain `KEY`,
  `SECRET` or `TOKEN`, unless a fragment's `shell_environment_policy` sets
  `ignore_default_excludes = true`. Codex reads `AGENTS.override.md` before `AGENTS.md`,
  and a project `.codex` only in a trusted project. A fragment that sets `model_provider`,
  `model_providers`, `projects`, `hooks.state`, `features`, `analytics`, `otel`, `notify`,
  `web_search`, `feedback.enabled`, `check_for_update_on_startup`, `openai_base_url`,
  `chatgpt_base_url`, `responses_api_metadata`, a key of the form
  `experimental_realtime_*_base_url`, `profile`, `profiles`, `cli_auth_credentials_store`,
  `allow_login_shell`, `model_instructions_file`, `experimental_compact_prompt_file`,
  `model_catalog_json`, `js_repl_node_path`, `sqlite_home` or `log_dir` fails the compose
  with ``Remove the reserved key from `settings/codex/config.toml`.`` and the next line
  ``qory reserves `<key>`; a Codex run takes no value for it from the harness.``, with
  each control character of the key written as an escape, such as `\n`. A walled run whose
  Codex arguments before `--` set one of these keys, `model_reasoning_effort`, `skills` or
  `cloud.skills`, a key under one, or a table above one, such as `cloud`, with `--config`,
  `--config=`, `-c`, `-c<key>=<value>` or `-c=<key>=<value>`, stops before the agent
  starts with ``Remove the `<flag>` flag and its value from the run's Codex arguments.``
  and the next line ``In this run, qory reserves `<key>`; the run takes no value for it
  from the arguments.``, where `<flag>` is `-c` or `--config` as typed. For a table above
  a reserved key, such as `cloud`, the next line is `` `cloud` is a table that contains
  `cloud.skills`, which qory reserves in this run. `` A fragment may still set
  `model_reasoning_effort`. Such a run also stops on `--oss` or `--local-provider` before
  `--`, with ``Remove `<flag>` from the run's Codex arguments.`` and the next line ``A
  Codex run takes its model provider from qory and Forager, not from the arguments.``, and
  on `--profile` or `-p`, with ``Remove `--profile` from the run's Codex arguments.`` and
  the next line ``A Codex run takes its settings from qory and Forager, not from a
  profile.``, and on `--enable` or `--disable` with every feature other than `hooks`,
  `codex_hooks` and `unified_exec_tty`, which Codex reads as `--config
  features.<feature>=true` or `false`, with ``Remove the `--enable` flag and its value
  from the run's Codex arguments.`` and the next line ``A walled Codex run takes no
  feature from its arguments, `<feature>` included.`` (`--disable` likewise). It stops on
  `--ignore-user-config` with ``Remove `--ignore-user-config` from the run's Codex
  arguments.`` and the next line ``A Codex run takes its settings from qory and Forager,
  not from the arguments.``, and on `--search` with ``Remove `--search` from the run's
  Codex arguments.`` and the next line ``A Codex run takes its web search setting from
  qory and Forager, not from the arguments.`` It stops on `--remote` or
  `--remote-auth-token-env`, also with `=`, with ``Remove `--remote` from the run's Codex
  arguments.`` and the next line ``A Codex run takes its app server from qory and Forager,
  not from the arguments.`` (`--remote-auth-token-env` likewise). Every Codex run, walled
  or not, stops before the agent starts on `--dangerously-bypass-hook-trust`, with
  ``Remove `--dangerously-bypass-hook-trust` from the run's Codex arguments.`` and the
  next line ``A Codex run takes its hook trust from qory and Forager, not from the
  arguments.``; on `--trust`, also with `=`, with ``Remove `--trust` from the run's Codex
  arguments.`` and the next line `` `--trust` saves trust for a project folder with no
  trust setting, and Codex then reads the folder's `.codex/config.toml`, which can set
  `features.hooks = false`. ``; on a `--config` or `-c` key at `hooks`, `features.hooks`,
  `features.codex_hooks`, `features.unified_exec_tty` or `projects`, under one, or
  `features`, with the `--config` lines; and on `--enable` or `--disable` of `hooks`,
  `codex_hooks` or `unified_exec_tty`, with ``Remove the `<flag>` flag and its value from
  the run's Codex arguments.``, where `<flag>` is `--enable` or `--disable` as typed, and
  the next line ``In this run, qory reserves `features.<feature>`; the run takes no value
  for it from the arguments.`` A key or feature in these lines has each control character
  written as an escape. A hook in `settings/codex/config.toml` or
  `settings/codex/hooks.json` whose `timeout` or `additionalContextLimit` is negative or
  fractional, such as 2.0, fails the compose with ``Set `<key>` of the `<event>` hook to a
  whole number of 0 or more.`` and the next line ``The hook is in the harness's
  `settings/codex/<file>`, and Codex ignores the whole file otherwise.`` A hook group or a
  hook there that Codex 0.170.0 cannot decode, such as a `command` hook with no `command`
  text or a `type` other than `command`, `mcp_tool`, `prompt` and `agent`, fails the
  compose with ``Fix `hooks.<event>[<group>].hooks[<hook>]` in `settings/codex/<file>`.``,
  the indexes counted from 0, and the next line ``<cause>, and Codex reads no hook of a
  file with such a hook.`` Codex has no allow list of skills, so the
  `Skill(harness:<name>)` rules in `permissions.allow` of `settings/claude/settings.json`
  are the Codex list too. In the home of a walled Codex run, `config.toml` disables every
  other skill by name, with a `[[skills.config]]` entry with `enabled = false` after any
  entry the fragment has: each skill of the home's `skills/` and of the checkout's
  `.codex/skills` and `.agents/skills`, by its frontmatter `name` and its folder's name,
  and after the name of each plugin above it. It also sets `skills.bundled.enabled =
  false` and `cloud.skills.enabled = false`. Any other rule with a pattern, such as
  `Skill(harness:review-*)`, allows no skill. Every skill of the checkout's
  `.codex/skills` and `.agents/skills` also gets a `[[skills.config]]` entry with its
  `SKILL.md` path in the wall and `enabled = false`, after the name entries, so a checkout
  skill with the name of an allowed harness skill is disabled too. `Skill(harness:*)`
  allows every skill of the harness home, so none of them gets a `[[skills.config]]`
  entry; a skill of the checkout with another name still gets one. A harness with no
  `Skill(harness:<name>)` rule gets no skill on Codex. qory does not cover a skill that
  the agent writes during the run, or one the agent reads as a file with shell commands;
  the wall is the limit.
- **gemini**: `settings.json` with `model.name` and `mcpServers`, links for skills and hooks,
  one `agents/<name>.md` with `name` and `description`, one `commands/<name>.toml` with
  `$ARGUMENTS` as `{{args}}`. Gemini reads a project `.gemini` only in a trusted folder.
- **opencode**: links for commands and hooks, `agents/<name>.md` with `description`, `mode`
  and `model`, `opencode.json` when a module ships one, the stack sets a model or the
  compose contains a server, and for a launch the skills linked under `skills/`, not linked
  into the checkout (§Launching). A server with `url` becomes `{type: remote, url, headers}`, any
  other `{type: local, command: [command, args...], environment: env}`.
- **cursor**: `agents/<name>.md` with `name`, `description` and `model`, links for hooks,
  the `settings/cursor/` files such as `hooks.json`, `cli.json` and `mcp.json`, the last
  with the servers under `mcpServers`, and `plugin/`, the launch spec, never linked into
  the checkout (§Launching).
- **copilot**: `agents/<name>.agent.md` with `name`, `description`, `tools`, `model` and,
  for an agent whose tier's entry sets an effort, `reasoning-effort`, each
  `settings/copilot/<name>` file written under `hooks/` as `qory-<name>`, so
  `settings/copilot/hooks.json` reaches `.github/hooks/qory-hooks.json`, and for a launch
  `workspace/.github` with the skills and agents, and `mcp.json` with the servers
  (§Launching). Copilot CLI loads every `*.json` file in `.github/hooks`, so a
  repository's `.github/hooks/hooks.json` runs beside the harness's hooks and stays as
  the repository has it. qory passes over a file at a `qory-` name that it did not write,
  and lists it under `kept`.
- **amp**: the `settings/amp/` files such as `settings.json`, with the servers under
  `amp.mcpServers`.
- **goose**: `agents/<name>.md` with `name`, `description` and `model`.
- **any**: nothing.

An agent's tier is written as `model` and `effort` in a claude agent, and as `model` and `reasoning-effort` in a copilot agent. The effort is one of `low`, `medium`, `high`, `xhigh` and `max` for claude, and one of `minimal`, `low`, `medium`, `high`, `xhigh` and `max` for copilot; another value fails the compose with exit 2. Copilot CLI reports a level the agent's model does not offer and leaves it unapplied. Cursor and opencode take an agent's effort only inside `model`, as `<id>[effort=<level>]` and `<provider>/<model>#<variant>`, which qory does not write: a tier is written as `model` alone for cursor, opencode and goose, and the agent runs at the session's effort, with a `skipped` row that lists the agents. A tier is written as `model` and `model_reasoning_effort` for codex in a home composed for codex alone, the effort one of `none`, `low`, `medium`, `high`, `xhigh`, `max` and `ultra`, another value failing the compose with exit 2. In a home composed for codex and another runtime, a tier is not written for codex, since one tier map serves every runtime, with a row. A tier is not written at all for gemini, with a row. On every compose for claude, qory refuses `env` in a `settings/claude/settings.json` fragment that sets `ANTHROPIC_MODEL`, `ANTHROPIC_DEFAULT_OPUS_MODEL`, `ANTHROPIC_DEFAULT_SONNET_MODEL`, `ANTHROPIC_DEFAULT_HAIKU_MODEL`, `ANTHROPIC_DEFAULT_FABLE_MODEL`, `CLAUDE_CODE_SUBAGENT_MODEL`, `CLAUDE_CODE_SUBAGENT_MODEL_FORCE`, `CLAUDE_CODE_EFFORT_LEVEL`, `CLAUDE_CODE_SUBAGENT_EFFORT` or `CLAUDE_CODE_WORKFLOW_SUBAGENT_MODEL`. It refuses them the same way in the configuration's `env`, in `harness.launch.claude.env` and in a module's `env` export. With the home inside the checkout, it also refuses them in the checkout's `.claude/settings.json` and `.claude/settings.local.json` and in the user's Claude Code settings file.

Programs that read `AGENTS.md` and `.agents/skills` and have no separate package include
DeepSeek Harness, Kilo Code, Kimi CLI, Factory Droid, Mistral Vibe, JetBrains Junie, Augment
CLI, Warp, Windsurf, Zed and Crush; compose for them with `target.runtime: any`. A
package for one of them under `internal/render/` implements one interface.

## Launching

A runtime whose program takes a harness from outside the checkout reads a home the
checkout does not link to. `qory harness launch --runtime <name>` prints the command
that starts the program on the composed home, on one line quoted for a POSIX shell, so
a launcher runs it as it is and depends on nothing of the home's layout; `--json` prints the
same as one object, `{command, args, env, addresses}`, for a launcher that spawns without
a shell, `env` holding every variable the line sets:

```sh
cd <checkout> && eval "$(qory harness launch --runtime claude)"
```

`addresses` is, per kind, the name the session started by that line registers each
composed agent, skill and command under, and each role as the entry that fills it, from
a `bind` or a module's `provides` (§References): `{"agents": {"coder": "harness:rails-coder", "reviewer":
"harness:reviewer"}, "skills": {"implement": "harness:implement"}}` for claude, whose
plugin prefixes every kind, the names as the modules wrote them for every other runtime,
and a kind the runtime skips left out. `--address <kind>/<name>` prints that one
registered name and nothing else, so a launcher builds its first prompt from an entry
point, `/$(qory harness launch --runtime claude --address skills/implement)`, and the
prompt refers to what the session has whichever runtime it is. The names come from the same
resolver that writes them into the documents and the instructions, so the three agree.

The home is found the way `compose` finds it, from `--home`, `harness.home` or the
checkout; the runtime is `--runtime`, or the one runtime the harness is composed for, and
one the harness is not composed for is refused with status 2. The paths printed are
absolute.

The line comes from the runtime's launch template: the program, groups of arguments and
environment variables, each referring to the home's files through `${home}`, the home, and
`${dir}`, the runtime's directory in it. Every runtime below ships one, and
`harness.launch.<runtime>` in the configuration puts a `command`, an `args` list or an
`env` mapping in place of the runtime's, field by field (§The configuration). A group
that refers to a file under either placeholder that the compose did not write, such as
`mcp.json` without a server, is left out whole, and so is a variable; the placeholder and
what follows it to the end of the word is the file, so `@${dir}/mcp.json` refers to
`mcp.json`. When the report records an effort, the line passes it in the place the
runtime's descriptor in Forager lists for the effort: for `claude`, `--effort <value>`
before the template's arguments, so a later flag of `harness.launch.<runtime>` wins.

The line sets the launch's environment through `env`, in three parts with no name in two:

1. `QORY_HARNESS_HOME`, the home's absolute path.
2. The fixed variables, which qory sets: the variables of the runtime's template, and
   nothing else.
3. The defaults, what an author wrote: the configuration's `env`, the `env` of
   `harness.launch.<runtime>`, which stands in for the template's whole and is a default
   with its placeholders replaced, the variables a settings fragment sets (§Runtimes), and
   what the modules export, each the module's path in the home.

The variables of the runtime's template are fixed: a variable of the harness with the
same name, the configuration's, a fragment's or a module's export, does not replace one,
and the report leaves it out. When the `env` of `harness.launch.<runtime>` replaces the
template's, its variables are defaults, and the harness's layer over them. The harness's
variables layer over each other: a fragment's, then the modules' over them, then the
configuration's over both. Every one of them is a default, a module's export included.
`qory run` passes the same environment to Forager. The report keeps these variables
under `launch_env` (§The report), and `qory harness compose --check` compares them with
what the compose sets, so a changed export, a changed `env` in the configuration or a
changed fragment variable is stale, a row `launch_env/<runtime>/<NAME>` each.
The plugin and the directories below are rendered on every compose, with links or
without, so `launch` works on a home inside the checkout too, and none of them is linked
into the checkout. A runtime without a template reads its harness from the checkout
alone, through the links a compose with `harness.links: checkout` writes, and `launch`
reports it with status 2: `goose` and `any`.

| `target.runtime` | Template | Rendered for it under the runtime's directory | Reaches the session from outside | Read from the checkout alone |
|---|---|---|---|---|
| `claude` | `claude --plugin-dir ${dir}/plugin --settings ${dir}/settings.json --mcp-config ${dir}/mcp.json --append-system-prompt-file ${dir}/launch/AGENTS.md --setting-sources user` | `plugin/`, a plugin in Claude Code's layout: the skills and commands linked, or written where a document contains a reference, the agents copied, since Claude Code passes over a link in a plugin's `agents/` where it follows one everywhere else, the output styles under `output-styles/` with the manifest's `outputStyles` pointing there, `.claude-plugin/plugin.json` setting its name to `harness`, so a skill is `/harness:<name>` and an agent `harness:<name>`, and every reference in the plugin resolves to that name; `launch/AGENTS.md`, the instructions with their references resolved the same way and, after them, the names the session registers, per kind, with each role | everything: the plugin, the permissions, hooks and model in `settings.json`, the environment the line sets, the servers, the instructions appended to the system prompt; `--setting-sources user` keeps every `.claude` of the checkout and of the directories above it out | nothing |
| `cursor` | `cursor-agent --plugin-dir ${dir}/plugin` | `plugin/`, the same layout under `.cursor-plugin/plugin.json`, with the agents in Cursor's shape, a copy of `hooks.json` as `hooks/hooks.json` and of `mcp.json` as `.mcp.json`, since the CLI takes no settings file | the skills, agents, hooks and servers | the instructions, `AGENTS.md` |
| `copilot` | `copilot --add-dir ${dir}/workspace --additional-mcp-config @${dir}/mcp.json` | `workspace/.github/skills` linked and `workspace/.github/agents` written, which `--add-dir` loads as trusted configuration, and `mcp.json` with the servers under `mcpServers`, so the MCP kind has a place in copilot | the skills, agents and servers | the hooks and the instructions |
| `codex` | `env CODEX_HOME=${dir} codex` | `skills/` copied as folders and regular files and the instructions as `AGENTS.md`, ending with the names the session registers, which a Codex home registers as the modules wrote them, beside `config.toml` and the agents, so the directory is a Codex home. Behind a wall, `CODEX_HOME` is `/var/lib/qory-codex-home`, a writable folder of the run, and qory binds `config.toml`, `hooks.json`, `.env`, `environments.toml`, `AGENTS.override.md`, `AGENTS.md`, `rules/`, `skills/` and `agents/` read-only over it. The harness's Codex hooks go into `hooks.json` of a folder that qory binds read-only at `/etc/codex`, where Codex runs them as managed hooks, see [docs/run.md](../../../docs/run.md#codex-behind-a-wall) | everything | nothing; Codex reads its model key from `OPENAI_API_KEY`. qory writes no `auth.json`, Codex's login file, and a module that ships `files/codex/auth.json` fails the compose with ``Remove `files/codex/auth.json` from module <name>.`` and the next line ``A Codex run takes its model key from the run, not from a file.`` |
| `opencode` | `env OPENCODE_CONFIG_DIR=${dir} opencode` | `skills/` linked, beside `opencode.json`, the agents, commands and hooks, since OpenCode reads the directory the way it reads `.opencode` | the model, servers, agents, commands, hooks and skills | the instructions, `AGENTS.md` |
| `amp` | `amp --settings-file ${dir}/settings.json` | nothing more | the servers and the permissions | the skills and the instructions |
| `gemini` | `env GEMINI_CLI_SYSTEM_SETTINGS_PATH=${dir}/settings.json gemini` | nothing more; the file is read as the system settings, over the user's and the workspace's | the model, servers and hooks | the skills, agents, commands and instructions |

The hooks and the servers of `claude` reach the session through the settings and the
MCP file, the same files the checkout's links point at, and not through the plugin, so
nothing runs twice; `cursor` gets copies in the plugin because its CLI has no other way
in. Each template matches the program's command line as qory ships the template; where a
program's flags differ from its template, set `harness.launch` to replace the template.

## The report

`.qory/harness-report.json`, or `<home>-report.json` beside a home outside the
checkout, `version` 1: the stack name and file, the target, the checkout, the home,
`links: none` when the compose wrote nothing into the checkout, the modules, the entries with their module, the excludes, the checkout
paths a `--force` compose replaced, the `env` the harness exports with
`$QORY_HARNESS_HOME` in place of the home, `launch_env`, per runtime with a launch, every
variable the harness sets in it, `{name, value, from, fixed}`, `from` being `module
<name>`, `module <name>, settings/<runtime>/<file>` or `configuration` and `fixed` false,
since every one is a default, and the stack's `extensions` as written. The
target's `runtime` is always an array: the runtimes the home contains after the compose,
the targeted ones first, and its `effort` is the effort value of the compose, absent when
it has none, which a launch passes in the place the runtime's descriptor lists for the effort. A module has its `name`, its `source` as the stack writes it,
its `pin`, `dirty` when git saw uncommitted changes under a path source, the `variant`
chosen, its `link` when the stack sets one, `copied` when the home contains copies of its
entries, and `base` when it is the base stack's. A
source that selects an export is written as the stack wrote it, `<url>#<ref> module
<name>`. An entry an `only` brought in has `for`, the `<kind>/<name>` that required it,
and an entry whose documents reference others has `references`, the `<kind>/<name>` keys
as written, a role's included; the roles are `bind`, `<kind>/<role>` to the name of the
entry that fills it, from the base's `bind`, the modules' `provides` and the checkout's
`bind`, absent when there are none; and `roles`, the same roles sorted, each `{role,
agent, from}`, `from` being `base`, `provides <module>` or `bind`:
`[{"role": "agents/developer", "agent": "typescript-developer", "from": "provides
typescript"}]`. A group role (§References) is in `roles` alone, one row per member in the
members' order, each with `group: true`, and after them one row with `replaced: true` for
each member the checkout's `bind` replaced, so a reader sees which member the checkout
dropped. `qory harness inspect` and `qory harness compose --dry-run` print them in
the Bind table, with `from` as the third column and `group` or `replaced by bind` as the
fourth. An agent with a tier has `tier`. `tiers` lists each tier the agents use that has a map, with `model`, `effort` when set, and `from`: `--tier`, for an entry of `--tier` and `--tier-effort`, or the file that set it. `egress` is the union of the hosts the modules
declared (§The module manifest), sorted by host, each with the `modules` that declared it
in stack order, and absent when no module declares; when one does, the target runtime's
hosts, the model endpoint for `claude`, join under the runtime's name, so a stack
declares what its modules reach and not what the program needs. A harness whose modules
declare and list no host writes `egress: []`. `qory run` passes the hosts to Forager
(§Launching).
`worktree.base` is the repository's base branch, the configuration's `worktree.base`
resolved against the refs already fetched when the compose ran, so a program beside
qory reads the answer and derives nothing: `branch`, the branch's name with no remote
before it, what a pull request targets, absent when the base is a tag or a commit; `ref`,
what git reads for a diff or a merge, `<remote>/<branch>` when the remote has the
branch; and `source`, the absolute path of the file that set the key, `remote HEAD`, or
`checkout` for the main checkout's current branch, the default when the remote has no
HEAD branch. The compose reaches no remote for it. A repository with no commit, or a
main checkout on no branch and no key, has no `worktree` in its report; a `worktree.base`
that resolves to nothing is a row of the compose, which goes on, and the report leaves it out.
The report of a checkout that extends a stack records the `base`: its `name`, `source` and `pin`. A path source's pin is `working-tree`; a git source's pin is twelve
characters of its commit. The report records the `qory` that wrote it, its `version`,
`commit` and `source`, `release` or `source`, as `qory version --json` reports them,
and leaves the field out when the build has no version. `qory harness inspect`
refuses a report of another version.

## The configuration

One schema under one of two names, `qory.yaml` or `harness.yaml`, read at three levels:
the user's file, `$XDG_CONFIG_HOME/qory/qory.yaml` or `~/.config/qory/qory.yaml`, and
the files of the checkout's ancestor directories contain the machine's choices; the file
in the checkout root is committed with the repository and contains what the repository
needs. Every key may appear at any level and the nearest file wins. A directory contains
one of the two names, never both, and `qory setup repo` writes under the name the
directory already uses. `harness.yaml` is for a repository whose committed file does not
mention the tool that reads it.

```yaml
apiVersion: qory.dev/v1alpha1     # optional; the newest format this qory reads when left out
harness:
  runtime: [claude, codex]       # instead of the document's target.runtime
  model: opus                    # instead of the document's target.model
  effort: high                   # the main agent's effort value, in the place the runtime's descriptor lists
  force: true                    # replace a tracked, unmodified file where a link goes
  update: always                 # fetch every git source again on each compose
  home: ~/.cache/qory/homes      # where the harness is composed: a directory outside the checkout, or .qory/harness
  links: none                    # what the checkout gets: checkout, links into the home, or none
  launch:                        # how a runtime's program is started on the home, over the runtime's template
    claude: {command: /opt/claude/bin/claude}
    codex: {env: {CODEX_HOME: "${dir}", OPENAI_API_KEY: sk-...}}
  extends: {git: git@git.example.com:acme/harness, ref: main, path: nextjs-15}
  target: {runtime: claude, model: opus}   # beside extends: what this checkout composes the base for
  modules:                       # with extends or a target: what this checkout composes
    - name: app                  # extends may be left out when compose -f selects the base
worktree:
  dir: ..                        # where worktrees go, relative to the main checkout
  name: wt-{branch}              # a worktree's directory name; {repo} is the main checkout's
  base: main                     # the repository's base branch; a new worktree branch starts from it
  branch: delete                 # what worktree remove does with the branch: delete or keep
  link: [.env, .env.local]       # linked from the main checkout into a new worktree
  copy: [config/local.json]      # copied once into a new worktree
    # {from: ~/secrets/app.env, to: .env} brings a path from outside the checkout
  run:
    add: [pnpm install]          # run in a new worktree, after links and copies
    remove: [docker compose down] # run in a worktree before it is removed
git:
  timeout: 10m                   # the longest one git command may run
  cache: /var/cache/qory         # where git sources are fetched to
env:
  HARNESS_PROFILE: nextjs        # set in every launch, a default
exports:                         # in a repository delivering stacks or modules (§Exports)
  dir: ./harness                 # where stacks/ and modules/ are; default: the root
  stacks: [nextjs-15]            # harness/stacks/nextjs-15/qory-stack.yaml
  modules: [core, nextjs]        # harness/modules/<name>/qory-module.yaml
```

| Key | Default | Meaning |
|---|---|---|
| `apiVersion` | `qory.dev/v1alpha1` | the format the file is written in, the newest this qory reads when left out, since the file is the repository's or the machine's and not a delivered one; a retired version is read as it, with a `retired` row, and a version this qory does not read is refused |
| `qory` | none | the qory versions the file is written for, as in a stack; read under `extends` as well, since it can only narrow the base's range. At the root of a repository delivering stacks it is the range every stack in it is checked against, joined with the stack's range when the stack file is read (§The stack) |
| `harness.runtime` | the document's `target.runtime` | one runtime name or a list; `--runtime` wins over it |
| `harness.model` | the document's `target.model` | `--model` wins over it |
| `harness.effort` | none | the effort value the main agent runs at, one of the values the runtime's descriptor in Forager lists for a run, passed in the place the descriptor lists for the effort: for `claude`, the flag `--effort`, with `low`, `medium`, `high`, `xhigh` or `max`. A runtime whose descriptor has no place for it gets none, and the compose prints a row. An empty value is no effort. `--effort` wins over it |
| `harness.tiers` | none | the model id each tier maps to, for the agents whose `model` is `${qory:<tier>}`: a model id, or `{model, effort}`, the effort one of the values the runtime takes for an agent. The tiers are `light`, `balanced` and `thinking`. In the user's file and an ancestor's it is the machine's map, the nearest file per tier; in the checkout root's file it is the repository's map, read under `extends` too. Per tier, `--tier`, with the effort of `--tier-effort` for the same tier, wins over the machine's map, and the machine's map over the repository's; an entry comes whole from one of the three, so `--tier-effort` needs `--tier` for its tier. |
| `harness.runtimes` | none | the version `qory run` installs of each runtime, as in a stack (§The stack). Only the checkout root's file sets it. In the user's file, and in an ancestor's file that discovery or `-f` selects to compose (§Discovery), qory stops with `Move runtimes to the checkout's qory.yaml or to the base qory-stack.yaml.`, then `Only the checkout's qory.yaml and the base stack pin a runtime's version.` and `harness.runtimes is in <file>.` on the next two lines. In another ancestor's file, qory leaves the pin unread |
| `harness.force` | `false` | what `--force` does on every compose; `--force=false` wins over it |
| `harness.update` | `never` | `always` fetches every git source again on each compose; `--update` and `--update=false` win over it |
| `harness.home` | `.qory/harness` | where the harness is composed: `.qory/harness` in the checkout, or a directory outside it, absolute or under `~`, containing one home per checkout as `<name>-<digest>` (§Rendering); `--home` wins over it |
| `harness.launch.<runtime>` | the runtime's template | how the runtime's program is started on the home, for `qory harness launch`: `command`, the program; `args`, a list of groups, each a word or a list of words; `env`, a mapping; each key set stands in for the template's, with `${home}` the home and `${dir}` the runtime's directory in it (§Launching) |
| `harness.links` | `checkout` inside the checkout, `none` outside | `checkout` links the checkout into the home and excludes the links; `none` writes nothing into the checkout, and a runtime reads the home through `qory harness launch` (§Launching); `checkout` is refused with a home outside the checkout; `--no-links` wins over it |
| `harness.target`, `harness.modules` | none | in a checkout's file, a stack the checkout defines: the target, `runtime` and `model`, and the modules, with `name`, `description`, `bind` and `extensions` beside them, as in a `qory-stack.yaml`, which has every one of them but the target |
| `harness.extends`, `harness.target`, `harness.modules` | none | in a checkout's file, the stack it extends, the target it composes it for and the modules it appends; `extends` may be left out when `qory harness compose -f <stack>` selects the base; `target` may be left out when `harness.runtime` or `--runtime` supplies the runtime, and is checked against the base's `extending.target`; `modules` may be left out under `extends` or `-f`, for a document containing its target or its extensions alone (§Extending a stack) |
| `worktree.dir` | `..` | where `qory worktree add` puts a worktree, relative to the main checkout unless absolute |
| `worktree.name` | `wt-{branch}` | one directory name under `worktree.dir`; `{branch}` is the branch with each slash made a dash, `{repo}` the main checkout's directory name |
| `worktree.base` | the remote's HEAD branch, else the main checkout's branch | the repository's base branch: where new work starts, what a pull request targets, what a diff is taken against. `qory worktree add` cuts a new branch off it, and the report contains it resolved (§The report). A branch the remote has is written bare, `release`, and read as `<remote>/release`, so a clone that never checked it out has it and a local copy that fell behind is not used; `heads/release` selects the local branch. A tag or a commit is read as written. It has to resolve to a commit, so a repository with no commit is refused |
| `worktree.branch` | `delete` | what `qory worktree remove` does with the worktree's branch: `delete` or `keep`; `--keep-branch` and `--delete-branch` win over it |
| `worktree.link` | none | paths linked into a new worktree: a path inside the checkout, linked from the main checkout to the same path, or `{from: <path>, to: <path>}`, `from` anywhere on the machine, absolute or under `~`, `to` the path in the worktree, relative and inside it, required when `from` is outside the checkout. A `from` that is not there is reported, not an error; a `to` already in the worktree, a dangling link too, is kept. The `{from, to}` form belongs in the user's file when `from` is one machine's path, so it never lands in the committed file; the committed file may use it for a path every machine has |
| `worktree.copy` | none | paths copied once into a new worktree, in the same two forms |
| `worktree.run.add` | none | commands run in a new worktree after links and copies, in order, with `QORY_WORKTREE`, `QORY_MAIN`, `QORY_BRANCH` and, when the branch's base is recorded, `QORY_BASE` set; a failure stops with the worktree kept |
| `worktree.run.remove` | none | commands run in a worktree before it is removed, in order, with the same variables set |
| `git.timeout` | `10m` | a git command running past it is stopped and the compose fails; the fetch a worktree add starts with is bounded by it too |
| `git.cache` | the user's cache directory, `qory/sources` under `~/Library/Caches`, `$XDG_CACHE_HOME` or `~/.cache` | absolute, or relative to the file that sets it |
| `env` | none | variables every launch sets, over what the modules export and what a settings fragment sets, each a default (§Launching), but a name the runtime's launch template sets; a name two modules export with different values needs one here |
| `exports.dir` | the repository root | where the exported stacks and modules are: one directory containing `stacks/` and `modules/`, or `{stacks: <dir>, modules: <dir>}` setting each; relative to the root, inside the repository. Read at the repository root alone |
| `exports.stacks`, `exports.modules` | none | the names the repository publishes, each one directory under the stacks or modules directory containing its document; a consumer selects them with `stack` and `module` (§Exports) |

Every key is optional; a file that sets none is read and changes nothing. A key the file
does not read is refused, and the message has the file by its name. A list, such
as `worktree.link`, is the nearest file's whole. `qory config` prints every effective
value and the file it came from. Under `extends` the machine harness settings of the
checkout's file, `harness.runtime`, `harness.model` and the rest, and its `git` and
`env` keys are not read;
the document under `harness` is (§Extending a stack).

**forager.yaml.** What `qory run` does on this machine is a second file,
`forager.yaml`, beside the user's `qory.yaml` under the configuration directory and
nowhere else: it has no counterpart in a repository or an ancestor directory, so a
checkout cannot set the policy the agent runs under, which server the run reports to or
what the agent is enclosed in. Its sections follow Forager's parts: `gateway` holds
`egress`, `server`, `credentials`, `tools` and `integrations`; `session` holds
`instance` and `run`; `wall` is the wall. `gateway.egress` and `gateway.server` are the
Forager contract's objects, `https://qory.dev/contracts/forager/v1/`, in that contract's
grammar; `qory run` passes them to Forager as they are, `gateway.server` with the access
key's id and the pin from the environment when the file holds none. `wall` chooses that
contract's wall and defines what `qory run` passes to it.

```yaml
# ~/.config/qory/forager.yaml
apiVersion: qory.dev/v1alpha1
gateway:
  egress:                        # the run policy; absent: observe everything, nothing to deny by
    mode: enforce                # or observe: record every connection, deny only what deny lists
    allow: [api.anthropic.com, api.example.com, "*.github.com"]
    deny: [gist.github.com]      # denied in either mode, whatever allow lists
    paths:                       # the paths a run may request on a host; every run needs a wall
      api.example.com: ["/v1/*"]
  server:                        # the server every run reports to; absent: files only
    url: https://apiary.example  # a scheme and a host; https, or http to this machine
    access_key_id: ak_0123456789abcdef # the machine's access key; or QORY_ACCESS_KEY_ID
    apiary_public_key:           # the pin, the server's keys; or QORY_APIARY_PUBLIC_KEY
      - {alg: ed25519, public_key: mptNqtgGKgLhLZxmOGfpBQkdeBNH7QN3Qs9ETNumy8Q}
  tools:                         # programs that serve hosts for a run; a run's policy selects them
    files:
      command: [/opt/tools/files, --prefix, "${argument}"]
      argument: '[a-z0-9-]+/[a-z0-9-]+'
      serves: [files.tools.internal]
session:
  instance:                      # this machine as the server shows it
    name: build-01               # this instance's name on the server; absent: the host name
wall:                            # the container the runtime starts in; absent: a process of this machine
  adapter: docker
  image: go                      # the default: a name of images, or a reference; or --image
  images:                        # the images a run's policy selects by name: image: go-docker
    go:
      ref: example.com/agent-go@sha256:…      # yours: the runtime and the toolchain
    go-docker:
      ref: example.com/agent-go-docker@sha256:…
      runtime: sysbox-runc       # the container runtime the wall starts it under
      docker: true               # experimental: nested Docker
  env: [ANTHROPIC_API_KEY]       # names; the values come from qory run's environment
  helper: /opt/qory/qory-linux   # a static Linux build of qory; absent on Linux: this binary
```

| Key | Default | Meaning |
|---|---|---|
| `gateway.egress.mode` | `observe` | `observe` records every connection and denies only what `deny` lists; `enforce` denies a connection to a host outside `allow` as well, and records the denial |
| `gateway.egress.allow` | none | the hosts the runtime may reach: a lower-case name, or `*.` and a name for every host below it, the grammar of a module's `egress` (§The module manifest). The hosts the harness declares are reported beside it as `harness_hosts` and narrow nothing |
| `gateway.egress.deny` | none | the hosts the runtime may not reach, in `allow`'s grammar, in either mode: the gateway decides them before the mode and the allow list, so a host an entry covers is denied under `observe` as under `enforce`, whatever `allow` lists, with the entry as the rule recorded |
| `gateway.egress.paths` | none | The paths a run may request on a host, by host, as a policy's egress.paths. They apply to every run on this machine: a run's policy, from --policy or the server, keeps its path rules, and a request to a host either lists must match an entry of each that lists it. The proxy reads those hosts' requests, so every run on this machine needs a wall. |
| `gateway.server.url` | none | the server the run reports to: `https`, or `http` to this machine, a scheme and a host with nothing after. With it set, `qory run` fetches the server's configuration, signed, and does not start unless the server answers; the events go where it defines, and its run configuration, when it defines one, is the run's policy. `--local` runs with the files alone and the server is not contacted |
| `gateway.server.access_key_id` | `QORY_ACCESS_KEY_ID` | the id of the machine's access key, an Ed25519 key: `ak_` and 16 characters, which the server assigns when the key enrols; `qory access-key enrol` writes it. Set in the file and in the environment, it is refused. A run without one does not start |
| `gateway.server.apiary_public_key` | `QORY_APIARY_PUBLIC_KEY`, as JSON | the pin: the server's Ed25519 public keys, each `{alg: ed25519, public_key: <base64url>}`, a list so the key can rotate. Every answer of the server is verified under one of them. `qory access-key enrol` writes it when there is none. Set in the file and in the environment, it is refused; a pin that lists the Forager contract's published fixture key is refused. A run without one does not start, `apiary_public_key_missing` |
| `gateway.server.via` | none: the server is reached directly | the gateway this machine reaches the server through, on a machine with a separate access key whose node goes through a gateway: enrolment, the server's configuration, the runs' registrations and outcomes go through it, unchanged, and each run goes through the gateway its run key names. Such a machine runs walled only. `qory access-key enrol --via` writes it. See [docs/run.md](../../../docs/run.md#a-machine-with-a-separate-access-key-behind-a-gateway) |
| `gateway.server.via.url` | none | the gateway: an https URL of a host and an optional port, with nothing after. Required in `gateway.server.via` |
| `gateway.server.via.ca_file` | none: the system's roots | a PEM file of the authorities the gateway's certificate chains to, in place of the system's roots, relative to the directory of `forager.yaml` unless absolute |
| `gateway.server.via.certificate_sha256` | none | the SHA-256 of the gateway certificate's public key, in standard base64 with padding; with it, only a certificate with that key is accepted, and its chain is still checked |
| `gateway.server.enrolment_code_file` | none | `qory gateway`'s alone: the file Qory Apiary writes the gateway's enrolment code (`qecg_…`) into, relative to the directory of `forager.yaml` unless absolute. When `qory gateway` starts with no access key, it waits until the file exists, enrols with the code as `qory access-key enrol` does, and starts. See [docs/gateway.md](../../../docs/gateway.md#the-access-key) |
| `gateway.listen` | none | The address qory gateway listens on for the runs of other machines, host:port. qory run never listens on it: it starts a gateway itself on a loopback port for each run. `--listen` wins over it |
| `gateway.tls.certificate`, `gateway.tls.key` | none | the certificate and key `qory gateway` serves the other machines with, files in PEM, relative to the directory of `forager.yaml` unless absolute; both or neither. `qory gateway` needs them, whatever address it listens on: a gateway serves other machines. `qory config` lists the paths as written and never what the key file holds |
| `session.instance.name` | the host name, or its first label when the whole does not fit | this instance's display name on the server, and the name an enrolled key gets: 1 to 64 of `A-Z`, `a-z`, `0-9`, dot, underscore and dash, starting with a letter or a digit. The instance's id is made by qory and kept in the file `instance-id` beside `forager.yaml` |
| `wall.adapter` | none | `docker`, the one there is. With it set, every `qory run` starts the runtime in a container with no route out except to the gateway's proxy; `--wall none` runs once without it, `--wall docker` once with it |
| `wall.image` | none | the container's image when the run's policy selects none, which contains the runtime and the project's toolchain: the name of one of `wall.images`, or a reference. A name `wall.images` defines is read as that image first; `--image` sets another for one run, read the same way, and a policy's selection wins over both. qory builds none, and a wall without an image is refused unless the run's `--policy` selects one and `wall.images_allow` is not set; a machine that reports to a server sets it, because the server's run configuration arrives once the run starts and may select none |
| `wall.images.<name>` | none | an image this machine defines, which a run's policy selects by its name, `image: <name>`, the Forager contract's §Images. The name is in a credential's grammar. `ref`, required, is the reference, pinned by digest where the machine wants the same image every time; `runtime` is the container runtime the wall starts it under, one the engine has, such as `sysbox-runc`, the engine's default when absent; `docker: true`, experimental, gives the agent nested Docker, a Docker daemon inside the wall, separate from the machine's; the helper starts it before the agent, and it needs Docker Engine 28 or later and a `runtime` that runs one without privileges. A `--policy` that selects a name the section does not define, or selects one for a run without a wall, is refused before the run; every command that reads the file refuses `docker: true` without a `runtime`. `qory config` shows whether `wall.image` is a name of this section or a reference. Define an image with `docker: true` only where every run may get one: any policy can select it |
| `wall.prebuild_image` | none: a prebuild from a run code stops before anything is fetched | the name of the image of `wall.images` a prebuild from a run code runs in, behind the wall of its run: one with `docker: true` and a `runtime`, such as Qory's `prebuild` image under `sysbox-runc`. The prebuild builds, seeds and pushes the target's images in the image's nested Docker: neither the build nor the push runs in this machine's Docker engine or reads its registry logins |
| `wall.images_allow` | none: a run whose policy names an image by reference is refused, `image_not_allowed` | where a run's image may come from when the run's policy, the server's signed run configuration or `--policy`, names one by a reference pinned by digest, `NAME@sha256:` and 64 lower-case hexadecimal digits, in place of a name of `wall.images`: a list of the prefixes of the references the machine runs, in lower case and up to a whole part of a reference, such as `registry.example/agents/`, a registry and a path, or `registry.example/agents/go@`, a name and its `@`. `registry.example/agents` covers `registry.example/agents/go@sha256:…` and not `registry.example/agents-other/go@sha256:…`. Every command that reads the file refuses an empty entry, `*`, and one that is not such a prefix. An image by reference starts under the default image's runtime, with no nested Docker, so `qory run` refuses a list without a default, `wall.image` or `--image`; it runs as `wall.user`, which is then a numeric `uid:gid` with neither part 0 |
| `wall.env` | none | names of variables of `qory run`'s environment that go into the container, such as the model credential, with `--env` adding to them. The launch's variables go in, `QORY_HARNESS_HOME` among them; nothing else of the environment does. A name with no value here is left out |
| `wall.command` | `docker` | the program the adapter runs, such as `podman`, supported where the wall's conformance suite passes |
| `wall.user` | `qory run`'s | the `uid:gid` the container runs as. Root is refused, so a machine where qory runs as root sets another, one that can write the checkout |
| `wall.mounts` | none | what the container sees of the machine beside the checkout and the composed home, each at the same path as on the machine: an absolute path, with `:ro` after it for one the container cannot change; `--mount` adds to them. A socket, or a directory containing a container runtime's, is refused |
| `wall.cpus`, `wall.memory`, `wall.pids_limit`, `wall.shm_size` | the engine's | what the container may use, as `docker run`'s flags of those names read them: `3.5`, `14g`, `4096`, `2g`. A flag of the same name sets another for one run |
| `wall.setup` | `outside` | where the setup of a run from a control plane whose run configuration lists repositories runs, after qory fetched them and composed the harness, before the agent starts: `outside`, in a separate container of the run's image on the wall's network, which runs until the run closes, or until qory removes it: on a read run (docs/run.md, §Read workflows) before the agent starts, and on a run with a push step (docs/run.md, §The push step of a write run) before that step, or `inside`, in the agent's container. The setup is one `setup` step, `prepare`: it adds the files of a prebuilt image's copy of the repository and runs the repository's `postCreateCommand` and `postStartCommand`. Outside, the setup container gets the working folder, the agent's container's limits, the wall's proxy and authority variables, the run's placeholders, and the agent's variables from sources 2 to 5 of [a run's variables](../../../docs/run.md#a-runs-variables), with the same deny lists, `QORY_RUN_SYSTEM` and `QORY_RUN_REPOSITORIES` among them; a service that a setup command starts keeps running there, and the agent reaches it by the name `qory-setup`. A read run's agent cannot reach such a service. A setup command that exits otherwise than 0 fails the run before the agent starts |
| `session.run.timeout` | none | how long a runtime may run, a duration such as `5h30m`. At the limit Forager stops it, `dev.qory.run.exited` contains `reason: timeout`, and `qory run` exits 124. `--timeout` sets another for one run, and `--timeout 0` none |
| `session.run.stop_signal` | the runtime's descriptor's, else `SIGTERM` | the signal that requests a runtime to stop when Forager stops it, at the limit or on a signal to `qory run`: `SIGTERM`, `SIGINT`, `SIGHUP`, `SIGQUIT`, `SIGUSR1` or `SIGUSR2`. Runtimes differ in what a signal means, one closes its session on SIGINT and drops it on SIGTERM, so the runtime's descriptor defines which and the machine may set another; `--stop-signal` sets another for one run |
| `session.run.stop_grace` | the runtime's descriptor's, else `10s` | how long a runtime gets between the stop signal and SIGKILL; `--stop-grace` sets another |
| `wall.ca_env` | `SSL_CERT_FILE`, `GIT_SSL_CAINFO`, `NODE_EXTRA_CA_CERTS`, `REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`, `AWS_CA_BUNDLE` | the variables that point a program at the authorities it trusts, the image's or this machine's and the run's, when a run has a credential or path rules: in the container, or on this machine for a run without a wall |
| `gateway.credentials.<name>` | none | a credential this machine has for its runs. Exactly one of `env`, a variable of `qory run`'s environment, `file`, an absolute path read whenever the token is used, and `adapter`, a program by its absolute path with its arguments, defines where the token comes from. An adapter prints the token with the hosts, the scheme and the paths it is for, the Forager contract's `credential.schema.json`, and takes the policy's argument as `${argument}` when `argument`, a regular expression, matches it whole; its `hosts` and `paths` here are the most it may claim. With `env` and `file`, `hosts`, `auth` (`scheme: bearer`, `basic` with `username`, or `header` with `header`) and `paths` define how the token is used. `placeholders` lists variables the container gets with a value that is no credential |
| `gateway.tools` | none | The tools this machine has for its runs, by name. A run's policy selects among them, with an argument, and defines none. Behind a wall the gateway starts each for the run, outside the container, and the proxy hands it the requests it allows to the hosts the tool serves. |
| `gateway.integrations.<key>` | none | an integration this machine declares: a program that speaks the [integration contract](https://github.com/qoryai/integrations/tree/b5ae1a43582ee58a490f17edb0fce2e4c19a6c6d/contracts/integration/v1), `program`, an absolute path or a name on the `PATH`, `qory-<key>` when absent, with `settings`, its settings document, `{}` when absent. The key is 1 to 64 of `a-z`, `0-9`, `_` and `-`, starting with a letter or a digit. The program is outside the checkout and outside every mount the wall makes read-write in the container, judged by where its links lead: a link on a `PATH` entry the checkout controls that resolves outside the checkout is judged by where it resolves. The rule applies to each run as it starts, so a program written into a directory while that directory was mounted read-write is judged by where it is at the start of each run; keep a program's directory out of the read-write mounts. The resolved program, every directory above it up to `/`, and every directory above each link on the way belong to root or the user running qory, and so does each link; other users may write none of them; a group may write one when it is root's, gid 0, `wheel`, `admin`, or the owner's primary group when its name is the owner's; a directory root owns with the sticky bit set keeps the rule. On a machine whose `PATH` is not its owner's alone, set `program` to its absolute path. `<program> describe` runs in `/`, in a separate process group, for 10 seconds at most; its settings schema is draft 2020-12 and marks a secret `writeOnly` on a property of the settings themselves, `properties.<name>`. qory checks the settings against the description; a `writeOnly` setting with a value ends the command with an error, since the settings go on a command line, and the setting's `<name>_file` defines the path of the file that contains it. The `credential` role defines the credential `<key>`: `adapter: [<program>, credential, --settings, <json>, --, "${argument}"]`, the settings as compact JSON with `<`, `>`, `&`, U+2028 and U+2029 escaped and every `$` written `\u0024`, and the role's `argument` and `hosts`. `qory run` describes the integrations a policy on this machine selects, and every one when the server supplies the policy; `qory config` describes every one. A name `gateway.credentials` defines itself is that section's: the integration defines no credential under it, `qory run` and `qory config` print a line that states it, and `qory config` alone describes it. A role qory does not expand is left alone, and an integration that plays none it expands ends the command with an error |
| `wall.helper` | this binary, on Linux | the absolute path of a static Linux build of qory for the engine's architecture, mounted read-only into the container as the relay, the hook forwarder, and what starts the daemon of an image with `docker: true`. Required where qory itself is not a Linux build |

`gateway.egress` is the machine's policy. One run may bring a policy file, `qory run
--policy <file>`, a document of the Forager contract's policy format kept outside the
checkout and outside everything the container may write. It narrows only: under a
section in mode `enforce` the run reaches the file's hosts the section covers, and with
no section, or one in mode `observe`, the file stands as it is; the `deny` lists of
both apply either way. With a server configured
the server's run configuration is the policy and `--policy` is refused, unless `--local`
keeps the run to the files.

The access key's secret is never in the file. `qory run`, `qory run resend`, `qory run
register`, `qory run end` and `qory gateway` read it from the descriptor `--access-key-secret-fd` names, else from `QORY_ACCESS_KEY_SECRET`,
else from the file `access-key-secret` beside `forager.yaml`: one line, `qak_` and 43
characters, read only from a regular file the user owns that grants nothing to the group
or to others, in a directory that is the user's alone. The Forager contract's published
fixture key is refused. `QORY_ACCESS_KEY_ID`, `QORY_ACCESS_KEY_SECRET`,
`QORY_APIARY_PUBLIC_KEY`, `QORY_SERVER_SECRET` and `QORY_RUN_KEY`, the run key a starter
gives a job, are Forager's: qory takes them
from its environment when a command starts, so they never enter a session's
environment, and `wall.env` and `--env` refuse their names. `qory run` and `qory run
resend` also take the run key from the descriptor `--run-key-fd` names, 0 for stdin; an
unwalled run with `QORY_RUN_KEY` set is refused, `run_key_needs_wall`.
`gateway.server.access_key`,
`gateway.server.secret` and `QORY_SERVER_SECRET` held a workspace access key, which
servers no longer accept: the two keys are refused, and so is `QORY_SERVER_SECRET` when
the file has a `gateway.server` section. The two keys are removed from the file and `QORY_SERVER_SECRET`
is unset first, since `qory access-key enrol` reads the file and refuses them too; then
the machine connects as a node: `qory access-key enrol <server> <code>`, or a key
generated on the node's page in Qory Apiary.

A run's policy also selects credentials, `credentials: [{name: product, argument:
acme/shop}]`, may restrict a host to paths, `egress.paths`, and may select an image among
the ones `wall.images` defines, `image: go`. All three need the wall. The
gateway keeps a selected credential outside the container, and its proxy sets it on the
requests to the hosts it is for, ending the container's TLS for those hosts alone with
an authority made for the run. The proxy sets a credential on the paths it lists alone,
and under `enforce` the run reaches a host with paths on those and no other. A policy
defines no credential: it chooses among the ones this file has, its `gateway.credentials` and
the ones its `gateway.integrations` define, alike.

`qory config` lists the file's values under their sections, `gateway.`, `session.` and
`wall.`; a file that does not read stops every command, the way a `qory.yaml` that does
not read does. The schema is [forager.schema.json](forager.schema.json). The schema of `qory.yaml` is
[config.schema.json](config.schema.json).

## Exit status

| Status | Meaning |
|---|---|
| 0 | done |
| 1 | anything not listed below: a file that could not be written, a git source that could not be fetched |
| 2 | a mistake in the input: the command line, the stack, a module, a fragment |
| 3 | a collision, printed with the excludes that resolve it |
| 4 | a path qory did not write standing where a link goes, which `--force` may replace |
| 5 | the running qory is outside the range a document's `qory` key declares |
| 6 | `--check`: the home does not match what the stack and modules define, or nothing is composed |
