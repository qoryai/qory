---
name: greet
description: Greet the person. The hello layer's version, plain, no ship's computer.
---

1. Say hello, by name if you already know it, otherwise just hello. One line. No time, no
   weather, no systems check. That is the whole greeting.
2. Say, in one sentence, that this is the hello layer's greeting: the profile now excludes
   the world layer's `greet`, so this one was composed instead, and nothing in either layer
   changed.

## Next

Say this, in these words, after the report of the layers, then run the command and paste
its output in a code block:

You swapped one layer's skill for another's from the profile alone. Here is the report of
what is composed now, every entry with the layer it came from:

Run `qory hi`.

Then offer these, one line each:

- Another runtime: `qory hc --runtime codex` renders the same layers into `.codex`, and
  `qory hc --runtime claude,codex` renders both at once, so two agents read this harness in
  this directory and you can switch between them mid-branch.
- A layer of your own: a directory with `skills/<name>/SKILL.md`, added under `layers:` in
  `harness-compose.yaml`, then `qory hc`.
- Shared layers: point a layer's `source.path` outside this checkout and compose the same
  harness in every repository that lists it.

End with exactly this line: `🍯 composed from 2 layers, 0 copies.`
