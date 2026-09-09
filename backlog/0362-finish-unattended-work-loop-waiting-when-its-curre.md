---
id: "0362"
title: Finish unattended work-loop waiting when its current session is hard-stopped
status: done
priority: 1
created: "2026-09-08"
updated: "2026-09-09"
depends_on: []
spec_refs:
    - §9.1 Unattended work loop
    - §18.7 Interrupt and steer
---

## Description
Prevent the unattended work-loop runner from waiting indefinitely when its current session is hard-stopped or reclaimed. Preserve graceful stop and separate work-loop continuation behavior.

## Acceptance criteria
- Stopped/reclaimed current sessions produce an explicit terminal runner outcome and clear stale current-session state; no infinite polling.
- An externally hard-stopped current session terminates the batch without silently restarting work.
- Graceful StopWorkLoop normally lets current work finish rather than hard-cancelling it.
- Shutdown/cancellation releases waiters and produces truthful persisted state/digest.
- Controllable realRunSession tests cover hard stop, disappearance/reclaim, normal idle/error, and graceful loop stop.

## Outcome
The runner now ends the batch on external stop/removal or cancellation, clears and persists current-session state, and retains partial session accounting. Identity-aware atomic reclaim prevents terminal-status races from restarting the batch or cancelling a same-ID replacement. Manager shutdown cancels and joins work loops and wakes provider-retry waits; graceful stop still lets active work finish.

Independent review accepted after the terminal-ownership race was fixed. Real-runner lifecycle and replacement regressions pass repeatedly under the race detector. The full Go suite passes in the isolated task-only tree. Existing unrelated changes, including continuation work, are preserved and excluded from this commit.

Commit: `fix work loop exit after current session stops`
