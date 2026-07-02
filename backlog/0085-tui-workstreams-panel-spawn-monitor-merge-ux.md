---
id: "0085"
title: 'TUI: Workstreams panel + spawn/monitor/merge UX'
status: done
priority: 4
created: "2026-06-30"
updated: "2026-07-02"
depends_on:
    - "0084"
spec_refs:
    - Client UI (TUI)
    - RPC protocol (Connect)
---

## Description
## Context
Fifth step of the parallel-workstreams design (see `docs/design/parallel-workstreams.md` §8, §10.5). Add the TUI surface to spawn, monitor, and reconcile parallel workstreams.

## Scope
- Multi-select spawn from the backlog browser ("Run in parallel (N workstreams)").
- A Workstreams panel listing active workstreams with live status (running / idle / awaiting-review / conflict), focused task, branch, commit count; each row drills into the existing session view (reused unchanged).
- A merge/accept overlay showing trial-merge result (clean / conflicted paths) + integrated diff; merge / discard actions.

## Acceptance criteria
- [ ] User can spawn N workstreams from the backlog browser.
- [ ] Workstreams list shows live per-workstream status and drills into the session transcript.
- [ ] Merge overlay shows trial-merge result + integrated diff and supports merge/discard.
- [ ] Conflicts are visually distinct (a clear row state), never a silent failure.
- [ ] build/vet/test pass.

## Acceptance criteria

## Plan

Add the TUI surface for parallel workstreams (design docs/design/parallel-workstreams.md §8), building on the daemon RPCs landed in 0081–0084 (SpawnWorkstream / ListWorkstreams / PreviewMerge / MergeWorkstream / DiscardWorkstream).

1) Daemon enrichment for live rows (small, targeted):
   - Extend proto `WorkstreamInfo` with `int64 commit_count = 10` (commits on the branch since base_commit) and `string session_status = 11` (running | idle | paused | stopped | error; empty when unknown). Regenerate with buf (buf.gen.yaml exists; protoc-gen-go/-connect-go on PATH).
   - git: add `Repo.CountCommits(base, head string) (int, error)` using `git rev-list --count base..head`; unit test alongside existing worktree tests.
   - server.ListWorkstreams: for each entry, populate commit_count (open the project's primary repo once per project; best-effort, 0 on error, only for non-terminal workstreams) and session_status via a small manager helper (live session status; else "stopped"/""). Keep Spawn/other paths unchanged. Extend internal/server/workstream_rpc_test.go.

2) TUI: multi-select spawn from the backlog browser (internal/tui/tui.go):
   - In the backlog browser LIST view: `space` toggles selection on tasks with status "todo" (marker like `[x]` before the row; selection stored in a set on the model, cleared on browser close). Footer shows the count and the spawn key.
   - `P` (capital P; avoids clashing with existing browser keys — verify) = "Run in parallel (N workstreams)": one SpawnWorkstream per selected task with Project=m.project, TaskId, Prompt like "Work on backlog task <id>: <title>" and default interaction level. Requires m.project non-empty — otherwise a backlogNotice explaining workstreams need a registered project (daemon-registry mode). On success: notice "spawned N workstreams" and open the Workstreams panel.

3) TUI: Workstreams panel (new modal browser, same pattern as backlog/plans/cost browsers):
   - Reachable from the browse selector (add a "workstreams" route to browseTargets) and after spawn.
   - Rows: id, task id, short branch, commit count, and a live status cell. Status precedence: registry terminal status (merged/discarded/stale) → that; locally-known conflict (last preview/merge returned conflicts) → "conflict" rendered loud/distinct (error style); locally-known needs_accept → "awaiting-review"; else session_status ("running" / "idle"). Keep a per-workstream local overlay map (id → conflict/awaiting-review) fed by PreviewMerge/MergeWorkstream responses.
   - Live refresh: modest tick re-calling ListWorkstreams while the panel is open (reuse the menuRefresh/waitingSeq tick pattern to avoid timer multiplication).
   - Enter on a row: drill into the session — reuse the existing attach path the session browser uses (ResumeSession is idempotent for live sessions → stateSession + Subscribe(session_id)); the session view itself is unchanged.

4) TUI: merge/accept overlay:
   - `m` on a row → PreviewMerge: overlay (viewport, like backlog detail) shows either "clean" + the integrated diff, or the conflicted paths styled distinctly. When clean: `enter`/`y` calls MergeWorkstream(accept=true); on merged, show the merge commit in a notice and refresh the list. On conflicts: mark the row conflict, never silently drop it.
   - `d` on a row → two-step confirm (footer prompt) → DiscardWorkstream → refresh.
   - RPC errors surface in the panel footer notice (same as backlogNotice pattern).

5) Tests:
   - git.CountCommits unit test.
   - server: ListWorkstreams returns commit_count/session_status (extend workstream_rpc_test.go).
   - tui: extend fakeClient (tui_test.go) with the five workstream RPCs; tests for space-toggle multi-select + spawn (N calls with right task ids/project), panel rendering incl. a visually distinct conflict row, merge overlay flow (preview → accept → merged) and discard confirm.
   - `go build ./... && go vet ./... && go test ./...` pass.

Out of scope (keep tight): "Merge all clean" bulk action, home-menu spawn entry point, remote sync. Any follow-ups discovered get new backlog tasks.

### Starting points
- internal/tui/tui.go — single-file bubbletea model; modal browsers: backlog (ctrl+b, ~line 251), browse selector browseTargets (~line 2984), menuRefresh tick pattern (~line 1116)
- internal/server/workstream.go — toWorkstreamInfo + 5 RPC handlers; s.mgr is *session.Manager
- internal/session/session.go SpawnWorkstream (~line 1030) requires Project name; m.projects.Resolve
- internal/session/workstream_merge.go — MergePreview/MergeOutcome shapes, PreviewWorkstreamMerge, MergeWorkstream(id, accept)
- internal/git/worktree.go — Repo worktree primitives; add CountCommits here
- proto/ycc/v1/ycc.proto §WorkstreamInfo (~line 409); regen: buf generate
- internal/tui/tui_test.go fakeClient (~line 929) — in-memory SessionServiceClient to extend
- TUI attach-to-live-session path: tui.go ~line 2429 ('ResumeSession is idempotent … reopens/attaches')
- internal/server/workstream_rpc_test.go — existing RPC test scaffolding

## Work log
- 2026-07-02 plan: Add the TUI surface for parallel workstreams (design docs/design/parallel-workstreams.md §8), building on the daemon RPCs landed in 0081–0084 (SpawnWorkstream / ListWorkstreams / PreviewMerge / Mer
…[truncated]
- 2026-07-02 context hints: 9 recorded with plan
- 2026-07-02 context hints: internal/tui/tui.go — single-file bubbletea model; modal browsers: backlog (ctrl+b, state ~line 251), browse selector browseTargets (~line 2984), menuRefresh/waitingSeq tick pattern (~line 1116); in
…[truncated]
- 2026-07-02 implementer report: Implemented Task 0085: TUI Workstreams panel + spawn/monitor/merge UX. All acceptance criteria met; `go build ./...`, `go vet ./...`, and `go test ./...` all pass.  ## Backend enrichment (for live row
…[truncated]
- 2026-07-02 review tier: single-opus — reviewers: Claude
- 2026-07-02 review (Claude): accept — The change fully implements Task 0085: multi-select spawn from the backlog browser (space/P with project guard), a modal Workstreams panel with live per-workstream status (correct precedence, visually
…[truncated]
- 2026-07-02 decision: accept — commit: tui: workstreams panel — multi-select parallel spawn from the backlog browser, live status rows (running/idle/awaiting-review/conflict + commit count via enriched ListWorkstreams), session drill-in,
…[truncated]
- 2026-07-02 usage: 66,511 tok (in 378, out 66,133, cache_r 16,403,758, cache_w 254,775) · cost n/a (unpriced)
  implementer: 51,675 tok (in 314, out 51,361, cache_r 15,400,098, cache_w 160,676) · cost n/a (unpriced)
  reviewer:Claude: 7,474 tok (in 40, out 7,434, cache_r 450,605, cache_w 36,725) · cost n/a (unpriced)
  coordinator: 7,362 tok (in 24, out 7,338, cache_r 553,055, cache_w 57,374) · cost n/a (unpriced)
