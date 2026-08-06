---
id: "0242"
title: Fix duplicate backlog task ids (0120, 0175, 0192–0195, 0211–0218)
status: done
priority: 3
created: "2026-08-05"
updated: "2026-08-06"
depends_on: []
spec_refs: []
---

## Description

Fourteen ids are used by two different task files each (`0120`, `0175`, `0192`, `0193`, `0194`, `0195`, `0211`, `0212`, `0213`, `0214`, `0215`, `0216`, `0217`, `0218`). Reproduce with:

```
rg -o '^id: "[0-9]+"' backlog/*.md | sed 's/.*id: //' | sort | uniq -d
```

This breaks the `get_task` / `update_task` tools: they resolve an id to the *first* matching file, so the second task of a colliding pair is unreachable and an update silently mutates the wrong task. Hit in practice while starting work on the iOS drawer task — `update_task 0214 in_progress` flipped a *done* task ("reference focused backlog tasks in session names") instead.

Likely cause: `create_task` allocating the next id from a stale view of the directory (parallel workstreams committing tasks concurrently, or a merge that took both sides).

## Acceptance criteria

- [ ] Every `backlog/*.md` has a unique `id`, and each file's `id` frontmatter matches its filename prefix.
- [ ] Renumbered tasks keep their content, and any `depends_on` referring to a renumbered id is updated to point at the intended task.
- [ ] `create_task` id allocation is hardened against the collision (e.g. scan for the max existing id across files rather than trusting an index, and fail loudly on collision).
- [ ] A cheap check (test or `ycc spec-check`-style guard) fails when two task files share an id.


## Work log
- 2026-08-07: Closed as superseded by the dedupe self-heal (`internal/docs/dedupe.go`): every
  Store scan renumbers the younger claimant and `ycc doctor` reports the moves. The 14 historical
  collisions were healed into 0262-0275; `rg -o '^id: "[0-9]+"' backlog/*.md | sort | uniq -d`
  is now empty. Remaining prevention work is tracked by 0249 / 0276.
