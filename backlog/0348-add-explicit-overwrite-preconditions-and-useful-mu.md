---
id: "0348"
title: Add explicit overwrite preconditions and useful mutation receipts to file tools
status: done
priority: 2
created: "2026-09-08"
updated: "2026-09-09"
depends_on: []
spec_refs:
    - §8 Tools and access policy
---

## Description
Prevent accidental full-file overwrites and expose useful bounded mutation receipts, complementing exact Edit and the future transactional Patch tool.

## Acceptance criteria
- Write exposes explicit create/overwrite policy and a revision precondition; stale/conflicting writes leave files unchanged.
- Missing/invalid fields never implicitly authorize replacement; explicitly empty content remains valid.
- Receipts distinguish creation and overwrite, include before/after metrics and resulting revisions, and provide bounded line-referenced Edit changed regions.
- Preserve exact matching, path confinement, file conventions, and document-update hooks without fuzzy mutation.
- Cover creation, accidental overwrite, stale content, intended empty files, bounded receipts, and failure preservation.

## Outcome
Write defaults to create-only; explicit overwrite requires a full-content SHA-256 expected revision. Read exposes source revisions separately from projection hashes within its revision bound; larger files can use sha256sum. Overwrite revision checks stream existing content with bounded memory. Atomic publication preserves existing modes, honors umask for creation, and retains confinement and OnWrite hooks. Write/Edit return structured metrics and resulting revisions; Edit includes bounded line-referenced excerpts. Patch remains separate scope (0325).

Verification: both comprehensive reviewers accepted after streaming and umask fixes. Full `go test ./...` passed in the workspace and on an isolated task-only snapshot; reviewers also passed `go test -race ./internal/tools`. Unrelated pre-existing changes preserved through a selective commit.

Commit subject: Guard file overwrites with revisions and return bounded mutation receipts
