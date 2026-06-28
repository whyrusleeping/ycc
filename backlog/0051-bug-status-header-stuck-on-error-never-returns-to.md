---
id: "0051"
title: 'Bug: status header stuck on "error" — never returns to "running" after recovery'
status: todo
priority: 2
created: "2026-06-27"
updated: "2026-06-27"
depends_on: []
spec_refs: []
---

## Description
## Summary

The session status shown at the top of the screen can transition from `running` to `error`, but once an error is recovered it never transitions back. The header stays on `error` indefinitely even though the agent is running again normally.

## Expected behavior

When the underlying error condition recovers (e.g. a transient failure resolves and the agent resumes turns), the top-of-screen status should return to `running` (or the appropriate non-error state).

## Notes / likely cause

- Investigate where the status field is set on error and confirm there is no corresponding path that clears it back to `running` on the next successful turn/recovery.
- Status is likely sticky because it's only ever set to `error` and never reset when normal activity resumes.
- Coordinate with the auto-retry work (task 0050) — after a retry succeeds, the status should reflect the recovered state.

## Acceptance criteria

- [ ] After an error is surfaced in the status header, a subsequent successful turn / recovery resets the status away from `error`.
- [ ] The status header accurately reflects the current state (running/idle/error) rather than latching on `error`.
- [ ] Reproduce the original stuck-status scenario and confirm it now clears.

## Acceptance criteria

## Work log
