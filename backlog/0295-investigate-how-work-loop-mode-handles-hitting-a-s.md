---
id: "0295"
title: Investigate how work loop mode handles hitting a subscription usage limit
status: done
priority: 3
created: "2026-08-08"
updated: "2026-08-08"
depends_on: []
spec_refs: []
---

## Description
I’m concerned that if i leave work loop running overnight and the subscription runs out, it will get into a weird state. Ideally, its able to wait the correct amount of time and resume (like when the ant or openai subs have a usage reset) or at least wait nicely with a “resume” button for the user to press.

## Acceptance criteria

## Plan

## Investigation findings (current behavior when a subscription usage limit hits mid-loop)

1. **How the limit arrives.** Anthropic OAuth subscription exhaustion arrives as HTTP 429 → `ClassifyAPIError` = `rate_limit`, retryable. ChatGPT/codex can instead deliver it as an in-stream error frame inside an HTTP 200 (`usage_limit_reached`); apierror.go's status-less signature list only matches `server_error`/`internal_error`, so a codex usage-limit frame classifies as `unknown` (NOT retryable) today.
2. **Engine level.** A 429 is retried only `DefaultRateLimitMaxAttempts=3` total attempts with sub-minute backoff (≤ ~45 s), then the turn fails, a `session_error` event is emitted, and the session parks in `StatusError`.
3. **Loop level.** `workLoop.realRunSession` treats `StatusError` as terminal and returns **nil error**. The loop then re-lists the backlog: if the dead session changed nothing, the fingerprint stall guard fires → outcome **"loop stopped: session made no progress"** (misleading). If the session had already moved a task to `in_progress`, exactly one more doomed session starts, fails on its first turn, and then the guard fires. No waiting, no resume; the loop finishes terminally.
4. **"Weird state" risk assessment.** No crash/hang/corruption. Residue is: (a) a misleading outcome line, (b) possibly a task left `in_progress` with uncommitted work in the tree (the next work session's assess step handles resuming that), (c) the loop is dead for the rest of the night — the user's actual complaint.

## Implementation plan — daemon-side patience: wait and auto-resume

**A. Classification (internal/engine/apierror.go):** add a status-less provider *rate-limit* signature list (`usage_limit_reached`, `rate_limit_error`, `usage limit`) → `KindRateLimit`, retryable (mirrors `providerServerSignatures`, matched only when no HTTP status parsed). Unit tests.

**B. Capture session failure in the loop (internal/session/workloop.go):** `loopSessRec` gains `errKind string` + `errRetryable bool`; `realRunSession` fills them when `sess.Status()==StatusError` by scanning events backwards for the last `session_error` (fields `kind`, `retryable`).

**C. Loop control:** after `runSession` returns a rec with `errKind != ""`:
- **retryable** (`rate_limit`, `overloaded`, `server`, `timeout`, `network`): enter state **"waiting"** — set `resumeAt` + `waitKind`, persist, sleep stop-aware (wake ≤1 s after StopWorkLoop), then return to "running" and start the next session. Escalating wait schedule 1m, 2m, 5m, 10m, 20m, then 30m repeating; give up when cumulative consecutive outage wait exceeds a patience cap (**8 h** — covers Anthropic's 5-hour window with margin) → finish `"loop stopped: provider unavailable (<kind>), gave up after 8h"`. Reset the outage accumulator whenever a session completes without error.
- **non-retryable** (`auth`, `invalid_request`, `context_length`, `unknown`, `refusal`): finish immediately with a truthful outcome `"loop stopped: session failed (<kind>)"` instead of letting the stall guard mislabel it.
- Stall guard: `loopDecideInput` gains `prevErrored bool`; when true, skip the fp==prevFP no-progress stop (an errored session is expected to leave the backlog unchanged; the patience cap bounds the loop instead). Budget caps and empty-backlog checks unchanged.
- Test seams: injectable sleep/schedule (like the existing `m.newRunSession` seam) so tests run instantly.

**D. Lifecycle plumbing:** `StartWorkLoop` treats "waiting" as live (refuse duplicate); `StopWorkLoop` moves "waiting" → "stopping" and the waiter wakes promptly; persistence (`workloop_persist.go`) accepts "waiting" and restores it as interrupted (same as running/stopping — never auto-resume across daemon restarts, consistent with 0280).

**E. Surface:** `WorkLoop` snapshot + proto `WorkLoopInfo` gain `resume_at` (RFC3339, field 16) and `wait_kind` (field 17); state comment becomes `running | waiting | stopping | finished`. Regen Go protos (buf, local). Attempt Swift regen (remote BSR plugins, network); if unavailable, skip — committed Swift protos tolerate unknown fields — and note it. Map fields in the server RPC layer.

**F. TUI (internal/tui/workloop.go):** treat "waiting" like a live state (attach + keep polling) and render it with the resume time in the loop indicator.

**G. Spec:** extend the daemon work-loop paragraph in §9: retryable session failures put the loop in `waiting` with escalating retry up to ~8 h patience; non-retryable failures stop with a truthful outcome.

**H. Tests:** decideLoop prevErrored; loop waits-then-resumes on retryable failure (fake runSession seam); truthful stop on non-retryable; patience exhaustion; Stop during wait unblocks promptly; persist/restore "waiting"; apierror signatures; RPC field mapping.

**Follow-ons (created separately):** iOS rendering of the waiting state; optional precise scheduling from provider reset telemetry (subusage is documented telemetry-only, so that needs a design decision).

Verify: `go build ./... && go test ./internal/engine ./internal/session ./internal/server ./internal/tui` (internal/session has known flaky tests — compare against HEAD before blaming the change), plus `buf` regen drift check.

### Starting points
- internal/session/workloop.go — run()/realRunSession/decideLoop/loopSessRec; test seam m.newRunSession
- internal/session/workloop_persist.go — persist/restore, state whitelist in readPersistedWorkLoop
- internal/engine/apierror.go — providerServerSignatures pattern to mirror for rate-limit signatures
- internal/engine/loop.go:608-670 runTurn (engine retry; unchanged, context only)
- proto/ycc/v1/ycc.proto:649-668 WorkLoopInfo (add fields 16/17)
- internal/server — workloop RPC mapping (rg 'WorkLoopInfo' internal/server)
- internal/tui/workloop.go:58 attach condition treats only running|stopping as live
- spec.md ~line 795 daemon-side loop paragraph (§9)
- event.SessionError data fields: kind, retryable, msg (loop.go:786-810)
- buf regen: buf in ~/go/bin; Go plugins local, Swift remote BSR (network)

## Work log
- 2026-08-08 plan: ## Investigation findings (current behavior when a subscription usage limit hits mid-loop)  1. **How the limit arrives.** Anthropic OAuth subscription exhaustion arrives as HTTP 429 → `ClassifyAPIEr
…[truncated]
- 2026-08-08 context hints: 10 recorded with plan
- 2026-08-08 context hints: workLoop.run drives iterations; realRunSession polls sess.Status() for StatusIdle/StatusError (workloop.go:519-578); session_error event data keys: msg, kind, retryable, attempts, status (internal/eng
…[truncated]
- 2026-08-08 preload: 3 file(s), ~45 KiB seeded into implementer context
- 2026-08-08 implementer report: Implemented task 0295 daemon-side provider patience.  - Classified status-less `usage_limit_reached`, `rate_limit_error`, and `usage limit` errors as retryable rate limits. - Captured the last structu
…[truncated]
- 2026-08-08 review tier: single-opus — reviewers: sol
- 2026-08-08 review (sol): accept — The change implements the planned daemon-side patience behavior end to end: status-less subscription-limit errors are classified as retryable, failed session metadata drives waiting versus truthful te
…[truncated]
- 2026-08-08 revision: Addressed both review findings in `internal/session/workloop.go`:  - Re-list the backlog once immediately after any errored session, so non-retryable, patience-exhausted, and stop-requested terminal d
…[truncated]
- 2026-08-08 review (sol): accept — The revision addresses both prior findings. Errored-session paths now refresh the backlog before terminal handling, with a regression test proving pre-failure backlog updates appear in the digest. The
…[truncated]
