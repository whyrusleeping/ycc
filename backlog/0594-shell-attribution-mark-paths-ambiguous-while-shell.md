---
id: "0594"
title: 'Shell attribution: mark paths ambiguous while shells overlap, and let a session release a wrong claim'
status: proposed
priority: 3
created: "2026-11-23"
updated: "2026-11-23"
depends_on: []
spec_refs:
    - spec.md#7.3
---

## Description
Current state: proposed on 2026-11-22 from `docs/reports/shared-tree-concurrency-2026-11.md`. The user asked for it to be filed as a proposal in session s_fc31ffbed8395135; it is not yet accepted.

`claimShellChanges` (`internal/tools/shellclaims.go`) claims for the command's scope every path that changed during the command and that no other scope claims at the end. When two scopes' shells overlap, the first to finish claims what the other is still writing, such as generator output or a web build. The later shell then finds the paths claimed and records nothing for them. About 6 such misattributions occurred on 2026-11-21 and 2026-11-22. In one, the 0580 session (other-work review note) was attributed `server.go`, the proto bindings and `dist`, all written by 0582 (project sessions view). Sessions also have no way to release a claim; s_b33f38df5f019e1c noted at event 325 that "No tool exposes claim release".

## Acceptance criteria
- A path that changed during a shell command while another scope's shell was running in the same worktree, and that no file tool claimed, is recorded as ambiguous for both scopes rather than claimed by the first to finish. Both changesets and both coordinators see it listed as ambiguous.
- An ambiguous path becomes a normal claim when a scope's file tool writes it or a commit takes it. A shell write that happens with no other scope's shell running also makes it a normal claim.
- A coordinator can release its own claim on a path it did not write, and the release is recorded in the session log.
- Single-shell attribution behaves as before. Tests cover two overlapping shells writing different paths.
- spec §7.3 describes ambiguous claims and release.

## Work log
