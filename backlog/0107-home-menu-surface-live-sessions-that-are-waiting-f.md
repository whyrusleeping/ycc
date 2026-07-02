---
id: "0107"
title: 'Home menu: surface live sessions that are waiting for the user'
status: done
priority: 3
created: "2026-07-01"
updated: "2026-07-02"
depends_on: []
spec_refs:
    - 18.6 Session history browser & reopen
    - 9. Modes (the home menu)
---

## Description
## Description
The home menu shows blocked *tasks* ("⚠ N tasks blocked — waiting on you") but says nothing about *sessions*: one sitting on an unanswered ask_user, paused mid-steer, or still running keeps going invisibly (persistent daemon) unless the user remembers ctrl+r. Add an awareness line, e.g. `⚠ 1 session waiting for your answer — press s to open`, sourced from the durable session index / live manager (status running/paused + pending question flag). Selecting it should jump straight into that session (reopen/attach), not just the list.

## Acceptance criteria
- [ ] Menu shows a line when any live session has a pending question or is paused; count + shortest route in
- [ ] The key/entry attaches directly to that session (picker when several)
- [ ] No line when nothing needs the user
- [ ] Works against both one-shot and persistent daemons (ListSessionHistory/ListSessions already carry status)

## Acceptance criteria

## Plan

Goal: the home menu surfaces live sessions that need the user (pending ask_user question, or paused mid-steer) with a count + a one-key route in that attaches directly to the session (picker when several). No line when nothing needs the user.

1. Proto: add `bool waiting_input = 12;` to `SessionSummary` in proto/ycc/v1/ycc.proto (comment: live session blocked on an unanswered ask_user). Regenerate with `buf generate` (buf + protoc-gen-go/protoc-gen-connect-go are on PATH).

2. internal/session:
   - Add an exported accessor `func (s *Session) PendingQuestion() bool { return s.inter.pending() }` (same gate the reaper uses).
   - Add `Waiting bool` to `session.SessionSummary` (history.go). In `Manager.ListSessionHistory`, capture `pending` in the liveInfo snapshot under lock and set `Waiting` on the live overlay rows (both the override path and the "live with no disk snapshot" path). Disk-only rows stay false — a non-live session cannot hold a pending in-memory question.

3. internal/server: map `su.Waiting` → `WaitingInput` in the ListSessionHistory response.

4. internal/tui (tui.go):
   - New msg `waitingSessionsMsg{sessions []*v1.SessionSummary; err error}` + cmd `fetchWaitingSessions` that calls ListSessionHistory (same as fetchHistory) but delivers to a dedicated model field `waitingSessions` so the session-browser state is not clobbered. Filter to rows that need the user: `s.Live && (s.WaitingInput || s.Status == "paused")`. Ignore errors silently (this is an awareness line, not a screen — do not flash).
   - Freshness: fire `fetchWaitingSessions` from Init alongside fetchBacklog, and on every transition back to stateMenu (the `m.state = stateMenu` sites: session outcome return ~line 939, esc/q from history ~2134/2187, and the ~3727 return). Also arm a modest refresh tick (e.g. 5s) while in stateMenu so a question raised in a background session appears without a keypress; guard with a sequence int (like flashSeq) or by checking `m.state == stateMenu` on tick so stale ticks don't multiply.
   - menuView: below the blocked-tasks line, when len(waitingSessions) > 0 render a warn line, e.g. `⚠ 1 session waiting for your answer — press s to open` (use "waiting for you" when it's paused rather than a question; for N>1 `⚠ N sessions waiting for you — press s to pick`). Append ` · s open waiting session` to the footer when applicable. No line when the slice is empty.
   - updateMenu key "s": same guard as "w" (only when waiting sessions exist AND the prompt is empty so typing is never hijacked). Exactly one waiting session → `m.reopenSession(id)` directly (ResumeSession is idempotent for live sessions, so this attaches). Several → open the session browser (stateHistory) via fetchHistory with a new `historyWaitingOnly bool` filter (mirroring backlogBlockedOnly) so the list shows only the waiting sessions; clear the flag on esc/q/ctrl+r normal entry. Apply the filter in historyView/row selection consistently (filter m.history when the flag is set, or filter in the historyMsg handler).

5. Tests:
   - internal/session/history_test.go: live session with a pending question (set `s.inter.waiting = make(chan string, 1)` like reaper_test.go) → ListSessionHistory row has Waiting=true; without → false.
   - internal/server (if a test harness exists there) or rely on session-level test for the flag; mapping is trivial.
   - internal/tui/tui_test.go: follow TestBlockedIndicator — (a) no waiting sessions → menuView lacks the line; (b) one waiting session → line present with count and "press s"; (c) "s" with one waiting session issues the reopen cmd (assert state/cmd effect); (d) "s" with two waiting sessions enters stateHistory with the waiting-only filter; (e) "s" with text in the prompt types normally (no hijack).

6. Verify: go build ./... && go test ./... (and `buf generate` diff is committed).

### Starting points
- proto/ycc/v1/ycc.proto: SessionSummary (add waiting_input=12); regen: buf generate
- internal/session/history.go: SessionSummary struct + Manager.ListSessionHistory live overlay (liveInfo snapshot under lock)
- internal/session/interaction.go: (in *interaction) pending(); reaper_test.go:79 shows simulating a pending ask_user via s.inter.waiting
- internal/server/server.go:115 ListSessionHistory proto mapping
- internal/tui/tui.go: menuView ~5049 (blocked-tasks warn line pattern), updateMenu ~2232 ('w' key guard pattern at 2247), fetchHistory ~1001, reopenSession ~1025 (ResumeSession idempotent when live), backlogBlockedOnly ~229 as the filter precedent
- internal/tui/tui_test.go:1692 TestBlockedIndicator — pattern for menu-indicator tests

## Work log
- 2026-07-02 plan: Goal: the home menu surfaces live sessions that need the user (pending ask_user question, or paused mid-steer) with a count + a one-key route in that attaches directly to the session (picker when seve
…[truncated]
- 2026-07-02 context hints: 6 recorded with plan
- 2026-07-02 context hints: proto/ycc/v1/ycc.proto: SessionSummary (add waiting_input=12); regen: buf generate; internal/session/history.go: SessionSummary struct + Manager.ListSessionHistory live overlay (liveInfo snapshot unde
…[truncated]
- 2026-07-02 implementer report: Implemented Task 0107 — the home menu now surfaces live sessions that need the user.  ## Changes **Proto** (`proto/ycc/v1/ycc.proto` + regenerated `ycc.pb.go` via `buf generate`): - Added `bool wait
…[truncated]
- 2026-07-02 review tier: single-opus — reviewers: Claude
- 2026-07-02 review (Claude): accept — The change fully and correctly implements Task 0107. It adds the `waiting_input` proto field (regenerated cleanly), a nil-safe `PendingQuestion()` accessor, populates `Waiting` on both live-overlay pa
…[truncated]
- 2026-07-02 decision: accept — commit: tui: home menu surfaces live sessions waiting for the user; 's' attaches directly or opens a filtered picker (0107)
