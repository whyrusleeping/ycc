---
id: "0186"
title: 'iOS: ycc:// deep links + ntfy click-through convention'
status: todo
priority: 4
created: "2026-07-08"
updated: "2026-07-08"
depends_on:
    - "0181"
    - "0182"
spec_refs:
    - 14. Persistence & remote sync
    - docs/design/ios-client.md#8. Notifications (decision)
---

## Description
Deep links per `docs/design/ios-client.md` §6 phase 2 step 7 and §8 (notifications decision: reuse ntfy, defer APNs).

## Description
- Register a `ycc://` URL scheme: `ycc://session/<id>` opens the session view (resolving which saved server via the active profile; optionally `?server=<name>`), `ycc://project/<name>` opens the filtered session list.
- Cold-start and warm-start handling (onOpenURL + scene restoration to the right screen after connect/auth).
- Document the ntfy `click` URL convention so daemon notifications can deep-link: a short section in docs/remote-api.md or the [notify] docs describing setting a click URL like `ycc://session/<id>` (verify what the notifier currently sends and whether a small daemon-side addition — e.g. including the session deep link as the ntfy Click header — is warranted; if so, implement it here).

## Acceptance criteria
- Tapping a `ycc://session/<id>` link (e.g. from an ntfy notification) opens the app on that session, including from cold start.
- Unknown/stale ids fail to a graceful error, not a crash.
- Docs updated for the ntfy click-through convention.

## Work log
