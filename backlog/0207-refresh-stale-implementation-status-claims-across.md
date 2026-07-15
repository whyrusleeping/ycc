---
id: "0207"
title: Refresh stale implementation-status claims across the spec
status: todo
priority: 3
created: "2026-07-15"
updated: "2026-07-15"
depends_on: []
spec_refs:
    - Vision & philosophy
    - Build plan / milestones
    - Onboarding flows
---

## Description
The living spec contains semantic drift that deterministic reference checking cannot catch. Examples include the top-level `design (pre-implementation)` status, descriptions of the substantial iOS client as merely planned, and wording that says implemented wiring is still remaining or currently absent.

Run a focused spec-doctor comparison pass against the current code and recent completed backlog, updating factual implementation-status claims while preserving genuine future design and known limitations.

## Acceptance criteria
- [ ] The top-level project status accurately reflects the implemented, actively developed system.
- [ ] iOS, onboarding, structured questions, config saving, session reopen, and other affected sections distinguish shipped behavior from remaining work accurately.
- [ ] Milestone/task references are checked against current backlog status and code rather than mechanically changed.
- [ ] Contradictory duplicate statements across `spec.md` and linked design docs are reconciled.
- [ ] No aspirational design is falsely labeled implemented; remaining limitations stay explicit.
- [ ] `ycc spec-check` passes after the edits.
- [ ] The change is documentation-only unless a newly discovered code defect is filed separately.

## Work log
