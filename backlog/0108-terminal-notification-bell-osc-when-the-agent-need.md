---
id: "0108"
title: Terminal notification (bell/OSC) when the agent needs the user or finishes
status: done
priority: 3
created: "2026-07-01"
updated: "2026-07-02"
depends_on: []
spec_refs:
    - 18.2 Settings overlay
    - 11. Interaction levels
---

## Description
## Description
When the agent asks a question, pauses, or goes idle while the user is in another tmux window/desktop, nothing signals it — the "walk away and come back" workflow (the whole point of a daemon-first harness) depends on polling the screen. Emit a terminal bell (BEL) and/or OSC 9 / OSC 777 desktop notification on `question_asked`, `interrupted`, `session_idle`, and `session_error`, gated behind a UI preference (default: bell on, desktop notification opt-in). Client-only — no daemon change.

## Acceptance criteria
- [ ] Bell emitted on question_asked / session_idle / session_error / interrupted when enabled
- [ ] Preference in the settings overlay UI-prefs section, persisted via clientconfig
- [ ] No bell for events replayed on reopen/transcript load (only genuinely new events)
- [ ] Optional: OSC 9 notification with the question text when supported

## Acceptance criteria

## Plan

Goal: emit a terminal bell (BEL) and optional OSC 9 desktop notification when a live session emits question_asked / session_idle / session_error / interrupted, gated by persisted client prefs, and never for replayed events. Client-only.

1. clientconfig (internal/clientconfig/clientconfig.go):
   - Add `NotifyBell bool` (default true) and `NotifyDesktop bool` (default false) to Prefs. Load() already starts from Default() and unmarshals over it, so an absent key keeps the default — no migration needed. Update Default() and add/extend a test asserting defaults survive a config file missing the new keys.

2. Notification emission (internal/tui/tui.go):
   - Add a small helper, e.g. `func (m *model) maybeNotify(ev *v1.Event)`, called ONLY from the live-subscription path (the `evMsg` handler: both the initial msg.ev and the drain loop) — NOT from transcript-load paths — so browsing a replayed transcript never rings.
   - Replay suppression: record `m.notifyAfter = time.Now()` when a session view opens/subscribes (the `openedMsg` handler around line ~1900 where sessionStart is set). In maybeNotify, parse ev.Ts (RFC3339); notify only if the parse succeeds and the timestamp is >= notifyAfter. This suppresses the daemon's replay of the persisted log on reopen (those events predate the subscribe) while genuinely-new events pass.
   - Event set: question_asked, session_idle, session_error, interrupted. Judgement call: suppress the session_idle bell while `m.looping` is true (the loop auto-advances itself; ringing per task would be noise) but keep question_asked/session_error.
   - Output: write escape bytes via a package-level `var notifyOut io.Writer = os.Stdout` (swap-able in tests). Bell = "\a". Desktop = OSC 9: "\x1b]9;<text>\x07" where text is the question text (dataField(ev,"question"), truncated to ~120 chars, control chars stripped) for question_asked, else a short label like "ycc: session idle". Single Write call each so it cannot interleave mid-frame with the renderer (writes to the same *os.File are single syscalls). Only emit OSC 9 when NotifyDesktop is on; bell when NotifyBell is on.

3. Settings overlay:
   - Add two rows in the UI-prefs section after "auto-expand agent logs": "notify: terminal bell" and "notify: desktop (OSC 9)" showing on/off. Insert `ovNotifyBell`, `ovNotifyDesktop` into the ov* iota block (before ovInterrupt), keeping ovCount consistent. left/right (overlayAdjust) and enter (overlayActivate) toggle + clientconfig.Save, mirroring ovFollow/ovAutoExpand handling. Update the overlayView rows slice in matching order.

4. Tests (internal/tui):
   - Unit-test the notify decision + output: with a bytes.Buffer as notifyOut, an event with Ts after notifyAfter and type question_asked produces "\a" when NotifyBell on; produces the OSC 9 sequence when NotifyDesktop on; an event with Ts before notifyAfter produces nothing; disabled prefs produce nothing; non-trigger types produce nothing; looping suppresses session_idle.
   - clientconfig test: defaults for the new fields when keys absent.

5. Verify: go build ./... && go test ./... ; check existing overlay snapshot/tests still pass (update snapshots if the overlay rows are snapshotted).

### Starting points
- internal/tui/tui.go:1943 evMsg handler + drain loop — the only live-event path; call maybeNotify here
- internal/tui/tui.go:1896-1910 openedMsg handler — set m.notifyAfter = time.Now() here
- internal/tui/tui.go:3683 ov* iota block, overlayAdjust/overlayActivate/overlayView — add the two pref rows
- internal/clientconfig/clientconfig.go — Prefs struct, Default(), Load() unmarshals over defaults
- dataField(ev, "question") / dataList helpers already exist for question_asked payloads
- proto Event has Ts string (RFC3339) and Seq int64

## Work log
- 2026-07-02 plan: Goal: emit a terminal bell (BEL) and optional OSC 9 desktop notification when a live session emits question_asked / session_idle / session_error / interrupted, gated by persisted client prefs, and nev
…[truncated]
- 2026-07-02 context hints: 6 recorded with plan
- 2026-07-02 context hints: internal/tui/tui.go:1943 evMsg handler + drain loop — the only live-event path; call maybeNotify there; internal/tui/tui.go:1896-1910 openedMsg handler — set m.notifyAfter = time.Now() here; inter
…[truncated]
- 2026-07-02 implementer report: Implemented Task 0108: terminal bell / OSC 9 notification when a live session needs the user or finishes. Client-only, no daemon changes.  ## Changes  **internal/clientconfig/clientconfig.go** - Added
…[truncated]
- 2026-07-02 review tier: single-opus — reviewers: Claude
- 2026-07-02 review (Claude): accept — The change fully and correctly implements Task 0108. clientconfig gains NotifyBell (default true) and NotifyDesktop (default false), loaded over defaults so legacy configs keep the bell on. tui.go add
…[truncated]
- 2026-07-02 revision: Addressed all three reviewer polish items in internal/tui/tui.go and extended tests.  1. **Auto-answered questions suppressed**: `maybeNotify` now returns early for `question_asked` events whose paylo
…[truncated]
- 2026-07-02 review (Claude): accept — The revision addresses all previous nits correctly. sanitizeNotify now truncates on rune boundaries (verified by TestSanitizeNotifyRuneBoundary), notifyText falls back to the first prompt of a batch a
…[truncated]
- 2026-07-02 decision: accept — commit: tui: terminal bell + optional OSC 9 desktop notification when the agent needs the user or goes idle/errors (0108)
