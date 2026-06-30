---
id: "0084"
title: RPC surface for workstreams (spawn/list/preview/merge/discard)
status: todo
priority: 4
created: "2026-06-30"
updated: "2026-06-30"
depends_on:
    - "0082"
    - "0083"
spec_refs:
    - RPC protocol (Connect)
    - Process & data-flow model
---

## Description
## Context
Fourth step of the parallel-workstreams design (see `docs/design/parallel-workstreams.md` §8, §10.4). Expose the workstream lifecycle over Connect RPC.

## Scope
- Add to `proto/ycc/v1`: `SpawnWorkstream(project, base_ref, task_id?, prompt?, interaction_level)`, `ListWorkstreams(project)`, `PreviewMerge(workstream_id)`, `MergeWorkstream(workstream_id, strategy)`, `DiscardWorkstream(workstream_id)`.
- Server handlers delegate to the session manager / workstream registry. Reuse `Subscribe(session_id)` for per-workstream event streaming.

## Acceptance criteria
- [ ] New RPCs defined in proto + regenerated; server handlers wired to the manager.
- [ ] `PreviewMerge` returns clean/conflicts + diff without mutating base.
- [ ] A scripted client can spawn 2 workstreams, observe both run via Subscribe, and merge both back (one clean, one conflicting) with the conflict surfaced.
- [ ] HTTP/JSON path works (consistent with the Connect surface).
- [ ] build/vet/test pass.

## Acceptance criteria

## Work log
