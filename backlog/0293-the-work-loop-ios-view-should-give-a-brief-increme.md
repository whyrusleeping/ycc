---
id: "0293"
title: The work loop ios view should give a brief incremental description of what has been done so far as it goes along
status: done
priority: 3
created: "2026-08-07"
updated: "2026-08-09"
depends_on: []
spec_refs: []
---

## Description

## Acceptance criteria

## Plan

Goal: while a daemon-side work loop is running, GetWorkLoop snapshots should already describe what has been done so far (completed / blocked / in-review / created task rows + the summary counts), so the iOS Work loop view shows incremental progress instead of only session ids until the loop finishes.

Root cause: internal/session/workloop.go only calls buildDigestLocked from finish(), so the digest arrays (and the iOS header summary line "N sessions · X completed, …" plus the digest sections, which the iOS view already renders whenever non-empty) stay empty for the whole run.

Changes (all Go, no proto changes needed — WorkLoopInfo already carries the digest fields):

1. internal/session/workloop.go — incremental digest rebuild:
   - In workLoop.run(), after each session completes and is folded in via wl.accumulate(rec, breach) (the non-error path; error paths already re-list and finish, which rebuilds the digest), re-read the backlog (store.List()), assign it to `tasks`, then lock wl.mu, call wl.buildDigestLocked(tasks), unlock, and wl.persist(). Order matters: accumulate first (buildDigestLocked rolls up wl.sessions), then rebuild, then persist so the durable snapshot carries the incremental digest too (persistence already round-trips the digest fields — no persist changes needed).
   - Update the comments on the digest fields / buildDigestLocked to say the digest is rebuilt after every session, not only at finish.

2. buildDigestLocked classification tightening (needed so the incremental view isn't flooded by pre-existing backlog state): today blocked and in_review tasks are appended UNCONDITIONALLY, so a project with many tasks already blocked/in_review at loop start would show them all as "done by the loop" from the very first mid-loop snapshot (and in the final digest). Change classification to include a blocked/in_review task only when its status CHANGED from baseline (base != current status) OR the loop actually touched it (it has a recorded commit sha, or it was some session's focus — i.e. statusByTask/shaByTask has the id). Completed already requires base != done; created (not in baseline) stays as-is. The existing TestBuildLoopDigest fixtures already use changed-status tasks, so it should keep passing.

3. Tests (internal/session/workloop_test.go):
   - Extend TestBuildLoopDigest (or add a sibling) covering: a task blocked at baseline and still blocked, untouched → excluded; a task in_review at baseline, still in_review, but focused by a session → included; changed-status tasks still classified as before.
   - Add a loop-drive test via the existing loopTestManager/runSession seam: session 1's fake marks its task done; session 2's fake calls m.GetWorkLoop (the loop is mid-run at that point — runSession is invoked after the digest rebuild of the previous iteration... note the rebuild happens right AFTER accumulate of session 1 and BEFORE decide/runSession of session 2, so this observation is deterministic) and asserts the snapshot's Completed contains session 1's task while State is still "running". Record the observed snapshot into a channel/var and assert after the loop finishes.

4. iOS: no code change required — WorkLoopView already renders digestSections and summaryLine from the polled snapshot, so incremental sections appear as the loop runs. Verify by reading, and only touch Swift if something actively suppresses digest display while running (nothing seen in review of WorkLoopView.swift / WorkLoopModel.swift).

Verification: go build ./... && go test ./internal/session/ (plus go vet). Note memory: `go test ./...` has known flakes; scope test runs.

### Starting points
- internal/session/workloop.go: run(), accumulate(), finish(), buildDigestLocked() — digest only built at finish today
- internal/session/workloop_test.go: TestBuildLoopDigest, loopTestManager (runSession seam) for drive tests
- internal/session/workloop_persist.go already persists Completed/Blocked/InReview/Created — no change needed
- clients/ios/App/WorkLoopView.swift + clients/ios/YccKit/Sources/YccKit/WorkLoopModel.swift: digestSections/summaryLine already render whenever non-empty — expected: no Swift change
- proto WorkLoopInfo already carries digest fields — no proto change

## Work log
- 2026-08-09 plan: Goal: while a daemon-side work loop is running, GetWorkLoop snapshots should already describe what has been done so far (completed / blocked / in-review / created task rows + the summary counts), so t
…[truncated]
- 2026-08-09 context hints: 5 recorded with plan
- 2026-08-09 context hints: internal/session/workloop.go — run(), accumulate(), finish(), buildDigestLocked(); internal/session/workloop_test.go — TestBuildLoopDigest, loopTestManager seam; internal/session/workloop_persist.
…[truncated]
- 2026-08-09 preload: 2 file(s), ~55 KiB seeded into implementer context
- 2026-08-09 implementer report: Implemented task 0293 in `internal/session/workloop.go` and `internal/session/workloop_test.go`.  Changes: - Rebuild and persist the work-loop digest after every successfully returned session, after s
…[truncated]
- 2026-08-09 review tier: single-opus — reviewers: sol
- 2026-08-09 review (sol): accept — The change correctly rebuilds and persists the digest after each successfully completed session, with accumulation happening first, so running GetWorkLoop snapshots expose incremental session counts a
…[truncated]
- 2026-08-09 decision: accept — selective commit (0293 Go edits only; 0303's uncommitted duration changes and other tasks' in_review work left in tree/index)
- 2026-08-09 usage: 815,054 tok (in 446,889, out 23,589, cache_r 2,477,286, cache_w 31,906) · cost n/a (unpriced)
  implementer: 472,419 tok (in 254,889, out 5,050, cache_r 212,480, cache_w 0) · cost n/a (unpriced)
  reviewer:sol: 327,187 tok (in 191,946, out 3,145, cache_r 132,096, cache_w 0) · cost n/a (unpriced)
  coordinator: 15,448 tok (in 54, out 15,394, cache_r 2,132,710, cache_w 31,906) · cost n/a (unpriced)
