---
id: "0123"
title: Stop generating the backlog.md index file (duplicate of backlog/ task files)
status: todo
priority: 3
created: "2026-07-02"
updated: "2026-07-02"
depends_on: []
spec_refs:
    - Backlog
---

## Description
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

## Acceptance criteria

## Work log
