# Changelog

Every release of qory, newest first, in the shape of [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
The version numbers follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html); before 1.0 a minor
release may change what an existing document does, and says so under Upgrading.

## [0.4.0] - 2026-09-12

A harness repository publishes its stacks and modules by name, and a consumer names them
instead of their directories. And a fleet operator's request: a customer's committed file
carries only what is the repository's own, its modules and its extensions, and the runner
supplies the base from the stack tree it holds, so the file names no version, ref or URL
of the operator's, and may say nothing of the tool that reads it.

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

The first release shaped by an integration: a harness repository moved four modules and
two stacks off its own composer onto 0.2.x and wrote down what it had to work around.
This release takes every item of that report.

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
  runners changes no client file. A build from source between tags has no version to
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

[0.4.0]: https://github.com/qoryai/qory/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/qoryai/qory/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/qoryai/qory/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/qoryai/qory/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/qoryai/qory/releases/tag/v0.1.0
