---
id: "0128"
title: event.Log transient broadcast path (turn_delta groundwork for streaming)
status: done
priority: 3
created: "2026-07-03"
updated: "2026-07-03"
depends_on: []
spec_refs:
    - 5. Session & event log
    - 18.4 Reasoning (thinking) in the event stream
---

## Description
Split from task 0114 (Stream model output incrementally into the session view): the
ycc-side transport groundwork that needs NO gollama changes, per the design decided
with the user on 2026-07-05 (Option A — transient, non-persisted events through the
existing Subscribe pipe). Doing this now shrinks 0114 to "consume gollama TurnStream +
TUI tail row" once gollama task 0120 unblocks.

Scope:
- `event.Log` gains a broadcast-without-persist path (e.g. `Broadcast(actor, t, data)`):
  the event is delivered to **live subscribers only** — no seq assigned, never written
  to events.jsonl, never appended to the in-memory replay slice, invisible to
  `Snapshot`/`ReadLog`/late subscribers. Delivery must not reorder or drop persisted
  events, and a slow/cancelled subscriber must not wedge the log. Transient delivery
  may be lossy under backpressure (they are ephemeral UI hints), but persisted events
  stay lossless.
- A `transient` marker on the event (and the `turn_delta` event type constant) so
  subscribers can distinguish them; seq stays 0.
- `Server.Subscribe` RPC forwards transient events unchanged; resume/from_seq semantics
  are unaffected (seq-0 events are never used as a resume cursor).
- TUI subscriber tolerates seq-less transient events safely (ignores them for now — no
  rendering yet, no corruption of its seq tracking / reducers).
- Spec design note (§5 area, referenced from §18.4): how partial output flows to
  clients without corrupting the append-only log/replay.

## Acceptance criteria
- [ ] `Broadcast` (transient emit) on `event.Log`: live subscribers receive the event; it never appears in events.jsonl, the replay slice, `Snapshot()`, or a later `Subscribe` replay — covered by tests
- [ ] Persisted-event semantics unchanged (lossless, ordered, seq-contiguous) — existing tests still pass
- [ ] `turn_delta` event type + transient marker defined in the event package
- [ ] Server Subscribe stream carries transient events; from_seq resume unaffected (test)
- [ ] TUI event loop ignores transient events without corrupting state
- [ ] Spec updated with the design note (transient events never persisted)

## Plan

Goal: add a transient (never-persisted) event path through the existing event pipeline, per the Option A design recorded in task 0114, so a later task can stream turn_delta events to live clients without touching the append-only log/replay.

1. event package (internal/event):
   - Add `Transient bool` field to `Event` (`json:"transient,omitempty"`). Transient events carry Seq=0.
   - Add event type constant `TurnDelta` ("turn_delta").
   - `Log` gains `Broadcast(actor string, t Type, data map[string]any) Event`: stamps TS + Transient=true, assigns NO seq, does NOT write to the jsonl file, does NOT append to l.events, and delivers to currently-live subscribers only.
   - Implementation approach (implementer may refine): today Subscribe pumps from the shared l.events slice by cursor, so transients need a per-subscriber side channel — e.g. register each subscriber in a map with a small transient queue (or buffered chan); Broadcast appends to each registered subscriber's queue under l.mu and cond.Broadcast()s; the pump drains persisted tail first (order among persisted events must stay lossless/ordered/seq-contiguous), then any queued transients. A slow or cancelled subscriber must never wedge the log or other subscribers: transient delivery may drop under backpressure (bounded queue, drop-oldest or drop-new — document the choice); persisted delivery stays lossless as today. Closed log: Broadcast is a no-op. Unsubscribe (cancel) must deregister the queue.
   - Tests (internal/event): live subscriber receives a Broadcast event with Seq==0 and Transient==true; the events.jsonl file on disk never contains it (re-read via ReadLog); Snapshot() and a fresh Subscribe(0) replay never contain it; interleaving Broadcast with Record keeps persisted seq order intact; Broadcast after Close is a no-op; cancelled subscriber doesn't block Broadcast.
2. proto/server:
   - Add `bool transient = 6;` to `message Event` in proto/ycc/v1/ycc.proto; regenerate with `buf generate` (buf is installed). Map it in server.toProto.
   - Server.Subscribe already forwards the Log.Subscribe channel, so transients flow through automatically once Log delivers them. Add a server test: subscribe via the RPC path (or the existing server test harness pattern), Broadcast on the session log, assert the streamed event arrives with transient=true/seq=0, and that a reconnect with from_seq resumes correctly (transients never advance the cursor).
3. TUI tolerance:
   - In the evMsg handling (tui.go ~2265, appendEvent ~6421): ignore events with Transient==true (early return before appendEvent) so state/reducers/seq tracking are untouched. No rendering yet (that's task 0114).
4. Spec:
   - Add a short design note in §5 (Session & event log, near §5.2 Event shape): transient events — broadcast-only, seq-less, marked `transient`, never written to events.jsonl / replay / transcripts; used for streaming turn_delta output; subscribers must tolerate seq-less events; delivery is best-effort. Cross-reference from §18.4 (and/or §7) with one sentence pointing at 0114's upcoming turn_delta usage.
5. Verify: go build ./... && go test ./... (plus go vet). Ensure buf-generated code is committed.

### Starting points
- internal/event/log.go — Log/Subscribe cursor-over-l.events design; Record is the persist path
- internal/event/event.go:125 Event struct; Type constants at top of file
- internal/server/server.go:176 Subscribe RPC, :728 toProto
- proto/ycc/v1/ycc.proto message Event (fields 1-5); buf.gen.yaml at repo root; `buf generate`
- internal/tui/tui.go:2265 evMsg handling, :6421 appendEvent
- spec.md §5.2 Event shape (line ~157), §18.4 (line ~1049)
- design decision recorded in backlog/0114-...md (Option A: transient non-persisted events)

## Work log
- 2026-07-03 plan: Goal: add a transient (never-persisted) event path through the existing event pipeline, per the Option A design recorded in task 0114, so a later task can stream turn_delta events to live clients with
…[truncated]
- 2026-07-03 context hints: 7 recorded with plan
- 2026-07-03 context hints: internal/event/log.go — Log/Subscribe cursor-over-l.events design; Record is the persist path; internal/event/event.go:125 Event struct; Type constants at top of file; internal/server/server.go:176 
…[truncated]
- 2026-07-03 implementer report: Implemented the transient (never-persisted) broadcast path for event.Log — the ycc-side transport groundwork for streaming turn_delta output (task 0128, split from 0114, Option A).  ## Changes  **in
…[truncated]
- 2026-07-03 review tier: single-opus — reviewers: Claude
- 2026-07-03 review (Claude): accept — The transient broadcast path is implemented cleanly and fully meets the task. event.Log gains a per-subscriber bounded transient queue drained after each persisted tail; Broadcast enqueues under lock 
…[truncated]
- 2026-07-03 decision: accept — commit: event.Log transient broadcast path: seq-less turn_delta groundwork for streaming (task 0128)  Adds Broadcast() on event.Log delivering transient (never-persisted) events to live subscribers only — n
…[truncated]
- 2026-07-03 usage: 42,186 tok (in 208, out 41,978, cache_r 4,163,358, cache_w 168,310) · cost n/a (unpriced)
  implementer: 30,757 tok (in 124, out 30,633, cache_r 2,580,991, cache_w 65,313) · cost n/a (unpriced)
  coordinator: 7,508 tok (in 44, out 7,464, cache_r 1,295,097, cache_w 77,598) · cost n/a (unpriced)
  reviewer:Claude: 3,921 tok (in 40, out 3,881, cache_r 287,270, cache_w 25,399) · cost n/a (unpriced)
