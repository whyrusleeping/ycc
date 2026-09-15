---
id: "0383"
title: Allow active task bookkeeping on pre-existing uncommitted backlog files
status: done
priority: 3
created: "2026-09-15"
updated: "2026-09-15"
depends_on: []
spec_refs: []
---

## Description
Fix the observed spawn_implementer refusal when an accepted task's backlog file was untracked or modified at session start, then changed by normal status/plan updates. Task 0382 session s_77d203b03f238210 failed before implementation for exactly this reason.

## Acceptance criteria
- Starting and completing an accepted task can include its own uncommitted backlog document without requiring a preparatory commit or fresh session.
- Review and commit use the same explicit task-document ownership scope, including after session reopen.
- Unrelated pre-existing source and backlog changes remain protected; do not broadly ignore dirty files or recapture the baseline after mutation.

## Outcome

Explicit task-document adoption now flows through delegation, review, revision, reopen, and finalization without changing the persisted baseline. Untracked, unstaged, and already-staged task records work; unrelated dirty paths and independently staged variants remain protected. Regression tests cover the reported workflow and exact commit scope. Task 0382 returned to todo; no implementation of that task occurred.

Validation: focused suites and final `go test ./...` pass; the first full run hit a session idle-pause timeout which passed on retry. `git diff --check` passes. Deployment requires rebuilding/restarting the daemon.

Commit subject: fix: include active task bookkeeping in scoped changesets.
