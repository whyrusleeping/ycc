---
id: "0402"
title: 'iOS perf phase 1: transport timeouts/metrics, home refresh fan-out, cached derived state'
status: done
priority: 1
created: "2026-09-28"
updated: "2026-10-10"
depends_on: []
spec_refs: []
---

## Description
User report: many little things laggy (menus, nav) on a remote (ngrok/cellular) link; ~150–300ms per RTT. Audit (2026-09-28) findings to fix:
- Transport: custom URLSessionConfiguration for RetryGuardHTTPClient with long timeoutIntervalForRequest so idle SubscribeSessionView isn't killed at 60s; separate unary ProtocolClient with a bounded per-call timeout (~20s) sharing the same HTTP client; ignore empty keepalive SessionViewUpdates before enqueue (no transcriptRevision bump); LatencyInterceptor records URLSessionTaskMetrics (protocol, reused connection, TLS/connect, request→response) via handleResponseMetrics.
- Home refresh (SessionListModel.performRefresh/refreshHistories, LandingView path-empty/foreground triggers): fan out history+work-loop using the cached project list concurrently with ListProjects (reconcile if set changed), publish once per refresh instead of per project, throttle refresh-on-return (skip if <~10s since last successful refresh unless forced); loadMoreHistory pages projects concurrently.
- Derived state: activityByProject/totalActivity computed once on writes, not per drawer row per render (drawer is always mounted); parse ISO dates once at ingestion; `sessions`/sections not re-sorted per read; displayTitle regexes cached; FileEntryRow static date formatters.
- Serial chains → async let: TaskDetailModel (GetTask ‖ ListSessionHistory), ReviewTiersModel (ListReviewTiers ‖ ListModels).
Preserve unread/routing semantics and existing tests; add YccKit tests where behavioural (refresh throttle, single publish, keepalive ignore). Leave uncommitted as in_review for on-device verification per iOS convention.

## Acceptance criteria

## Work log
- 2026-09-28: implemented (uncommitted, iOS convention). YccKit built and 411 tests run in Docker swift:6.2-noble against a patched scratch copy (/tmp/ycc-0402-real); only pre-existing Linux-only failures (DiffFormatter CRLF, EventContract testdata path). Independent review: no blockers; fixed rollover/resume/answers → 120s bulk client, stall watchdog armed from stream start (65s), legacy subscribe watchdog, metrics log at .debug. Not compiled here: App/LandingView.swift, App/FileBrowsing.swift, Darwin Logger branch in LatencyInterceptor. Remaining: Xcode build + `swift test` on Mac, restart daemon onto 27b251e (keepalive), on-device check that quiet sessions no longer flash "reconnecting", Console `ycc/transport` shows h2 + reused connections, home return feels instant.
