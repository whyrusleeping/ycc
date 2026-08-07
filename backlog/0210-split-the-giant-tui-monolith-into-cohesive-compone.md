---
id: "0210"
title: Split the giant TUI monolith into cohesive components
status: done
priority: 3
created: "2026-07-15"
updated: "2026-08-07"
depends_on: []
spec_refs:
    - Client UI (TUI)
    - Package layout
---

## Description
`internal/tui/tui.go` is roughly 10.9k lines and `tui_test.go` roughly 6.7k lines. Backend setup, settings, history, workstreams, backlog, cost, diff rendering, session input, and home-menu behavior share one large implementation/test file, increasing merge conflicts and making hidden state coupling difficult to reason about.

Refactor incrementally into cohesive files and, where boundaries are stable, subpackages/components. Preserve behavior and Bubble Tea message flow; this is structural work, not a visual redesign. Good first seams include backend/setup forms, settings, browser/history, backlog, workstreams, commit diff, selection/mouse, and session rendering.

## Acceptance criteria
- [x] `tui.go` is reduced to the core model/state machine and top-level dispatch rather than containing every screen implementation.
- [x] Major modal/screen domains live in clearly named files or internal subpackages with narrow interfaces and ownership of their state/messages/rendering.
- [x] `tui_test.go` is split into corresponding focused test files with shared helpers isolated cleanly.
- [x] Package dependency direction avoids cycles and does not expose broad mutable model internals merely to move code.
- [x] Existing keyboard, mouse, render-cache, session-stream, settings, backlog, history, workstream, and E2E behavior remains unchanged.
- [x] Warm/cold transcript render benchmarks do not materially regress.
- [x] `go test ./...`, `go test -race ./internal/tui/...`, and the E2E suite pass.
- [x] A short package/file map is added to developer documentation so future features land in the appropriate component.

## Plan

Goal: turn `internal/tui/tui.go` (10.7k lines) and `tui_test.go` (6.6k lines) into a set of cohesive, clearly-named files in the SAME `package tui`, with zero behavior change, plus a developer-facing file map.

Why files, not subpackages: every screen mutates the one shared `model` struct. Promoting screens to subpackages today would require exporting broad mutable model internals — explicitly ruled out by the task's acceptance criteria. So: split by domain into files now, keep the package boundary; the doc records the ownership rules so future features land in the right file and stable seams can later graduate to subpackages.

## Phase A — split tui.go (verbatim moves only)

Target files (all `package tui`, all in internal/tui/). Each gets a short header comment saying what it owns:

- `tui.go` (core, target well under ~1.5k lines): package doc, `state` + consts, the `model` struct (keep the field comments intact), `Run`, `initialModel`, `Init`, the top-level `Update` dispatch, `View`/`render`, `flash`/`clearFlash`/`noteFlash`, quit-guard (`quitGuardActive`/`confirmQuit`), `markConnected`/`rpcOK`, `dropMouseFragment` + `mouseFragmentRe`.
- `msgs.go`: every tea.Msg type (`modesMsg`…`mbDiscoverMsg`, `menuEntry`).
- `layout.go`: `newSessionInput`, `newChatInput`, `styleChatInput`, `restyleInputs`, `framedInput`, `indentBlock`, `inputRow`, `inputViewHeight`, `relayout`, `footerStackHeight`, `titleBar`, `footerBar`, `clampCardLines`, `modalCard`.
- `picker.go`: `fetchProjects`, `addProject`, `updatePicker`, `pickerScreenView`.
- `menu.go`: `fetchModes`/`fetchModels`, `startSession`, `isWorkEntry`, `stopSession`, `sessionFinished`, `updateMenu`, `menuView`, `menuHeader`, `menuReadyCount`, `lastSessionLabel`, `waitingSessionsLine`, `fitSeg`/`fitSegmentStrip`/`chosenSegs`, `refreshMenu`, `menuRefreshTick`, `maybeFetchSpend`, `fetchGitInfo`, `fetchTodaySpend`, `fetchWaitingSessions`, `sessionNeedsUser`, `blockedTaskCount`, `locationLabel`, onboarding probes (`needsOnboarding`, `specIsEmpty`, `hasBacklogTasks`, `isAllDigits`).
- `workloop.go`: `startWorkLoop`/`stopWorkLoop`/`fetchWorkLoop`/`fetchWorkLoopDigest`/`loopRefreshTick`, `loopSessRec`/`digestTask`/`loopDigest`/`digestFromWorkLoop`, `digestRows`/`digestView`/`updateDigest`, `shortSHA`.
- `session.go`: `subscribe`/`waitEvent`, `sendInput`/`interrupt`/`resume`, `updateSession`, `sessionView`, `footer`, `interruptKeyHint`, `reopenSession`, `fetchTranscript`.
- `status.go`: `statusBar`, `fmtTokens`, `fmtElapsed`.
- `transcript.go` (event pipeline + caches): `appendEvent`, `applyTransient`, `retryNoteText`, `sessionErrorHead`, `rebuild`, `makeRenderer`, `invalidateRender`/`invalidateRow`/`invalidateNeighbors`, `hiddenRow`/`computeHiddenRow`, `eventAt`, `ensureVisible`, `toggle`, `autoExpand`, the pairing/fold helpers (`mergedResultIdx`, `isMergedResult`, `askQuestionIdx`, `resultCallIdx`, `answerIdxFor`, `questionIdxForAnswer`, `answerEventFor`, `isAskUserPlumbing`, `isFoldedAnswer`, `planProposalIdx`, `isPlanProposalPlumbing`, `isEmptyModelTurn`, `isFinishTurnEcho`), `renderLiveTails` + `liveTailMaxLines`.
- `eventrender.go`: `renderBlock`, `firstOfRun`, `lastOfSubRun`, `renderHeader`, `expandedDetailLine`, `renderHeaderDetail`, `bodyFor`, `bodyWrapWidth`, `wrapTo`, `renderBody`, `markdown`, `bodyBar`, `indentLines`, `styleLines`.
- `toolcards.go`: `renderToolCall`, `toolStatusGlyph`, `toolCollapsed`, `toolCardExpanded`, `cardParams`, `cardResult`, `titledBox`, `expandTabs`, `prettyArgs`, `argField`, `argSummary`, `callFor`, `durSuffix`, `highlightResult`/`catnRe`, `highlightToolResult`, `toolView`/`viewNode`/`toolViewOf`/`renderToolView`/`renderViewNodes`/`viewKindStyle`/`viewGlyph`.
- `glyphs.go`: actor/type/verdict styling — the style `var` block, `actorStyle`, `actorGlyph`, `actorColumn`, `isSub`, `typeGlyph`, `typeGlyphStyle`, `verdictStyle`.
- `diffrender.go`: `looksDiff`, `looksCatN`, `colorizeDiff`, `dimLineNumbers`, `unifiedDiff`, `diffKind`/`diffOp`/`diffOps`.
- `search.go`: `moveSelection`, `searchableText`, `yankText`, `matchesQuery`, `searchStep`, `runSearch`, `searchCount`, `jumpToEvent`, `clearSearch`, `typeMatches`, `searchBar`.
- `question.go`: `startWizard`, `loadWizQuestion`, `clearWizard`, `recordWizAnswer`, `answerQuestion`, `answerQuestions`, `choosePickerOption`, `questionPrompt`, `pickerView`, `wizardView`, `questionBody`, `batchQuestionBody`, `answerLines`, `autoAnswerLine` (plus the `wizQuestion`/`wizAnswer` types wherever they currently live).
- `history.go`: `fetchHistory`, `updateHistory`, `openHistModal`, `updateHistoryModal`, `refreshHistModalVP`, `applyHistModalContent`, `resetHistModalNav`, the `hist*` search/jump helpers, `histEventLine`, `renderTranscriptContent`, `renderTranscript`, `historyView`, `historyRows`, `histModalView`, `histModalSearchBar`, `transcriptView`, `historyWhen`.
- `browse.go`: `browser`, `browserRow`, `navUp`/`navDown`/`clampCursor`/`listWindow`, `browser` methods, `browserCard`, `browseTargets`, `openBrowse`, `updateBrowse`, `browseView`.
- `backlog.go`: `fetchBacklog`, `fetchTask`, `updateTaskCmd`, `editorCommand`, `openEditorCmd`, `updateBacklog`, `selectedBacklogTasks`, `backlogTargetID`, `statusForDigit`, `reprioritizeCmd`, `openTaskInEditor`, `taskFileLocal`, `visibleBacklogTasks`, `backlogView`, `taskDetailContent`, `refreshBacklogDetailVP`, `taskDetailView`.
- `plans.go`, `cost.go`, `workstreams.go`, `commitdiff.go`, `capture.go`, `settings.go`, `modelbackends.go`: the corresponding existing blocks (cost = usage fetch + table/render helpers; workstreams = panel + merge overlay + its RPC cmds; commitdiff = parse/render/update + `fetchCommitDiff`; settings = overlay consts/vars/update/adjust/activate/view + `toggleAutoExpand`/`eventExpanded`/reviewer helpers + `setThinking`/`setRoleConfig`; modelbackends = every `mb*` decl plus `fmtPrice`/`parsePrice`/`parseModelIDs`).

Rules for Phase A:
- MOVE code verbatim: no renames, no signature changes, no behavior tweaks, no drive-by cleanups, no reordering beyond grouping. Comments move with their decl.
- Prefer scripted extraction (awk/sed by line range) over retyping, then `gofmt`.
- Existing files (`help.go`, `highlight.go`, `select.go`, `theme.go`, `snapshot/`) stay as they are; move new decls into them only if they obviously belong (e.g. don't).

## Phase B — split tui_test.go
- `helpers_test.go`: `fakeClient` + all its methods, `newFakeClient`, `drive`, `keyMsg`, `runCmds`, `typeText`, `newBackendsModel`, and any other shared fixture.
- Then one `<domain>_test.go` per production file above, each holding the tests for that domain (e.g. `modelbackends_test.go`, `backlog_test.go`, `menu_test.go`, `history_test.go`, `cost_test.go`, `workstreams_test.go`, `settings_test.go`, `capture_test.go`, `eventrender_test.go`, `transcript_test.go`, `diffrender_test.go`, `search_test.go`, `question_test.go`, `session_test.go`, `plans_test.go`, `workloop_test.go`, `layout_test.go`). Existing `commitdiff_test.go`, `select_test.go`, `snapshot_test.go`, `steer_queued_test.go`, `tui_stream_test.go`, `rebuild_bench_test.go`, `theme_test.go` stay; fold matching tests from `tui_test.go` into them where it's a clean fit. Anything genuinely cross-cutting can stay in `tui_test.go`.
- Tests move verbatim too (no assertion changes). If a test file's imports shrink, `gofmt`/goimports accordingly.

## Phase C — docs
- Add `docs/tui-components.md`: a short file map (one line per file: what it owns, what state it touches) + the rule "one domain per file; the shared `model` struct stays in tui.go; new screens get their own file with its own update/view/cmds" + a note on why it's one package today and what would have to change to promote a seam to a subpackage.
- Add a one-line pointer to it from `spec.md` §15 (near the `internal/tui` description around line 1398) and from `docs/e2e-tui.md`'s intro reference to in-process tests.

## Verification (all must pass, report numbers)
- `gofmt -l internal/tui` clean; `go vet ./internal/tui/...`; `go build ./...`.
- `go test ./internal/tui/...` (~90s), `go test -race ./internal/tui/...`, `go test ./...`.
- E2E: `go test ./internal/e2e` (needs a PTY; report if it skips).
- Benchmarks vs the pre-refactor baseline: `go test ./internal/tui -run XXX -bench BenchmarkRebuild -benchtime 3x -count 3` — must not materially regress.
- Strongly recommended equivalence check: write a throwaway `go/ast` program in /tmp that prints every top-level decl (name + gofmt-printed body) for a package dir, sorted; run it against `git show HEAD:...` (or a `git worktree add` of HEAD) and against the refactored tree, and diff. The diff should be empty for non-test files apart from intentional additions. Report the result.

### Starting points
- internal/tui/tui.go is ~10.7k lines; `rg -n '^(func|type|const|var) ' internal/tui/tui.go` gives the full decl outline used to build this map
- Shared test fixtures currently live at internal/tui/tui_test.go:1044 (`fakeClient`) and ~1381-1457 (`drive`, `keyMsg`, `runCmds`, `typeText`, `newBackendsModel`)
- Baseline benches: BenchmarkRebuildWarm/Cold in internal/tui/rebuild_bench_test.go (spec §18.9 mentions them)
- Existing package files that must stay put: help.go, highlight.go, select.go, theme.go, snapshot/
- docs/e2e-tui.md and spec.md:1398 are the natural places to point at the new file map

## Work log
- 2026-08-07 plan: Goal: turn `internal/tui/tui.go` (10.7k lines) and `tui_test.go` (6.6k lines) into a set of cohesive, clearly-named files in the SAME `package tui`, with zero behavior change, plus a developer-facing 
…[truncated]
- 2026-08-07 context hints: 5 recorded with plan
- 2026-08-07 context hints: `rg -n '^(func|type|const|var) ' internal/tui/tui.go` prints the full decl outline this map was built from — use it as the worklist; Shared test fixtures: internal/tui/tui_test.go:1044 `fakeClient` 
…[truncated]
- 2026-08-07 implementer report: Implemented task 0210 as a same-package structural refactor with no behavior changes.  Changes: - Reduced `internal/tui/tui.go` from 10,772 to 1,579 lines. It now contains the shared `model`, initiali
…[truncated]
- 2026-08-07 review tier: single-opus — reviewers: sol
- 2026-08-07 review (sol): accept — The refactor satisfies the task as a behavior-preserving same-package split. `tui.go` is reduced from 10,772 to 1,579 lines and now contains shared model/core dispatch, major domains and their tests a
…[truncated]
- 2026-08-07 decision: accept — commit: tui: split the monolith into cohesive per-domain files + component map (task 0210)
