---
id: "0117"
title: Preserve workstream session transcripts after merge/discard removes the worktree
status: done
priority: 5
created: "2026-07-02"
updated: "2026-07-02"
depends_on:
    - "0085"
spec_refs: []
---

## Description
## Context
Task 0085's Workstreams panel drills into a workstream's session via ResumeSession. For a live workstream this works; but once a workstream is merged or discarded, its worktree (and the session log under `<worktree>/.ycc/sessions/<id>/events.jsonl`) is removed, so drilling into a terminal row surfaces a transient error notice instead of the transcript.

## Scope
Preserve (or relocate) a workstream session's event log at merge/discard time so its transcript remains viewable from the Workstreams panel / session browser afterwards — e.g. copy the log into the primary workspace's `.ycc/sessions/` before the worktree is removed, or persist it daemon-side.

## Acceptance criteria
- [ ] After merging or discarding a workstream, its session transcript is still viewable (panel drill-in or session browser) instead of erroring.
- [ ] No regression to the merge/discard cleanup flow; build/vet/test pass.

## Acceptance criteria

## Plan

Problem: a workstream session's event log lives at `<worktree>/.ycc/sessions/<id>/events.jsonl`. MergeWorkstream and DiscardWorkstream stop the session and then remove the worktree, destroying the log — so drilling into a terminal workstream row (ResumeSession) or fetching its transcript (SessionTranscript) errors, because both resolve session logs against the project's PRIMARY workspace.

Fix (in internal/session/workstream_merge.go):
1. Add a helper on Manager, e.g. `preserveWorkstreamSession(ws workstream.Workstream)`, that copies the session directory `<worktree>/.ycc/sessions/<sessionID>/` into the primary workspace at `<primary>/.ycc/sessions/<sessionID>/`:
   - Resolve the primary workspace via m.projects.Resolve(ws.Project); no-op if unknown, or if ws.SessionID/ws.WorktreePath is empty, or the source dir doesn't exist.
   - Copy the directory contents (recursively is fine; in practice it's events.jsonl). Do NOT overwrite an existing destination file/dir if one already exists (session ids are unique, so collision means it was already preserved).
   - Entirely best-effort: any error is swallowed (optionally logged) — preservation must never block the merge/discard lifecycle transition, matching the existing best-effort cleanup philosophy.
2. Call the helper in MergeWorkstream step 4 AFTER `m.Stop(ws.SessionID)` (so the log is complete, including workstream_merged + session_stopped events) and BEFORE `m.cleanupWorktree(repo, ws)`.
3. Call it likewise in DiscardWorkstream: after `m.Stop(ws.SessionID)` and before the RemoveWorktree cleanup block. (For stale workstreams whose tree is already gone the helper just no-ops.)

Tests (internal/session/workstream_merge_test.go — follow the existing test setup patterns there):
- After a successful merge, assert `<primary>/.ycc/sessions/<sessionID>/events.jsonl` exists and `m.SessionTranscript(project, sessionID)` returns the events (including the workstream_merged event).
- Same for DiscardWorkstream (transcript viewable, includes workstream_discarded).
- Confirm existing merge/discard tests still pass (no regression to cleanup flow).

Verification: go build ./... && go vet ./... && go test ./...

### Starting points
- internal/session/workstream_merge.go — MergeWorkstream step 4 (~line 227) and DiscardWorkstream (~line 279) are the two insertion points
- internal/session/workstream_merge.go:emitWorkstreamEvent shows the worktree log path shape: filepath.Join(ws.WorktreePath, ".ycc", "sessions", ws.SessionID, "events.jsonl")
- internal/session/session.go:SessionTranscript / Reopen resolve logs at <primary>/.ycc/sessions/<id>/events.jsonl — that's where the copy must land
- internal/session/workstream_merge_test.go has existing merge/discard test scaffolding (fake repos, spawn helpers)
- primary workspace resolution: m.projects.Resolve(ws.Project) — see Manager.primaryRepo

## Work log
- 2026-07-02 plan: Problem: a workstream session's event log lives at `<worktree>/.ycc/sessions/<id>/events.jsonl`. MergeWorkstream and DiscardWorkstream stop the session and then remove the worktree, destroying the log
…[truncated]
- 2026-07-02 context hints: 5 recorded with plan
- 2026-07-02 context hints: internal/session/workstream_merge.go — MergeWorkstream step 4 (~line 227) and DiscardWorkstream (~line 279) are the two insertion points; internal/session/workstream_merge.go:emitWorkstreamEvent sho
…[truncated]
- 2026-07-02 implementer report: Implemented Task 0117: workstream session transcripts are now preserved after merge/discard removes the worktree.  ## Changes (internal/session/workstream_merge.go) - Added `preserveWorkstreamSession(
…[truncated]
- 2026-07-02 review tier: single-opus — reviewers: Claude
- 2026-07-02 review (Claude): accept — The change correctly implements Task 0117. A best-effort `preserveWorkstreamSession` helper copies the session directory from the worktree (`<worktree>/.ycc/sessions/<id>/`) into the primary workspace
…[truncated]
- 2026-07-02 decision: accept — commit: session: preserve workstream session transcripts across merge/discard (task 0117)
- 2026-07-02 usage: 15,065 tok (in 90, out 14,975, cache_r 855,789, cache_w 74,760) · cost n/a (unpriced)
  coordinator: 6,761 tok (in 38, out 6,723, cache_r 534,338, cache_w 38,864) · cost n/a (unpriced)
  implementer: 6,521 tok (in 32, out 6,489, cache_r 249,876, cache_w 26,345) · cost n/a (unpriced)
  reviewer:Claude: 1,783 tok (in 20, out 1,763, cache_r 71,575, cache_w 9,551) · cost n/a (unpriced)
