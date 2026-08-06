---
id: "0267"
title: 'TUI: migrate work (loop) to daemon-side loop RPCs; delete client driver'
status: done
priority: 2
created: "2026-07-15"
updated: "2026-08-06"
depends_on:
    - "0179"
spec_refs:
    - 9. Modes (the home menu)
    - "20.6"
    - docs/design/ios-client.md#9. Daemon-side work loop (decision, prerequisite for loop parity)
---

## Description
Migrate the TUI's `work (loop)` from its client-side driver to the daemon-side loop RPCs added in 0179 (`StartWorkLoop`/`StopWorkLoop`/`GetWorkLoop`), deleting the client loop machinery while preserving the UX.

Context: 0179 moves the loop driver into the daemon (engine + RPCs + no-progress guard + budget caps + digest). This task retires the now-redundant TUI driver and points the UI at the daemon.

## Scope
- Delete the client-side loop driver in `internal/tui/tui.go`: `loopNext`, `applyLoopDecision`, `loopDecisionMsg`, the `loopStopping`/`loopStarted`/`loopPrevFP`/`loopRun` bookkeeping, `snapshotLoopSession`, `buildLoopDigest`/`applyUsage`/`fetchLoopUsage`/`notifyLoopDigest` and the client-side no-progress/budget-cap enforcement — replaced by daemon RPC calls.
- Wire the home-menu **tab** toggle to `StartWorkLoop` and in-session **shift+tab** toggle to `StartWorkLoop`/`StopWorkLoop` (graceful). Observe loop state by polling `GetWorkLoop` (and Subscribe to the current session id it reports) so the `⟳ loop` indicator and advancing-through-sessions UX are preserved.
- Render the end-of-batch **digest** from the daemon-provided `WorkLoopInfo` (completed/blocked/in_review/created tasks, per-session tokens/cost) instead of building it client-side; keep the re-openable digest view.
- Reconnect behaviour: on entering a project with a running loop, GetWorkLoop lets the TUI re-attach and gracefully stop it.
- Keep `GetBudget` usage only where still needed for display; loop-cap enforcement is now daemon-side.

## Acceptance criteria
- TUI `work (loop)` UX preserved (tab/shift+tab toggles, `⟳ loop` indicator, end-of-batch digest view) driven entirely by the new RPCs.
- The client-side loop driver code is removed; no client-side no-progress/budget-cap/digest logic remains.
- A loop started from the TUI keeps running across a TUI restart; reconnecting re-attaches and can gracefully stop it.
- TUI tests updated/added for the RPC-driven flow; `go build ./...` and `go test ./...` green.

## Plan

Retire the TUI's client-side work-loop driver and drive `work (loop)` entirely from the daemon RPCs added in task 0179 (`StartWorkLoop` / `StopWorkLoop` / `GetWorkLoop`), preserving the UX. All work is in `internal/tui` (+ tests); no proto/daemon changes.

### 1. Delete the client driver
Remove from `internal/tui/tui.go` (and their tests in `tui_test.go`): `loopNext`, `applyLoopDecision`, `loopDecisionMsg`, `loopRunState`, `loopSessRec` accumulation, `snapshotLoopSession`, `buildLoopDigest`, `applyUsage`, `fetchLoopUsage`/`loopUsageMsg`, `notifyLoopDigest`, `fetchDigestTask`/`digestTaskMsg`, `backlogFingerprint`, `topReadyTask`, and the model fields `loopStarted`, `loopPrevFP`, `loopRun`, `loopSessBreach`, `loopCostCap`, `loopTokenCap` plus `fetchBudget`/`budgetCapsMsg` **if** they are used only for loop caps (keep them if any display still needs GetBudget). Keep `mergeCostStatus` / `blockedReasonFromBody` only if something else still uses them; otherwise delete with their tests. No client-side no-progress guard, budget-cap enforcement, digest accumulation, or digest `Notify` push may remain — the daemon owns all of it (it already pushes the `digest` notification, so keeping the client push would double-notify).

### 2. Daemon-driven loop state
- New model fields: `loopInfo *v1.WorkLoopInfo` (last snapshot), `loopSeq int` (tick guard), `loopArmed bool` (deferred start, see §3). `looping` stays as "a daemon loop is running for this project" and keeps driving the `⟳ loop` indicator, footer hints, `sessionFinished()` exclusion, and the `maybeNotify` idle-bell suppression.
- New commands/messages: `startWorkLoop()`, `stopWorkLoop()`, `fetchWorkLoop()` → `workLoopMsg{info, err}`; `loopTickMsg{seq}` polling every ~2s, seq-guarded exactly like the existing `wsTickMsg` pattern (tui.go:1948). Polling runs while `m.looping` (armed by a start/attach) and stops once a snapshot reports `finished`; a poll error flashes the status and keeps retrying (a transient RPC failure must not silently drop the loop).
- `workLoopMsg` handling:
  - `running` / `stopping` → `m.looping = true`, store the snapshot; when `info.CurrentSessionId` is non-empty and differs from `m.sessionID`, attach to it via the existing `reopenSession(id)` path (`Manager.Reopen` is a no-op for a live session, so this is a safe attach that reuses `startedMsg` → subscribe). Between sessions (`current_session_id` empty) leave the transcript in place with a "waiting for the next task" status.
  - `finished` (when we were looping) → `m.looping = false`, build the digest from the snapshot (§4), open the digest modal, set `m.status` to `info.Outcome`, return to the menu and `refreshMenu()`.
  - Unknown/absent loop → `m.looping = false`, stop polling.
- `streamClosedMsg` while looping no longer decides anything: just set an informative status ("loop: session ended — waiting for the daemon to pick the next task") and let the poll drive the next attach. Keep the fresh-event-channel reset in `startedMsg` (it exists precisely so a loop's next session doesn't reuse a closed channel).

### 3. Key bindings (UX preserved)
- **Home menu, `tab` toggle + `enter` on the work entry**: call `StartWorkLoop(project)` instead of the client driver. On `FailedPrecondition` (a loop is already running for that workspace — e.g. started from the phone or a previous TUI run) do NOT error out: fetch `GetWorkLoop` and attach to the running loop, with a status line saying so. Success → `looping = true`, start polling, status "loop started".
- **Session `shift+tab`**: if a loop is running/stopping → `StopWorkLoop` (graceful) with status "loop stopping: current task finishes, next not picked". If no loop is running and the session is a work session → *arm* the loop rather than starting a competing daemon session immediately: set `loopArmed`, status "loop armed: starts when this session finishes", and issue `StartWorkLoop` when this session ends (`streamClosedMsg`, or the idle transition already handled by `loopStopping`). Pressing `shift+tab` again disarms. This preserves today's "roll this session into a loop" meaning without ever running the user's manual session and a daemon loop session concurrently — call it out in a comment.
- Footer/indicator: keep `⟳ loop`, add a distinct rendering for `stopping` (e.g. `⟳ loop (stopping)`) and for `loopArmed`. Update the work-session footer hints accordingly.

### 4. Digest from the daemon
- New `digestFromWorkLoop(info *v1.WorkLoopInfo) *loopDigest`: the existing `digestTask` / `loopDigest` structs already mirror `WorkLoopDigestTask` / `WorkLoopInfo` field for field (id/title/status/sha/verdict tally/tokens/cost/price status/reason, plus totals + cost status + outcome + started_at). Map sessions into the surviving `loopSessRec` fields (id/focus/tokens/cost/priceStatus) and drop struct fields the daemon can't supply (per-session `dur`, `commits`, `verdicts`) along with any rendering that used them — or derive loop duration from `started_at`. Blocked reasons arrive from the daemon, so no follow-up task fetch is needed.
- Keep the digest browser surface (`digestRows`, `digestView`, `updateDigest`, Enter-to-open-task) unchanged apart from the removed fields.
- The home-menu **`digest`** browse entry now fetches `GetWorkLoop` and renders that snapshot's digest, so the last batch digest survives a TUI restart (it was previously in-memory only); its empty state stays when the daemon reports no loop.

### 5. Tests
Delete the obsolete driver tests (`TestLoopDecision*`, no-progress / cap / breach tests, digest roll-up + pricing tests, `topReadyTask` / `backlogFingerprint` tests) and add, with `fakeClient` gaining `StartWorkLoop`/`StopWorkLoop`/`GetWorkLoop` stubs (it embeds the interface, so this is additive):
- `digestFromWorkLoop` mapping: sections, blocked reason, per-task tokens/cost/price status, totals/cost status/outcome.
- `workLoopMsg` handling: `running` with a new `current_session_id` attaches (a command is returned) and does not re-attach when the id is unchanged; `finished` closes the loop, opens the digest, clears `looping`, and sets the outcome status; an error keeps `looping` and polls again.
- Menu enter on `work (loop)` calls `StartWorkLoop`; a `FailedPrecondition` response attaches to the existing loop instead of erroring.
- `shift+tab` in a loop session calls `StopWorkLoop`; `shift+tab` in a plain work session arms (and a second press disarms), and the armed start fires on `streamClosedMsg`.
- Poll tick is seq-guarded (a stale tick is ignored).

### 6. Docs
Only if stale: check `spec.md` §9/§20.6 and `docs/remote-api.md` for lingering "the TUI drives the loop / client-driven cap" wording left over from before 0179 and correct it in a sentence or two. No design-doc rewrites.

### Verify
`gofmt`, `go build ./...`, `go test ./internal/tui/...`, then `go test ./...` (memory notes pre-existing flakes in `internal/session TestReconcileWorkstreams`, `internal/setup TestConfigPath`, `internal/tools TestBackgroundBashWaitReturnsExitAndOutput` — confirm against HEAD before attributing). Confirm with `rg` that none of the deleted driver symbols remain.

## Work log
- 2026-08-06 renumbered 0195 → 0267 (duplicate id detected, 0195 kept by another task)
- 2026-08-06 plan: Retire the TUI's client-side work-loop driver and drive `work (loop)` entirely from the daemon RPCs added in task 0179 (`StartWorkLoop` / `StopWorkLoop` / `GetWorkLoop`), preserving the UX. All work i
…[truncated]
- 2026-08-06 implementer report: Implemented task 0267.  Changes: - Removed the TUI’s client-side loop driver, backlog fingerprint/ready-task selection, local no-progress and budget-cap enforcement, usage roll-up, blocked-task foll
…[truncated]
- 2026-08-06 review tier: high-powered — reviewers: sol
- 2026-08-06 review (sol): revise — The migration removes the client-side driver and the build/tests pass, but the new asynchronous RPC state handling has lifecycle races that can lose attachment to a daemon loop or fail to surface an i
…[truncated]
- 2026-08-06 revision: Addressed all three review findings.  Changes: - Added `initiated` metadata to user-triggered work-loop messages (start, FailedPrecondition attach fallback, stop, and digest fetch). A `finished` user 
…[truncated]
- 2026-08-06 review (sol): revise — The previous lifecycle-race findings are substantially addressed and all builds/tests pass, but the fix conflates an explicit digest fetch with a loop start/stop transition, causing the re-openable di
…[truncated]
- 2026-08-06 revision: Fixed the remaining digest lifecycle issue.  Changes: - Separated historical digest reads from start/stop lifecycle responses in `workLoopMsg` semantics and comments. - `fetchWorkLoopDigest` now perfo
…[truncated]
- 2026-08-06 review (sol): accept — The revision correctly separates historical digest reads from loop lifecycle transitions, preserving the digest as a modal over a live session. The prior immediate-finish and stale-poll races remain c
…[truncated]
- 2026-08-06 decision: accept — commit: tui: drive work (loop) from the daemon RPCs; delete the client driver (task 0267)  The TUI no longer runs its own loop: StartWorkLoop/StopWorkLoop/GetWorkLoop own iteration, the no-progress guard, bud
…[truncated]
