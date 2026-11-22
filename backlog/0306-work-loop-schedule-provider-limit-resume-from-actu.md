---
id: "0306"
title: 'Work loop: schedule provider-limit resume from actual reset times (subusage telemetry)'
status: proposed
priority: 4
created: "2026-08-08"
updated: "2026-08-08"
depends_on:
    - "0295"
spec_refs: []
---

## Description
Follow-on to 0295 (investigation of how the work loop handles a subscription usage limit). The work loop now retries on an escalating fixed schedule (1m→30m, 8h patience) after a provider usage-limit failure. It could instead wait "the correct amount of time" by consulting the provider allowance windows (internal/subusage: Anthropic `resets_at`, OpenAI `reset_at`) and setting `resume_at` to the earliest applicable window reset (+ small slack), falling back to the fixed schedule when telemetry is unavailable.

Design decision needed first: package subusage is explicitly documented as *telemetry only — callers must never use their results to gate or route model turns*. Using it as a scheduling hint (with the fixed schedule as authoritative fallback and a still-mandatory probe on resume) is arguably compatible, but that doctrine should be consciously relaxed/refined in the spec before implementing.

## Acceptance criteria
- Spec updated to define the allowed advisory use of subusage for loop resume scheduling.
- Loop wait uses earliest future reset among windows for the session's provider when fresh telemetry exists; fixed schedule otherwise; patience cap still applies.
- Unit tests with a fake Fetcher.

## Work log
- 2026-11-22 groom: glossed 0295 and moved the acceptance list under the empty Acceptance criteria heading. Checked that internal/subusage still documents itself as telemetry only and that the work loop's 8 h provider patience (internal/session/workloop.go) is unchanged; still proposed.
