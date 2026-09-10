# hello

Two layers. Both ship a `greet` skill, because everyone believes they invented greeting.
The profile keeps the world layer's and excludes the hello layer's.

`qory harness init` writes this directory. From it:

```sh
qory hc            # composes into ./.claude
qory hi            # the report: every entry and its layer
claude             # type /hello
qory hr            # removes it again
```

Then delete the `exclude` lines in `harness-compose.yaml` and compose again. `qory` refuses,
names both layers, and prints the lines that resolve it. Put them under the world layer
instead, compose, start `claude` again and type `/hello`: the other greet answers, and
says where to go from there.

## The same harness for another agent

```sh
qory hc --runtime codex          # or claude,codex for both at once
codex                            # then: greet me
```

Codex reads `AGENTS.md` and `.agents/skills`, so the greeting is there. It has no place for
commands, so `/hello` is a Claude Code thing and the compose says which entry it skipped.
