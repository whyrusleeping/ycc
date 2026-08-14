---
id: "0323"
title: Bound model-facing tool output and replace full diff replay with manifests
status: proposed
priority: 2
created: "2026-08-12"
updated: "2026-08-12"
depends_on: []
spec_refs:
    - §7.3 Subagents and asynchronous jobs
    - §8 Tools and access policy
    - §10 Work orchestration
---

## Description
Reduce context amplification from large Read/Bash results and repeatedly replayed diffs. Keep durable event logs lossless enough for inspection, but give model history a bounded result with head/tail, byte/line counts, a digest, and an explicit continuation path. Change implementer handoff from an embedded staged diff to a compact change manifest/stat plus report; reviewers receive a bounded diff once per fresh round and a changed-since-prior-round delta/manifest on retained re-review.

Acceptance criteria:
- Read/Bash model-facing results have explicit byte/line budgets and advertise truncation plus how to retrieve a range or narrower command.
- Durable tool-result events distinguish full captured output from the bounded model projection, without leaking unbounded output into replayed history.
- Implementer completion no longer injects the same large diff into coordinator history by default; it returns changed paths/stat and a bounded excerpt, with ordinary git inspection available on demand.
- Retained re-review does not accumulate complete historical diffs round after round.
- Tests cover UTF-8-safe truncation, head/tail retrieval, replay equivalence, and large-diff revision behavior.

## Acceptance criteria

## Work log
