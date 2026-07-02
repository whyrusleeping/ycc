---
id: "0084"
title: RPC surface for workstreams (spawn/list/preview/merge/discard)
status: done
priority: 4
created: "2026-06-30"
updated: "2026-07-02"
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

## Plan

Expose the existing workstream lifecycle (Manager.SpawnWorkstream / Workstreams / PreviewWorkstreamMerge / MergeWorkstream / DiscardWorkstream — all implemented in tasks 0082/0083) over the Connect RPC surface.

1. Proto (proto/ycc/v1/ycc.proto):
   - `WorkstreamInfo` message mirroring workstream.Workstream: id, project, base_commit, branch, worktree_path, session_id, task_id, status, created_at (RFC3339).
   - `SpawnWorkstreamRequest{project, base_ref, task_id, prompt, interaction_level}` → `SpawnWorkstreamResponse{WorkstreamInfo workstream}` (session_id is inside the info, per design §8 sketch).
   - `ListWorkstreamsRequest{project}` → `ListWorkstreamsResponse{repeated WorkstreamInfo}`.
   - `PreviewMergeRequest{workstream_id}` → `PreviewMergeResponse{clean, repeated conflicts, diff}`.
   - `MergeWorkstreamRequest{workstream_id, accept}` → `MergeWorkstreamResponse{merged, commit, needs_accept, diff, repeated conflicts}`. NOTE: the design §8 sketch says "strategy" but explicitly calls itself a sketch; the manager's real gate is the accept bool (review-gated clean merge under interactive/judgement levels), so the proto carries `accept`. Document this in a comment.
   - `DiscardWorkstreamRequest{workstream_id}` → `DiscardWorkstreamResponse{}`.
   - Add the five rpcs to SessionService with doc comments referencing docs/design/parallel-workstreams.md §6/§8. Subscribe(session_id) is reused as-is for per-workstream streams — say so in the comment.
2. Regenerate: `buf generate` (and `buf lint proto`) — protoc-gen-go / protoc-gen-connect-go are installed at ~/go/bin.
3. Server handlers (internal/server/server.go): thin delegation to the manager. Error mapping: empty required fields → CodeInvalidArgument; "unknown workstream"/"unknown project" → CodeNotFound; workstream.ErrWorktreeInUse → CodeFailedPrecondition; not-active status → CodeFailedPrecondition; other errors → CodeInternal (follow the file's existing conventions). Add a toWorkstreamInfo converter.
4. Tests:
   a. internal/server: an end-to-end scripted-client test that stands up the real Connect handler on an httptest h2c server with a yccv1connect client (this simultaneously proves the HTTP path): register a temp git project (reuse the newWorkstreamManager idiom from internal/session/workstream_test.go, adapted locally), spawn 2 workstreams over RPC, commit a clean change in ws1's worktree and a conflicting change (same file changed on base + ws2), Subscribe(session_id) for each and observe events (at least workstream_created replayed; merged/conflict after the merge call), PreviewMerge for both (ws1 clean+diff, ws2 conflicts, and verify base HEAD unchanged after preview), MergeWorkstream both with accept (ws1 merged with commit; ws2 returns conflicts and base HEAD untouched, worktree kept, registry still active).
   b. An explicit HTTP/JSON test: POST application/json to /ycc.v1.SessionService/ListWorkstreams (and/or SpawnWorkstream) on the httptest server and assert a JSON response decodes — proving the Connect JSON codec path.
   c. Unit-ish error mapping tests: unknown workstream id → NotFound for Preview/Merge/Discard; missing project → InvalidArgument for Spawn.
5. Verify: go build ./... && go vet ./... && go test ./...

Scope note: TUI wiring is task 0085; no client/TUI changes here.

### Starting points
- proto/ycc/v1/ycc.proto — add messages + rpcs; regenerate with `buf generate`
- internal/server/server.go — handler conventions (connect codes, toProto, errNoSession)
- internal/session/session.go: SpawnWorkstreamConfig, Manager.SpawnWorkstream, Manager.Workstreams
- internal/session/workstream_merge.go: MergePreview, MergeOutcome, PreviewWorkstreamMerge, MergeWorkstream(id, accept), DiscardWorkstream
- internal/workstream/registry.go: Workstream struct, Status, ErrWorktreeInUse
- internal/session/workstream_test.go: newWorkstreamManager test idiom (temp git repo + project.NewMemory + workstream.NewMemory)
- internal/session/workstream_merge_test.go: commitInto helper for making branches diverge
- internal/daemon/serve.go: buildHandler shows h2c + NewSessionServiceHandler wiring for an httptest server
- git.Open(dir) auto-inits? see internal/git/worktree.go + tests for how temp repos get an initial commit

## Work log
- 2026-07-02 plan: Expose the existing workstream lifecycle (Manager.SpawnWorkstream / Workstreams / PreviewWorkstreamMerge / MergeWorkstream / DiscardWorkstream — all implemented in tasks 0082/0083) over the Connect 
…[truncated]
- 2026-07-02 context hints: 9 recorded with plan
- 2026-07-02 context hints: proto/ycc/v1/ycc.proto — add messages + rpcs; regenerate with `buf generate` (run from repo root; plugins at ~/go/bin); internal/server/server.go — handler conventions (connect codes, toProto, err
…[truncated]
- 2026-07-02 implementer report: Exposed the workstream lifecycle over the Connect RPC surface (task 0084).  ## Changes - **proto/ycc/v1/ycc.proto**: Added `WorkstreamInfo` (mirrors `workstream.Workstream`: id, project, base_commit, 
…[truncated]
- 2026-07-02 review tier: single-opus — reviewers: Claude
- 2026-07-02 review (Claude): accept — The change exposes the full workstream lifecycle (spawn/list/preview/merge/discard) over the Connect RPC surface exactly as specified. Proto messages and RPCs were added with good documentation and re
…[truncated]
- 2026-07-02 decision: accept — commit: rpc: expose workstream lifecycle (spawn/list/preview/merge/discard) over Connect (0084)
- 2026-07-02 usage: 29,351 tok (in 106, out 29,245, cache_r 2,980,825, cache_w 201,216) · cost n/a (unpriced)
  implementer: 20,390 tok (in 60, out 20,330, cache_r 2,058,667, cache_w 92,466) · cost n/a (unpriced)
  coordinator: 6,099 tok (in 24, out 6,075, cache_r 763,690, cache_w 80,634) · cost n/a (unpriced)
  reviewer:Claude: 2,862 tok (in 22, out 2,840, cache_r 158,468, cache_w 28,116) · cost n/a (unpriced)
