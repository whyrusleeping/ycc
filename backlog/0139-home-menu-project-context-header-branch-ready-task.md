---
id: "0139"
title: 'Home menu: project context header (branch, ready tasks, today''s spend) + continue-last-session'
status: done
priority: 3
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 18. Client UI (TUI)
---

## Description
The home menu shows modes + blocked/waiting warnings, but no orientation: *which* project am I in, on what branch, is the tree dirty, how much work is ready, what have I spent today? Users juggling multiple projects (persistent daemon) or returning after hours away need this at a glance. All the data already exists (workspace path, git helpers, ListBacklog readiness, usage aggregator).

## Acceptance criteria
- [ ] A one/two-line context header on the home menu: project name/workspace path · git branch (+ dirty marker) · N ready / M blocked tasks · today's spend (when priced usage exists).
- [ ] Degrades gracefully: non-git workspace, no backlog, no usage → segments drop out (reuse the status bar's priority-fit approach).
- [ ] Nice-to-have: when a recent resumable session exists, a "c continue last session" affordance on the menu (one keypress instead of ctrl+r → pick → o).

## Plan

Add a project-context header to the home menu (internal/tui/tui.go) plus a "c continue last session" affordance.

1. Model state + messages
   - New model fields: gitBranch string, gitDirty bool, gitInfoOK bool (or just empty-branch = unknown); todaySpend (cost float64, status string, loaded bool) with a lastSpendFetch time.Time throttle; lastSession *v1.SessionSummary (most recent resumable session for the "c" affordance).
   - New msgs: menuGitMsg{branch string, dirty bool, err error}, menuSpendMsg{cost float64, status string, err error} handled in Update to populate the fields (errors just clear/skip — graceful degrade).

2. Data fetching
   - fetchGitInfo (tea.Cmd): shell out `git -C m.workspace rev-parse --abbrev-ref HEAD` and `git -C m.workspace status --porcelain` (exec.Command, no shell). Any error (non-git dir, remote/non-local workspace) → segment drops out. Run it from refreshMenu() alongside fetchBacklog/fetchWaitingSessions (5s cadence is fine; it's async and local).
   - fetchTodaySpend (tea.Cmd): GetUsage RPC with Since=Until=time.Now().Format("2006-01-02"), GroupBy ["day"]; deliver Total.Cost + Total.PriceStatus. Throttle: refreshMenu only re-issues it when >60s since lastSpendFetch (or never fetched), so the log-scanning aggregator isn't hammered every 5s tick. RPC error → drop the segment silently.
   - Continue-last-session: piggyback on fetchWaitingSessions (it already calls ListSessionHistory every refresh) — extend waitingSessionsMsg to also carry the most recent session summary (first entry of resp.Msg.Sessions if the list is most-recent-first; verify ordering, else pick by timestamp). Store as m.lastSession. Only sessions with a session id count; nil when none.

3. Rendering (menuView)
   - Insert a one-line context header right after the titleBar: segments joined by dim " · ":
     • project: base name of m.workspace (typeStyle) — always present (highest prio)
     • git: "⎇ <branch>" plus a dirty marker (e.g. recoStyle "*" or "±") when dirty; dropped when branch unknown
     • backlog: "N ready / M blocked" — ready = tasks with Ready && (status todo|in_progress); blocked = blockedTaskCount(); dropped when no backlog tasks at all; blocked half styled warnStyle when M>0
     • spend: "$X.XX today" (successStyle when priced, recoStyle with * when partial) — only when loaded and cost > 0
   - Width fitting: reuse the statusBar approach — either factor the greedy priority fitter (segs + chosenSegs + render loop) into a small shared helper used by both statusBar and the header, or replicate the small loop locally. Must stay one physical row (ANSI-aware truncate as final clamp, like statusBar).
   - When m.lastSession != nil (and no more pressing warnings needed — it can coexist), render a dim affordance line, e.g. `  c continue last session · <title/short id> (<mode> · <when>)`, near the footer hints or after the warnings block. Add "c continue" to the footer hint string only when applicable.

4. Key handling (updateMenu)
   - New case "c": guarded exactly like "w"/"s" — only intercept when m.lastSession != nil AND strings.TrimSpace(m.prompt.Value()) == "", so typing is never hijacked; set m.status = "reopening …" and return m.reopenSession(id). Otherwise fall through to the textarea.

5. Help modal (internal/tui/help.go): add the "c continue last session" binding to the home-menu section.

6. Tests (internal/tui/tui_test.go or a new file):
   - Header shows project name + ready/blocked counts from backlogMsg; git + spend segments appear when their msgs arrive and are absent otherwise (degrade).
   - Spend segment absent when cost == 0.
   - "c" with empty prompt and a lastSession issues a reopen (assert via the cmd/msg path used by existing "s" tests); "c" mid-composition types into the prompt instead.
   - Header stays one row on a narrow terminal (segments dropped by priority).
   - Run gofmt, go vet, go build ./..., go test ./internal/tui/... (and full go test ./... at the end).

### Starting points
- internal/tui/tui.go:7506 menuView — insert header after titleBar
- internal/tui/tui.go:7818 statusBar — the priority-fit segment pattern to reuse (chosenSegs helper)
- internal/tui/tui.go:3127 updateMenu — 'w'/'s' keys show the prompt-empty guard pattern to copy for 'c'
- internal/tui/tui.go:1316 fetchWaitingSessions / waitingSessionsMsg — piggyback most-recent session here
- internal/tui/tui.go:1348 refreshMenu — add git/spend fetch cmds (throttle spend ~60s)
- internal/tui/tui.go:1149+1715 existing GetUsage call sites (fetchLoopUsage / cost view) for request shape
- proto: GetUsageRequest{Project, GroupBy, Since, Until} → Total.Cost/PriceStatus
- internal/tui/help.go — il menu bindings list
- internal/tui/tui.go:1273 blockedTaskCount, 1287 topReadyTask (ready definition)

## Work log
- 2026-07-06 plan: Add a project-context header to the home menu (internal/tui/tui.go) plus a "c continue last session" affordance.  1. Model state + messages    - New model fields: gitBranch string, gitDirty bool, gitI
…[truncated]
- 2026-07-06 context hints: 9 recorded with plan
- 2026-07-06 context hints: internal/tui/tui.go:7506 menuView — insert header after titleBar; internal/tui/tui.go:7818 statusBar — priority-fit segment pattern (chosenSegs helper); internal/tui/tui.go:3127 updateMenu — 'w'
…[truncated]
- 2026-07-06 implementer report: Implemented Task 0139: home-menu project-context header + "c continue last session" affordance.  ## Changes  **internal/tui/tui.go** - Model fields: `gitBranch`, `gitDirty`; today's spend (`todaySpend
…[truncated]
- 2026-07-06 review tier: single-opus — reviewers: Claude
- 2026-07-06 review (Claude): accept — The change adds a project-context header to the home menu (project · git branch/dirty · ready/blocked counts · today's spend) plus a one-key "c continue last session" affordance, exactly matching t
…[truncated]
- 2026-07-06 decision: accept — commit: tui: home-menu project context header (branch, ready/blocked, today's spend) + c continue-last-session (task 0139)
