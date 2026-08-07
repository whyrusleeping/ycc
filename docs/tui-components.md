# TUI component map

The Bubble Tea client remains one `internal/tui` package because its screens coordinate through a single shared `model`. The file boundaries below are the current component seams: **one domain per file; the shared `model` struct stays in `tui.go`; a new screen gets its own file owning its update handler, view, commands, and domain-specific helpers.** Matching tests belong in `<domain>_test.go`; common fixtures belong in `helpers_test.go`.

## Core and shared infrastructure

| File | Ownership and model state |
| --- | --- |
| `tui.go` | Shared `model`, top-level states, initialization, global Bubble Tea dispatch/rendering, connection/flash/quit guards, and mouse-fragment filtering; it routes to domain update methods but does not implement screens. |
| `msgs.go` | Command response and timer `tea.Msg` types used between RPC commands and update handlers; no mutable state. |
| `layout.go` | Shared text inputs, relayout, title/footer bars, and modal cards; touches terminal dimensions, viewport sizing, and input widgets. |
| `glyphs.go` | Actor, event-type, and verdict glyph/style selection; reads event metadata and theme styles. |
| `status.go` | Status bar, usage totals, token counts, and elapsed-time formatting; reads current session/model/task/loop state. |

## Screens and modals

| File | Ownership and model state |
| --- | --- |
| `picker.go` | Project discovery/addition and workspace picker update/view; owns picker cursor and project list behavior. |
| `menu.go` | Mode/model loading, session launch/stop, home-menu refreshes, git/spend/waiting-session summaries, onboarding probes, and menu rendering; touches menu, role, history, spend, and loop summary state. |
| `browse.go` | Reusable browser rows/navigation/card plus the browse target screen; touches browser cursor/rows and routes to history, backlog, plans, cost, and workstreams. |
| `history.go` | Session-history RPCs/list, read-only transcript modal, modal search/jumps, transcript rendering, and history timestamps; owns history browser and modal viewport/search state. |
| `backlog.go` | Backlog/task RPCs, filtering, selection, status/priority changes, editor launch, list/detail update and rendering; owns backlog browser, multiselect, status prompt, and task viewport. |
| `plans.go` | Plan list/detail RPCs, update, rendering, and detail viewport; owns plan browser/detail state. |
| `cost.go` | Usage RPCs, grouping/drill-down, subscription usage, table formatting, and cost view; owns cost generation/grouping/browser state. |
| `workstreams.go` | Workstream list/spawn/preview/merge/discard commands, panel update/view, merge overlay, and polling; owns workstream browser, confirmations, and merge viewport. |
| `workloop.go` | Daemon work-loop start/stop/poll commands and completed-loop digest; owns loop generation, loop info, digest rows, and digest navigation. |
| `commitdiff.go` | Commit-diff fetch/parse, file navigation, scrolling, and modal rendering; owns commit-diff files/cursor/viewport. |
| `capture.go` | Capture session open/update/input/stream commands and view; owns capture stream, input, log, and transient question state. |
| `settings.go` | Settings overlay navigation/activation, auto-expand and reviewer controls, role/thinking persistence; owns overlay cursors and role/model/thinking settings. |
| `modelbackends.go` | Model backend list, add/edit/duplicate/remove forms, discovery, OAuth/backend presets, validation, and RPC commands; owns all `mb*` form/list/confirmation state. |

## Live session and transcript rendering

| File | Ownership and model state |
| --- | --- |
| `session.go` | Session subscribe/reopen/input/interrupt/resume commands, stream update handling, notifications, and session/footer views; owns live stream, status, input, and notification state. |
| `transcript.go` | Event append/transient pipeline, event pairing/folding, visibility, selection mapping, render cache invalidation/rebuild, and live tails; owns events, row maps, expansion state, and warm render caches. |
| `eventrender.go` | Generic event blocks, headers/details, body formatting, markdown, usage/details, and wrapping; reads transcript/event state without owning navigation. |
| `toolcards.go` | Tool call/result cards, argument/result formatting, structured tool views, highlighting, and duration suffixes; reads paired transcript events. |
| `diffrender.go` | Diff detection, unified-diff generation, colorization, and numbered-output dimming; stateless rendering helpers. |
| `search.go` | Live transcript movement, search, type jumps, yank text, and search bar; owns live selection/search query/direction state. |
| `question.go` | Single-question picker and batch wizard commands/state/rendering; owns pending question, wizard answers, picker cursor, and question input focus. |

## Tests and unchanged support files

Focused tests mirror these production files. `helpers_test.go` owns the shared fake RPC client and Bubble Tea-driving fixtures; `tui_test.go` is reserved for genuinely cross-cutting behavior. `rebuild_bench_test.go` protects warm/cold transcript rendering. Existing `help.go`, `highlight.go`, `select.go`, `theme.go`, and `snapshot/` retain their specialized ownership.

## When to create a subpackage

A seam can graduate to a subpackage only after it can be expressed through a narrow, stable input/state/output API. It must not require exporting the broad mutable `model`, and package dependencies must point from `tui` into the component rather than cycle back. In practice this means first isolating domain state and messages behind an owned component type, then passing immutable view data and explicit commands/events across the boundary.
