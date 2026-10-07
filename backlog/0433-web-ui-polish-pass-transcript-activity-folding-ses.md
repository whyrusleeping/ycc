---
id: "0433"
title: 'Web UI polish pass: transcript activity folding, session header, loop reports'
status: done
priority: 3
created: "2026-10-07"
updated: "2026-10-07"
depends_on: []
spec_refs:
    - docs/design/web-client.md#Desktop-first layout
---

## Description
A polish pass over the desktop web client, found by screenshotting every surface (light/dark, 1440/1024/760 px).

- Transcript: long runs of tool/reasoning rows (≥8) fold into one activity panel showing a summary (step count, per-tool counts, failures, duration) and the last few steps; one click reveals the rest. Consecutive identical system notices ("job claimed" ×3) collapse to one row with a count. Search still reaches hidden rows.
- Transcript: an animated "Working…" indicator; the "Jump to latest" pill also appears when the reader is more than a screen away from the live edge.
- Tool rows: abbreviated (truncated) JSON args no longer leak raw JSON into the summary — the path/command is salvaged from the partial payload.
- Session header: icon buttons with labels that drop away at narrow widths; meta stays on one line and ellipsizes; the session id is a copy button. Usable at 760 px.
- Work loop: per-session evidence and digest details render as (safe) markdown, clamped with a fade, instead of raw `**markdown**` text.
- Page header actions wrap instead of overflowing (backlog with the detail pane open at laptop widths).

Acceptance: typecheck + vitest pass; new pure logic unit-tested; screenshots at 1440 and 1024 px show the improvements; internal/web/dist rebuilt.

## Acceptance criteria

## Work log
