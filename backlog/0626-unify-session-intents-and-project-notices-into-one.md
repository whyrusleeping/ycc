---
id: "0626"
title: Unify session intents and project notices into one notice concept and tool
status: todo
priority: 3
created: "2026-11-26"
updated: "2026-11-26"
depends_on: []
spec_refs:
    - spec.md#7.3 Subagents and asynchronous jobs
---

## Description
## Status
Accepted (owner decision, 2026-11-26, chat session reviewing coordinator tool count — Option A). Not started.

## Why
Session coordination currently splits peer status across two concepts and three coordinator tools: `set_intent` (one ephemeral live-session line), `post_notice` (expiring project board entry), and `clear_notice` (clear by id). In recent logs (12-day cross-project audit, 2026-11-26), agents sometimes double-post the same status to both `set_intent` and `post_notice`, and every prompt, checkpoint digest, session listing, and client view has to explain and render both concepts separately. The only semantic difference is lifetime and cardinality: a session-scoped live status line (one per session, auto-clears when the session ends) versus a timed project notice (persists past session end until expiry or clear).

## Scope
- Replace `set_intent`, `post_notice`, and `clear_notice` with a single unified `notice` concept and coordinator tool (`internal/orchestrator/coordination.go`, `internal/orchestrator/modes.go`, `internal/session/noticeboard.go`):
  - **Session-scoped notice** (default when `expires_in` is omitted): sets or replaces this session's single live status line and ends automatically when the session ends. Calling with empty `text` clears it.
  - **Timed notice** (`expires_in` set, 1m–24h): posts a persisted project notice that survives past the session's end until expiry or explicit clear, with optional `subject`, preserving the existing per-session active and hourly rate bounds and soft-clipping of over-long agent text/subject.
  - **Clear timed notice** (`clear_id` + `reason`, mutually exclusive with setting/posting): clears an active timed notice (whoever posted it) and records the clearer and reason.
  - Preserve the work-loop session-status path when the project notice board is off (`deps.SetIntent` in `internal/session/session.go` / `modes.go`): the same `notice` tool accepts session-scoped `text` (without `expires_in` or `clear_id`) and reports cleanly if timed/clear operations are called while the board is off.
- Simplify the `boardGuidance` system prompt paragraph, checkpoint digests (`[project notice board]`), `list_sessions`, `list_backlog` footer, and `watch` run context to the single unified notice model—still rendering a session's live session-scoped notice inline on its session row and timed notices in the board section.
- Keep wire/client compatibility smooth: either keep populating existing `ListProjectNotices` fields (`LiveSession.intent` / `intents` alongside `notices`) as a projection of session-scoped vs timed notices, or update proto + web + iOS together cleanly (decide the cleaner boundary during implementation; do not break deployed clients).
- Preserve replay/reopen of historical event logs containing `set_intent`, `post_notice`, and `clear_notice` tool calls and prior `notice_posted` / `notice_cleared` / `coordination_notice` events.
- Update `spec.md` §7.3 (and `docs/remote-api.md` if proto changes).

## Acceptance criteria
- No coordinator mode offers `set_intent`, `post_notice`, or `clear_notice`; modes with the board offer the single `notice` tool instead (and work-loop sessions without the board offer session-scoped `notice` only).
- `notice` supports: (1) setting/replacing/clearing the session's live status when `expires_in` is omitted, (2) posting a timed notice when `expires_in` is provided, and (3) clearing an active timed notice by id and reason (unit tests).
- Checkpoint digests, `list_sessions`, and `list_backlog` footer surface both session-scoped status lines (inline per session) and timed notices under the unified framing (tests).
- Existing sessions whose logs contain `set_intent`, `post_notice`, or `clear_notice` calls reopen and replay cleanly (test).
- `go test ./internal/...` passes.

## Work log
