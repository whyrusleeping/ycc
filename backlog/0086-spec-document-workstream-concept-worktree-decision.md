---
id: "0086"
title: 'Spec: document workstream concept + worktree decision'
status: done
priority: 4
created: "2026-06-30"
updated: "2026-07-02"
depends_on:
    - "0078"
spec_refs:
    - Open questions
    - Persistence & remote sync
---

## Description
## Context
Final step of the parallel-workstreams design (see `docs/design/parallel-workstreams.md` §10.6). Keep the spec true (spec §1) by recording the decision and the new concept.

## Scope
- Revise spec §17 "Implementer isolation" to record the decision: git worktrees are adopted for parallel workstreams (the deferred revisit is now resolved).
- Add a short section describing the workstream concept, the daemon-side workstream registry, and the conflict-aware, review-gated merge flow.

## Acceptance criteria
- [ ] Spec §17 "Implementer isolation" updated to reflect the worktree decision.
- [ ] A spec section documents the workstream concept, registry, and merge flow consistent with the design doc.
- [ ] No stale "revisited only if/when" language remains where the decision is now made.

## Acceptance criteria

## Plan

Docs-only change to spec.md, keeping it consistent with both docs/design/parallel-workstreams.md and the shipped implementation (internal/workstream, internal/session/workstream_merge.go, internal/git/worktree.go, proto/ycc/v1).

1. Revise spec §17 "Open questions" → the "Implementer isolation" bullet: record that the deferred revisit is resolved — git worktrees are ADOPTED for parallel workstreams (design spike task 0078; docs/design/parallel-workstreams.md). Single-task implementers still edit the primary tree directly; parallel workstreams each get their own linked worktree. Remove the stale "Git worktrees revisited only if/when we want parallel tasks" language and point at the new section.

2. Add a new subsection §14.1 "Parallel workstreams (git worktrees)" under §14 "Persistence & remote sync" (task spec_refs name this section) documenting, concisely:
   - Concept: a workstream = a linked git worktree + branch `ycc/ws/<id>[-<task>]` + a `work` session scoped to the worktree's absolute path. A workstream is a CHILD of a project — never a top-level project entry (keeps the project picker clean).
   - Worktree location: out of the primary tree, under the daemon state dir `<state>/ycc/worktrees/<project>/<workstream-id>`; each worktree has its own `.ycc/sessions/<id>/events.jsonl` (git-ignored, never travels into the merge).
   - Single-writer invariant (spec §14) holds verbatim, per tree: one daemon/coordinator writes each worktree; parallelism is across trees.
   - Daemon-side workstream registry: serialized JSON (`workstreams.json` in the state dir, beside `projects.json`), id → {project, base commit, branch, worktree path, session id, status}; startup recovery reconciles stale worktrees via `git worktree list`/`prune`.
   - Merge flow: conflict-aware, sequential, review-gated. Non-mutating trial merge first; clean → auto-merge under autonomous level, otherwise gated behind explicit acceptance with the integrated diff; conflicted → emit `workstream_conflict` with the conflicted paths and stop — base is never left conflicted, the worktree is kept for resolution. Cleanup on merge/discard: `worktree remove`, branch delete, `prune`.
   - Lifecycle events on the workstream's own session stream: `workstream_created`, `workstream_merged`, `workstream_conflict`, `workstream_discarded`.
   - RPC surface (§12): `SpawnWorkstream`, `ListWorkstreams`, `PreviewMerge`, `MergeWorkstream`, `DiscardWorkstream`; `Subscribe` reused verbatim for the workstream's session stream.
   - One-line pointer to docs/design/parallel-workstreams.md for full rationale/alternatives.

3. If spec §12 enumerates the RPC set, add the five workstream RPCs there briefly (one line/group) so the RPC list stays true; skip if §12 is illustrative rather than exhaustive.

4. Grep spec.md for any other stale "revisited only if/when" / single-tree-only phrasing touching implementer isolation and fix if found.

Verification: re-read the diff for consistency with the design doc's §5–§8 and the actual code identifiers (branch prefix, state-dir paths, event type names, RPC names); no build needed (markdown only).

### Starting points
- spec.md:764-777 — §17 Open questions, the 'Implementer isolation' bullet to revise
- spec.md:695-709 — §14 Persistence & remote sync, where §14.1 goes
- docs/design/parallel-workstreams.md §5–§8 — lifecycle, merge strategy, state model, UX/RPC sketch
- internal/workstream/registry.go — workstreams.json + DefaultWorktreesRoot (<state>/ycc/worktrees)
- internal/session/session.go SpawnWorkstream — branch 'ycc/ws/<id>[-<task>]', worktree dir layout
- internal/session/workstream_merge.go — MergePreview/MergeOutcome semantics (NeedsAccept, conflicts keep worktree)
- internal/event/event.go:85-94 — workstream_created/merged/conflict/discarded event types
- proto/ycc/v1/ycc.proto:408-473,558-563 — workstream RPC surface

## Work log
- 2026-07-02 plan: Docs-only change to spec.md, keeping it consistent with both docs/design/parallel-workstreams.md and the shipped implementation (internal/workstream, internal/session/workstream_merge.go, internal/git
…[truncated]
- 2026-07-02 context hints: 8 recorded with plan
- 2026-07-02 context hints: spec.md:764-777 — §17 Open questions, the 'Implementer isolation' bullet to revise; spec.md:695-709 — §14 Persistence & remote sync, where §14.1 goes; docs/design/parallel-workstreams.md §5–
…[truncated]
- 2026-07-02 implementer report: Docs-only change to spec.md implementing task 0086. Three edits, all consistent with docs/design/parallel-workstreams.md and the shipped code (internal/workstream, internal/session/workstream_merge.go
…[truncated]
- 2026-07-02 review tier: simple (coordinator self-review)
- 2026-07-02 decision: accept — commit: spec: document workstream concept + record worktree decision (§14.1, §17, §12 RPCs)
- 2026-07-02 usage: 12,399 tok (in 52, out 12,347, cache_r 613,216, cache_w 61,554) · cost n/a (unpriced)
  coordinator: 8,260 tok (in 30, out 8,230, cache_r 382,499, cache_w 31,419) · cost n/a (unpriced)
  implementer: 4,139 tok (in 22, out 4,117, cache_r 230,717, cache_w 30,135) · cost n/a (unpriced)
