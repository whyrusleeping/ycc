---
id: "0346"
title: Validate model tool arguments before execution; prevent destructive empty defaults
status: done
priority: 1
created: "2026-09-08"
updated: "2026-09-08"
depends_on: []
spec_refs:
    - §8 Tools and access policy
---

## Description
Harness review found that Registry.Dispatch delegates to gollama.HandleToolCall, which unmarshals JSON but does not validate the declared schema. Write ignores an absent or wrongly typed content argument and overwrites with an empty string; Edit does the same for new_string and deletes the target. Other invalid values silently select defaults. Evidence: internal/tools/tools.go:90–103; internal/tools/worker.go:249–257,284–308; pinned gollama tools.go.

## Acceptance criteria
- Validate required fields, types, enums, numeric bounds, and nested collections centrally before handlers execute, after conservative argument repair where applicable.
- Distinguish absent, invalid, and explicitly empty values; explicit empty string remains valid for intentional deletion/empty files.
- Invalid requests return field-specific actionable errors and make no filesystem, job, or durable-project mutation.
- Preserve existing conservative argument repair and explain any repair to the model.
- Tests exercise dispatch, not just helpers: missing/non-string Write content and Edit replacement, malformed nested arguments, invalid enum/bounds, and string-valued booleans. All destructive invalid cases leave existing files unchanged.
- Inventory existing schemas/handlers for mismatches without broad unrelated tool redesign.

## Outcome

Central dispatch validates required fields, declared types, nested collections, enums, and numeric bounds after conservative argument repair and before handlers. Invalid calls return field-specific errors without execution; explicit empty Write/Edit strings remain valid. Null root arguments are rejected, and repair diagnostics survive both engine dispatch and validation failures.

Schema inventory aligned task priority, Read/preload ranges, wait timeout, web result count, and context-mode enums with intended handler semantics. Dispatch regressions cover destructive file safety, nested validation, command non-execution, durable backlog non-mutation, and null versus empty-object arguments.

Verification: full `go test ./...` passed on a task-only snapshot independent of unrelated workspace edits; both comprehensive reviewers accepted the final revision. Unrelated pre-existing changes preserved.

Commit subject: Validate tool arguments before executing handlers
