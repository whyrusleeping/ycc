---
id: "0098"
title: 'Work-loop batch digest: "here''s what happened while you were gone"'
status: todo
priority: 2
created: "2026-07-01"
updated: "2026-07-01"
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

## Work log
