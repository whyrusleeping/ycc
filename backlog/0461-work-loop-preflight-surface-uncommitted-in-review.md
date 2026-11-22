---
id: "0461"
title: 'Work loop preflight: surface uncommitted in_review work before starting'
status: done
priority: 2
created: "2026-10-12"
updated: "2026-11-22"
depends_on: []
spec_refs: []
---

## Description

**Evidence (vals, 2026-10-09..11):** two chat sessions left 6 reviewed `in_review` tasks (0366/0371/0372/0377/0403/0406) integrated into the main tree but uncommitted (~57 files). A work loop was then started on that tree. Every loop session routed around those files and split tasks into fragments (0425→0426, 0380→0427, …). None committed the batch, because the in_review gates belong to the owner.

**Proposal:** at StartWorkLoop (and in the digest), detect dirty paths in the workspace and in_review tasks whose work is uncommitted. Warn the owner, or require confirmation, before starting, e.g. "6 in_review tasks have uncommitted changes in 57 files; commit or confirm to proceed". At minimum, list these in the loop digest so the stall cause is visible without forensics.

**Acceptance:** starting a loop on a tree with uncommitted in_review work produces a visible warning/confirmation in TUI/web/iOS, and the loop digest lists those tasks. Tests use a fixture repo.

## Outcome

New GetWorkLoopPreflight RPC lists in_review tasks with uncommitted work: a dirty task file, or a dirty claimed file written while the claiming session focused that task (time-checked; ambiguous claims unattributed). StartWorkLoop records it in WorkLoopInfo.uncommitted_in_review/uncommitted_paths (persisted); finish notice counts it. Web start dialog and iOS confirmations warn and say "Start anyway"; loop views show a digest section. Verified: Go/web tests, fixture-repo test, scratch-daemon web/iOS screenshots, swift test, xcodebuild. TUI retired (0481). Shared files hold only 0461 hunks; 0580/0582 claims there were stale.

Commit: Work loop preflight: warn before starting on uncommitted in-review work and list it in the loop digest (0461)
