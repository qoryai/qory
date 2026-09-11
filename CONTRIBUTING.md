# Contributing

Thank you for considering a contribution.

## Where contributions go

Everywhere. Three kinds are the most useful:

- **Support for another runtime.** A runtime is one package under `internal/render/` that
  implements the `Runtime` interface: which links the checkout needs, what to write into
  its directory, which kinds it has no place for. Every existing runtime is under a
  hundred lines. Add a row to the runtimes table in `contracts/harness/v1/README.md` and a
  line in the runtime test. A runtime that changes what it reads is a fix to its package.
- **The contract.** The harness compose format under `contracts/harness/v1/`, its schemas
  and its fixtures. A module follows the contract and lives in your own repository; nothing
  in this one has to change for it.
- **The `qory` command** and the compose rules.

## Contributor Licence Agreement

Copyright in this project is held by a single owner: **8wonders GmbH, and its successors and
assigns**. To keep that true, every contribution is made under the Contributor Licence
Agreement in [CLA.md](CLA.md): a perpetual, worldwide, irrevocable licence to the
contribution, including the right to relicense it, and a patent grant on the same terms as
the Apache License, Version 2.0. You keep your copyright.

Opening a pull request against this repository is your acceptance of the agreement, for that
contribution and every later one. The pull request is the record of your acceptance. Read
[CLA.md](CLA.md) before your first pull request. A signing step on the pull request may be
added later; it will not change the terms.

The agreement names the owner with successors-and-assigns wording, so that if the
project moves into a dedicated entity, existing grants travel with it and nobody signs again.

Why a CLA at all: the licensing decisions of the project are only executable with a sole
copyright holder. Declaring it before a community exists is what makes it a kept promise
rather than a takeback.

## Licence

By contributing, you agree that your contribution is licensed under the Apache License,
Version 2.0 (see [LICENSE](LICENSE)) in addition to the CLA grant above.

## Development

The toolchain is pinned in `mise.toml`; `mise install` provides it. Go 1.27 and, for a
release, goreleaser.

```sh
go build -o qory .      # the binary
go test ./...           # every fixture under contracts/harness/v1/fixtures, and the unit tests
go test ./... -cover    # per-package statement coverage
gofmt -l .              # must print nothing
go vet ./...
go run github.com/mgechev/revive@v1.16.0 -config revive.toml ./...
```

A change to the compose format starts with a fixture. Each fixture directory holds a
`qory-stack.yaml`, its modules under `modules/`, and under `expected/` either
`entries.txt` (one `kind/name module` line per composed entry, with `AGENTS.md` beside it
and a merged settings file at `settings/<runtime>/<file>.json`, a TOML target as
`<file>.toml.json`, when the fixture exercises them) or `error.txt` (the exact error text).
The test suite runs every fixture and validates every document against the schemas, so the
schema, the fixtures and the reader cannot drift apart. Write the fixture, watch it fail,
then change the code.

A test is hermetic: `t.TempDir` for the tree, `t.Chdir` for the working directory,
`t.Setenv` for `HOME` and `GIT_CONFIG_GLOBAL`, and a local `user.name` in any checkout it
creates. No test reads the machine's git identity, the `gh` CLI, or the network. Name a test
for the behaviour it pins, not for the function it calls.

## Doc comments

Every package and every exported name carries a doc comment, and CI fails without one. The
conventions, beyond what `revive` can check:

- The first sentence starts with the name and is a complete sentence: `Compose reads ...`,
  `Stack is ...`. A package comment starts `Package x `.
- A package comment says what the package owns, the words it defines, how a caller uses it,
  and the invariants a caller must not break. It goes in `doc.go` when it runs past about
  eight lines.
- Exported struct fields carry a comment when the name alone does not settle what goes in
  them, in what format, or who sets them.
- Unexported types, and unexported functions longer than a few lines, are documented too.
  The comment says why the code exists or what is subtle in it, never what the next line
  does.
- Say what the code does, including what it refuses, what it overwrites, what it leaves
  behind, and which errors a caller matches with `errors.Is` or `errors.As`.
- Link identifiers as `[Stack]`, `[stack.Load]`. Indent code blocks with a tab, write
  lists as two spaces and a dash, and wrap at 90 columns.

One vocabulary, no synonyms: **runtime** is the program that runs the harness, such as
Claude Code; **stack** is `qory-stack.yaml`, the ordered modules and the target for one kind
of work; **configuration** is `qory.yaml`, the repository's own document at its root and
the machine's elsewhere, whose `harness` section names the stack a checkout extends and
the modules it appends; **manifest** is `qory-module.yaml`; **base** is the stack a
checkout extends; **module**, **source**, **entry**, **kind**, **variant**, **exclude**,
**collision**, **home**, **checkout**, **link**, **pin** and **report** mean what
`contracts/harness/v1/README.md` says they mean. A runtime is never a provider, a tool or a vendor; a configuration is
never a setting file or a preference.

The command reference under `docs/commands/` is generated: run
`go run ./internal/gendocs docs/commands` after changing a command and commit the result.

## Releases

A release is a tag on a branch named after it, `v0.3.0`, opened as one pull request. That
branch adds the release's section to `CHANGELOG.md`, `[X.Y.Z] - YYYY-MM-DD` with the day
the tag lands and a compare link at the foot of the file; a fix that goes to `main`
outside a release branch goes under `[Unreleased]` until the next one. Anything a person
upgrading has to do stands under Upgrading in the section. The release workflow takes
that section as the release body, above the commit list goreleaser writes, and refuses a
tag whose version has no section; `scripts/changelog-section.sh 0.3.0` prints what it
would take. Pushing `vX.Y.Z` to the GitHub mirror runs `.github/workflows/release.yml`,
which builds the archives for macOS and Linux with goreleaser, publishes them with their
checksums, and updates the Homebrew cask when `HOMEBREW_TAP_TOKEN` is set. Check the
configuration before tagging:

```sh
goreleaser check
goreleaser build --snapshot --clean --single-target
```

`qory version` reports the version without the `v`, `0.2.0` for the tag `v0.2.0`, in a
release build and in a source build at that tag; a source build between tags reports the
pseudo-version Go stamped, and one without version control reports the commit alone. The
`source` row, `release` or `source`, tells the two kinds of build apart, and
`qory version --json` prints every field as one object for a script.

Commit messages say what changed and why it was needed, in the imperative.
