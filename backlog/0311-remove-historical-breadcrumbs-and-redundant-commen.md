---
id: "0311"
title: Remove historical breadcrumbs and redundant comments from production code
status: done
priority: 3
created: "2026-08-10"
updated: "2026-08-10"
depends_on:
    - "0310"
spec_refs:
    - CONTRIBUTING.md#Documentation and comments
    - docs/design/doc-style.md#Shared register
---

## Description
Apply the lean comment standard across production Go and Swift code. Remove task-number history, routine spec-section citations, comments that restate code, and stale implementation chronology while preserving non-obvious security, concurrency, persistence, compatibility, and lifecycle reasoning.

## Acceptance criteria
- Production comments no longer use backlog task IDs as implementation history.
- Routine spec-section citations are removed; any retained design link is necessary to understand a non-local invariant.
- Comments that merely restate names or nearby code are deleted or shortened.
- Security, data-integrity, concurrency, compatibility, and surprising-behavior rationale remains clear.
- The cleanup is behavior-neutral and Go tests pass; Swift changes are compile-checked on a Swift-capable machine.

## Plan

1. Inventory production Go, Swift, and protobuf-source comments for backlog task-number history, routine spec/design section citations, stale chronology, and narration that only repeats the adjacent declaration; exclude tests and generated files from direct editing. 2. Remove historical/citation-only text and delete or shorten low-value comments, while retaining and clarifying rationale for security, data integrity, concurrency, persistence, lifecycle, compatibility, and surprising behavior. For generated RPC/model comments, edit proto source and regenerate both Go and committed Swift protobuf/connect outputs rather than hand-editing generated files. 3. Format/regenerate as needed, re-run targeted searches to confirm no production task-ID breadcrumbs remain and assess any retained design links as necessary non-local invariants. 4. Run the full Go test suite and available protobuf/Swift compile checks, documenting any unavailable Swift toolchain or pre-existing flaky failure. 5. Keep the existing uncommitted task 0310 worktree delta intact so task 0311 can be reviewed and committed independently.

### Starting points
- CONTRIBUTING.md#Documentation and comments defines the lean standard.
- docs/design/doc-style.md#Shared register governs direct wording.
- The worktree already contains uncommitted task 0310 changes in README.md, spec.md, internal/orchestrator/* and new backlog/docs files; preserve that baseline and avoid those files unless essential.
- Search production Go/Swift and proto source for task IDs, routine spec citations, chronology, and comments that simply restate nearby declarations. Generated protobuf comments must be changed at their proto source and both Go/Swift outputs regenerated.

## Work log
- 2026-08-10 plan: 1. Inventory production Go, Swift, and protobuf-source comments for backlog task-number history, routine spec/design section citations, stale chronology, and narration that only repeats the adjacent d
…[truncated]
- 2026-08-10 context hints: 4 recorded with plan
- 2026-08-10 context hints: CONTRIBUTING.md#Documentation and comments defines the lean standard.; docs/design/doc-style.md#Shared register governs direct wording.; The worktree already contains uncommitted task 0310 changes in
…[truncated]
- 2026-08-10 preload: 3 file(s), ~7 KiB seeded into implementer context
- 2026-08-10 implementer report: Implemented task 0311 as a behavior-neutral production-comment cleanup across Go, Swift, and proto sources. Removed backlog/task-number implementation history, routine spec/design section citations, s
…[truncated]
- 2026-08-10 revision: Fixed the index separation without changing worktree content:  - Reset the index only with `git reset`. - Selectively staged task 0311’s 132 production comment/proto/generated-output files plus the
…[truncated]
- 2026-08-10 review tier: single-opus — reviewers: sol
- 2026-08-10 review (sol): revise — The substantive 0311 edits are behavior-neutral, remove the targeted task/spec breadcrumbs, preserve important rationale, and the full Go suite passes. However, the task is not ready to land independe
…[truncated]
- 2026-08-10 revision: Repaired the malformed-comment cleanup comprehensively.  Changes: - Fixed all cited artifacts, including missing spaces in `.ycc`, `.git`, `.env`, IPv6 `::1`, `ycc ... | tail`, `## ...`, and provider
…[truncated]
- 2026-08-10 review (sol): accept — The revision correctly isolates 0311 in the index, repairs the malformed comment edits, remains behavior-neutral, and passes the full Go test suite. The targeted task-ID and routine spec-section bread
…[truncated]
- 2026-08-10 review (sol): accept — The remaining historical milestone/date breadcrumbs have been removed from the proto, regenerated outputs, and engine comment. Task 0311 remains cleanly isolated from the unstaged 0310 work, the stage
…[truncated]
- 2026-08-10 decision: accept — commit: Clean up production code comments
