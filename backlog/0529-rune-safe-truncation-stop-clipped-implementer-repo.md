---
id: "0529"
title: Rune-safe truncation; stop clipped implementer reports in work logs
status: done
priority: 3
created: "2026-11-10"
updated: "2026-11-10"
depends_on: []
spec_refs: []
---

## Description

## Description
## Why
`truncate` in `internal/orchestrator/orchestrator.go` slices bytes (`s[:n]`). `oneLine` uses it to cut implementer reports and block reasons to 200 bytes, and `AppendWorkLog` stores the result with a literal `…[truncated]` marker (lines ~1742 and ~1771). This has two effects:
- 10 backlog files across ycc and valstore contain invalid UTF-8 where a multi-byte character was split (e.g. ycc `backlog/0103-*.md`, valstore `backlog/0042-*.md`).
- About 790 task files across projects keep the first 200 bytes of a report and lose the evidence or finding that would make the entry useful.

The same `truncate` bounds injected memory (`modes.go`, `maxInjectedMemory`) and the implementer report and diff handoffs.

## Scope
- Make `truncate` cut on a rune boundary.
- Stop appending clipped reports to the work log. Either append nothing, since the session log keeps the full report and the coordinator writes the outcome, or append a short complete summary. The work-log rules in the doc-style backlog contract (filed with this task) say entries are outcomes and evidence, not tool output.
- Don't rewrite existing files here; the backlog-groom preset handles cleanup.

## Acceptance criteria
- Truncation never produces invalid UTF-8; a focused test covers a multi-byte boundary.
- New work-log entries from the implementer path contain no `…[truncated]` fragments.
- `go test ./internal/orchestrator ./internal/docs` passes.

## Acceptance criteria

- Truncation never produces invalid UTF-8; a focused test covers a multi-byte boundary.
- New work-log entries from the implementer path contain no `…[truncated]` fragments.
- `go test ./internal/orchestrator ./internal/docs` passes.

## Outcome

truncate and oneLine now cut on a rune boundary (reusing validUTF8Prefix); oneLine stays single-line with a "…" suffix. Implementer success reports are no longer appended to the work log (the session log keeps them; the coordinator records outcomes). Blocked reasons are recorded whole on one line so blockedReasonFromBody still finds them. New truncate_test.go covers every multi-byte cut point and both work-log paths; go test ./internal/orchestrator ./internal/docs ./internal/session passes. Existing backlog files are left for backlog-groom.

Commit: Rune-safe truncate/oneLine; stop copying clipped implementer reports into work logs (0529)
