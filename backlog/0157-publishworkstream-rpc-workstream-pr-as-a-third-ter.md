---
id: "0157"
title: 'PublishWorkstream RPC: workstream → PR as a third terminal state (published)'
status: proposed
priority: 4
created: "2026-07-06"
updated: "2026-07-06"
depends_on:
    - "0155"
spec_refs:
    - 14.1 Parallel workstreams (git worktrees)
    - docs/design/forge-integration.md#6. Flow 2 — workstream → PR (publish)
---

## Description
From docs/design/forge-integration.md §6 (design spike 0146). The flagship forge task.

Add **publish** as a third workstream terminal state beside merge/discard: push the `ycc/ws/<id>` branch and open a PR instead of merging locally.

- `PublishWorkstream` RPC mirroring `MergeWorkstream` (internal/session/workstream_merge.go, internal/server/workstream.go, proto): daemon owns worktree/registry/session/event log, and remote clients get the feature for free.
- Preconditions: workstream active; forge probe (task on internal/forge); remote resolves to a recognised forge — any failure is a specific error with nothing mutated.
- Gate: mirror MergeWorkstream's accept flow (NeedsAccept preview with branch/remote/PR title/body under interactive/judgement; autonomous proceeds — doc §8 policy 1, `forge.confirm_publish` knob).
- PR body composed from the session transcript via internal/export (GetSessionTranscript + export.Markdown); title defaults to the focused task title or first commit subject.
- Push + `gh pr create`; capture PR URL.
- New lifecycle event `workstream_published` {workstream, branch, pr_url} via emitWorkstreamEvent; new terminal registry status `published` storing the PR URL.
- Publish-specific cleanup: preserve session log (preserveWorkstreamSession), remove worktree by default (option to keep it), **do not delete the branch** (the PR points at it).
- Idempotent/resumable: on retry after a mid-publish failure (branch pushed, no PR), detect via `gh pr list --head`, reuse/create as needed, report the existing PR URL; registry transitions to `published` only after the PR URL is confirmed (doc §9).

## Acceptance criteria
- [ ] `PublishWorkstream` RPC + client surface; gated preview like merge.
- [ ] `workstream_published` event emitted and rendered as a session event; registry status `published` with PR URL.
- [ ] Branch survives; worktree cleanup per the doc; retry after partial failure converges without duplicate PRs.
- [ ] Failure modes from doc §9 covered with specific errors (no gh, no remote, unrecognised host, no push rights, PR exists).

## Work log
