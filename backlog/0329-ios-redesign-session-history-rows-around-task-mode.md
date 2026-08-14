---
id: "0329"
title: 'iOS: redesign session-history rows around task, mode, model, and token signal'
status: in_review
priority: 3
created: "2026-08-13"
updated: "2026-08-13"
depends_on: []
spec_refs:
    - docs/design/ios-client.md#Navigation and interaction
---

## Description

Redesign the iOS home/session-history row as a balanced activity view and historical ledger. The current row spends too much visual weight on routine or low-signal metadata: nearly every completed row says `idle`, work-loop provenance is a prominent capsule, focused task IDs are prepended to the title in the same typography (`[0198] …`), and turn count is less useful than model/token context.

Use a clearer hierarchy:

- Keep the session title as the primary content, but render focused task IDs separately as compact task-ID chips. Conservatively remove task boilerplate duplicated by those chips from the row title (for example an injected `[0198]`, `Work on task 0198:`, or a repeated leading `0198 —` when it refers to the same focused task); do not aggressively rewrite unrelated prompt text.
- Omit the routine `idle` badge. Show lifecycle state only when it carries attention or operational value, including running/waiting, paused, error, and stopped states. Preserve the existing unread and needs-answer emphasis.
- Demote work-loop provenance from a colored badge to subtle metadata such as `via loop`.
- Replace turn count in the routine metadata line with mode, a compact summary of logical models actually used, and total session tokens. Keep relative activity time, and keep project context in the all-project view.
- Preserve graceful behavior for old sessions and partial/corrupt logs: absent task, model, or token data should simply omit that element rather than show misleading zero/unknown labels.

Extend `SessionSummary`/`ListSessionHistory` as needed so model and token metadata are computed daemon-side while session logs are already being reduced, rather than issuing a per-row usage RPC from iOS. Model summary should be compact and deterministic: show the sole model directly; for multiple models, show the highest-token model plus a count of the others (for example `claude +2`). Total tokens include all actors/models in the session.

## Acceptance criteria

- Idle session rows no longer render an `idle` capsule; actionable or exceptional lifecycle states remain visually distinguishable and accessible.
- Focused task IDs render as compact chips separate from title typography, including a sensible compact treatment for multiple focused tasks.
- The displayed row title does not duplicate a focused task ID through the current injected bracket prefix and conservatively removes matching leading task boilerplate.
- Work-loop ownership is represented as subtle `via loop` metadata rather than the current colored loop capsule.
- Routine row metadata shows mode, compact model summary, total tokens, and relative last activity; turn count is removed. Project remains visible when browsing all projects.
- `SessionSummary` exposes the data necessary for the row without an N+1 RPC pattern. Persisted and live summary paths agree, model ordering is deterministic, and tokens are totalled across all session actors/models.
- Legacy/missing usage or model data degrades by omission, without fabricated values or broken layout.
- Long titles, multiple task IDs/models, Dynamic Type, VoiceOver labels, unread state, waiting-for-input state, and narrow iPhone widths remain legible and do not overflow.
- Go summary/API tests and headless Swift row-formatting/model tests cover idle suppression, exceptional states, title/task de-duplication, multi-task compaction, model compaction, token formatting, loop provenance, and missing metadata.
- Generated Go and Swift protobuf/Connect outputs are updated together if the wire schema changes, and the redesigned row is verified on an iPhone simulator or device.

## Plan

1. Extend the daemon’s session-history reduction and wire summary with deterministic per-model token totals plus an all-model session token total, reducing the already-read event stream once and omitting absent/zero metadata for legacy or partial logs. Keep persisted and live-overlay rows on the same data path, and update both Go and Swift generated protobuf outputs.
2. Add Go tests for persisted/live summary agreement, all-actor/model token aggregation, deterministic model ordering, and graceful malformed/missing usage behavior; update server mapping tests as needed.
3. Move row presentation rules into headlessly testable YccKit helpers: normalized/deduplicated task IDs, conservative matching-task boilerplate removal from titles, compact multi-task and model summaries, token formatting, lifecycle badge visibility/labels, and subtle loop provenance metadata.
4. Redesign the SwiftUI session row to use those helpers: task chips separated from the primary title, no idle badge, exceptional/actionable statuses retained, loop ownership rendered as “via loop”, and wrapping/adaptive metadata for project, mode, model, token total, and relative activity while preserving unread/waiting/live accessibility emphasis.
5. Add Swift unit tests covering title/task de-duplication, task/model compaction, status suppression, token and loop/missing metadata formatting, then run available Go/protobuf checks and Swift tests if the environment supports them. Document the simulator/device verification limitation if no Apple toolchain is available.

### Starting points
- internal/session/history.go — persisted reducer and live overlay
- proto/ycc/v1/ycc.proto#SessionSummary and internal/server/server.go mapping
- clients/ios/YccKit/Sources/YccKit/SessionListModel.swift — existing row title/status helpers
- clients/ios/App/LandingView.swift#SessionRow — current badges/metadata
- internal/usage/usage.go#ReduceEvents — existing all-actor logical-model token reduction semantics
- Tree is already dirty with unrelated work; preserve it and limit changes to task 0329 files/hunks.

## Work log
- 2026-08-13 plan: 1. Extend the daemon’s session-history reduction and wire summary with deterministic per-model token totals plus an all-model session token total, reducing the already-read event stream once and omi
…[truncated]
- 2026-08-13 context hints: 6 recorded with plan
- 2026-08-13 context hints: internal/usage/usage.go already defines reduction semantics, but avoid importing it if a small history-local reducer is cleaner and avoids exporting internals.; Use additive proto field numbers after
…[truncated]
- 2026-08-13 preload: 9 file(s), ~43 KiB seeded into implementer context
- 2026-08-13 implementer report: Implemented task 0329 across daemon, API, generated clients, iOS presentation, and tests.  Changes: - Extended session-history reduction with deterministic per-logical-model token totals and an all-se
…[truncated]
- 2026-08-13 review tier: high-powered — reviewers: sol, sol#2 (sol)
- 2026-08-13 review (sol): accept — Task 0329 is implemented coherently across the session reducer, wire API, generated Go/Swift protobufs, headless presentation helpers, and SwiftUI row. The reducer aggregates all actors, preserves det
…[truncated]
- 2026-08-13 review (sol#2/sol): accept — The task-specific implementation is correct and clean overall. Session usage is reduced daemon-side across all model-turn actors, deterministically ordered, mapped through the additive protobuf fields
…[truncated]
- 2026-08-13 revision: Addressed all accepted-review polish items:  - Fixed compact token unit carry at rounding boundaries:   - `999,500`–`999,999` now renders as `1M tok`, never `1000K tok`.   - `999,500,000`–`999,999
…[truncated]
- 2026-08-13 review (sol): accept — The revision addresses both prior findings. Token formatting now promotes values that would round to `1000K`/`1000M` into `1M`/`1B`, with focused threshold tests, and the `SessionRow` documentation no
…[truncated]
- 2026-08-13 review (sol#2/sol): accept — The revision cleanly addresses all prior findings. Compact token formatting now promotes rounded unit boundaries with focused regression tests, the SessionRow documentation matches the redesigned meta
…[truncated]
- 2026-08-13 decision: implementation accepted and committed as 54fba48; status remains in_review pending the acceptance criterion’s required iPhone simulator/device verification, which is unavailable in this Linux environment.
- 2026-08-13 usage: 10,154,554 tok (in 2,143,943, out 72,563, cache_r 7,938,048, cache_w 0) · cost n/a (unpriced)
  implementer: 5,224,673 tok (in 748,326, out 31,163, cache_r 4,445,184, cache_w 0) · cost n/a (unpriced)
  coordinator: 2,410,313 tok (in 523,111, out 16,866, cache_r 1,870,336, cache_w 0) · cost n/a (unpriced)
  reviewer:sol: 1,275,677 tok (in 444,460, out 12,017, cache_r 819,200, cache_w 0) · cost n/a (unpriced)
  reviewer:sol#2: 1,243,891 tok (in 428,046, out 12,517, cache_r 803,328, cache_w 0) · cost n/a (unpriced)
