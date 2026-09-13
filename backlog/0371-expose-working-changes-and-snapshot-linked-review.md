---
id: "0371"
title: Expose working changes and snapshot-linked review verdicts across clients
status: blocked
priority: 2
created: "2026-09-08"
updated: "2026-09-10"
depends_on:
    - "0353"
spec_refs:
    - §10 Work orchestration
    - §12 RPC protocol
    - §18 Client interaction model
---

## Description
iOS drops review verdict/findings from its review row and web uses generic system presentation. Clients can inspect committed diffs but lack a general non-mutating working-changes API, limiting remote pre-commit supervision. Evidence: internal/orchestrator/orchestrator.go:1038–1042; SessionProjection.swift review rendering; proto/ycc/v1/ycc.proto GetCommitDiff and PreviewMerge.

## Acceptance criteria
- Add a bounded authenticated working-changes read API scoped to project/session/changeset that does not stage files or mutate the workspace.
- Return a snapshot identity and explicit scope/truncation information so users know which tree was inspected; use safe changeset semantics from 0353.
- Review events/projections prominently show verdict, summary, finding counts/severity where available, reviewer/model, round, and reviewed snapshot; do not imply a verdict covers later edits.
- TUI/web/iOS allow inspecting the relevant uncommitted changes and review evidence without introducing a mandatory new human approval gate into unattended execution.
- Tests cover unrelated dirty files, stale snapshots, large/untracked changes, accept/revise/unknown outcomes, and legacy review events.
- Regenerate both Go and Swift outputs for any protobuf changes.

## Work log

- Preflight: blocked in this workspace by pre-existing changes in the required protobuf, server, changeset, and client projection files. The verified changeset ownership guard refuses editing baseline-dirty paths. No implementation attempted; start in an isolated clean task session or resolve the existing owners' changes before capturing a new baseline.
