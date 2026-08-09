---
id: "0254"
title: 'TUI + iOS: integration states, retry, merge-all-ready, needs-attention notifications'
status: in_review
priority: 3
created: "2026-08-06"
updated: "2026-08-09"
depends_on:
    - "0252"
spec_refs:
    - docs/design/workstream-integration.md#7. Surfaces
    - docs/design/ios-client.md#6. Screens & feature phases
---

## Description

Surface the extended lifecycle (task 0251/0252) in both clients so an auto-integrating
project is observable and its exceptions are one action away.

- **Row state** per workstream: working / ready / integrating / needs-attention / gated /
  merged, visually distinct (needs-attention loud).
- **Actions**: `RetryIntegration(workstream_id)` re-queues a `needs_attention` stream;
  "merge all ready" (one keystroke in the TUI, one button on iOS) for `mode = gate`; existing
  preview/merge/discard stay for `manual`.
- **Drill-in** reaches the `integrate` session's transcript, not just the work session.
- **Notifications**: ntfy on `needs_attention` (always) and merged (configurable), so an
  unattended run pings only when a human is actually needed.

Includes the `RetryIntegration` RPC + `WorkstreamInfo` integration-state fields, and the
YccKit-side model logic covered by headless tests (mirroring `WorkstreamsModelTests`).

## Acceptance criteria

- [ ] TUI Workstreams panel renders every new state and offers retry / merge-all-ready.
- [ ] iOS Workstreams pane does the same, with model logic unit-tested in YccKit.
- [ ] `RetryIntegration` re-queues and is idempotent for a stream already queued.
- [ ] A `needs_attention` transition emits a notification with a deep link to the workstream.

## Plan

Context: the integration lifecycle (0251/0252) is fully daemon-side already — registry statuses active/ready/needs_attention(+StatusReason)/merged/discarded/stale, IntegrateSessionID recorded per attempt, workstream_ready/integrating/needs_attention events, ntfy on attention (always, with ycc://session Click deep link) and merged. What's missing is the §7 client/RPC surface: RetryIntegration, integration-state fields on WorkstreamInfo, TUI/iOS retry + merge-all-ready + integrate-session drill-in.

1. Proto (proto/ycc/v1/ycc.proto):
   - WorkstreamInfo += `string status_reason = 13` (why it needs attention), `string integrate_session_id = 14` (latest recovery session, Subscribe target for drill-in), `string integration_state = 15` (live queue enrichment: "" | "queued" | "integrating", best-effort like session_status).
   - New rpc RetryIntegration(RetryIntegrationRequest{workstream_id}) returns (RetryIntegrationResponse{WorkstreamInfo workstream}).
   - Regen Go (`buf generate`, local plugins) and Swift (`buf generate --template buf.gen.swift.yaml`, remote BSR plugins — needs network; if it fails report rather than hand-editing generated files).

2. Manager (internal/session/workstream_integrate.go):
   - Track the in-flight id on workstreamIntegrator (set/cleared in drainWorkstreamIntegrations under m.mu).
   - Manager.WorkstreamIntegrationState(ws) -> "integrating" | "queued" | "" for list enrichment.
   - Manager.RetryIntegration(id) (workstream.Workstream, error):
     * unknown id -> "unknown workstream" error (NotFound mapping already exists).
     * needs_attention -> CAS Transition to ready (clears reason), emit WorkstreamReady{retry:true,...}, enqueue when effectiveIntegrationMode()=="auto".
     * ready -> enqueue when auto (enqueueWorkstreamIntegration already ignores queued ids => idempotent re-queue), success no-op otherwise.
     * any other status -> error containing "cannot be retried" (add that string to server.workstreamError's FailedPrecondition set).
   - Unit tests in internal/session: needs_attention→ready+queued; idempotent when already queued/ready; merged errors FailedPrecondition-shaped.

3. Server (internal/server/workstream.go): toWorkstreamInfo populates status_reason + integrate_session_id; ListWorkstreams enrichment adds integration_state for non-terminal rows; new RetryIntegration handler (validate id non-empty). Extend workstream_rpc_test.go.

4. TUI (internal/tui/workstreams.go + workstreams_test.go):
   - wsRowStatus: prefer live integration_state — "integrating…" (and "queued") over plain ready; needs_attention stays loud.
   - New keys in the panel: `t` retry integration for the selected needs_attention (or ready) row; `a` merge all ready — one cmd looping MergeWorkstream(accept=true) over currently-ready rows, notice "merged N" or first error; `i` drill into the integrate session transcript when integrate_session_id is set (reopenSession, same as enter does for the work session).
   - Show the needs-attention reason: when the cursor sits on a needs_attention row, surface StatusReason (truncated) in the hint/footer.
   - Update the hint line; extend the panel tests for rendering + key handling.

5. iOS (clients/ios — NOTE: tree already carries other in_review tasks' uncommitted work in these files; edit on top, never revert unrelated hunks):
   - YccKit WorkstreamsModel.swift: WorkstreamsSource += retryIntegration(workstreamId:); YccClient conformance (YccClient.swift wrapper mirroring mergeWorkstream); model exposes statusReason/integrateSessionID/integrationState, retry(_:) action, readyWorkstreams + mergeAllReady() (sequential accept=true merges, returns merged-count/first-error summary for the UI).
   - WorkstreamsModelTests.swift: cover retry (needs_attention row), mergeAllReady (only ready rows merged, error path), integrationState parsing.
   - App/WorkstreamsView.swift: needs-attention rows show statusReason + a loud Retry button; toolbar/section "Merge all ready" button visible when ≥1 ready; drill-in affordance to the integrate session transcript when present (route via the existing session navigation used for the work session).

6. Docs: docs/design/workstream-integration.md header note ("client retry/merge-all surfaces remain follow-ups") updated to reflect shipped surfaces.

Notifications criterion: already satisfied daemon-side (evaluateWorkstreamReadiness + integrationNeedsAttention notify KindAttention with the workstream's session deep link; merged notifies KindMerged behind config) — verify with existing tests, no new code expected.

Verification: go build ./... ; go test ./internal/session ./internal/server ./internal/tui ./internal/workstream ; buf generate both templates; Swift tests cannot run here (no toolchain) — user runs on-device, task lands in_review. Commit will be selective (Go/proto/docs only) since the tree holds other tasks' uncommitted iOS work.

### Starting points
- internal/server/workstream.go — toWorkstreamInfo, workstreamError, all workstream RPC handlers
- internal/session/workstream_integrate.go — enqueueWorkstreamIntegration, drainWorkstreamIntegrations, workstreamIntegrator struct, integrationNeedsAttention
- internal/session/workstream_ready.go — evaluateWorkstreamReadiness (ready-event emit + auto enqueue pattern to mirror in retry)
- internal/tui/workstreams.go — updateWorkstreams key handling, wsRowStatus, workstreamsView
- internal/tui/workstreams_test.go, internal/server/workstream_rpc_test.go, internal/session/workstream_integrate_test.go — test patterns
- clients/ios/YccKit/Sources/YccKit/WorkstreamsModel.swift + Tests/YccKitTests/WorkstreamsModelTests.swift
- clients/ios/YccKit/Sources/YccKit/YccClient.swift:684 mergeWorkstream wrapper pattern
- proto regen: buf in ~/go/bin; Go local plugins, Swift remote BSR (network)
- registry: internal/workstream/registry.go — Status, StatusReason, IntegrateSessionID, Transition(CAS)

## Work log
- 2026-08-09 plan: Context: the integration lifecycle (0251/0252) is fully daemon-side already — registry statuses active/ready/needs_attention(+StatusReason)/merged/discarded/stale, IntegrateSessionID recorded per at
…[truncated]
- 2026-08-09 context hints: 9 recorded with plan
- 2026-08-09 context hints: internal/session/workstream_ready.go:62-80 — ready-event emit + auto-enqueue pattern to mirror in RetryIntegration; internal/workstream/registry.go — Transition is CAS from listed statuses; SetSta
…[truncated]
- 2026-08-09 preload: 4 file(s), ~37 KiB seeded into implementer context
- 2026-08-09 implementer report: Implemented task 0254 end-to-end without committing or reverting unrelated work.  Changes: - Extended `WorkstreamInfo` with `status_reason`, `integrate_session_id`, and live `integration_state`; added
…[truncated]
- 2026-08-09 review tier: single-opus — reviewers: sol
- 2026-08-09 review (sol): revise — The RPC, queue-state enrichment, retry/idempotency handling, integration-session drill-in, generated clients, and most TUI/iOS presentation logic are implemented cleanly, and the Go build plus targete
…[truncated]
- 2026-08-09 revision: Addressed the merge-all mode-scoping review finding.  Changes: - Added `WorkstreamInfo.integration_mode` (field 16) and regenerated Go + Swift protobuf/connect outputs. - Exposed `Manager.EffectiveInt
…[truncated]
- 2026-08-09 review (sol): accept — The revision resolves the prior mode-scoping issue by surfacing the effective integration mode, rendering gate-ready rows as Gated, and limiting both TUI and iOS merge-all behavior to gate-mode rows. 
…[truncated]
- 2026-08-09 revision: Fixed the TUI retry-success notice: - `wsRetriedMsg` now carries the `WorkstreamInfo` returned by `RetryIntegration`. - `retryIntegrationCmd` preserves that response instead of discarding it. - Succes
…[truncated]
- 2026-08-09 usage: 18,619,768 tok (in 2,530,967, out 76,769, cache_r 18,602,470, cache_w 243,583) · cost n/a (unpriced)
  implementer: 15,712,432 tok (in 1,485,144, out 41,816, cache_r 14,185,472, cache_w 0) · cost n/a (unpriced)
  reviewer:sol: 2,882,768 tok (in 1,045,769, out 10,439, cache_r 1,826,560, cache_w 0) · cost n/a (unpriced)
  coordinator: 24,568 tok (in 54, out 24,514, cache_r 2,590,438, cache_w 243,583) · cost n/a (unpriced)
