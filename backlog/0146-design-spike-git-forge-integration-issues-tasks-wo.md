---
id: "0146"
title: 'Design spike: git-forge integration (issues → tasks, workstreams → PRs)'
status: done
priority: 4
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 14.1 Parallel workstreams (git worktrees)
    - 6.2 Backlog — structured items, markdown-rendered
---

## Description
Design spike. The harness's units map naturally onto forge primitives, and the seams already exist:

- **Issues → backlog**: import a GitHub/GitLab issue as a task (`spec_refs`/link back to the issue URL; sync status on done). Killer for teams whose intake lives in issues.
- **Workstream → PR**: a workstream is already a branch (`ycc/ws/…`) with a review-gated merge; "open a PR instead of merging to base" is a natural alternative terminal state, with the reviewers' findings + session summary (see ycc export, 0144) as the PR body.
- **Work session → PR** for non-workstream tasks: optional `commit → push → gh pr create` flow behind a Confirm gate.

Decide: shell out to `gh`/`glab` (zero deps, user's existing auth) vs. API clients; how much is agent-prompt (a plan/runbook) vs. first-class tooling. A minimal first cut might be a coordinator tool + prompt guidance using `gh` via Bash, plus `ycc task import <issue-url>`.

## Acceptance criteria
- [ ] Design doc (docs/design/forge-integration.md) covering the three flows, auth strategy, failure modes (no gh, no remote), and what stays prompt-level vs. becomes tools/RPCs.
- [ ] Follow-on implementation tasks filed from the doc.

## Plan

Design spike — produce docs/design/forge-integration.md (no code changes), then file follow-on tasks from it.

1. Ground the doc in the current architecture. Read: spec §6.2 (backlog structure, spec_refs), §8 (tools), §12 (RPC surface), §14.1 (workstreams: branch ycc/ws/<id>, registry workstreams.json, merge flow + lifecycle events), §17 (decisions); docs/design/parallel-workstreams.md as the house style for spike docs (Status: proposal header, context → goals → design → alternatives → rollout); cmd/ycc/task.go (task add/list/show, direct vs RPC backend), cmd/ycc/export.go + internal/export (session → shareable markdown, natural PR-body source), internal/session/workstream_merge.go + internal/server/workstream.go (merge/discard terminal states the "open PR" state sits beside), spec's confirmation-gate exception (§ around "Exception — confirmation gates").

2. Write docs/design/forge-integration.md covering, at minimum:
   - Status header (proposal, design spike task 0146; no code lands with this doc), context/problem, goals/non-goals.
   - **Forge access strategy (the core decision):** shell out to `gh`/`glab` vs Go API clients. Recommend `gh`/`glab` shell-out for v1: zero new deps, reuses the user's existing auth (keyring/SSO/token refresh handled by the CLI), and matches ycc's "daemon runs on the user's machine" trust model. API clients only if/when a headless multi-tenant need appears. Note the daemon-environment caveat (a persistent/headless daemon must have gh auth available in *its* environment, not the client's).
   - **Auth strategy:** never store forge tokens in ycc config for v1; delegate entirely to gh/glab. Detection/doctor story (`gh auth status`).
   - **Flow 1 — issues → backlog:** `ycc task import <issue-url>` (and GitLab equivalent). Field mapping (issue title→title, body→description, URL recorded as a link-back — propose the concrete mechanism, e.g. an `origin`/link line in the task body or a spec_refs-adjacent field), dedupe on re-import, status sync on done (recommend: optional close/comment behind config, off by default in v1).
   - **Flow 2 — workstream → PR:** "publish" as an alternative terminal state beside merge/discard: push `ycc/ws/…` branch, `gh pr create` with body from session summary + reviewer findings (reuse internal/export). Decide RPC vs client-side (recommend an RPC like PublishWorkstream so remote clients get it, mirroring MergeWorkstream), registry status ("published"), worktree cleanup semantics, and a new lifecycle event (e.g. `workstream_published` {pr_url}).
   - **Flow 3 — work session → PR (non-workstream):** keep prompt-level in v1 — a plans/ runbook driving commit → push → `gh pr create` via Bash behind a confirmation gate; explain why it does not need first-class tooling yet.
   - **Prompt-level vs first-class table** summarizing what is a tool/RPC vs runbook/prompt guidance in v1 and what might graduate in v2.
   - **Failure modes:** no gh/glab installed, not authenticated, no remote / remote not a recognized forge, fork vs origin push rights, PR already exists, network failure mid-publish (branch pushed but PR not created — must be resumable/idempotent).
   - **Safety/gating:** pushing branches and opening PRs are public, hard-to-reverse side effects → confirmation-gate semantics per interaction level (relate to the existing confirmation-gate exception in the spec).
   - **Event-log representation** of forge actions; **alternatives considered**; **phased rollout** ending in a concrete list of follow-on implementation tasks with rough scope.

3. Verify: doc reads coherently against the acceptance criteria (three flows, auth strategy, failure modes, prompt-level vs tools/RPCs split). No build impact.

4. (Coordinator, after review) File the follow-on tasks named by the doc via create_task as `proposed` (the spike was accepted scope; the implementation itself awaits user acceptance — matching precedent of tasks 0150/0154).

### Starting points
- docs/design/parallel-workstreams.md — house style for design-spike docs (Status header, structure)
- spec.md §14.1 (lines ~925-960): workstream concept, registry, merge flow, lifecycle events, RPC surface
- spec.md §6.2 backlog structure + spec_refs semantics; 'Exception — confirmation gates' near line 648
- cmd/ycc/task.go — taskCommand, backlogBackend (direct vs RPC), addParams: where `ycc task import` would slot in
- cmd/ycc/export.go + internal/export — session transcript → markdown (PR body source)
- internal/session/workstream_merge.go, internal/server/workstream.go — merge/discard terminal states and events a 'publish to PR' state sits beside

## Work log
- 2026-07-06 plan: Design spike — produce docs/design/forge-integration.md (no code changes), then file follow-on tasks from it.  1. Ground the doc in the current architecture. Read: spec §6.2 (backlog structure, spe
…[truncated]
- 2026-07-06 context hints: 6 recorded with plan
- 2026-07-06 context hints: docs/design/parallel-workstreams.md — house style for design-spike docs (Status header, structure); spec.md §14.1 (lines ~925-960): workstream concept, registry workstreams.json, merge flow, lifecy
…[truncated]
- 2026-07-06 implementer report: Wrote `docs/design/forge-integration.md` (424 lines), a design-spike doc for git-forge integration, following the house style of `docs/design/parallel-workstreams.md` (Status: proposal header → cont
…[truncated]
- 2026-07-06 review tier: single-opus — reviewers: Claude
- 2026-07-06 review (Claude): accept — The design spike deliverable — docs/design/forge-integration.md — is comprehensive, well-structured (house style matched), and technically grounded. It satisfies every acceptance-criteria item: th
…[truncated]
- 2026-07-06 decision: accept — commit: docs: forge-integration design spike — issues→tasks, workstream→PR publish, gh/glab strategy (task 0146); file follow-on tasks 0155–0163
