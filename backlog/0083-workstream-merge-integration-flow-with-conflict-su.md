---
id: "0083"
title: Workstream merge/integration flow with conflict surfacing
status: done
priority: 4
created: "2026-06-30"
updated: "2026-07-02"
depends_on:
    - "0082"
spec_refs:
    - Persistence & remote sync
    - Session & event log
    - Interaction levels
---

## Description
## Context
Third step of the parallel-workstreams design (see `docs/design/parallel-workstreams.md` §6, §10.3). Implement integrating a completed workstream back to base with explicit, conflict-aware, review-gated merges — never silent auto-resolution.

## Scope
- Implement the §6 flow: trial-merge to detect conflicts; under autonomous level auto-merge clean workstreams, under interactive/judgement levels surface the integrated diff as an accept gate; sequential reconciliation across workstreams (each rebased/merged against the latest base).
- Conflict path: emit a `workstream_conflict` event listing conflicted paths/hunks and stop, offering (a) a resolve-mode session in the worktree or (b) handing off to the user. The base branch is never left conflicted.
- Cleanup on success: `git worktree remove`, branch delete, prune.

## Acceptance criteria
- [ ] Clean trial-merge → merged per interaction level (auto under autonomous; accept-gated otherwise).
- [ ] Conflicting trial-merge → `workstream_conflict` event with conflicted paths; base branch untouched.
- [ ] Merges are sequential across workstreams (second sees the first's changes).
- [ ] New event types (`workstream_created/merged/conflict/discarded`) + reducer handling.
- [ ] Successful merge cleans up the worktree + branch.
- [ ] Tests cover clean + conflicting integration; build/vet/test pass.

## Acceptance criteria

## Plan

Implement the §6 merge/integration flow (docs/design/parallel-workstreams.md) on top of the 0082 registry + 0081 git primitives. Manager-level only — RPC (0084) and TUI (0085) come later.

1. Event model (internal/event):
   - New Type constants: `WorkstreamCreated = "workstream_created"`, `WorkstreamMerged = "workstream_merged"`, `WorkstreamConflict = "workstream_conflict"`, `WorkstreamDiscarded = "workstream_discarded"`.
   - Reducer (reduce.go): add Projection fields `WorkstreamID string`, `WorkstreamState string` ("created"/"merged"/"conflict"/"discarded"), `WorkstreamConflicts []string`. Handle the four events: created sets ID+state; conflict sets state + conflict paths (data "conflicts" — accept both []string fresh and []any JSONL-decoded); merged/discarded set state and clear conflicts. ALSO handle `InteractionLevelChanged` → `p.InteractionLevel = str(data, "to")` (needed so the merge gate reads the *current* level of a non-live session; today Reduce only sets it from session_started).

2. Git helper (internal/git): add `Repo.DiffMergeBase(branch string) (string, error)` → `git diff HEAD...branch` (the integrated-diff preview for the accept gate).

3. Manager flow (internal/session/session.go, or a new workstream.go file in the package):
   - `mergeMu sync.Mutex` on Manager; held for the whole of MergeWorkstream so merges across workstreams are strictly sequential and each trial-merges against the latest base HEAD.
   - Emit helper `(m *Manager) emitWorkstreamEvent(ws workstream.Workstream, t event.Type, data map[string]any)`: if the workstream's session is live in m.sessions use its emitter (`EmitAs("system", …)`); else `event.OpenLog(<worktree>/.ycc/sessions/<sid>/events.jsonl)`, emit, Close (never double-open a live log — seq authority must be single).
   - SpawnWorkstream: after registry Add succeeds, emit `workstream_created` on the new session's stream (data: workstream id, branch, base, worktree, project, task).
   - `type MergePreview struct { Clean bool; Conflicts []string; Diff string }` and `(m *Manager) PreviewWorkstreamMerge(id string) (MergePreview, error)`: look up (must be StatusActive), open primary repo, TrialMerge(branch); on clean also DiffMergeBase. Read-only, no events.
   - `type MergeOutcome struct { Merged bool; Commit string; NeedsAccept bool; Diff string; Conflicts []string }` and `(m *Manager) MergeWorkstream(id string, accept bool) (MergeOutcome, error)`:
     a. Lock mergeMu. Look up workstream; error unless StatusActive. Resolve project → primary; git.Open.
     b. TrialMerge(branch). Conflicting → emit `workstream_conflict` (data: conflicts paths) on the workstream's session stream, return Outcome{Conflicts}; base untouched, registry stays active, worktree kept (so a resolve session / the user can fix it).
     c. Clean → resolve the effective interaction level: live session's Level() if present, else Reduce(ReadLog(...)).InteractionLevel (default "judgement" when empty). If level != "autonomous" and !accept → return Outcome{NeedsAccept: true, Diff: DiffMergeBase(branch)}; no mutation, no event.
     d. Merge: repo.Merge(branch, MergeNoFF). If it unexpectedly conflicts (shouldn't, serialized): treat as (b) — Merge already aborted so base is restored.
     e. Success: emit `workstream_merged` (commit sha, branch) on the session stream FIRST (log still exists), then m.Stop(sessionID) best-effort, then cleanup: RemoveWorktree(dir), DeleteBranch(branch, false) with force fallback, PruneWorktrees — all best-effort — then registry SetStatus(merged). Return Outcome{Merged, Commit}.
   - `(m *Manager) DiscardWorkstream(id string) error`: active (or stale) only. Emit `workstream_discarded`, Stop session, RemoveWorktree + DeleteBranch(force) + Prune (skip git steps / tolerate errors for stale entries whose tree is already gone), SetStatus(discarded).

4. Tests (internal/session/workstream_merge_test.go, reuse newWorkstreamManager; commit into a worktree/primary via git.Open(path) + write file + repo.Commit):
   - Clean + autonomous: spawn (InteractionLevel "autonomous"), commit a file on the branch, MergeWorkstream(id, false) → Merged; primary tree has the file; worktree dir gone; branch deleted (RevParse of branch fails); registry status merged; `workstream_merged` visible via the held session's s.Log().Snapshot().
   - Accept gate: default (judgement) level, clean branch, MergeWorkstream(id, false) → NeedsAccept with non-empty Diff, base HEAD unchanged, status still active, worktree still present, no merged event; then MergeWorkstream(id, true) → Merged.
   - Conflict: commit conflicting edits to the same file in primary and in the worktree; MergeWorkstream → Conflicts lists the path; base HEAD unchanged (rev-parse before/after equal); worktree + branch + active status intact; `workstream_conflict` event with the paths in the session log.
   - Sequential: two workstreams off the same base; ws1 edits file A, ws2 edits the SAME file A differently. Merge ws1 (autonomous) → clean. Merge ws2 → conflict surfaced (proves ws2 was reconciled against the post-ws1 base). Plus a happy variant: ws2' touching a different file merges cleanly after ws1 and the final tree contains both changes.
   - Discard: spawn, DiscardWorkstream → status discarded, worktree removed, branch gone, `workstream_discarded` event.
   - Reducer: unit test in internal/event/reduce_test.go — created/conflict/merged/discarded fold into the new Projection fields; interaction_level_changed updates InteractionLevel.
   - `go build ./... && go vet ./... && go test ./...` pass.

Scope guard: no proto/server/TUI changes (0084/0085); no resolve-mode session spawning (the conflict outcome + kept worktree is the hook for it).

### Starting points
- internal/git/merge.go — TrialMerge/Merge already exist (abort-on-conflict, conflict path lists); add DiffMergeBase here
- internal/git/worktree.go — RemoveWorktree/DeleteBranch/PruneWorktrees
- internal/workstream/registry.go — Status constants, SetStatus, Get; StatusActive/Merged/Discarded/Stale + Terminal()
- internal/session/session.go:1023 SpawnWorkstream (cleanup closure pattern), :1132 ReconcileWorkstreams, :1443 Manager.Stop, Session.Level()/s.emitter/s.Log()
- internal/event/event.go Type constants; internal/event/reduce.go Reduce/Projection
- internal/event/log.go OpenLog continues seq from existing JSONL — safe for non-live session append
- internal/session/workstream_test.go newWorkstreamManager(t) harness; internal/git/worktree_test.go TestTrialMergeAndMergeConflict for repo-commit test idioms
- docs/design/parallel-workstreams.md §6 (merge strategy), §8 (event names)

## Work log
- 2026-07-02 plan: Implement the §6 merge/integration flow (docs/design/parallel-workstreams.md) on top of the 0082 registry + 0081 git primitives. Manager-level only — RPC (0084) and TUI (0085) come later.  1. Event
…[truncated]
- 2026-07-02 context hints: 8 recorded with plan
- 2026-07-02 context hints: internal/git/merge.go — TrialMerge/Merge already exist (abort-on-conflict, conflicted path listing); add DiffMergeBase here; internal/git/worktree.go — RemoveWorktree/DeleteBranch/PruneWorktrees; 
…[truncated]
- 2026-07-02 implementer report: Implemented the §6 workstream merge/integration flow at the manager level (RPC/TUI out of scope, per task).  ## Changes **internal/event/event.go** — added 4 Type constants: `WorkstreamCreated`, `W
…[truncated]
- 2026-07-02 review tier: single-opus — reviewers: Claude
- 2026-07-02 review (Claude): accept — The change implements the §6 workstream merge/integration flow at the manager level exactly as planned. Event model (4 new types + reducer projection fields + strSlice handling both []string and []an
…[truncated]
- 2026-07-02 decision: accept — commit: session: conflict-aware, review-gated workstream merge/discard flow + lifecycle events (0083)
