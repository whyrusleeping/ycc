---
id: "0498"
title: 'Watches: daemon-scheduled recurring check sessions with pass-forward notes'
status: done
priority: 2
created: "2026-11-02"
updated: "2026-11-03"
depends_on: []
spec_refs:
    - docs/design/watches.md
    - spec.md#11. Questions, unattended work, and confirmation
    - spec.md#14.2 Health monitoring
---

## Description
Replace model-built sleep/wait monitoring loops (wheeler s_7aac4d11912b6830: question-blocked gaps up to 12h, +10k context per check, lost on restart) with daemon-owned watches. Design: docs/design/watches.md.

User decisions (2026-11-02): a fresh session per run with notes passed forward; `ok` reports silent (may revisit); low-risk actions allowed without asking, provided the watch doesn't interfere with other agents' work.

Overlaps 0391 (experiment check-ins); client surfaces (TUI/web/iOS watch list and report timeline) can follow as separate tasks.

## Acceptance criteria
- A watch definition (brief, interval, model, optional probe/expiry/pre-authorized actions) persists per project and survives daemon restart; pause/resume/run-now/edit/delete via RPC.
- Due times don't drift; runs never overlap (a run still active at the next due time causes that run to be skipped and recorded); missed runs after downtime collapse into one catch-up run that states the gap.
- A passing probe records `ok` without a model call.
- Each run is a fresh session seeded with brief, notes, previous report summary, pending questions and the project activity snapshot; it ends with a `report` (status, summary, actions, replacement notes). A missing report records a failed run.
- A watch question parks only its own run; later runs continue on schedule and don't duplicate it; the answer resumes the parked run and is appended to the notes by the daemon.
- `alert` pushes; `ok` and `warn` don't push.
- Runs take only low-risk or pre-authorized actions, list each with how to undo it, report at least `warn` when they act, and ask instead of acting on anything attributable to a live session or `in_progress` task.
- `schedule_watch` creates a watch from chat only after user confirmation of the definition.
- Proto changes regenerated for Go, Swift and web.

## Work log
- 2026-11-03: Implemented. Daemon scheduler and run lifecycle in internal/session/watch.go; `watch` mode with `watch_report`, plus `schedule_watch` in attended chat on a persistent daemon (internal/orchestrator/watch.go); seven SessionService RPCs (internal/server/watch.go) with Go/Swift/web regen; spec §9.2. One focused review (sol) found probe shutdown hang, unbounded probe output, report/history crash windows, lost decisions after restart, watch runs missing from the activity snapshot, optional undo — all fixed with tests. Tested with fake runs plus a stubbed-model end-to-end run; no live-model run. The low-risk action policy is prompt-enforced, not sandboxed. internal/web/dist was not rebuilt because another session's web work is uncommitted, so TestBundleFresh fails until the next scripts/web-build.sh. Client surfaces: 0499 (web), 0500 (iOS).
