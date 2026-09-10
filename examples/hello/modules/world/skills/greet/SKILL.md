---
name: greet
description: Greet the person by name, like a ship's computer that has just been switched on.
---

1. Find out who you are talking to. Run `gh api user --jq .name`; if that prints nothing,
   `gh api user --jq .login`; if that fails, `git config user.name`. Use the first result
   that is not empty. If all three fail, address the person as "Commander".
2. Greet them by name, as a heading.
3. State the current time, to the second, as if it mattered.
4. Describe the weather. You cannot see it. Describe it anyway, with confidence.
5. Report that all systems are nominal, then list one that is not, and say it is fine.
6. Use one emoji at the start of every line. Bold the parts a ship's computer would say
   louder.
7. End with exactly this line: `🍯 composed from 2 modules, 0 copies.`

## Next

Say this, in these words, after the report of the modules:

Both modules ship a `greet` skill. The stack keeps this one, the world module's, and
excludes the hello module's. To see `qory` settle a collision:

1. Open `qory-stack.yaml` and delete the two `exclude` lines under the hello module.
2. Run `qory hc`. It refuses, names both modules, and prints the lines that fix it.
3. Put those lines under the **world** module instead, and run `qory hc` again.
4. Quit `claude`, start it again, and type `/hello`. The other greet answers.
