---
id: "0302"
title: The session titles in work loop mode are all the same and not super useful, should update the session title on task selection
status: done
priority: 3
created: "2026-08-08"
updated: "2026-08-08"
depends_on: []
spec_refs: []
---

## Description

## Acceptance criteria

## Plan

Problem: every work-loop session starts with the same canned kickoff prompt ("Work on the backlog: choose the next ready task…"), and SessionSummary.Title is derived from the first user_input (firstUserPrompt in internal/session/history.go). So all work-loop sessions show identical, useless titles in the TUI browser and iOS session list.

Fix (derivation-side, no new events; task_focus already carries the task id AND its title — see orchestrator.Deps.emitFocus):

1. In internal/session/history.go, replace the `Title: firstUserPrompt(evs)` call with a new `deriveTitle(evs)`:
   - Compute the raw first non-empty user_input text (trimmed, pre-truncation).
   - Find the FIRST task_focus event with a non-empty "task"; capture its optional "title" field.
   - If such a focus exists AND the opening prompt is empty or a canned default (exact trimmed match against defaultPrompt("work") / defaultPrompt("chat") / defaultPrompt("pm") in session.go — same package), return truncateTitle("<taskid> — <task title>") (just the id when the event has no title).
   - Otherwise keep today's behavior: truncateTitle of the first user prompt (user-typed prompts stay authoritative).
2. Because ListSessionHistory re-scans the persisted log on each call, a live loop session's title updates as soon as the coordinator focuses a task — satisfying "update the session title on task selection" with zero live-state plumbing. Title flows to TUI + iOS via server.go's existing SessionSummary mapping; no proto/client change needed.
3. Tests in internal/session/history_test.go:
   - work-loop-style log (canned work kickoff + task_focus with title) → title "0007 — <task title>".
   - task_focus without title data → title is the bare id.
   - custom user prompt + task_focus → title stays the user prompt.
   - no task_focus + canned prompt → title stays the truncated canned prompt (today's behavior).
4. Verify: go build ./... && go test ./internal/session/ (note: internal/session has known flaky tests unrelated to this — compare against HEAD if something fails).

### Starting points
- internal/session/history.go — firstUserPrompt/truncateTitle/scanSessionHistory (title derivation lives here)
- internal/session/session.go:1288 defaultPrompt(mode) — the canned kickoff strings (same package)
- internal/orchestrator/orchestrator.go:168 emitFocus — task_focus events carry data keys "task" and "title"
- internal/session/history_test.go — existing scan/summary test patterns
- server.go maps su.Title straight into the proto SessionSummary (line ~169); no client changes needed

## Work log
- 2026-08-08 plan: Problem: every work-loop session starts with the same canned kickoff prompt ("Work on the backlog: choose the next ready task…"), and SessionSummary.Title is derived from the first user_input (first
…[truncated]
- 2026-08-08 context hints: 5 recorded with plan
- 2026-08-08 context hints: internal/session/history.go — firstUserPrompt/truncateTitle/scanSessionHistory; internal/session/session.go:1288 defaultPrompt(mode); internal/orchestrator/orchestrator.go:168 emitFocus — emits ev
…[truncated]
- 2026-08-08 preload: 2 file(s), ~14 KiB seeded into implementer context
- 2026-08-08 implementer report: Implemented task 0302.  Changes: - Replaced persisted session title derivation with `deriveTitle` in `internal/session/history.go`. - Canned work/chat/pm kickoff prompts (or a missing opening prompt) 
…[truncated]
- 2026-08-08 review tier: single-opus — reviewers: sol
- 2026-08-08 review (sol): accept — The change correctly derives persisted session titles from the first non-empty task focus when the opening prompt is absent or exactly matches a canned work/chat/pm prompt, while preserving custom use
…[truncated]
- 2026-08-08 usage: 240,155 tok (in 152,822, out 14,117, cache_r 678,034, cache_w 35,721) · cost n/a (unpriced)
  implementer: 172,405 tok (in 103,648, out 2,709, cache_r 66,048, cache_w 0) · cost n/a (unpriced)
  reviewer:sol: 57,093 tok (in 49,134, out 791, cache_r 7,168, cache_w 0) · cost n/a (unpriced)
  coordinator: 10,657 tok (in 40, out 10,617, cache_r 604,818, cache_w 35,721) · cost n/a (unpriced)
