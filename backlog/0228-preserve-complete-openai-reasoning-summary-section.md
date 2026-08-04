---
id: "0228"
title: Preserve complete OpenAI reasoning summary sections
status: done
priority: 2
created: "2026-08-04"
updated: "2026-08-04"
depends_on: []
spec_refs:
    - Reasoning (thinking) in the event stream
---

## Description
Fix the Codex/OpenAI Responses adapter so multi-part reasoning summaries are requested and assembled without dropping or gluing completed sections. Parse authoritative `response.reasoning_summary_text.done` events and use sequential-cutoff delivery while retaining compatibility with delta-only streams.

## Acceptance criteria
- [x] Codex requests sequential-cutoff reasoning-summary delivery when reasoning summaries are enabled.
- [x] Multiple summary parts are preserved in order with readable separators.
- [x] Completed summary text replaces a partial delta for the same summary index rather than duplicating it.
- [x] Delta-only legacy streams still work.
- [x] Tests cover the request and stream assembly behavior.

## Work log
- 2026-08-04: Added sequential-cutoff requests and index-aware assembly of authoritative completed summary sections, retained delta-only and completed-item fallbacks, and passed `go test ./...`.
