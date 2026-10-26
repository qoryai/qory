# hello

Two modules. Both ship a `greet` skill, because everyone believes they invented greeting.
The stack keeps the world module's and excludes the hello module's.

`qory setup example` writes this directory. From it:

```sh
qory harness compose     # or: qory hc; composes into ./.claude
qory harness inspect     # or: qory hi; the report: every entry and its module
claude                   # type /hello
qory harness remove      # or: qory hr; removes it again
```

Then delete the `exclude` lines in `qory.yaml` and compose again. `qory` refuses,
lists both modules, and prints the lines that resolve it. Put them under the world module
instead, compose, start `claude` again and type `/hello`: the other greet answers, and
shows where to go from there.

## The same harness for another agent

```sh
qory harness compose --runtime codex     # or: qory hc --runtime codex; claude,codex for both at once
codex                                    # then: greet me
```

Codex reads `AGENTS.override.md` and `.agents/skills`, so the greeting is there. It has no
place for commands, so `/hello` is a Claude Code thing and the compose reports which entry
it skipped.

## The same stack by reference

In the qory repository, this folder also has a `qory-stack.yaml` with the same two
modules. A run takes the folder by reference, at a commit of the repository:

```sh
qory run claude -f https://github.com/qoryai/qory#<commit>:examples/hello -- -p "/hello"
```

`qory setup example` does not write that file: a folder contains a `qory-stack.yaml` or a
`qory.yaml` that lists modules, not both.

A run from a run code whose workflow is `hello` starts the command with the path of the
run's inputs: `/harness:hello <in>/inputs.json`. When that file has `instruction.text`,
Claude writes it after the list of modules, under `You asked:`, and answers it. Claude
then stops, with or without `instruction.text`: the greet skill's "Next" section, which
tells a person at a terminal what to try, is left out of a run.
