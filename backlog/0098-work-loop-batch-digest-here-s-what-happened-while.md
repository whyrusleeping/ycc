---
id: "0098"
title: 'Work-loop batch digest: "here''s what happened while you were gone"'
status: done
priority: 2
created: "2026-07-01"
updated: "2026-07-02"
depends_on: []
spec_refs: []
---

## Description
## Description

The `work (loop)` unattended drain (§9) is the product's boldest promise ("don't stop, I'll review at the end"), but when a loop finishes the client only shows a one-line status string ("loop complete: no ready tasks remain"). The user then has to reconstruct what actually happened from `git log`, per-task work-logs, and the session browser. There is no single **end-of-batch review surface**.

Build a **batch digest** that accumulates across all the work sessions a single loop run drove and presents it when the loop ends (and ideally is re-openable afterward). It should answer, at a glance: what got done, what needs me, and what did it cost.

Digest contents (per loop run):
- Tasks **completed** (id, title, commit sha, one-line review verdict/tally) with a fast path to view each diff.
- Tasks **blocked** — surfaced prominently with the specific question/reason each is blocked on (ties into the blocked-task notification task and §18.7 semantics), and a fast path to answer + re-queue.
- Tasks **in_review** / left unfinished.
- **Follow-on tasks created** during the loop (split/follow-up, §10).
- **Cost/tokens** for the whole run (reuse the usage projection, §20) — per task and total.
- Duration / number of sessions.

Implementation notes:
- The loop driver is client-side (`internal/tui/tui.go`: `loopNext`, `applyLoopDecision`, `loopDecisionMsg`, `backlogFingerprint`). It already knows each session id it started and can diff the backlog fingerprint before/after; extend it to record a per-session summary as the loop runs and roll them up.
- Data sources already exist: session summaries (`internal/session/history.go` `SessionSummary`, event `Reduce`), the usage aggregator (`internal/usage`), backlog `List`/`Get`, and git commits. Prefer projecting from the event log over new bookkeeping.
- Render it in the shared list+detail "browser" modal surface (§18.6) so it reuses `browser`/`browserRow`/`browserCard` and is consistent with the backlog/session/cost browsers.

## Acceptance criteria
- Finishing a `work (loop)` run shows a batch digest (not just a status line) listing completed / blocked / in_review / created tasks with commit + verdict + cost.
- Blocked tasks in the digest show the reason and offer a way to unblock (answer + re-queue) or at least jump to the task.
- Total and per-task token/cost for the run are shown (cost "—" when unpriced, per §20.4).
- The digest is reachable again after dismissal (e.g. from the session/history browser) rather than being a one-shot.
- Build + tests green; a test covers the digest roll-up from a scripted multi-session loop.

## Acceptance criteria

## Plan

Build a client-side batch digest for the "work (loop)" driver in internal/tui/tui.go, accumulated as the loop runs and shown as a modal browser card when the loop ends, re-openable from the browse selector.

1. Data model (new types in tui.go):
   - `loopRunState` (accumulator on `model`, e.g. field `loopRun *loopRunState`): `startedAt time.Time`, `baseline map[string]*v1.BacklogTaskSummary` (id → summary at loop start), `sessions []loopSessRec`.
   - `loopSessRec`: session id, focus task id (last task_focus event), duration, token tally (sum of m.usageByModel over the session), plus parsed-from-events: commits `[]loopCommit{task, sha, message}` (commit_made) and verdicts `[]string` (review_submitted "verdict" field), and later-filled cost/priceStatus from GetUsage.
   - `loopDigest` (the finished artifact, field `loopDigest *loopDigest` kept after dismissal): outcome/status line, started/duration, sessions, and classified task lists: completed (baseline status != done → done), blocked, inReview, created (id absent from baseline) — each entry `digestTask{id, title, status, sha, verdictTally, tokens int64, cost, priceStatus, reason}`; run totals (tokens, cost, costStatus).
   - Keep `buildLoopDigest(run *loopRunState, final []*v1.BacklogTaskSummary, outcome string) *loopDigest` a pure function so it is unit-testable.

2. Accumulation wiring:
   - Extend `loopDecisionMsg` with `tasks []*v1.BacklogTaskSummary`; `loopNext` passes `resp.Msg.Tasks` through.
   - In `applyLoopDecision`: if `m.loopRun == nil`, initialize it from `msg.tasks` (baseline + startedAt) — this covers both entry paths (menu enter with loop toggle, and shift+tab mid-session) since loopNext runs before/between every session. On each of the three terminal branches (err / no next / stall), build the digest via `buildLoopDigest`, set `m.loopDigest`, open the digest modal (`m.digest = true, m.digestCursor = 0`), clear `m.loopRun`, keep returning to stateMenu, and batch cmds: fetchBacklog + fetchLoopUsage + blocked-reason fetches (below). Keep the existing status strings as the digest's outcome line.
   - In the `streamClosedMsg` handler, while `m.looping && m.loopRun != nil`, append a `loopSessRec` snapshot built from `m.sessionID`, `time.Since(m.sessionStart)`, `m.usageByModel`, and a scan of `m.evs` (task_focus / commit_made / review_submitted via the existing `dataField` helper) BEFORE calling `m.loopNext()`.
   - Starting a new loop run (menu enter path at ~line 1811, shift+tab toggle-on at ~line 1920) resets `m.loopRun = nil` so a fresh baseline is captured; `m.loopDigest` is only replaced when the next run finishes.

3. Cost (spec §20.4): new cmd `fetchLoopUsage` calling GetUsage with `group_by: ["session"]` → `loopUsageMsg{rows, err}`. Handler matches rows to the run's session ids, fills each loopSessRec/digestTask's tokens/cost/priceStatus, recomputes run totals, and re-renders. Render cost with the existing `costCellTUI` semantics ("—" unpriced, "*" partial). Tokens shown even when cost is unpriced.

4. Blocked reasons: for each blocked digest task issue a GetTask fetch → `digestTaskMsg{id, task}`; extract a one-line reason from the TaskDetail body — last "## Work log" bullet (prefer the last bullet mentioning "blocked", else the last bullet), via a small pure helper `blockedReasonFromBody(body string) string` (unit-tested). Show it dim/truncated on the blocked row.

5. UI:
   - New modal handled like the other browsers: `if m.digest { return m.updateDigest(msg) }` in Update's dispatch (next to m.backlog/m.cost, ~line 1561-1581) and `if m.digest { return m.digestView() }` in render (~line 4231).
   - `digestView` uses the shared `browser`/`browserRow`/`browserCard` component. Title " ycc — loop digest ". Non-task summary header row(s) (outcome, N sessions, duration, total tokens/cost) then grouped task rows with markers: `✔` completed (suffix: commit sha · verdict tally · tokens · cost), `⛔` blocked (suffix: reason), `◌` in_review/unfinished, `+` created during the run. Hint: "↑/↓ · enter open task · esc close".
   - `updateDigest`: up/down navigation, esc/q closes, enter on a task row jumps to that task: close digest, open the backlog browser detail (`m.backlog = true` + fetchTask for the id) — the "fast path to answer + re-queue / jump to the task". Rows that aren't tasks ignore enter.
   - Re-openable: add a "digest" target to `browseTargets` + case in `updateBrowse` that opens `m.digest` when `m.loopDigest != nil` (empty-state message "no completed loop run yet" otherwise, via browser.empty).

6. Tests (tui_test.go, following existing loop-test style with fakeClient):
   - Scripted multi-session roll-up: init loopRun via applyLoopDecision with a baseline task list; simulate session 1 (startedMsg, evMsg stream with task_focus/commit_made/review_submitted/model_turn-usage, streamClosedMsg) and session 2 that blocks a task; final applyLoopDecision with next:"" and a final task list containing a done task, a blocked task, and a new created task → assert digest classification, commit sha, verdict tally, per-session records, and that m.digest opened.
   - loopUsageMsg fills per-task + total cost; unpriced renders "—" (assert via digestView output).
   - blockedReasonFromBody unit test.
   - Digest re-openable: after closing, browse selector "digest" reopens it.
   - `go build ./... && go test ./...` green; run `gofmt`.

Scope note: full in-digest "answer + re-queue" UX is satisfied by the jump-to-task fast path (the backlog detail already shows the work log); anything richer is follow-on.

### Starting points
- internal/tui/tui.go: loopNext/applyLoopDecision/backlogFingerprint ~lines 543-596; streamClosedMsg handler ~1378; loop start paths ~1811 (menu enter) and ~1920 (shift+tab); modal dispatch ~1561-1581; render overlay dispatch ~4231
- internal/tui/tui.go: shared browser component ~2160-2360 (browser, browserRow, browserCard, browseTargets, updateBrowse, browseView); costCellTUI/commasTUI ~2407-2447
- internal/tui/tui.go: dataField(ev, key) parses v1.Event.DataJson; usage tally m.usageByModel updated in model_turn handling ~3896; event.Usage in internal/event
- events carry: task_focus{task}, commit_made{task, sha, message}, review_submitted{model, verdict, summary} (see internal/orchestrator/orchestrator.go ~680-690, ~744)
- proto: GetUsageRequest group_by supports "session"; UsageRow has session/total/cost/price_status; BacklogTaskSummary has id/title/status/priority
- internal/tui/tui_test.go: fakeClient ~931-1100 (ListBacklog, GetUsage, StartSession, StopSession fakes); existing loop tests ~2640-2775 show the applyLoopDecision/idle-stop test style

## Work log
- 2026-07-02 plan: Build a client-side batch digest for the "work (loop)" driver in internal/tui/tui.go, accumulated as the loop runs and shown as a modal browser card when the loop ends, re-openable from the browse sel
…[truncated]
- 2026-07-02 context hints: 6 recorded with plan
- 2026-07-02 context hints: internal/tui/tui.go: loopNext/applyLoopDecision/backlogFingerprint ~lines 543-596; streamClosedMsg handler ~1378; loop start paths ~1811 (menu enter) and ~1920 (shift+tab); modal dispatch ~1561-1581; 
…[truncated]
- 2026-07-02 implementer report: Implemented Task 0098: a work-loop batch digest ("here's what happened while you were gone") in `internal/tui/tui.go`, plus tests in `internal/tui/tui_test.go`. Scope stayed entirely within `internal/
…[truncated]
- 2026-07-02 review tier: single-opus — reviewers: Claude
- 2026-07-02 review (Claude): accept — The work-loop batch digest is implemented correctly and completely against the task's acceptance criteria. The loop driver now accumulates per-session summaries (commits, verdicts, tokens, focus task)
…[truncated]
- 2026-07-02 decision: accept — commit: tui: work-loop batch digest — per-run roll-up of completed/blocked/in_review/created tasks with commit, verdicts, tokens/cost; shown when a loop ends, re-openable from browse (0098); includes a ques
…[truncated]
- 2026-07-02 usage: 70,534 tok (in 252, out 70,282, cache_r 6,494,174, cache_w 257,457) · cost n/a (unpriced)
  implementer: 43,314 tok (in 138, out 43,176, cache_r 4,522,942, cache_w 102,872) · cost n/a (unpriced)
  coordinator: 19,207 tok (in 66, out 19,141, cache_r 1,327,287, cache_w 113,271) · cost n/a (unpriced)
  reviewer:Claude: 8,013 tok (in 48, out 7,965, cache_r 643,945, cache_w 41,314) · cost n/a (unpriced)
