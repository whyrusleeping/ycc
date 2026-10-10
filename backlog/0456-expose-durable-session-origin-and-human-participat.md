---
id: "0456"
title: Expose durable session origin and human participation in history summaries
status: done
priority: 2
created: "2026-10-10"
updated: "2026-10-10"
depends_on: []
spec_refs: []
---

## Description
Necessary supporting scope for accepted web personal inbox (0451). Session history must expose explicit durable launch origin (user, work loop, automatic memory groom, other automation; unknown for unproven legacy records) and whether a real human has subsequently engaged. Automatic canned opening prompts are not human input; later actual user_input and nonautomatic question answers are. Preserve summaries' bounded selective decoding and canonical replay. Classification must survive loop replacement/restart and not rely on titles/modes/preset alone. Update protocol/generated clients coherently; avoid adding unused telemetry or exposing transcript contents.

## Outcome
Explicit launch provenance and real human participation are persisted/reduced and exposed in SessionSummary; automatic opening echoes and answers are excluded. Selective history decoding remains payload-bounded, live incremental overlays/reopen retain evidence, and legacy origins stay unknown. Persisted unanswered human questions remain visible after process loss; live gates override stale state and confirmation restoration semantics are unchanged. Go, Swift and web protobuf targets regenerated together. Session/server tests, focused race tests, RPC serialization and selective/full-reader parity pass; final web bundle freshness test passes. Running daemon not restarted. Commit: `web: polish personal inbox and desktop UX`.
