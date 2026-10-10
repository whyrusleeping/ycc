---
id: "0437"
title: 'Usage analytics: instrument the TUI'
status: done
priority: 5
created: "2026-10-08"
updated: "2026-10-10"
depends_on:
    - "0434"
spec_refs:
    - docs/design/usage-analytics.md#Kinds
---

## Description

Low priority (the user rarely uses the TUI). Record views (home menu, session, backlog browser, settings…) and key actions with `via=keyboard`, flushed through RecordUiEvents to the attached daemon, per docs/design/usage-analytics.md.

## Outcome

TUI records private usage analytics (client=tui): views derived from render precedence with dwell/from, meaningful keyboard actions (via=keyboard) across home/session/backlog/sessions/workstreams/cost/plans/settings/model backends/projects, quick-capture open/submit/cancel flow, content-free flash error codes; best-effort flush every 30s/100 events and on exit; catalog sent until first success (no shortcut labels, since all TUI input is keyboard and would falsely flag "shortcut not learned"). Tests + design doc note added; reviewer accepted, follow-up fixes verified with build and tests.

Commit: tui: usage analytics — view dwell, keyboard actions, flash errors via RecordUiEvents (0437)
