---
id: "0384"
title: Speed up iOS Recent with progressive history display and cheaper sorting
status: in_review
priority: 2
created: "2026-09-16"
updated: "2026-09-16"
depends_on: []
spec_refs: []
---

## Description
User reports Recent frequently loads slowly. Implement the progressive home-list slice of 0376: publish each project's history without waiting for other projects or supplemental work-loop badges, retain prior rows while refreshing, and avoid repeated timestamp parsing/sorting on UI reads. Preserve deterministic deduplication/routing, unread baselines, partial errors, authorization, and coalesced refresh behavior. Broader correlated diagnostics in 0376 remain separate. Verify with focused Swift regression coverage; on-device timing/build needs user's Mac.

## Acceptance criteria

## Outcome

- Recent publishes per-project histories independently of slow projects and supplemental loop badges; pending/failed rows and failed badge loads retain prior data. Final routing and unread baselining remain independent of completion order.
- Sorting parses recency once per row, not per comparison; UI section reads reuse ingestion order.
- Investigation found substantial cold daemon cost. Added a summary-only JSON reader that skips unrelated transcript payload materialization while retaining existing reduction and tolerant/cache semantics.
- Read-only comparison of 684 local logs (~718 MiB): all summaries, accepted-event counts and cacheability flags matched; paired full/selective scan totals were 12.15s / 7.05s (~42% lower). Payload-rich synthetic benchmark: 126ms / 76ms, allocations 38.24MB / 1.18MB (~97% lower). These are local server-side measurements with warm OS filesystem cache, not end-to-end iOS timings.
- Passed full `go test ./internal/session ./internal/server -count=1`, targeted history/summary race tests, 30-second differential fuzz run (~807k executions), and diff whitespace checks. Swift gated regressions added for progressive publication, fallback, ordering, unread and badge retention; no Swift toolchain here, so Mac build/tests and device use remain pending.
- Implements the progressive-display slice of 0376; its broader latency diagnostics remain separate.
- Commit subject: `Speed up iOS Recent loading and daemon history summaries`.
