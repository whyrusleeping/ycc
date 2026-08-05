---
id: "0235"
title: Update Anthropic subscription OAuth and rate-limit handling
status: done
priority: 1
created: "2026-08-05"
updated: "2026-08-05"
depends_on: []
spec_refs:
    - spec.md#13. Model backends, roles & routing
    - spec.md#7.2 Retry policy
---

## Description
Bring ycc's Anthropic subscription OAuth flow in line with the current Claude Code authorization/token endpoints and scopes, provide a clear migration/re-login path for legacy credentials, and improve persistent 429 handling so one incompatibility does not produce a misleading flood of retries. Add focused tests and update durable documentation.

## Acceptance criteria

## Work log
