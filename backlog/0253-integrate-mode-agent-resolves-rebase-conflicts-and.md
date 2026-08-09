---
id: "0253"
title: 'integrate mode: agent resolves rebase conflicts and verify failures in the worktree'
status: done
priority: 2
created: "2026-08-06"
updated: "2026-08-08"
depends_on:
    - "0252"
spec_refs:
    - Parallel workstreams (git worktrees)
    - docs/design/workstream-integration.md#4. The integration queue
---

## Description

The integration queue's fast path (task 0252) handles clean rebase + green verify. Everything
else — merge conflicts, a test that breaks only after rebasing onto the advanced base — should
be handled by an agent, not by interrupting the user (that interrupt is the cost
auto-integration exists to remove).

Add an **`integrate` mode**: a coordinator session **scoped to the workstream's worktree**
(never the primary tree — the worktree is the correct blast radius) with the worker toolset
plus git. Seed prompt names the base branch, the conflicted paths and/or the failing verify
output, and the contract: resolve or fix, re-run `verify`, then request integration; if the
call isn't yours to make, `report_blocked`.

- Bounded by `[integration] agent_attempts` (default 1).
- Success → the queue re-runs verify itself and advances base; the agent never touches base.
- Failure/blocked/attempts exhausted → `needs_attention` + notification, worktree intact.
- The `integrate` session is a normal session: it streams, is drillable from the workstream
  row, and its transcript is preserved on cleanup.

## Acceptance criteria

- [ ] A conflicting rebase is resolved by the integrate session and the workstream merges,
      with the resolution visible in its transcript.
- [ ] A verify failure introduced by base drift is fixed and merged.
- [ ] An unresolvable case ends in `needs_attention` after `agent_attempts`, base untouched.
- [ ] The integrate session cannot advance base itself (the daemon owns that step).
- [ ] Spec §14.1 wording flips from "planned" to the implemented behaviour once 0252+0253
      land.

## Plan

Implement the `integrate` agent path on top of task 0252's fast-path queue, per docs/design/workstream-integration.md §4 step 2.

1) Config (internal/config/config.go)
- Add `AgentAttempts *int` (`toml:"agent_attempts,omitempty"`) to `Integration`: nil → default 1; explicit 0 → agent path disabled (pure 0252 fast path); negative → validation error (add to the validate path beside max_parallel). Deep-copy the pointer in `IntegrationConfig()` so the registry copy stays isolated. Add a small helper (config or session side) `effectiveAgentAttempts(Integration) int`.

2) Registry (internal/workstream/registry.go)
- Add `IntegrateSessionID string json:"integrate_session_id,omitempty"` to `Workstream` and a persisting `SetIntegrateSessionID(id, sessionID)` (mirror SetSessionID). This is the drill-in hook for 0254; no proto/client changes in this task.

3) Session-level blocked signal (internal/session/session.go)
- In the run loop's idle branch, include `"blocked": true` in the `SessionIdle` event data when `res.Blocked` is set (engine.Result already carries Blocked from the report_blocked control tool; today only integrate mode will ever set it at session level). Harmless for other modes.

4) `integrate` mode (internal/orchestrator/modes.go, prompts.go; internal/tools/worker.go)
- `BuildMode` case "integrate": `tools.Editing(ws)` (Read/Write/Edit/Bash — git rides on bash) + a new `RequestIntegration()` control tool (Finish clone: name `request_integration`, description "call when the branch is rebased onto base, conflicts resolved/verify green — ends the run and asks the daemon to re-verify and integrate"; returns Control{Stop:true, Report}) + `tools.ReportBlocked()`. No backlog/spawn/mode-switch tools.
- New `integrateModeSystem` prompt in prompts.go: you are the integration agent for one workstream, scoped to its worktree; the daemon aborted a conflicted rebase / saw a red verify; your job: run `git rebase <base>` yourself, resolve conflicts (or fix the verify failure), commit on the workstream branch, re-run the verify command, then `request_integration`. NEVER touch/advance the base branch, never push, never merge into base — the daemon owns that step and will independently re-rebase + re-run verify before advancing base. If the right resolution isn't yours to decide, `report_blocked`.

5) Queue refactor (internal/session/workstream_integrate.go)
- Split `integrateReadyWorkstream` into a wrapper + `integrationFastPath(id)` (holds mergeMu; the existing body). The fast path no longer transitions to needs_attention on the two agent-fixable failures — rebase conflict and verify red — it returns a structured outcome {kind: merged/skip/deferred/handled, or conflict{paths}/verifyFailed{cmd, output, err}, base, reason string preserving today's exact reason formats}. All other failure paths (git errors, dirty-base defer, snapshot aborts) keep transitioning inline as today and return "handled".
- Wrapper: run fast path; while outcome is agent-fixable and attempts remain (effectiveAgentAttempts, auto mode only) and !integrationStop/integrationCtx alive: run the agent seam, then re-run the fast path. Agent blocked or errored → needs_attention immediately (reason includes the agent's report + original failure). Attempts exhausted with a still-failing outcome → needs_attention with the fast path's reason (byte-identical to today's formats so existing assertions keep meaning).
- Agent seam: Manager field `integrateAgent func(ws workstream.Workstream, base string, fail integrationFailure) integrateAgentResult{report string, blocked bool, err error}`, defaulted in NewManager to `m.runIntegrateSession`; tests inject. (Same pattern as workloop's runSession seam.)
- `runIntegrateSession` (real seam): emit `WorkstreamIntegrating` on the workstream stream with data {"agent": true, "attempt": n, "conflicts"/"verify_output" as applicable}; build the seed prompt naming the base branch, the verify command, the conflicted paths and/or a bounded tail of the failing verify output, and the contract; `m.start(Config{Workspace: ws.WorktreePath, Mode: "integrate", Prompt: seed, Unattended: true}, false)`; `SetIntegrateSessionID`; poll session status (200ms ticker, like workloop) until Idle/Error/Stopped, bounded by a generous const (e.g. integrationAgentTimeout = 1h) and by m.integrationCtx; then snapshot the log — StatusError → err; last SessionIdle with data blocked=true → blocked with report; else success with report. Always `m.Stop(s.ID)` before returning so the session is not writing during the re-verify snapshot.
- Merged/discard cleanup: when the workstream finishes (queue merged path, MergeWorkstream, DiscardWorkstream), also Stop the live `IntegrateSessionID` session (re-Get the ws from the registry for freshness) and preserve its transcript — factor `preserveWorkstreamSession` into a per-session-id helper and call it for both SessionID and IntegrateSessionID.

6) Tests (internal/session/workstream_integrate_test.go, stub seam unless noted)
- Conflict resolved: mode=auto, verify green, conflicting commit on base; stub seam performs the resolution with git (e.g. reset branch onto base + commit resolved content in the worktree) → workstream merges, resolution content in primary tree; assert the seam received the conflicted path and that base was UNCHANGED at seam time (daemon-owned advance).
- Verify failure fixed: verify checks for a file only base drift demands; seam commits the fix → merged.
- Unresolvable: seam returns blocked (and a variant that just doesn't fix) with agent_attempts=1 → needs_attention, base rev unchanged, worktree intact, seam called exactly once; reason recorded.
- agent_attempts=0 → seam never called, immediate needs_attention (0252 regression guard).
- agent_attempts=2, first seam call no-op, second fixes → merged, seam called twice.
- Transcript preservation: seam writes/creates an integrate session log (or sets IntegrateSessionID) → after merge, log dir copied into the primary workspace.
- Real-runner smoke: call m.runIntegrateSession (or run the flow with the real seam) against testRegistry's unreachable ollama backend with a minimal retry config → an "integrate"-mode session is created in the worktree, ends in error, result surfaces the failure; queue lands in needs_attention. Keep it fast (tune config.Retry); if engine retry makes it slow, test runIntegrateSession directly.
- Existing 0252 tests: keep passing; where they assert immediate needs_attention on conflict/red verify, set agent_attempts=0 or a stub no-op seam explicitly.
- orchestrator: BuildMode("integrate") returns request_integration + report_blocked + editing tools and no spawn/backlog tools; prompt contains the never-advance-base contract.

7) Docs
- spec.md §14.1: flip "the integrate-agent recovery path remains planned" to the implemented behaviour (integrate-mode session in the worktree, bounded by `agent_attempts` default 1, daemon re-verifies and owns the base advance, needs_attention on failure) and add `agent_attempts` to the `[integration]` key list.
- docs/design/workstream-integration.md status header: agent recovery shipped; client retry/merge-all surfaces (0254) remain.

Verify: go build ./... && go vet ./... && go test ./internal/session/... ./internal/config/... ./internal/orchestrator/... ./internal/tools/... ./internal/workstream/... ./internal/event/... ./internal/git/... ./internal/server/...

### Starting points
- internal/session/workstream_integrate.go — the 0252 fast path to refactor (integrateReadyWorkstream, drainWorkstreamIntegrations, integrationNeedsAttention, boundedIntegrationOutput)
- internal/session/workstream_merge.go — emitWorkstreamEvent, preserveWorkstreamSession, cleanupWorktree, MergeWorkstream/DiscardWorkstream (add IntegrateSessionID stop+preserve)
- internal/session/workloop.go:487 realRunSession — the status-polling wait pattern and the injectable-seam precedent (wl.runSession)
- internal/session/session.go:1187-1196 idle branch (add blocked flag), :1574 start() (mode/unattended/autoRegister=false), :1341-1390 integrationCtx/WG/Stop fields
- internal/orchestrator/modes.go BuildMode — add case "integrate"; internal/tools/worker.go Finish()/ReportBlocked() to clone for request_integration
- internal/config/config.go:503 Integration struct, ~810 validation, IntegrationConfig() accessor
- internal/workstream/registry.go SetSessionID — mirror for SetIntegrateSessionID
- internal/session/settings_test.go testRegistryWithIntegrationConfig — how tests set Integration config
- git.RebaseOnto ABORTS on conflict (worktree left restored) — the agent must re-run the rebase itself; seed prompt must say so
- existing reason formats to preserve: "rebase onto %s conflicts: %s" and "verify %q failed: %v\n%s"

## Work log
- 2026-08-08 plan: Implement the `integrate` agent path on top of task 0252's fast-path queue, per docs/design/workstream-integration.md §4 step 2.  1) Config (internal/config/config.go) - Add `AgentAttempts *int` (`to
…[truncated]
- 2026-08-08 context hints: 10 recorded with plan
- 2026-08-08 context hints: internal/session/workloop.go:487 realRunSession — the status-poll wait pattern; workloop.go:128-206 the injectable runSession seam precedent; internal/session/session.go:1574 start(cfg, autoRegister
…[truncated]
- 2026-08-08 preload: 5 file(s), ~41 KiB seeded into implementer context
- 2026-08-08 implementer report: Implemented task 0253’s bounded integrate-agent recovery path.  Changes: - Added `[integration].agent_attempts` with nil→1/zero-disable semantics, negative validation, effective-value helper, and 
…[truncated]
- 2026-08-08 review tier: high-powered — reviewers: sol
- 2026-08-08 review (sol): revise — The bounded recovery loop, integrate mode/tooling, session lifecycle, transcript preservation, configuration, docs, and daemon-side re-verification are otherwise implemented coherently, and the reques
…[truncated]
- 2026-08-08 revision: Addressed the review finding: - Conflict outcomes now carry `cfg.Verify` in `integrationOutcome.verifyCmd`, so real integrate-session prompts name the required verification command after conflict reso
…[truncated]
- 2026-08-08 review (sol): accept — The revision fixes the conflict-recovery prompt path by carrying the configured verify command in the conflict outcome, with an assertion covering that handoff. The integrate agent now receives the ba
…[truncated]
- 2026-08-08 usage: 9,568,774 tok (in 2,154,894, out 53,112, cache_r 12,144,851, cache_w 187,811) · cost n/a (unpriced)
  implementer: 7,877,916 tok (in 1,406,199, out 27,941, cache_r 6,443,776, cache_w 0) · cost n/a (unpriced)
  reviewer:sol: 1,672,265 tok (in 748,631, out 6,642, cache_r 916,992, cache_w 0) · cost n/a (unpriced)
  coordinator: 18,593 tok (in 64, out 18,529, cache_r 4,784,083, cache_w 187,811) · cost n/a (unpriced)
