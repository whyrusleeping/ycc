---
id: "0452"
title: 'Web: replace live badges with clear user-facing session states'
status: done
priority: 3
created: "2026-10-10"
updated: "2026-10-10"
depends_on: []
spec_refs: []
---

## Description
Accepted UI audit improvement 4. Remove the technical live badge and distinguish Working, Needs your answer, Waiting on background jobs, Paused, ready/idle, and Error consistently in lists and open session. Label graceful Interrupt as Pause, explain checkpoint behavior, retain destructive Stop confirmation and distinguish pending pause. Avoid implying success from idle or disconnect.

## Outcome
Removed technical live badges from lists/task links; state labels describe work, pending human answers, background jobs, pause, readiness and errors. Interrupt is Pause with checkpoint explanation; pending pause/cancel/resume and confirmed Stop remain distinct. Native Chromium visual check and full web build/test pass. Commit: `web: polish personal inbox and desktop UX`.
