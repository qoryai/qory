---
name: deploy
description: The deploy skill of the core module.
---

1. Dispatch ${qory:agents/coder} for the code.
2. Run ${qory:skills/test}, then dispatch ${qory:agents/reviewer}.
3. Read [the checklist](checklist.md).
