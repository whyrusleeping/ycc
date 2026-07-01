---
id: "0104"
title: 'TUI: transient RPC errors must not replace the UI with a fatal error screen'
status: done
priority: 2
created: "2026-07-01"
updated: "2026-07-01"
depends_on: []
spec_refs:
    - 18. Client UI (TUI)
---

## Description
## Description
Nearly every TUI command (`sendInput`, `interrupt`, `resume`, `answerQuestion`, `setLevel`, `setThinking`, `setRoleConfig`, fetches…) returns `errMsg{err}` on failure, and `render()` short-circuits to a full-screen `error: … (ctrl+c to quit)`. `m.err` is never cleared, so a single transient RPC hiccup permanently nukes a live session view whose event stream is fine.

Reserve the fatal screen for genuinely unrecoverable startup failures (can't reach the daemon). Everything else should surface as an inline, dismissible status-bar/toast error while the session view keeps rendering.

## Acceptance criteria
- [ ] A failed `SendInput`/`Interrupt`/`Answer*`/settings RPC shows an inline error (status bar or toast) and the session view stays usable
- [ ] Inline errors clear on the next successful action/event (or a key/timeout)
- [ ] Fatal screen retained only for unrecoverable cases; if kept, it offers a retry/back affordance rather than only ctrl+c
- [ ] Tests cover: transient send failure → view still renders events and accepts input

## Acceptance criteria

## Plan

Goal: transient RPC failures surface as an inline, self-clearing status-bar error while the UI keeps rendering; the full-screen fatal view is reserved for genuinely unrecoverable startup failures and gains a retry/back affordance.

1. Split error severities in internal/tui/tui.go:
   - Keep `errMsg{err}` but make its Update handler set a NEW transient field (e.g. `m.flashErr string` + a monotonically increasing `m.flashSeq int`) instead of `m.err`. Arm a `tea.Tick`-based clear command (~5s) carrying the seq so a stale timer never clears a newer error.
   - Add a `fatalMsg{err}` (or equivalent) that sets `m.err`, used ONLY by startup-critical paths where there is no UI worth keeping: the Init-time fetches (`fetchModes`, `fetchProjects`) failing before any screen has data (i.e. daemon unreachable). All other producers — sendInput, interrupt, resume, answerQuestion/answerQuestions, setLevel, setThinking, setRoleConfig, stopSession, startSession, fetchHistory/fetchTranscript/fetchBacklog/fetchTask/fetchPlans/fetchPlan/fetchUsage etc. — stay `errMsg` and therefore become transient. (For fetches that populate modal browsers, keep any existing modal-local error style like mbErr; just don't touch m.err.)
   - Nuance: `fetchModes` runs at Init but is also re-run later; make fatality depend on context — simplest robust rule: `errMsg` never fatal, and Init fetches return a distinct fatal message type only when the model has never successfully talked to the daemon (e.g. a `m.connected bool` set on the first successful RPC/message). Implementer may pick the cleanest variant that satisfies the acceptance criteria.
2. Clearing: transient error clears on (a) the ~5s timeout, (b) the next successful RPC-result message (startedMsg, answers applied, fetch results, etc. — a small helper `m.clearFlash()` called from those handlers is fine), and optionally on keypress. Multiple failures just overwrite the flash.
3. Rendering:
   - `render()` short-circuits to the fatal screen only when `m.err != nil`; fatal screen text now offers a retry ("r" re-runs the Init fetches) and quit, not just ctrl+c.
   - Show the transient error prominently: an errStyle segment in `statusBar()` at priority 0 (so it never gets dropped by the width-greedy fitter), and also surfaced on the menu/picker views (a one-line errStyle notice) so failures outside a session are visible too.
4. Tests (internal/tui/tui_test.go): drive Update with a live session (events present), inject `errMsg{...}` (as sendInput failure would produce), assert: view still renders the session (not the fatal screen), the status bar contains the error text, input remains focused/usable; then deliver the clear tick (or a successful msg) and assert the flash is gone. Add a test that the fatal path still renders the fatal screen for the startup-failure message.
5. Verify: go build ./... && go test ./... green; go vet clean.

### Starting points
- internal/tui/tui.go:353 `type errMsg struct{ err error }` — ~20 producers, all become transient
- internal/tui/tui.go:1227-1229 errMsg handler sets m.err (never cleared)
- internal/tui/tui.go:4071-4074 render() short-circuits to fatal screen on m.err
- internal/tui/tui.go:4344 statusBar() — greedy priority-based segment fitter; add flash at prio 0
- internal/tui/tui.go:404-409 Init() fetches: fetchModes/fetchModels/fetchBacklog(+fetchProjects) — the only startup-critical path
- internal/tui/tui.go:236-237 mbErr/mbInfo — existing modal-local inline error pattern to imitate
- internal/tui/tui_test.go — existing Update/View test patterns

## Work log
- 2026-07-01 plan: Goal: transient RPC failures surface as an inline, self-clearing status-bar error while the UI keeps rendering; the full-screen fatal view is reserved for genuinely unrecoverable startup failures and 
…[truncated]
- 2026-07-01 context hints: 7 recorded with plan
- 2026-07-01 context hints: internal/tui/tui.go:353 `type errMsg struct{ err error }` — ~20 producers, all should become transient; internal/tui/tui.go:1227-1229 errMsg handler sets m.err (never cleared); internal/tui/tui.go:4
…[truncated]
- 2026-07-01 implementer report: Implemented task 0104: transient RPC errors no longer replace the TUI with a fatal error screen.  ## Changes (internal/tui/tui.go) - **Split error severities.** Added model fields `flashErr string`, `
…[truncated]
- 2026-07-01 review tier: single-opus — reviewers: Claude
- 2026-07-01 review (Claude): accept — The change cleanly implements task 0104. errMsg no longer sets the fatal m.err; instead the new flash()/clearFlash()/rpcOK()/markConnected() helpers route RPC failures to a transient, self-clearing st
…[truncated]
- 2026-07-01 decision: accept — commit: tui: transient RPC errors surface as an inline self-clearing flash instead of the fatal screen; fatal screen reserved for pre-connection startup failures and gains an 'r' retry (0104)
- 2026-07-01 usage: 32,981 tok (in 162, out 32,819, cache_r 2,470,618, cache_w 132,231) · cost n/a (unpriced)
  implementer: 20,016 tok (in 116, out 19,900, cache_r 1,955,415, cache_w 56,527) · cost n/a (unpriced)
  coordinator: 6,872 tok (in 24, out 6,848, cache_r 385,315, cache_w 54,365) · cost n/a (unpriced)
  reviewer:Claude: 6,093 tok (in 22, out 6,071, cache_r 129,888, cache_w 21,339) · cost n/a (unpriced)
