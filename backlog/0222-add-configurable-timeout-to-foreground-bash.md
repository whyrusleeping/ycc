---
id: "0222"
title: Add configurable timeout to foreground Bash
status: done
priority: 2
created: "2026-07-20"
updated: "2026-07-20"
depends_on: []
spec_refs:
    - Tool surface
    - docs/design/async-jobs.md#3.3 Shell jobs
---

## Description
Improve the Bash tool API so long-running commands whose results are immediately required can run synchronously without abusing `run_in_background` followed by `wait`.

## Acceptance criteria
- Bash accepts a foreground `timeout_s` parameter with a documented default and sensible validation/bounds.
- `run_in_background` remains for genuinely asynchronous/concurrent work and is not presented as the general solution to foreground timeout limits.
- Tool descriptions explicitly discourage starting one background job and immediately waiting when foreground execution is appropriate.
- Existing sandbox and background-job behavior remains intact.
- Tests cover custom foreground timeouts and the updated schema/description behavior.
- Durable design documentation is updated.

## Work log
