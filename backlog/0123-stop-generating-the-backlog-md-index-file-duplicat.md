---
id: "0123"
title: Stop generating the backlog.md index file (duplicate of backlog/ task files)
status: done
priority: 3
created: "2026-07-02"
updated: "2026-07-03"
depends_on: []
spec_refs:
    - Backlog
---

## Description

The docs store (`internal/docs`, spec §6.2) renders the entire backlog into a single generated `backlog.md` index at the repo root on every mutation. The canonical data already lives in `backlog/<id>-<slug>.md` (one file per task with YAML frontmatter), so the index is just duplicated content that churns in git on every backlog change and requires per-directory locking to keep it consistent.

Remove the generated index:

- Drop the `backlog.md` regeneration from the docs store (create/update/status-change paths).
- Delete the existing generated `backlog.md` from the repo.
- Update the spec (§6.2) and any prompts/docs that mention `backlog.md` so agents don't look for or try to regenerate it.
- Check for any code that *reads* `backlog.md` (TUI, orchestrator prompts, onboarding) and point it at the per-task files / ListBacklog instead.
- Simplify or remove the index-related locking/regeneration logic if it exists only for the index.

## Acceptance Criteria

- [ ] Backlog mutations (add/update/mark done) no longer write or update a `backlog.md` file.
- [ ] `backlog.md` is removed from the repo and nothing in the codebase depends on its existence.
- [ ] Spec/prompt references to the generated index are updated accordingly.
- [ ] Existing backlog listing features (ListBacklog RPC, TUI backlog browser, list_backlog tool) still work from the canonical `backlog/` files.
- [ ] Tests covering index generation are updated/removed; `go test ./...` passes.

## Plan

Remove the generated backlog.md index; the per-task files under backlog/ remain the canonical (and only) store.

1. internal/docs/docs.go:
   - Delete RenderIndex and renderIndexLocked.
   - Update the package comment (drop the "plus a generated backlog.md index" clause).
   - KEEP the per-directory locking (dirLocks / Store.mu) — it serializes concurrent mutations (next-id assignment, concurrent Store instances from work session + capture agent), not just index rendering — but reword the dirLocks comment so it no longer justifies itself via the generated index.
2. internal/orchestrator/:
   - orchestrator.go:719 — remove d.Docs.RenderIndex() call in the update_task tool; fix the tool Description ("...and regenerate the backlog index" → drop that clause).
   - modes.go:165 and capture.go:107 — remove RenderIndex() calls in create_task tools; fix the create_task Description ("Regenerates the backlog index." → drop).
3. internal/server/server.go: remove store.RenderIndex() in UpdateTask (~line 575) and its comment; update the UpdateTask doc comment (~line 532) — a no-mutation request is still a valid "refresh" that re-reads the task file, just no index regeneration.
4. proto/ycc/v1/ycc.proto: update the UpdateTaskRequest comment (~line 334) to drop the backlog.md mention, then regenerate with `buf generate` (buf is installed) so ycc.pb.go comments match.
5. internal/tui/tui.go: update stale comments (lines ~557, ~1553, ~2328, ~7293) that mention regenerating backlog.md. Behavior unchanged (the no-mutation UpdateTask refresh path stays — it still re-reads hand-edited files).
6. Tests:
   - internal/docs/docs_test.go: remove TestRenderIndex; if the concurrency test (~line 155) uses RenderIndex, repoint it at Create/Update/List so lock coverage remains.
   - internal/server/server_test.go: drop index-file assertions (indexPath reads, os.Remove/os.Stat checks around lines 214–273); keep the rest of the UpdateTask RPC test asserting task-file mutation/refresh.
   - internal/tui/tui_test.go ~line 779: keep the "non-task .md file in backlog/ doesn't count" case but rename it away from "generated index" wording (still worth covering that stray .md files are ignored).
7. spec.md: §6.2 (~lines 227, 261) — remove the generated index from the description and from the docs-package responsibilities; §7 flow (~line 508) — "on finish: regenerate backlog.md" → drop the regeneration step (e.g. "on finish: emit session_idle, return to user").
8. Delete the generated backlog.md file at the repo root (git rm).
9. Verify: go build ./... && go vet ./... && go test ./...; rg 'backlog\.md' to confirm no remaining code/spec references outside backlog/ task-history files (historical mentions in old task files are fine and must NOT be edited).

Note: the currently running ycc daemon (old binary) may re-create backlog.md when the coordinator updates task status; the coordinator will remove any regenerated backlog.md just before committing.

### Starting points
- internal/docs/docs.go: RenderIndex/renderIndexLocked (lines ~315-340), dirLocks comment (~20-28), package comment
- internal/orchestrator/orchestrator.go:703,719 (update_task tool)
- internal/orchestrator/modes.go:146,165 and internal/orchestrator/capture.go:107 (create_task tools)
- internal/server/server.go:529-577 (UpdateTask RPC)
- proto/ycc/v1/ycc.proto:331-334; regen with `buf generate`
- internal/tui/tui.go comments at ~557, ~1553, ~2328, ~7293
- internal/docs/docs_test.go:116-175, internal/server/server_test.go:210-273, internal/tui/tui_test.go:777-783
- spec.md:224-262 (§6.2), spec.md:508 (§7 flow)

## Work log
- 2026-07-03 plan: Remove the generated backlog.md index; the per-task files under backlog/ remain the canonical (and only) store.  1. internal/docs/docs.go:    - Delete RenderIndex and renderIndexLocked.    - Update th
…[truncated]
- 2026-07-03 context hints: 8 recorded with plan
- 2026-07-03 context hints: internal/docs/docs.go: RenderIndex/renderIndexLocked (~315-340), dirLocks comment (~20-28), package comment; internal/orchestrator/orchestrator.go:703,719; internal/orchestrator/modes.go:146,165; inte
…[truncated]
- 2026-07-03 implementer report: Removed the generated `backlog.md` index; per-task files under `backlog/` remain the only canonical store.  Changes: - **internal/docs/docs.go**: Deleted `RenderIndex`/`renderIndexLocked`. Updated the
…[truncated]
- 2026-07-03 review tier: single-opus — reviewers: Claude
- 2026-07-03 review (Claude): accept — The change fully removes the generated backlog.md index: RenderIndex/renderIndexLocked are deleted from the docs store, all call sites (orchestrator create_task/update_task tools, capture agent, serve
…[truncated]
- 2026-07-03 decision: accept — commit: Remove generated backlog.md index; backlog/ task files are the sole store (task 0123)
- 2026-07-03 usage: 25,383 tok (in 154, out 25,229, cache_r 2,032,508, cache_w 98,500) · cost n/a (unpriced)
  implementer: 15,433 tok (in 104, out 15,329, cache_r 1,425,156, cache_w 40,395) · cost n/a (unpriced)
  coordinator: 7,311 tok (in 36, out 7,275, cache_r 545,000, cache_w 39,494) · cost n/a (unpriced)
  reviewer:Claude: 2,639 tok (in 14, out 2,625, cache_r 62,352, cache_w 18,611) · cost n/a (unpriced)
