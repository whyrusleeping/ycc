---
id: "0142"
title: 'Push notifications: daemon-side webhook/ntfy channel for questions, idle, errors, digests'
status: todo
priority: 3
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 14. Persistence & remote sync
    - docs/remote-api.md#Overview
---

## Description
Notifications today are terminal-local (BEL + OSC 9, task 0108) — they only help if the terminal is visible. The signature workflow ("kick off autonomous work, walk away, answer questions from the phone") needs the *reach-out* half: the daemon pushing "agent needs you" to wherever the user is. A plain webhook POST (ntfy.sh-compatible: title/body/priority/click-URL) covers phones, Slack, and home-automation with zero vendor lock-in, and pairs with the documented remote API (the click-through target).

## Acceptance criteria
- [ ] Daemon-side notifier configured in ycc.toml (e.g. `[notify] url = "https://ntfy.sh/mytopic"`, optional auth header); absent = disabled.
- [ ] Fires on: `question_asked` (incl. Confirm gates), `session_idle` (with the final-report first line), `session_error`, work-loop completion digest, and a blocked implementer.
- [ ] Payload includes project, session id, event kind, and a short human line; delivery is best-effort/async (never blocks or fails a session).
- [ ] Per-event-kind enable/disable so autonomous loop users can pick "questions + digest only".

## Work log
