---
id: "0140"
title: 'Transcript: drill into a commit''s diff from the commit_made row'
status: done
priority: 3
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 18.9 Transcript rendering is incremental (render caches)
---

## Description
The transcript renders `commit_made` as sha + message, and `< / >` jumps between commits — but the user can't *see the change* without dropping to a shell. Reviewing what the agent actually committed is the core trust loop of the harness; it should be one keypress. The diff renderer + syntax highlighting already exist (implementer diffs, workstream merge preview).

## Acceptance criteria
- [ ] Enter (or a dedicated key) on a selected `commit_made` row expands/opens the commit's diff (`git show`), syntax-highlighted, scrollable, foldable per file for large commits.
- [ ] Works in the read-only session-browser transcript too (same rendering path).
- [ ] Large diffs are windowed/truncated safely (never blow up the render cache invariants of §18.9).

## Plan

Goal: one keypress on a selected `commit_made` transcript row opens the commit's diff (`git show`), colorized, scrollable, foldable per file — in the live session view AND both read-only session-browser transcripts (stateHistory transcript and the histModal-over-live-session transcript).

Design: a full-screen modal OVERLAY (like the existing wsMerge preview overlay) with its own viewport, rather than expanding inline in the transcript. This keeps the §18.9 render caches completely untouched (criterion 3), gives free scrolling, and lets us implement per-file folding cleanly.

1) git layer (internal/git/git.go):
   - Add `func (r *Repo) Show(sha string) (string, error)`: validate sha with `^[0-9a-fA-F]{4,40}$` (reject anything else — shas from commit_made events are short hex; this also blocks flag/ref injection), then run `git show --no-color --stat --patch --end-of-options <sha>`.

2) Manager (internal/session):
   - `func (m *Manager) CommitDiff(project, sha string) (string, error)`: resolve workspace like SessionTranscript does (default workspace when project == "", else projects.Resolve → ErrUnknownProject), git.Open, return repo.Show(sha). Works for workstream commits too since linked worktrees share the primary repo's object DB.

3) RPC (proto/ycc/v1/ycc.proto + `buf generate`):
   - `rpc GetCommitDiff(GetCommitDiffRequest) returns (GetCommitDiffResponse)`.
   - `GetCommitDiffRequest { string project = 1; string sha = 2; }`
   - `GetCommitDiffResponse { string diff = 1; bool truncated = 2; }`
   - Server handler (internal/server/server.go): validate sha non-empty (InvalidArgument), map ErrUnknownProject → NotFound, git errors → NotFound/Internal as appropriate. Cap the returned diff server-side at ~1 MiB, truncating at a line boundary and setting `truncated = true` (append nothing server-side; the client renders a truncation notice). This bounds the wire payload and the client render.

4) TUI overlay (internal/tui/tui.go):
   - Model state: `cdiffOpen bool`, `cdiffSha, cdiffMsgTxt string` (sha + commit message for the title), `cdiffLoading bool`, `cdiffErr string`, `cdiffDiff string`, `cdiffTruncated bool`, `cdiffVP viewport.Model`, parsed `cdiffFiles []cdiffFile` (path, header/body line ranges or strings, +n/−m counts), `cdiffFold []bool`, `cdiffCursor int` (file cursor).
   - `fetchCommitDiff(sha string) tea.Cmd` → GetCommitDiff(m.project, sha) → `commitDiffMsg{sha, diff, truncated, err}`; handle it in Update (ignore if the overlay was closed meanwhile or sha mismatch).
   - Parse: split raw `git show` output into a preamble (commit header + stat) and file sections on lines starting `"diff --git "`. Per-file line counts (+/−) computed from the section body.
   - Content: preamble, then per file a header line `▾/▸ path  (+a −b)` (cursor file highlighted with selStyle) followed, when unfolded, by the section colorized via the existing `colorizeDiff`. If truncated, a dim "… diff truncated (showing first ~1 MiB)" trailer. Large-commit safety: when the diff exceeds ~1500 lines or ~25 files, initialize all files FOLDED so the overlay opens instantly and the user unfolds what they want.
   - Keys (updateCommitDiff): ↑/↓/pgup/pgdn/wheel scroll the viewport; tab / shift+tab move the file cursor (next/prev file, scroll its header into view); enter/space toggle fold of the cursor file; `a` toggle fold-all; esc/q/backspace close (clear all cdiff state). ctrl+c → confirmQuit.
   - Dispatch: in Update, handle `commitDiffMsg` and route keys to `updateCommitDiff` when `m.cdiffOpen` — this check must come BEFORE the `stateHistory` branch so the overlay works over the history transcript. In render(), return `commitDiffView()` early (right after helpOpen) so it draws over any underlying screen. Recompute/resize the viewport on WindowSizeMsg like refreshWsMergeVP does.
   - View: titleBar " commit <sha> " + one-line commit message, viewport body, footerBar hint ("tab/shift+tab file · enter fold · a fold all · ↑↓ scroll · esc close"); "loading diff…" while pending; error shown inline.

5) Openers (one keypress on a selected commit_made row):
   - Live session (updateSession, empty-input "enter" branch): if `m.selected >= 0 && m.evs[m.selected].Type == "commit_made"`, open the overlay (set state, loading, return fetchCommitDiff(dataField(ev,"sha"))) instead of `m.toggle`.
   - stateHistory read-only transcript (updateHistory transcript branch): "enter"/"o" currently reopen the session. Change "enter": when the selected event (via `< >` jump or search) is a commit_made, open the diff overlay; otherwise keep the reopen behavior. "o" always reopens (unchanged). Update the transcript footer hint accordingly.
   - histModal transcript (updateHistoryModal transcript branch): add "enter": look up m.histModalEventLines for the entry whose line == m.histModalCurLine; if its type is commit_made, find the corresponding event in m.histModalEvents to get the sha and open the overlay. Otherwise fall through to viewport handling.
   - Guard: no sha → no-op.

6) Help (internal/tui/help.go): add the key to the transcript/navigation section ("enter on ● commit — view its diff", plus the overlay's own keys if there's a natural place).

7) Docs: spec.md — add a sentence to §18.6 (session browser / transcript) describing the commit-diff drill-in and add `rpc GetCommitDiff` to the §12 service sketch with a one-line comment.

8) Tests:
   - internal/git: TestShow (init temp repo, commit a file, Show(sha) contains "diff --git"; Show("--flag") and Show("main") rejected).
   - internal/server or session: GetCommitDiff happy path + unknown project → NotFound + bad sha → InvalidArgument (follow existing server_test.go patterns).
   - internal/tui: unit tests for the diff parser (file splitting, +/- counts) and fold rendering (folded file hides body, truncation notice shown); a key-flow test that Enter on a selected commit_made row sets cdiffOpen and issues the fetch cmd (follow existing tui_test.go patterns).

9) Verify with plans/build-and-test.md (go build ./..., go vet ./..., go test ./...). Regenerate proto with `buf generate` (buf + plugins are on PATH).

### Starting points
- internal/tui/tui.go: wsMerge overlay pattern — refreshWsMergeVP/wsMergeContent/wsMergeView (~4303-4360), colorizeDiff (~9062), updateSession enter/toggle (~3688), updateHistory transcript keys (~2967-3038), updateHistoryModal transcript keys (~3101-3200), Update dispatch order (~2780-2876), render() (~7563)
- internal/tui/tui.go: dataField(ev, "sha") / dataField(ev, "message") for commit_made events (see snapshotLoopSession ~1169)
- internal/session/session.go: SessionTranscript (~1449) shows project→workspace resolution; ErrUnknownProject
- internal/git/git.go: Repo.run pattern
- internal/server/server.go: GetSessionTranscript handler (~146) as the pattern for the new handler; internal/server/workstream.go workstreamError for error mapping
- proto/ycc/v1/ycc.proto service block ~line 492; regenerate with `buf generate`
- help.go: key help tables
- spec.md §12 service sketch (~line 654) and §18.6 (~line 1178)

## Work log
- 2026-07-06 plan: Goal: one keypress on a selected `commit_made` transcript row opens the commit's diff (`git show`), colorized, scrollable, foldable per file — in the live session view AND both read-only session-bro
…[truncated]
- 2026-07-06 context hints: 8 recorded with plan
- 2026-07-06 context hints: internal/tui/tui.go: wsMerge overlay pattern (refreshWsMergeVP ~4303, wsMergeContent ~4323, wsMergeView ~4344), colorizeDiff ~9062, Update dispatch ~2780-2876, render() ~7563, updateSession enter ~368
…[truncated]
- 2026-07-06 implementer report: Implemented task 0140: drill into a commit's diff from the commit_made transcript row.  ## What changed  **git layer** (`internal/git/git.go`): added `Repo.Show(sha)` — validates the sha against `^[
…[truncated]
- 2026-07-06 review tier: single-opus — reviewers: Claude
- 2026-07-06 review (Claude): accept — The change fully implements task 0140: a full-screen commit-diff overlay (`git show`, stat+patch) opened with Enter on a selected commit_made row, syntax-highlighted via the existing colorizeDiff, scr
…[truncated]
- 2026-07-06 decision: accept — commit: tui: drill into a commit's diff from the commit_made row — GetCommitDiff RPC + foldable git-show overlay (task 0140)
- 2026-07-06 usage: 59,874 tok (in 302, out 59,572, cache_r 11,773,753, cache_w 254,587) · cost n/a (unpriced)
  implementer: 46,409 tok (in 252, out 46,157, cache_r 10,681,335, cache_w 130,753) · cost n/a (unpriced)
  coordinator: 8,431 tok (in 22, out 8,409, cache_r 824,763, cache_w 90,827) · cost n/a (unpriced)
  reviewer:Claude: 5,034 tok (in 28, out 5,006, cache_r 267,655, cache_w 33,007) · cost n/a (unpriced)
