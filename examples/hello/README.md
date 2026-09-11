# hello

Two modules. Both ship a `greet` skill, because everyone believes they invented greeting.
The stack keeps the world module's and excludes the hello module's.

`qory setup example` writes this directory. From it:

```sh
qory hc            # composes into ./.claude
qory hi            # the report: every entry and its module
claude             # type /hello
qory hr            # removes it again
```

Then delete the `exclude` lines in `qory.yaml` and compose again. `qory` refuses,
names both modules, and prints the lines that resolve it. Put them under the world module
instead, compose, start `claude` again and type `/hello`: the other greet answers, and
says where to go from there.

## The same harness for another agent

```sh
qory hc --runtime codex          # or claude,codex for both at once
codex                            # then: greet me
```

Codex reads `AGENTS.override.md` and `.agents/skills`, so the greeting is there. It has no
place for commands, so `/hello` is a Claude Code thing and the compose says which entry it
skipped.
