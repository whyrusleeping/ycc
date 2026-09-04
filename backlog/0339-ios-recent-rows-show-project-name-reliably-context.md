---
id: "0339"
title: 'iOS Recent rows: show project name reliably, context length instead of total tokens'
status: in_review
priority: 2
created: "2026-08-16"
updated: "2026-08-16"
depends_on: []
spec_refs:
    - 20.5 Surfaces
---

## Description
User report: the unscoped Recent sessions feed shows a bare folder icon with no project name, and the per-row "N tok" cumulative total is not useful — they want the session's context length ("how full is this session").

Changes:
- Proto: `SessionSummary.context_tokens` (field 15) — coarse prompt-size estimate from the newest completed coordinator `model_turn` (`context_tokens_est`), subagent turns ignored, 0 when telemetry is absent. Go + Swift regens.
- Daemon: `internal/session/history.go` folds `sessionContextTokens` into the summary; `internal/server` maps it onto the wire.
- iOS: `SessionListModel.contextSummary` ("1.2M ctx") replaces `tokenSummary` in row metadata; `displayProject(for:)` falls back to the workspace folder name when routing yields no registered project name (display only — routing RPCs still send the routed name).
- Docs: remote-api ListSessionHistory notes + spec §20.5.

Acceptance criteria:
- Recent rows show a context-length item from the daemon's coordinator estimate; zero/missing telemetry omits the item (no "0 ctx").
- Project label on the unscoped feed never renders an icon without a name.
- Go tests cover the fold (newest coordinator wins, subagent/malformed ignored) and wire mapping; Swift tests cover formatting and the display-project fallback.
- Verified on device (Mac build).

## Acceptance criteria

## Work log
