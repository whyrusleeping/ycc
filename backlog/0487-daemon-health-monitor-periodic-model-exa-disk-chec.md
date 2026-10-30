---
id: "0487"
title: 'Daemon health monitor: periodic model / Exa / disk checks + GetHealth RPC'
status: in_review
priority: 2
created: "2026-10-29"
updated: "2026-10-29"
depends_on: []
spec_refs:
    - spec.md#14.2 Health monitoring
---

## Description
The persistent daemon periodically checks that its external dependencies work and exposes the results so clients can alert the user.

User decisions (2026-10-29):
- Models: probe every enabled model that is subscription-authenticated (auth=oauth) or local (ollama); probe a paid API-key model only when a role or review tier uses it. Hourly cadence; a successful real turn within the interval counts as healthy without a probe.
- Exa: probe with the /contents endpoint (fetch_page, $1/1k pages) instead of search ($4–7/1k). A failing real web_search/fetch_page call retriggers the check.
- Disk: warn below 20 GB free, fail below 10 GB, on the filesystems holding daemon state and registered projects.
- Surfaces: banner + Health panel in web and iOS (separate tasks); no push.

Acceptance:
- `GetHealth` returns each check (id, label, status ok/warn/fail, detail, remedy, checked_at) without making provider calls; `refresh` re-runs checks now.
- Real model-turn and Exa failures of a credential/endpoint kind schedule a confirming re-check soon (debounced); real successes mark the check healthy.
- No credential values appear in check details.
- Go, Swift, and web generated code regenerated.

## Acceptance criteria

## Work log
