---
id: "0129"
title: Engine streaming seam + TUI live tail row for turn_delta (pre-gollama)
status: done
priority: 3
created: "2026-07-03"
updated: "2026-07-03"
depends_on: []
spec_refs: []
---

## Description
Split from task 0114 (its gollama-independent remainder; the 0128 transport groundwork
— event.Log.Broadcast, transient turn_delta, Subscribe pass-through, TUI tolerance,
spec §5.2/§18.4 design note — is already done). Task 0114 keeps only the final gollama
TurnStream adoption once 0120 lands.

Build the ycc-side streaming seam end to end so that any streaming-capable backend
client lights up incremental output in the TUI with no further plumbing:

**Engine seam (internal/engine):**
- Optional capability interface, e.g. `StreamTurner { TurnStream(opts gollama.RequestOptions, onDelta func(text string)) (*gollama.ResponseMessageGenerate, error) }`
  where `onDelta` receives **snapshots** (the full accumulated turn text so far), not
  increments — snapshot semantics make lossy transient delivery and mid-turn retries
  harmless (the UI just replaces its tail row).
- Loop turn call site: if the client implements StreamTurner, use it and Broadcast
  transient `turn_delta` events (data `{"text": <snapshot>}`, actor = the loop's actor),
  throttled to ~10/s; otherwise call `Turn` exactly as today (graceful fallback, no
  deltas). Final `model_turn` emission unchanged either way.
- No stale tails: on turn end (success OR error) broadcast a clearing delta (e.g.
  `{"text": "", "done": true}`) so clients drop the tail row even if no model_turn follows.
- `WithRetry` forwards the streaming capability only when the inner Turner has it
  (capability-preserving wrapper); a retried attempt restarts snapshots cleanly.
- Emitter grows a Broadcast path that type-asserts its Recorder for the optional
  Broadcast capability (satisfied by *event.Log) and no-ops otherwise.

**TUI (internal/tui):**
- Maintain per-actor live tail state from transient turn_delta events (replace-on-
  snapshot, remove on done/empty or on the next persisted model_turn from that actor).
- Render a live-updating tail row in the conversation view while an actor is streaming
  (visually marked in-progress); the persisted model_turn replaces it seamlessly.
- Transient events still never enter reducers/replay/seq tracking.

## Acceptance criteria
- [ ] StreamTurner seam + throttled snapshot turn_delta broadcasts from the engine loop, covered by tests with a fake streaming client
- [ ] Plain (non-streaming) Turner behaves exactly as today — no deltas, turn semantics unchanged (test)
- [ ] WithRetry preserves the streaming capability accurately and retries restart snapshots (test)
- [ ] TUI shows the streamed text incrementally and swaps in the final model_turn with no stale tail rows (test with synthetic events)
- [ ] Deltas provably never persisted: events.jsonl / replay / transcripts unaffected (existing 0128 invariants keep holding)
- [ ] go build ./... && go test ./... clean

## Plan

Build the ycc-side streaming pipeline end to end, behind an optional engine capability interface, fully testable with fakes (gollama TurnStream — task 0120 — plugs in later via a small adapter in task 0114).

1. Engine seam (internal/engine/loop.go):
   - Add `StreamTurner` interface: `TurnStream(opts gollama.RequestOptions, onDelta func(text string)) (*gollama.ResponseMessageGenerate, error)`. Contract: onDelta receives SNAPSHOTS — the full accumulated assistant text so far — not increments. Snapshot semantics make lossy transient delivery (bounded queues drop oldest) and mid-turn retries harmless: the UI just replaces its tail. Document this contract clearly on the interface.
   - At the turn call site (loop.go ~line 385, `client.Turn(opts)`): if `client` implements StreamTurner AND the emitter supports broadcast, call TurnStream with a callback that broadcasts transient `turn_delta` events (data `{"text": <snapshot>}`) via the loop's Emitter, throttled to at most ~1 per 100ms (always deliver the first). On turn end — success OR error — broadcast a clearing delta `{"text": "", "done": true}` (defer) so no stale tail survives a failed turn. Otherwise call `Turn` exactly as today. Final model_turn/error emission is unchanged in both paths.
   - Callback may be invoked from another goroutine — keep the throttle state local/mutex-free by confining it to the closure if gollama guarantees serial callbacks; assume serial callbacks (document), but keep the broadcast itself safe (event.Log.Broadcast already locks).

2. Emitter broadcast path (internal/event/event.go):
   - Add optional capability: `type Broadcaster interface { Broadcast(actor string, t Type, data map[string]any) Event }` (already satisfied by *event.Log). Add `(*Emitter).Broadcast(t Type, data map[string]any) (Event, bool)` (or similar) that type-asserts e.rec and no-ops (false) when the Recorder can't broadcast (e.g. StdoutRecorder, orchestrator capture wrappers). Check internal/orchestrator/capture.go — if its capture Recorder wraps an inner Recorder, decide explicitly whether to forward Broadcast (forward to inner if inner is a Broadcaster, and do NOT capture transients into workstream merge state); keep transients out of any persistence path.

3. Retry wrapper (internal/engine/retry.go):
   - Capability-preserving WithRetry: when `inner` implements StreamTurner, return a wrapper type that also implements StreamTurner (e.g. `streamRetryTurner{retryTurner}` with a TurnStream method that retries inner.TurnStream under the same policy); when it doesn't, return the plain retryTurner so type assertion in the loop stays accurate. Because deltas are snapshots, a retried attempt simply restarts from a short snapshot — no reset protocol needed; note this in a comment.

4. TUI live tail (internal/tui/tui.go):
   - Add per-actor tail state (map[string]string). In the evMsg handler and its drain loop, route transient turn_delta events into that map instead of dropping them: set/replace on non-empty text; delete on `done`/empty text. Still never call appendEvent/maybeNotify for transients.
   - Clear the actor's tail when a persisted model_turn (or session_error) from that actor arrives — the durable event replaces the live row.
   - Render: while an actor has tail text, show a live tail row at the bottom of the conversation (after the last persisted row), styled visibly in-progress (e.g. dimmed with the actor label and a streaming marker like `…`), wrapped to the viewport like normal rows and capped to a handful of trailing lines so it doesn't dominate. Follow-mode GotoBottom keeps it in view since rebuild already runs on each evMsg batch.
   - Make sure the tail map is reset on session close/reopen so a reconnect doesn't show stale tails.

5. Spec touch-up (spec.md §18.4 or §5.2): one short paragraph documenting the snapshot semantics of turn_delta payloads ({"text": full-text-so-far}, throttled, cleared by a done/empty delta or the final model_turn) so clients agree on the contract.

6. Tests:
   - engine: fake StreamTurner ⇒ turn_delta broadcasts observed via a real event.Log subscriber (snapshots, throttle honored, clearing delta on both success and error paths); fake plain Turner ⇒ zero broadcasts, behavior byte-identical to today; WithRetry capability check both ways + a retry that restarts snapshots.
   - event: Emitter.Broadcast no-ops on non-Broadcaster Recorder; broadcasts on *Log; events.jsonl/replay still never contain deltas (extend/rely on 0128 tests).
   - tui: synthetic transient turn_delta evMsg ⇒ View() shows streaming text; subsequent snapshots replace it; persisted model_turn ⇒ tail gone, final text rendered once.
   - go build ./... && go test ./... clean; go vet ./... clean.

### Starting points
- internal/engine/loop.go:19-24 Turner interface; ~line 385 client.Turn(opts) call site; Loop.Emitter is *event.Emitter
- internal/engine/retry.go WithRetry/retryTurner — needs capability-preserving streaming variant
- internal/event/log.go:162 (*Log).Broadcast(actor, t, data) — transient broadcast, bounded lossy queues
- internal/event/event.go:95-101 TurnDelta type; 154-184 Emitter/Recorder — add Broadcaster capability + Emitter.Broadcast
- internal/tui/tui.go:2265-2298 evMsg handler + drain loop currently drops Transient events; rebuild()/relayout() coalescing; conversation rendering near line 3618 (detailLine)
- internal/server/subscribe_transient_test.go — existing turn_delta data shape {"text": ...} and transport invariants
- internal/orchestrator/capture.go — capture Recorder wrapper; decide Broadcast forwarding
- spec.md §5.2 (~line 195) and §18.4 (~line 1078) existing turn_delta design notes

## Work log
- 2026-07-03 plan: Build the ycc-side streaming pipeline end to end, behind an optional engine capability interface, fully testable with fakes (gollama TurnStream — task 0120 — plugs in later via a small adapter in 
…[truncated]
- 2026-07-03 context hints: 8 recorded with plan
- 2026-07-03 context hints: internal/engine/loop.go:19-24 Turner interface; ~line 385 client.Turn(opts) call site; Loop.Emitter is *event.Emitter; internal/engine/retry.go WithRetry/retryTurner; internal/event/log.go:162 (*Log).
…[truncated]
- 2026-07-03 implementer report: Implemented the ycc-side streaming seam end to end (task 0129), behind an optional engine capability, fully testable with fakes. gollama's TurnStream (task 0120) plugs in later via a small adapter (ta
…[truncated]
- 2026-07-03 review tier: single-opus — reviewers: Claude
- 2026-07-03 review (Claude): accept — The change implements the ycc-side streaming seam end to end exactly as specified: an optional StreamTurner capability with snapshot-semantics onDelta, throttled transient turn_delta broadcasts with s
…[truncated]
- 2026-07-03 decision: accept — commit: engine streaming seam + TUI live tail row for turn_delta (task 0129)  Adds the ycc-side streaming pipeline behind an optional StreamTurner capability: the loop broadcasts throttled transient turn_delt
…[truncated]
