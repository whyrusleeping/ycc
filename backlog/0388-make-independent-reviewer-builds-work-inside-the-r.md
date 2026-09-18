---
id: "0388"
title: Make independent reviewer builds work inside the read-only sandbox
status: done
priority: 2
created: "2026-09-17"
updated: "2026-09-18"
depends_on: []
spec_refs:
    - 7.3 Subagents and asynchronous jobs
    - 10. Work orchestration
---

## Description

Vals retrospective found recurring Cargo verification failures under reviewer sandboxing: 50 review summaries mention cross-device errors and 62 mention read-only restrictions (overlapping sets, not exclusive failure totals). Reviewers sometimes run prebuilt binaries instead, which does not establish fresh-source provenance by itself. The sandbox already permits scratch writes; the gap is a reliable, discoverable source-bound build/test workflow, not removal of workspace write protection.

Acceptance criteria:
- Provide/document bounded per-review scratch output/cache setup (Rust CARGO_TARGET_DIR as initial case) or an isolated snapshot copy when the build requires source-local writes.
- Associate executed verification with the reviewed snapshot and distinguish independently rebuilt/executed, inspected prior evidence, and unavailable verification.
- Preserve read-only original source, Git index, lockfiles and unrelated work; test Landlock and available fallback behavior including symlink/path boundaries.
- Handle dependencies, build scripts, shared cache collisions, cleanup and runtime/disk bounds explicitly; do not allow arbitrary writes to make tests pass.
- Demonstrate a small Rust fixture can compile/test under the production reviewer sandbox while attempted source mutation remains denied. Also verify stale prebuilt artifacts cannot be mistaken for current-snapshot evidence.

Evidence: docs/reports/vals-harness-retrospective.md; sessions s_c26a24042a1601f4 #345 and s_23ae3e3df5110f71 #1035. Promoted to accepted work during user-authorized backlog triage; original-source write protection remains required.

## Outcome

Implemented source_bound reviewer Bash against exact immutable Git trees, fresh private Cargo/Go caches and targets, hard 4 GiB/1M-inode scratch limits, per-command snapshot/result receipts, and truthful unavailable/inspected/executed classifications. Bubblewrap and MountNS preserve original source bytes/modes, isolate PID/proc and descendants, sanitize bootstrap, and prevent nested quota bypasses; inadequate confinement fails closed. Rust fixture passed on both mechanisms; uncached full suite and focused race tests passed, with independent two-reviewer acceptance and coordinator focused reruns. Unrelated pre-existing work preserved.

Commit: Enable source-bound reviewer builds in a confined scratch environment
