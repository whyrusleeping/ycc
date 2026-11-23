---
id: "0594"
title: 'Shell attribution: mark paths ambiguous while shells overlap, and let a session release a wrong claim'
status: todo
priority: 3
created: "2026-11-23"
updated: "2026-11-23"
depends_on: []
spec_refs:
    - spec.md#7.3
---

## Description
Current state: accepted by the user on 2026-11-23 in session s_0d8d2921ae239466, after it was widened to cover false overlap notices. Not started. It was proposed on 2026-11-22 from `docs/reports/shared-tree-concurrency-2026-11.md` in session s_fc31ffbed8395135.

`claimShellChanges` (`internal/tools/shellclaims.go`) claims for the command's scope every path that changed during the command and that no other scope claims at the end. When two scopes' shells overlap, the first to finish claims what the other is still writing, such as generator output or a web build. The later shell then finds the paths claimed and records nothing for them. About 6 such misattributions occurred on 2026-11-21 and 2026-11-22. In one, the 0580 session (other-work review note) was attributed `server.go`, the proto bindings and `dist`, all written by 0582 (project sessions view). Sessions also have no way to release a claim; s_b33f38df5f019e1c noted at event 325 that "No tool exposes claim release".

The same race also produces false overlap notices when the true writer already claims the path. Scope A claims a file and rewrites it from a shell. A's claim is renewed only when that command ends. If scope B's shell finishes first, `claimDirtyChanges` finds the change on a path with an older foreign claim and nothing renewed during B's command. It records B as an overlapping writer, and A is told that B "just wrote" A's file. On 2026-11-23 in wheeler, all three such coordination notices in seven parallel backlog sessions were false, and the commands blamed were read-only (a vindex verify, a ClickHouse benchmark, a short probe):
- s_4ff4cd2bae0288c3 (0445) edited `internal/l2/export.go`, and the notices named s_a865539bec3b689c and s_5a316b94976a87d4.
- s_4a7ca49b30cbbb04 (0452) edited `deploy/prometheus-wheeler-rules.test.yml`, and the notice named s_2c430d6bcafac6d2 and s_a865539bec3b689c.
- s_5a316b94976a87d4 (0317) edited `internal/l2/shadow.go`, and the notice named s_4a7ca49b30cbbb04.

Each notice cost the receiving session an investigation. Two of that day's six board notices (n4 and its rebuttal n5) only spread and then corrected the false attribution. Three commits had to name the file in `paths`.

## Acceptance criteria
- A path that changed during a shell command while another scope's shell was running in the same worktree, and that no file tool claimed, is recorded as ambiguous for both scopes rather than claimed by the first to finish. Both changesets and both coordinators see it listed as ambiguous.
- An ambiguous path becomes a normal claim when a scope's file tool writes it or a commit takes it. A shell write that happens with no other scope's shell running also makes it a normal claim.
- A change to a path that another scope already claims, made while that scope's shell was running, does not record an overlap or send a "just wrote" notice that names the other shell's scope as the writer. At most it is reported as ambiguous. A test covers a claimant's shell rewriting its own file while a read-only shell from another scope starts and finishes inside that window.
- A coordinator can release its own claim on a path it did not write, and the release is recorded in the session log.
- Single-shell attribution behaves as before. Tests cover two overlapping shells writing different paths.
- spec §7.3 describes ambiguous claims and release.

## Work log
