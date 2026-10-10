---
id: "0440"
title: Replace lifetime worktree leases with write-scoped locks and manifest-based change attribution
status: done
priority: 2
created: "2026-10-09"
updated: "2026-10-10"
depends_on: []
spec_refs:
    - spec.md#7.3
    - docs/design/async-jobs.md#Mutation safety
---

## Description
## Why
A mutating implementer/agent holds the daemon-wide worktree lease for its whole lifetime (spawn → finish). In vals (2026-10-09) the 0401 implementer held it for hours while it mostly watched a hardware run, and another session could not edit docs or code at all. The lifetime claim exists only because change attribution is a whole-tree diff against the session baseline ("everything that changed while I owned the tree is mine"). That guarantee is already broken by unleased shells, so the cost buys little.

Docs-layer edits were exempted first (docs.Store.DocsWriteLock). This task finishes the job.

## Goal
Agents and sessions can work concurrently in the same worktree. Locks are held only for the duration of an individual write. Attribution comes from what each scope actually wrote, not from exclusive ownership.

## Acceptance
- File tools (Edit/Write) and commits take only short, operation-scoped locks. No lease is held across a delegated agent's lifetime, and spawning a mutating implementer/agent is never refused because another session or agent is mutating the same tree.
- Each execution scope records a write manifest of paths written by its file tools. Its changeset is limited to the manifest paths (plus the task file) relative to the session baseline. Another scope's concurrent edits to other files are not reviewed or committed as this task's work.
- When two scopes wrote the same path, the overlap is surfaced (e.g. in the changeset/review/commit output) rather than prevented.
- Shell writes remain best-effort and attribution-approximate, as documented today.
- Spec §7.3, §14 references and docs/design/async-jobs.md describe the new model.
- Tests cover: two sessions editing different code files concurrently; a mutating implementer running while another session edits and commits; overlap reporting.

## Stretch / follow-up idea
Agents coordinating directly (e.g. a cross-session message or "who is touching what" view) — out of scope unless cheap; file separately.

## Acceptance criteria

## Work log

- 2026-10-09 implemented (uncommitted; this tree also holds 0438's in-progress work).
  - **Leases.** Leases are now operation-scoped: one code write, the git half of a commit, or a workstream spawn/merge/discard. Callers wait instead of being refused. Lifetime leases and the intra-session LiveMutating refusals are gone; one implementer per session remains.
  - **Claims.** Claims are per session (`workspacelease/claims.go`): file-tool writes, plus shell writes detected by status/stat snapshots around each command. Backlog and memory are unclaimed bookkeeping. Claims persist in `<gitdir>/ycc/claims.json` and are retired once the path matches HEAD.
  - **Changeset attribution.** `git.Attribution` classifies paths as Foreign, Shared, Preexisting, Deferred, or Unclaimed. HEAD fast-forwards rebase the baseline; partly committed dirty paths keep their leftover changes. A commit overtaken by a concurrent commit is redone on the new HEAD. The index is published under `index.lock`.
  - **Docs and tests.** Docs-layer writes take the docs-store lock only. Spec §7.3 and the async-jobs/workstreams designs are updated. Tests: `git/attribution_test.go`, `orchestrator/shared_worktree_test.go`, `workspacelease/claims_test.go`, plus finalization retry tests.
  - **Review.** Two opus review rounds; all high and medium findings are addressed. Accepted limitations: a docs edit can race a workstream merge; a concurrent unclaimed write can still be picked up by a shell snapshot.
