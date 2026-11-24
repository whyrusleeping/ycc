---
id: "0593"
title: Candidate tree for verification, review and commit in shared worktrees (HEAD plus this session's changes)
status: proposed
priority: 2
created: "2026-11-23"
updated: "2026-11-23"
depends_on: []
spec_refs:
    - spec.md#7.3
---

## Description
Current state: proposed on 2026-11-22 from `docs/reports/shared-tree-concurrency-2026-11.md`. The user asked for it to be filed as a proposal in session s_fc31ffbed8395135 (event 164, 2026-11-23 02:07 UTC); it is not yet accepted. It would absorb 0586 (line attribution of code files, proposed), whose open question is whether a mix of two sessions' hunks builds; building the candidate answers it.

In the shared tree, review snapshots and reviewers' read-only shells see other sessions' in-progress work. From 18:38 to 19:15 UTC on 2026-11-22 that cost about 6 extra review rounds and 7 blockers that belonged to other tasks; for example, every 0582 round had the blocker that 0461's handler broke the build. It also led to 5 commits landed by hand. Three sessions (0461, 0580, 0582) each built a `/tmp` `git worktree add --detach` copy to test or commit their change alone. Each did so only after the conflict appeared. 0582 landed as `b5c61d7` without a reviewer accept, after `update-ref` and `git reset` in the shared tree.

Proposed direction: when the tree is shared, the daemon builds a candidate tree, a scratch worktree with HEAD plus only this session's changeset.
- The coordinator's verification commands and the reviewers' read-only shells run there.
- The commit takes exactly the reviewed candidate, then publishes into the shared index as today.
- Code files that another session also changed contribute only this session's hunks. For file-tool writes the hunks come from the write record. For shell edits of a file another scope claims, the before and after file contents are stored (for example as git blobs), but only for those files.
- Hunks that cannot be separated fall back to the current whole-file behaviour, with the other-work note (0585, non-blocking review note) and the commit refusal (0567, refusing commits of shared files).

## Acceptance criteria
- With another live session's uncommitted changes in a shared code file, the review snapshot, the reviewers' shell and the commit see only this session's changes, and the other session's lines stay uncommitted in the worktree.
- A build or test failure caused only by another session's in-progress work does not appear in this session's candidate.
- A candidate that does not build when separated from the other work is reported before reviewers start, naming the files.
- Hunks that overlap or cannot be attributed fall back to the whole-file behaviour, with the existing note.
- The candidate tree is removed after the commit or when the review ends; it never moves the shared HEAD except through the commit's existing compare-and-swap.
- spec §7.3 describes the candidate tree; 0586's acceptance criteria are met or 0586 is closed as superseded.

## Work log
- 2026-11-23 groom: added the event reference for the filing request. Not implemented. Flagged the proposed absorption of 0586 for the user in 0550 (backlog groom decisions). Still proposed.
