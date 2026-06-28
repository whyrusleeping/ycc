---
id: "0045"
title: RemoveModel should also reject models referenced by live session role assignments
status: in_progress
priority: 3
created: "2026-06-27"
updated: "2026-06-28"
depends_on:
    - "0041"
spec_refs: []
---

## Description
## Description

`Registry.RemoveModel` (task 0041) rejects removing a model still referenced by a role, but it
only checks the static `cfg.Roles`. A model assigned to a *running session* via
`Session.SetRoleConfig` (stored on the session as `s.coordinator/implementer/reviewers`, not in
`cfg.Roles`) can still be removed. After removal, that session's next spawn would call
`Registry.Build` and fail with "unknown model", violating the "session never points at a missing
backend" guarantee in this edge case.

### Design
- Have `Manager.RemoveModel` (or the server handler) also consult live sessions' current role
  assignments before removing, rejecting the removal if any running session references the model.
  Requires exposing each session's current coordinator/implementer/reviewers to the Manager.

## Acceptance criteria
- [ ] Removing a model referenced by a running session's live role config (set via SetRoleConfig,
      not in cfg.Roles) is rejected with a clear error.
- [ ] Unit test covering: start/configure a session referencing model X via SetRoleConfig, then
      RemoveModel(X) is rejected; removing an unreferenced model still succeeds.

## Notes
- Follow-on from 0041 (reviewer-noted edge case). Low priority / hardening.

## Acceptance criteria

## Work log
- 2026-06-28 plan: Add a guard so a logical model referenced by a running session's *live* role config (set via SetRoleConfig, stored on the Session, not in cfg.Roles) cannot be removed.  1. internal/session/session.go:
…[truncated]
- 2026-06-28 implementer report: Implemented task 0045: RemoveModel now rejects models referenced by live session role assignments.  Changes (internal/session/session.go): - Added `(*Session).ReferencesModel(name string) bool` — re
…[truncated]
- 2026-06-28 review tier: single-opus — reviewers: Claude
- 2026-06-28 review (Claude): accept — The change adds Session.ReferencesModel (checks live coordinator/implementer/reviewers under lock) and updates Manager.RemoveModel to iterate running sessions and reject removal with a clear error bef
…[truncated]
