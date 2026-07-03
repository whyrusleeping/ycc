---
id: "0124"
title: create_task duplicates the "## Description" header in task bodies
status: done
priority: 4
created: "2026-07-02"
updated: "2026-07-03"
depends_on: []
spec_refs: []
---

## Description
The create_task tool (internal/orchestrator/modes.go, createTask) wraps the supplied description in a `"## Description\n" + desc` body. When the caller's description already begins with its own `## Description` header (agents often supply fully-structured markdown), the resulting task file contains the header twice (see e.g. backlog/0120, 0123). Same risk applies to a duplicated `## Acceptance criteria` section.

## Acceptance criteria
- [ ] createTask (and the capture path, if it shares the assembly) does not emit a duplicate `## Description` / `## Acceptance criteria` header when the supplied description already contains one
- [ ] unit test covering both a plain-text description and a pre-structured markdown description
- [ ] existing task files are unaffected (no migration required; optionally tidy the known duplicates)

## Plan

Fix duplicate section headers in generated task bodies.

1. Extract a shared helper (e.g. `taskBody(desc string) string`) in internal/orchestrator (near createTask in modes.go, or a small shared file) that assembles the task body from a supplied description:
   - empty/whitespace desc → return "" (preserve current behavior).
   - Prepend `## Description\n` only if the description does not already begin with a `## Description` header (check the first non-empty line; tolerate leading whitespace, match case-insensitively).
   - Append `\n\n## Acceptance criteria` only if the description does not already contain a `## Acceptance criteria` header line (match `^## Acceptance criteria\s*$` case-insensitively on its own line).
   - Append `\n\n## Work log\n` only if not already present as a header line.
2. Use this helper in BOTH assembly sites: createTask in internal/orchestrator/modes.go (~line 159) and the capture path in internal/orchestrator/capture.go (~line 101), replacing the inline `"## Description\n" + desc + ...` string.
3. Unit tests (modes_test.go or a new file): (a) plain-text description → body has exactly one `## Description`, one `## Acceptance criteria`, one `## Work log`; (b) pre-structured description that already starts with `## Description` and contains its own `## Acceptance criteria` → no duplicated headers, work log still appended; (c) empty description → empty body. Note: count only header LINES (regex anchored), since descriptions may mention `## Description` inline in text.
4. Tidy known duplicates in existing backlog files: backlog/0120-gollama-streaming-...md, backlog/0123-...md, and backlog/0124-...md each have a stray duplicated `## Description` and/or empty duplicate `## Acceptance criteria` section — remove the duplicate header lines only (do not touch content). Careful: inline mentions of the headers in prose must not be removed.
5. Verify: go build ./... && go test ./internal/orchestrator/...

### Starting points
- internal/orchestrator/modes.go:159 createTask body assembly
- internal/orchestrator/capture.go:101 same inline assembly in capture path
- internal/orchestrator/modes_test.go existing tests
- known duplicated headers: backlog/0120-gollama-streaming-turn-api-*.md, backlog/0123-*.md, backlog/0124-*.md

## Work log
- 2026-07-03 plan: Fix duplicate section headers in generated task bodies.  1. Extract a shared helper (e.g. `taskBody(desc string) string`) in internal/orchestrator (near createTask in modes.go, or a small shared file)
…[truncated]
- 2026-07-03 context hints: 4 recorded with plan
- 2026-07-03 context hints: internal/orchestrator/modes.go:159 — createTask inline body assembly; internal/orchestrator/capture.go:101 — capture path duplicates the same assembly; internal/orchestrator/modes_test.go — exis
…[truncated]
- 2026-07-03 implementer report: Fixed create_task duplicating the "## Description" / "## Acceptance criteria" headers.  Changes: - internal/orchestrator/modes.go: added a shared `taskBody(desc string) string` helper plus supporting 
…[truncated]
- 2026-07-03 review tier: simple (coordinator self-review)
- 2026-07-03 decision: accept — commit: orchestrator: don't duplicate section headers in create_task bodies (task 0124)  Extract a shared taskBody helper used by both create_task and the capture path; it only adds "## Description" / "## Acc
…[truncated]
- 2026-07-03 usage: 13,637 tok (in 68, out 13,569, cache_r 509,143, cache_w 30,823) · cost n/a (unpriced)
  implementer: 8,329 tok (in 50, out 8,279, cache_r 322,304, cache_w 16,840) · cost n/a (unpriced)
  coordinator: 5,308 tok (in 18, out 5,290, cache_r 186,839, cache_w 13,983) · cost n/a (unpriced)
