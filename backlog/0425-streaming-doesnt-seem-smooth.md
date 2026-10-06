---
id: "0425"
title: Streaming doesnt seem smooth
status: done
priority: 3
created: "2026-10-06"
updated: "2026-10-06"
depends_on: []
spec_refs: []
---

## Description

Streaming output from the server doesn't really render that smoothly on either the webui or the ios app, not sure why. worth looking into.

## Outcome

Cause: leading-edge-only 100ms turn_delta throttle (no trailing flush) delivered irregular bursts that clients rendered verbatim. Engine now coalesces suppressed snapshots into a mutex-guarded trailing timer ordered before the done delta (tests added). Web paces liveTail reveals (smoothText.ts EMA-rate pacer + useSmoothText rAF hook, surrogate-safe, backlog-capped) and follow-scroll tracks in-row growth via ResizeObserver; dist rebuilt. iOS pacing split to task 0426.

Commit: streaming: trailing-flush turn_delta throttle; pace the web live tail with rAF reveal and resize-driven follow-scroll (0425); split iOS pacing to 0426
