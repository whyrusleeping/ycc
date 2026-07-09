---
id: "0186"
title: 'iOS: ycc:// deep links + ntfy click-through convention'
status: done
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

## Plan

ycc:// deep links + ntfy click-through (iOS app + a small daemon-side Click header + docs).

1. Daemon (Go): internal/notify — add Click support. config gets nothing new: convention is fixed. In Send(), when sessionID is non-empty set the ntfy `Click` header to `ycc://session/<sessionID>`. Digest (no session) sends no Click. Unit-test header presence/absence in notify_test.go.
2. iOS URL scheme: register `ycc://` via project.yml's `info:` block (CFBundleURLTypes). Parse links in YccKit (pure, testable `DeepLink` parser): `ycc://session/<id>[?server=<name>]` and `ycc://project/<name>`; invalid → nil.
3. iOS routing: AppModel holds a pendingDeepLink; `.onOpenURL` in YccApp/RootView sets it. When connected, LandingView consumes it: session link → resolve + push live SessionView (verify how a bare session id resolves on a multi-project daemon — Subscribe takes only session_id, so live open works; for the project param check ListSessionHistory lookup or default-project fallback); project link → set the project filter. `?server=<name>` switches the active saved profile via ConnectionStore before connecting (only if the profile exists; else error). Cold start: pending link survives until auth/connection completes. Unknown/stale session id → graceful alert, no crash.
4. Docs: document the click convention (daemon sets Click: ycc://session/<id> on question/idle/error/blocked ntfy notifications; iOS registers the scheme) in docs/remote-api.md's Notify section + a note in docs/design/ios-client.md §8 if it claims otherwise.
5. Tests: DeepLink parser unit tests (valid/invalid/host-vs-path forms, query param); notify Click header tests (Go). Verify: go test ./internal/notify/...; swift test; xcodegen + xcodebuild simulator build. Extend plans/ios-client-smoke.md (open ycc://session/<id> via Safari/simctl openurl, cold + warm).

### Starting points
- internal/notify/notify.go — Send() builds ntfy headers (Title/Priority/Tags); add Click: ycc://session/<id> when sessionID non-empty; digest kind has no session
- clients/ios/project.yml — `info:` block emits App/Info.plist; add CFBundleURLTypes for the ycc scheme (Info.plist is git-ignored, generated)
- clients/ios/App/YccApp.swift + RootView.swift + AppModel.swift — app entry/routing; deep link must survive cold start (pending-target held until connected)
- clients/ios/App/LandingView.swift — LiveSessionTarget/navigationDestination pattern for pushing a session
- clients/ios/YccKit/Sources/YccKit/ConnectionStore.swift — saved server profiles (?server=<name> resolution)
- docs/remote-api.md — notify webhook docs to extend with the click convention
- verify: how a bare session id resolves to a project (Subscribe takes only session_id; GetSessionTranscript takes project+id) — check internal/server/server.go

## Work log
- 2026-07-08 plan: ycc:// deep links + ntfy click-through (iOS app + a small daemon-side Click header + docs).  1. Daemon (Go): internal/notify — add Click support. config gets nothing new: convention is fixed. In Sen
…[truncated]
- 2026-07-08 context hints: 7 recorded with plan
- 2026-07-08 context hints: internal/notify/notify.go — Send() builds ntfy headers (Title/Priority/Tags); add Click header; internal/notify/notify_test.go has the test harness (Flush for determinism); clients/ios/project.yml �
…[truncated]
- 2026-07-08 implementer report: Implemented task 0186: ycc:// deep links + ntfy click-through convention (daemon Click header, iOS scheme + routing, docs).  ## Daemon (Go) - internal/notify/notify.go: Send() now sets the ntfy `Click
…[truncated]
- 2026-07-08 review tier: single-opus — reviewers: claude
- 2026-07-08 review (claude): accept — The task is well implemented across all three surfaces. The daemon (internal/notify/notify.go) sets an ntfy `Click: ycc://session/<id>` header when a session id is present, with a matching unit test. 
…[truncated]
- 2026-07-08 decision: accept — commit: iOS: ycc:// deep links (scheme, parser, cold/warm routing) + ntfy Click header + docs (task 0186)
