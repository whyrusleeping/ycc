---
id: "0252"
title: 'Integration queue: serialized rebase → verify → fast-forward, with auto/gate/manual modes'
status: done
priority: 2
created: "2026-08-06"
updated: "2026-08-08"
depends_on:
    - "0248"
    - "0251"
spec_refs:
    - Parallel workstreams (git worktrees)
    - docs/design/workstream-integration.md#4. The integration queue
    - docs/design/workstream-integration.md#5. Configuration
---

## Description

Build the per-project integration queue described in
`docs/design/workstream-integration.md` §4. One integrator per project, **serialized** (it
subsumes today's `mergeMu`), fed by `workstream_ready` (task 0251).

Per attempt, the **fast path only** (the agent path is task 0253):

1. In the workstream's worktree: `git rebase <base>`; then run `[integration] verify`.
2. Both succeed → advance base by fast-forward (task 0248's `AdvanceBranch`), preserve the
   session log, remove worktree, delete branch, prune, status `merged`, emit
   `workstream_merged`, notify.
3. Rebase conflicts or verify fails → status `needs_attention` + notification, worktree and
   branch left intact (task 0253 later inserts the agent attempt here).
4. Base tree dirty → defer and notify; never touch the user's uncommitted work.

Config (`[integration]`): `mode = auto | gate | manual` (**auto** is the default), `verify`,
`strategy = rebase-ff | squash | merge-no-ff` (default `rebase-ff`), `max_parallel`.

Guardrails:
- **`auto` requires a configured `verify`**; without one it degrades to `gate` with a logged
  warning. Never auto-merge unverified code.
- `gate` keeps the existing accept-diff flow but marks the stream as awaiting acceptance.
- `manual` is exactly today's behaviour.
- `max_parallel` caps concurrently active workstreams at spawn time (0 = unlimited); it is
  also the stand-in for the deferred resource-lease work.

## Acceptance criteria

- [ ] Two ready workstreams integrate sequentially; the second rebases onto a base that
      already contains the first.
- [ ] Clean + green integrates with no model calls at all (fast path costs zero tokens).
- [ ] Conflict or red verify leaves base untouched, the worktree intact, and the stream in
      `needs_attention` with the failing output recorded.
- [ ] `auto` without `verify` degrades to `gate`.
- [ ] `max_parallel` refuses spawns past the cap with a clear error.
- [ ] `mode = manual` reproduces current behaviour exactly (regression).

## Plan

Implement the per-project integration queue (fast path only — agent path is 0253), per docs/design/workstream-integration.md §4–§5.

1) Config (internal/config/config.go)
- Extend `Integration` struct: `Mode` ("auto"|"gate"|"manual", empty = auto), `Verify` (shell command), `Strategy` ("rebase-ff"|"squash"|"merge-no-ff", empty = rebase-ff), `MaxParallel` (int, 0 = unlimited).
- Validate mode/strategy enum values and MaxParallel >= 0 in the config validate path (mirror how notify.events is validated).
- Registry accessor `IntegrationConfig() Integration` (copy under RLock, like WorktreeConfig). Keep IntegrationBase as-is.
- Effective-mode helper (can live in session): empty mode → auto; auto with empty Verify → degrade to gate (log a warning when this degradation suppresses an auto attempt — never auto-merge unverified code). Auto with a non-rebase-ff strategy also degrades to gate with a warning (only rebase-ff is implemented by the queue for now).

2) Events + notifications
- Add `event.WorkstreamIntegrating Type = "workstream_integrating"` (emitted when an attempt starts) and handle it in event.Reduce (WorkstreamState = "integrating", clear attention reason) alongside the existing workstream cases.
- Notify: add kinds `KindAttention = "attention"` (high priority) and `KindMerged = "merged"` (default priority) in internal/notify and config.NotifyEventKinds. Send attention on needs_attention and on deferred-dirty-base; send merged on successful auto-integration. Use m.notify(...) helper as elsewhere.

3) The queue (new file internal/session/workstream_integrate.go)
- Per-project serialized integrator owned by Manager: map[project]*integrator (guarded by m.mu), lazily created; each has a pending id set/queue and a single drainer goroutine so attempts for one project run strictly one at a time and duplicate enqueues coalesce. Every attempt also takes m.mergeMu so auto attempts never interleave with manual MergeWorkstream (mergeMu is the cross-project serializer it subsumes).
- Also make DiscardWorkstream take mergeMu, closing the discard-during-integration race.
- Enqueue triggers: (a) in evaluateWorkstreamReadiness right after emitting WorkstreamReady, when effective mode == auto; (b) at the end of ReconcileWorkstreams, enqueue every StatusReady workstream when effective mode == auto (so a daemon restart doesn't strand ready streams).
- Attempt (fast path), under mergeMu:
  a. Re-fetch ws; require Status == StatusReady, else skip (CAS-safe).
  b. repo := primaryRepo(ws); base := workstreamBaseBranch(repo, ws).
  c. repo.CheckBaseClean(base): dirty → DEFER: leave status ready, notify attention ("integration deferred: base tree dirty ..."), do not touch anything.
  d. workstream.VerifyUnderRoot for the worktree path (fail → needs_attention with reason).
  e. Emit WorkstreamIntegrating on the ws session stream (emitWorkstreamEvent).
  f. repo.RebaseOnto(worktree, base): conflict → Transition ready→needs_attention with reason "rebase onto <base> conflicts: <paths>", emit WorkstreamNeedsAttention (include conflicts in data), notify attention. Hard git error → needs_attention with the error text.
  g. Run verify: `sh -c <verify>` in the worktree, env = os.Environ() + worktreeConfigFor(primary).Env (same env agent shells get), with a fixed generous timeout (e.g. 30m const). Capture combined output. Failure → needs_attention; reason records the command + a bounded tail of the output (~2KB), same detail in the event data; notify attention. Base branch is untouched in this path (only the ws branch was rebased; worktree left intact).
  h. repo.AdvanceBranch(base, ws.Branch) → on success follow MergeWorkstream's finish order: emit WorkstreamMerged (with commit/base_branch), Transition ready→merged (CAS — if it fails due to a concurrent transition, stop without cleanup), Stop the session, preserveWorkstreamSession, cleanupWorktree, notify merged. AdvanceBranch ErrBaseTreeDirty at this late stage → treat as defer (leave ready, notify), since the rebase already happened and is harmless.
- No model calls anywhere in this path.

4) max_parallel (SpawnWorkstream in internal/session/session.go)
- After resolving the project and before creating anything: if IntegrationConfig().MaxParallel > 0, count this project's workstreams with Status == StatusActive; at/over the cap → refuse with a clear error like `project %q already has %d active workstreams (integration.max_parallel = %d)`.
- Map that error in internal/server/workstream.go workstreamError (string contains "max_parallel" → connect.CodeResourceExhausted).

5) Mode semantics
- manual: never enqueue — current behaviour exactly (MergeWorkstream/Discard unchanged).
- gate (and degraded auto): never enqueue; StatusReady is the awaiting-acceptance marker; existing accept-diff flow (PreviewWorkstreamMerge/MergeWorkstream) is unchanged.

6) Tests (internal/session/workstream_integrate_test.go, package session, modeled on workstream_merge_test.go / newWorkstreamManager; add a config-registry hook so tests can set Integration config — see how testRegistry() builds the config.Registry and add a setter or construct with the desired Config)
- Fast path: mode=auto, verify="true" → spawn, commit in worktree, call evaluateWorkstreamReadiness(id, idle, false); poll (deadline) until StatusMerged; assert file in primary tree, worktree removed, branch deleted, workstream_merged event in the session snapshot; assert no subagent/model events were added after ready (zero-token fast path).
- Sequential: two workstreams committing different files, both made ready → both merge; base contains both files and history is linear (second rebased onto first; e.g. CountCommits(base pre, base post) == 2 and merge commit absent).
- Red verify (verify="false") → StatusNeedsAttention, reason mentions verify + output, base rev unchanged, worktree dir still exists.
- Rebase conflict (commit conflicting file to base first) → needs_attention listing the conflicted path, base untouched, worktree intact.
- auto without verify → stream stays StatusReady, base unchanged (degrades to gate).
- max_parallel=1 → second SpawnWorkstream refused with the clear error; after the first is merged/discarded, spawn succeeds again.
- mode=manual with verify configured → ready stream stays ready; MergeWorkstream still works (regression).
- Dirty base defer: dirty file in primary checkout → stream stays ready, no rebase applied, notifier (sink) got an attention line. Reuse the notifier sink pattern from notify_test.go if cheap.

7) Docs: update spec.md §14.1 (parallel workstreams section) briefly to record the implemented integration queue + `[integration]` config keys (mode/verify/strategy/max_parallel, auto→gate degradation); adjust the design doc's status line if it claims pre-implementation for §4/§5 fast path.

Out of scope (already tracked or follow-on): agent path (0253), RetryIntegration RPC/TUI/iOS surfaces + merge-all-ready (0254), squash/merge-no-ff execution strategies (validated but degrade auto to gate — note in config docs).

Verify with: go build ./... && go vet ./... && go test ./internal/session/... ./internal/config/... ./internal/git/... ./internal/event/... ./internal/notify/... ./internal/server/...

### Starting points
- internal/session/workstream_merge.go — MergeWorkstream order-of-operations, emitWorkstreamEvent, cleanupWorktree, preserveWorkstreamSession
- internal/session/workstream_ready.go — evaluateWorkstreamReadiness (enqueue hook goes after WorkstreamReady emit)
- internal/session/session.go:1634-1815 SpawnWorkstream (max_parallel check), 1821-1909 ReconcileWorkstreams (startup enqueue), worktreeConfigFor
- internal/git/merge.go — RebaseOnto, CheckBaseClean, AdvanceBranch, ErrBaseTreeDirty
- internal/config/config.go — Integration struct (~503), NotifyEventKinds (~609), WorktreeConfig accessor pattern (~876)
- internal/event/event.go workstream event types; internal/event/reduce.go workstream cases (~114-135)
- internal/notify/notify.go — kinds + Send priority switch
- internal/server/workstream.go workstreamError (string-matched mapping)
- tests: internal/session/workstream_test.go newWorkstreamManager, workstream_merge_test.go commitInto/sessionGitAt, notify_test.go sink pattern

## Work log
- 2026-08-08 plan: Implement the per-project integration queue (fast path only — agent path is 0253), per docs/design/workstream-integration.md §4–§5.  1) Config (internal/config/config.go) - Extend `Integration` 
…[truncated]
- 2026-08-08 context hints: 9 recorded with plan
- 2026-08-08 context hints: internal/session/workstream_merge.go — MergeWorkstream finish order, emitWorkstreamEvent, cleanupWorktree, preserveWorkstreamSession, workstreamBaseBranch, primaryRepo; internal/session/workstream_r
…[truncated]
- 2026-08-08 preload: 3 file(s), ~39 KiB seeded into implementer context
- 2026-08-08 implementer report: Implemented task 0252’s fast-path integration queue.  Changes: - Extended `[integration]` config with `mode`, `verify`, `strategy`, and `max_parallel`; added validation and a thread-safe `Integratio
…[truncated]
- 2026-08-08 review tier: high-powered — reviewers: sol
- 2026-08-08 review (sol): revise — The configuration, queue serialization, fast-path rebase/verify/fast-forward flow, failure handling, notifications, max-parallel check, docs, and tests are generally well implemented, and the requeste
…[truncated]
- 2026-08-08 revision: Addressed both reviewer issues.  Changes: - Pinned the exact rebased workstream tip SHA before running verify. - After verify, re-check both the branch SHA and registry status; if either changed, abor
…[truncated]
- 2026-08-08 review (sol): revise — The revision correctly pins the branch tip, advances base to that immutable SHA, restores the ready projection on late dirty-base deferral, and adds focused regression tests; all requested build/vet/t
…[truncated]
- 2026-08-08 revision: Closed the remaining verify/worktree-content race.  Changes: - Added `git.Repo.WorktreeStatusPorcelain`, returning a stable porcelain-v1 snapshot with all untracked files included. - After pinning the
…[truncated]
- 2026-08-08 review (sol): accept — The revision now protects the verification boundary by pinning the rebased commit, snapshotting the worktree before and after verification, rejecting tracked dirt before verification, aborting on cont
…[truncated]
