---
id: "0323"
title: Bound model-facing tool output and replace full diff replay with manifests
status: done
priority: 2
created: "2026-08-12"
updated: "2026-09-09"
depends_on: []
spec_refs:
    - §7.3 Subagents and asynchronous jobs
    - §8 Tools and access policy
    - §10 Work orchestration
---

## Description
Reduce context amplification from large Read/Bash results and repeatedly replayed diffs while retaining inspectable command captures and exact model-history replay.

## Acceptance criteria
- Read/Bash projections have explicit byte/line budgets, UTF-8-safe truncation, counts/digests, and usable continuation. Text reads report shown ranges, whether more exists, next offsets, and unknown totals when scans are bounded.
- Foreground command previews preserve useful head/tail and terminal diagnostics, with omitted byte/line counts.
- Captured commands have stable authorized artifact references, bounded storage/retention, explicit storage loss, and range retrieval without rerunning. Background eviction is visible and retained artifacts remain discoverable after cancellation.
- Durable events distinguish capture metadata from the exact delivered excerpt; replay never substitutes full output or a later excerpt.
- Implementer handoffs use changed-path/stat manifests and bounded excerpts rather than large embedded diffs; fresh reviews get one bounded diff and retained re-review gets changed-since-prior evidence.
- Regression coverage protects UTF-8 boundaries, head/tail retrieval, replay equivalence, and large-diff revision behavior.

## Outcome
Implemented bounded Read reporting and command head/tail projections, agent-scoped `tool_output` range retrieval (4 MiB per capture, 16 MiB retained, bounded records), explicit loss/eviction and killed-job capture references. Artifacts are in-memory and expire on restart; durable history preserves the exact delivered projection. Implementer reports now use bounded manifests/excerpts and retained reviews receive delta manifests with immutable Git retrieval commands.

Both independent reviewers accepted. `go test ./...` and affected tools/jobs/engine/orchestrator race suites passed, including in an isolated task-only candidate built from HEAD. Unrelated pre-existing workspace edits were preserved outside this commit.

Commit subject: Bound tool output and replace diff replay with manifests
