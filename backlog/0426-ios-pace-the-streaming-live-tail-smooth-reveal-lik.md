---
id: "0426"
title: 'iOS: pace the streaming live tail (smooth reveal) like the web client'
status: done
priority: 3
created: "2026-10-06"
updated: "2026-10-10"
depends_on:
    - "0425"
spec_refs: []
---

## Description
Split from 0425. The daemon sends turn_delta snapshots at ≤10 Hz (now with a trailing flush); the iOS app renders each snapshot verbatim via IncrementalStreamingText, so text and the follow-scroll advance in jerky ~100–300ms steps (worse over ngrok jitter). The web client now paces reveals (clients/web/src/features/session/smoothText.ts + useSmoothText.ts).

Approach:
- Port the pacing algorithm to a pure YccKit type (first target / non-prefix replacement → show immediately; prefix growth revealed at backlog / clamp(EMA inter-arrival, 50..250ms) with a min rate; backlog capped ~2000 code units; fractional accumulator; never split a surrogate pair / grapheme), unit-tested in YccKit.
- Drive paced live tails from SessionViewModel (or the live-tail view) at display rate only while a backlog exists, recomputing liveAppend/liveAppendBaseUTF8 for the revealed increments so IncrementalStreamingText stays on its append fast path.
- Scroll-follow must track paced growth using a SEPARATE revision token (do not bump transcriptRevision per frame — snapshot refetch guards compare it and would loop).

Acceptance criteria:
- Streaming text on iOS reveals smoothly between snapshots; follow-latest stays pinned; scrolled-away readers are not moved.
- Opening a session mid-stream shows the current text immediately (no type-in from zero).
- YccKit unit tests for the pacer; verified on device (task goes in_review until then).

## Acceptance criteria

## Work log
- 2026-10-06 implementer report: Implemented Task 0426. Added pure Sendable LiveTextPacer with web-equivalent UTF-16 rate/EMA/clamp/backlog/fractional-budget semantics, immediate initial/replacement snapshots, whole-grapheme reveal b
…[truncated]
- 2026-10-06 revision: Fixed the reset/empty-target bug with an observation-ignored liveTargetsDirty flag: resetLiveReveal marks targets dirty, publishProjection rebuilds even when both source arrays are empty, and publishL
…[truncated]
