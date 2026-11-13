---
id: "0548"
title: Fix date-dependent TestGetBacklogGroom failure
status: proposed
priority: 2
created: "2026-11-13"
updated: "2026-11-13"
depends_on: []
spec_refs: []
---

## Description
`internal/server` TestGetBacklogGroom fails at HEAD f294066 (and earlier, per 0546's verification note): `GetBacklogGroom progress = pending <nil-ish>, next 1790086400`. The test uses a hardcoded groom start date, so the pending-change count and 24-hour cooldown no longer match once the wall clock passes it.

## Acceptance criteria

- TestGetBacklogGroom derives its timestamps from the current time (or an injected clock) and passes regardless of run date.
- `go test ./internal/server/` passes.

## Work log
