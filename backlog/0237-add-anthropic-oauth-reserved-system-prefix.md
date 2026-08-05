---
id: "0237"
title: Add Anthropic OAuth reserved system prefix
status: done
priority: 1
created: "2026-08-05"
updated: "2026-08-05"
depends_on:
    - "0235"
spec_refs:
    - spec.md#13. Model backends, roles & routing
    - spec.md#7.4 Reasoning (extended/adaptive thinking + effort)
---

## Description
For Anthropic subscription OAuth turns, prepend the reserved subscription-classification system prefix before ycc's behavioral system prompt while preserving ycc's prompt and normal API-key behavior. Use a truthful ycc entrypoint in the billing marker, cover request ordering and streaming/non-streaming paths with tests, document the wire contract, and verify a live OAuth inference turn.

## Acceptance criteria

## Work log
