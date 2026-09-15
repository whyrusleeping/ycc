---
id: "0381"
title: Reduce remaining iOS transcript load latency and reliably open at latest
status: in_review
priority: 1
created: "2026-09-15"
updated: "2026-09-15"
depends_on: []
spec_refs: []
---

## Description
Follow-up to 0380: on-device latest vals load improved but still takes ~5 seconds, then remains at the start of the loaded 200-row window rather than bottom. Investigate remaining transfer/decode/layout cost and apply bounded compatible optimizations. Ensure initial loaded history reliably opens at latest without reintroducing live scrollback stealing or lazy-stack blanking. Verify server/transport improvements where possible here; iOS builds/device validation on user's Mac.

## Acceptance criteria

## Work log
- Measured the now ~47 MB vals log. An opt-in GetSessionTranscript presentation response omits only model_turn.thinking_blocks without mutating shared or persisted history. Actual protobuf+gzip benchmark: 17,223,757 -> 8,514,799 bytes; encode/compress 1.12s -> 0.85s locally (single run, not device latency).
- iOS requests the presentation response, uses ProtoCodec, and forwards unary responses off the main URLSession delegate queue before Connect decoding. Both Go/Swift protobuf bindings regenerated.
- Added one-shot initial-replay readiness (including empty snapshots), recreated the loading scroll container once, and bounded initial-only bottom-pin corrections. Reconnect/paging retain container identity; user scrollback cancels pinning.
- Added server binary/JSON round trips and typed/persisted shared-state protection, an opt-in real-log benchmark, Swift decode-executor and initial-readiness regressions. Server/daemon/event/proto tests and targeted server race tests pass. Swift compilation and device checks remain for user's Mac.
- Payload reduction requires an updated daemon as well as iOS; older daemons safely return full history. No daemon restart performed during this work.
