---
id: "0591"
title: Line-attribute all docs-layer Markdown, not only backlog task files and memory.md
status: proposed
priority: 2
created: "2026-11-23"
updated: "2026-11-23"
depends_on: []
spec_refs:
    - spec.md#7.3
---

## Description
Current state: proposed on 2026-11-22 from `docs/reports/shared-tree-concurrency-2026-11.md`. The user asked for it to be filed as a proposal in session s_fc31ffbed8395135; it is not yet accepted.

Backlog task files and `memory.md` are attributed line by line (0566, line-attributed bookkeeping commits; spec §7.3, `ycc/bookkeeping-lines.json`). Other docs-layer Markdown, including `spec.md`, `docs/**` and `plans/`, is still claimed and committed whole. Of 158 commits since 2026-11-01, 54 touched `spec.md`. On 2026-11-22, review rounds for 0580 (the other-work review note) and 0582 (the project sessions view) flagged other sessions' `spec.md` and `docs/remote-api.md` hunks. Those files were then landed by hand with `git apply --cached`. Prose hunks need no build, so the existing line mechanism fits.

## Acceptance criteria
- Docs-layer Markdown written through file tools or the docs store is attributed by line under the docs-store lock, like backlog and memory files. Review snapshots and commits take HEAD plus this session's and unattributed lines.
- Two sessions editing different sections of `spec.md` each review and commit only their own lines. The other session's lines stay uncommitted in the worktree. Tests cover this case.
- Overlapping edits fall back to the existing rule for bookkeeping files (take the other change along when needed for a well-formed result, otherwise refuse naming the alternatives).
- spec §7.3 describes the wider scope.

## Work log
