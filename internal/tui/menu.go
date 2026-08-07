// This file owns the home menu, its refresh commands, and onboarding probes.
package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"

	tea "charm.land/bubbletea/v2"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"

	"github.com/whyrusleeping/ycc/internal/docs"
)

func (m model) fetchModes() tea.Msg {
	resp, err := m.client.ListModes(m.ctx, connect.NewRequest(&v1.ListModesRequest{}))
	if err != nil {
		return errMsg{err}
	}
	return modesMsg{modes: resp.Msg.Modes, presets: resp.Msg.Presets}
}

func (m model) fetchModels() tea.Msg {
	resp, err := m.client.ListModels(m.ctx, connect.NewRequest(&v1.ListModelsRequest{}))
	if err != nil {
		return nil // models are optional for the overlay; don't surface as a fatal error
	}
	return modelsMsg{
		models:      resp.Msg.Models,
		coordinator: resp.Msg.Coordinator,
		implementer: resp.Msg.Implementer,
		reviewers:   resp.Msg.Reviewers,
		coordThink:  resp.Msg.CoordinatorThinking,
		implThink:   resp.Msg.ImplementerThinking,
		revThink:    resp.Msg.ReviewersThinking,
	}
}

// startSession starts a normal attended session in the given mode.
func (m model) startSession(mode, prompt string) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.StartSession(m.ctx, connect.NewRequest(&v1.StartSessionRequest{
			Mode: mode, Prompt: prompt, Workspace: m.workspace, Project: m.project,
		}))
		if err != nil {
			return errMsg{err}
		}
		return startedMsg{id: resp.Msg.SessionId, mode: mode}
	}
}

// isWorkEntry reports whether a menu entry is the plain "work" mode (not a preset,
// which would carry an opening prompt). Only this entry supports the loop toggle.
func isWorkEntry(e menuEntry) bool { return e.mode == "work" && e.openingPrompt == "" }

// stopSession hard-terminates the current session via StopSession (spec §12).
func (m model) stopSession() tea.Cmd {
	id := m.sessionID
	return func() tea.Msg {
		if _, err := m.client.StopSession(m.ctx, connect.NewRequest(&v1.StopSessionRequest{SessionId: id})); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// sessionFinished reports whether the current session view has reached a terminal
// state the user should be offered a clean exit from (task 0127): the agent went
// idle after finishing ("idle" — it now blocks in the daemon waiting for input, so
// leaving must StopSession to avoid an orphan) or the event stream already ended
// ("stream closed" — nothing left to stop). It deliberately EXCLUDES daemon-loop
// sessions (the daemon advances them — this must not interfere) and the recoverable
// "error"/"paused" states (esc →
// settings overlay → "back home" remains the escape hatch there).
func (m model) sessionFinished() bool {
	return m.state == stateSession && !m.looping && (m.status == "idle" || m.status == "stream closed")
}

// blockedTaskCount reports how many backlog tasks are currently marked "blocked"
// (an unattended loop session set them aside pending user input — spec §10/§11).
// The home menu uses it to surface a "waiting on you" indicator (task 0101).
func (m model) blockedTaskCount() int {
	n := 0
	for _, t := range m.backlogTasks {
		if t.Status == "blocked" {
			n++
		}
	}
	return n
}

// fetchWaitingSessions loads the session history and delivers just the live
// sessions that need the user — a pending ask_user question or a paused
// (mid-steer) session (task 0107). It reuses ListSessionHistory (which carries
// status + waiting_input for both one-shot and persistent daemons) but delivers
// to its own message so the session-browser list state is never clobbered.
func (m model) fetchWaitingSessions() tea.Msg {
	resp, err := m.client.ListSessionHistory(m.ctx, connect.NewRequest(&v1.ListSessionHistoryRequest{Project: m.project}))
	if err != nil {
		return waitingSessionsMsg{err: err}
	}
	var waiting []*v1.SessionSummary
	for _, s := range resp.Msg.Sessions {
		if sessionNeedsUser(s) {
			waiting = append(waiting, s)
		}
	}
	// The most-recent session (list is most-recent first) backs the "c continue
	// last session" affordance on the home menu (task 0139).
	var recent *v1.SessionSummary
	if len(resp.Msg.Sessions) > 0 {
		recent = resp.Msg.Sessions[0]
	}
	return waitingSessionsMsg{sessions: waiting, recent: recent}
}

// sessionNeedsUser reports whether a live session is waiting on the user: it is
// blocked on an unanswered ask_user question, or it is paused mid-steer. Only
// live sessions can hold either state (task 0107).
func sessionNeedsUser(s *v1.SessionSummary) bool {
	return s.Live && (s.WaitingInput || s.Status == "paused")
}

// menuRefreshTick arms the next home-menu refresh of waiting sessions, tagged
// with the current waitingSeq so a stale tick (from a previous menu visit) is
// dropped rather than compounding timers (task 0107).
func (m model) menuRefreshTick() tea.Cmd {
	seq := m.waitingSeq
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return menuRefreshMsg{seq} })
}

// refreshMenu re-polls the home-menu awareness data (backlog + waiting sessions)
// and (re)arms the waiting-session refresh tick, bumping waitingSeq so an older
// tick can't multiply the in-flight timers (task 0107).
func (m *model) refreshMenu() tea.Cmd {
	m.waitingSeq++
	cmds := []tea.Cmd{m.fetchBacklog, m.fetchWaitingSessions, m.fetchGitInfo, m.fetchWorkLoop(), m.menuRefreshTick()}
	// Today's spend is throttled: the aggregator scans usage logs, so re-issuing
	// it on every 5s tick would be wasteful. Refetch at most ~once a minute.
	if cmd := m.maybeFetchSpend(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

// maybeFetchSpend returns the today's-spend fetch cmd when it hasn't run within
// the throttle window (~60s), and records the attempt so subsequent menu ticks
// don't hammer the log-scanning aggregator (task 0139). Returns nil otherwise.
func (m *model) maybeFetchSpend() tea.Cmd {
	if !m.lastSpendFetch.IsZero() && time.Since(m.lastSpendFetch) < 60*time.Second {
		return nil
	}
	m.lastSpendFetch = time.Now()
	return m.fetchTodaySpend
}

// fetchGitInfo reads the current git branch and working-tree dirtiness of the
// (local) workspace for the home-menu context header (task 0139). It shells out
// to git directly (no shell) and returns an empty branch on any error — a
// non-git workspace, a remote daemon whose workspace isn't local here, or git
// being absent — so the header segment simply drops out.
func (m model) fetchGitInfo() tea.Msg {
	ws := m.workspace
	if ws == "" {
		return menuGitMsg{err: fmt.Errorf("no workspace")}
	}
	branchOut, err := exec.Command("git", "-C", ws, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return menuGitMsg{err: err}
	}
	branch := strings.TrimSpace(string(branchOut))
	if branch == "" {
		return menuGitMsg{err: fmt.Errorf("no branch")}
	}
	dirty := false
	if statusOut, err := exec.Command("git", "-C", ws, "status", "--porcelain").Output(); err == nil {
		dirty = strings.TrimSpace(string(statusOut)) != ""
	}
	return menuGitMsg{branch: branch, dirty: dirty}
}

// fetchTodaySpend aggregates today's token spend for the home-menu context
// header (task 0139) via GetUsage scoped to today (Since=Until=today), grouped
// by day. Any error is delivered so the segment drops out silently.
func (m model) fetchTodaySpend() tea.Msg {
	today := time.Now().Format("2006-01-02")
	resp, err := m.client.GetUsage(m.ctx, connect.NewRequest(&v1.GetUsageRequest{
		Project: m.project, GroupBy: []string{"day"}, Since: today, Until: today,
	}))
	if err != nil {
		return menuSpendMsg{err: err}
	}
	if resp.Msg.Total == nil {
		return menuSpendMsg{cost: 0, status: ""}
	}
	return menuSpendMsg{cost: resp.Msg.Total.Cost, status: resp.Msg.Total.PriceStatus}
}

func (m model) updateMenu(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c":
			return m.confirmQuit()
		case "ctrl+n":
			// Quick-add a backlog item (spec §18.2, task 0016).
			m.openCapture()
			return m, nil
		case "ctrl+b":
			// Open the read-only backlog browser (spec §18.5).
			m.backlog, m.backlogCursor, m.backlogDetail = true, 0, nil
			m.backlogShowDone = false
			m.backlogBlockedOnly = false
			return m, m.fetchBacklog
		case "ctrl+w":
			// Jump to the blocked tasks the agent is waiting on (task 0101). Menu
			// affordances are ctrl-chords so a naked letter never triggers anything;
			// still gated on an empty prompt because the textarea binds ctrl+w to
			// delete-word-backward — mid-composition it must keep deleting.
			if m.blockedTaskCount() > 0 && strings.TrimSpace(m.prompt.Value()) == "" {
				m.backlog, m.backlogCursor, m.backlogDetail = true, 0, nil
				m.backlogShowDone = false
				m.backlogBlockedOnly = true
				return m, m.fetchBacklog
			}
		case "ctrl+s":
			// Jump straight to a live session that needs the user — a pending
			// ask_user question or a paused-mid-steer session (task 0107). Same
			// gating as ctrl+w: only intercept when a session actually needs the
			// user AND the prompt is empty, so a jump never abandons a drafted prompt.
			if len(m.waitingSessions) > 0 && strings.TrimSpace(m.prompt.Value()) == "" {
				if len(m.waitingSessions) == 1 {
					// Exactly one: attach directly (ResumeSession is idempotent for a
					// live session, so this reopens/attaches rather than restarts).
					id := m.waitingSessions[0].SessionId
					m.status = "reopening " + short(id) + "…"
					return m, m.reopenSession(id)
				}
				// Several: open the session browser filtered to just the waiting
				// sessions so the user picks which to attach.
				m.state = stateHistory
				m.historyCursor = 0
				m.history = nil
				m.historyTranscript = false
				m.historyWaitingOnly = true
				m.historyMsgTxt = "loading…"
				return m, m.fetchHistory
			}
		case "ctrl+l":
			// One-key "continue last session" (task 0139): reopen the most recent
			// session (resume = replay). ctrl+l = "last" (ctrl+c is quit). Same
			// gating as ctrl+w/ctrl+s: only intercept when a session exists AND the
			// prompt is empty, so the jump never abandons a drafted prompt.
			if m.lastSession != nil && strings.TrimSpace(m.prompt.Value()) == "" {
				id := m.lastSession.SessionId
				m.status = "reopening " + short(id) + "…"
				return m, m.reopenSession(id)
			}
		case "ctrl+r":
			// Open the session browser to inspect/reopen a session (spec §18.6).
			m.state = stateHistory
			m.historyCursor = 0
			m.history = nil
			m.historyTranscript = false
			m.historyWaitingOnly = false
			m.historyMsgTxt = "loading…"
			return m, m.fetchHistory
		case "ctrl+o":
			// Open the browse selector (backlog / sessions / cost) — spec §18.6/§20.5.
			m.openBrowse()
			return m, nil
		case "?", "ctrl+h":
			// Open the keybinding help modal (task 0111). Gated on an empty prompt so
			// a bare "?" still types into a composition and ctrl+h (== the legacy BS
			// byte 0x08, bound by the textarea to delete-char-backward) keeps deleting
			// mid-edit; fall through to the textarea otherwise. ctrl+_ is unconditional.
			if strings.TrimSpace(m.prompt.Value()) == "" {
				m.openHelp()
				return m, nil
			}
		case "ctrl+_":
			m.openHelp()
			return m, nil
		case "up":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		case "down":
			if m.cursor < len(m.entries)-1 {
				m.cursor++
			}
			return m, nil
		case "tab":
			// Toggle "work (loop)" on the work entry: an unattended run that keeps
			// starting fresh work sessions for each ready backlog task until none
			// remain. Only the work mode supports it; tab is a no-op elsewhere.
			if len(m.entries) > 0 && isWorkEntry(m.entries[m.cursor]) {
				m.loop = !m.loop
			}
			return m, nil
		case "enter":
			if len(m.entries) == 0 {
				return m, nil
			}
			e := m.entries[m.cursor]
			// The daemon owns unattended iteration, caps, progress detection, and the
			// durable digest; the TUI only starts and observes it.
			if m.loop && isWorkEntry(e) {
				m.status = "loop starting…"
				return m, m.startWorkLoop()
			}
			// Compose the preset's opening prompt with any typed text: choosing a
			// preset AND typing details means both — the preset supplies the
			// framing and the typed text is the user's upfront context. A typed
			// prompt on a plain mode entry is sent as-is; an empty prompt falls
			// back to the preset's opening prompt alone.
			prompt := strings.TrimSpace(m.prompt.Value())
			switch {
			case prompt == "":
				prompt = e.openingPrompt
			case e.openingPrompt != "":
				prompt = e.openingPrompt + "\n\nContext from the user (supplied upfront with this request):\n" + prompt
			}
			return m, m.startSession(e.mode, prompt)
		}
	}
	var cmd tea.Cmd
	m.prompt, cmd = m.prompt.Update(msg)
	return m, cmd
}

func (m model) menuView() string {
	var b strings.Builder
	b.WriteString(m.titleBar(" ycc — home ") + "\n")
	b.WriteString(m.menuHeader() + "\n\n")
	if m.quitArmed {
		b.WriteString("  " + errStyle.Render("⚠ "+quitGuardHint) + "\n\n")
	}
	if m.flashErr != "" {
		b.WriteString("  " + errStyle.Render("✗ "+m.flashErr) + "\n\n")
	}
	if n := m.blockedTaskCount(); n > 0 {
		noun := "task"
		if n > 1 {
			noun = "tasks"
		}
		b.WriteString("  " + warnStyle.Render(fmt.Sprintf("⚠ %d %s blocked — waiting on you", n, noun)) +
			dimStyle.Render(" · press ctrl+w to view") + "\n\n")
	}
	if n := len(m.waitingSessions); n > 0 {
		b.WriteString("  " + warnStyle.Render(waitingSessionsLine(m.waitingSessions)) +
			dimStyle.Render(" · press ctrl+s to open") + "\n\n")
	}
	if len(m.entries) == 0 {
		b.WriteString("  loading modes…\n")
	}
	for i, e := range m.entries {
		cursor := "  "
		// Surface the loop toggle on the work entry (tab toggles it).
		lbl, desc := e.label, e.description
		if m.loop && isWorkEntry(e) {
			lbl = e.label + " (loop)"
			desc = "Chew through every ready backlog task unattended — stuck tasks are marked blocked and skipped."
		}
		label := fmt.Sprintf("%-9s %s", lbl, dimStyle.Render(desc))
		switch {
		case i == m.cursor && e.prominent:
			// Selected AND recommended: keep the selection treatment but still
			// surface the ★ marker and "(recommended)" hint so onboarding reads
			// as recommended even when it's the default-selected row.
			cursor = selStyle.Render("▸ ")
			label = selStyle.Render("★ "+fmt.Sprintf("%-7s ", e.label)) + dimStyle.Render(e.description+"  (recommended)")
		case i == m.cursor:
			cursor = selStyle.Render("▸ ")
			label = selStyle.Render(fmt.Sprintf("%-9s ", lbl)) + dimStyle.Render(desc)
		case e.prominent:
			// Surface a recommended entry (e.g. onboarding on an un-onboarded
			// workspace) so it stands out without stealing the cursor highlight.
			label = recoStyle.Render("★ "+fmt.Sprintf("%-7s ", e.label)) + dimStyle.Render(e.description+"  (recommended)")
		}
		b.WriteString("  " + cursor + label + "\n")
	}
	b.WriteString(framedInput(m.prompt, 2) + "\n")
	// One-key affordance to reopen the most recent session (task 0139): resume the
	// last conversation instead of ctrl+r → pick → o.
	if m.lastSession != nil {
		b.WriteString("  " + typeStyle.Render("ctrl+l") + dimStyle.Render(" continue last session · "+lastSessionLabel(m.lastSession)) + "\n")
	}
	// Keep the footer to the essentials — the full keybinding catalog lives in
	// the help modal (?), and the conditional affordances (ctrl+w blocked tasks,
	// ctrl+s waiting session, ctrl+l continue last) are advertised by their own
	// body lines above, so they aren't repeated here.
	footer := "  ? help · ↑/↓ choose mode · enter start · esc settings"
	b.WriteString("\n" + m.footerBar(footer))
	return b.String()
}

// lastSessionLabel renders the compact descriptor for the "ctrl+l continue last
// session" affordance (task 0139): the session's title (or short id when it has
// none) plus its mode.
func lastSessionLabel(s *v1.SessionSummary) string {
	label := strings.TrimSpace(s.Title)
	if label == "" {
		label = short(s.SessionId)
	}
	if s.Mode != "" {
		label += " (" + s.Mode + ")"
	}
	return label
}

// waitingSessionsLine builds the home-menu awareness line for live sessions that
// need the user (task 0107). A single session waiting on an unanswered question
// gets the pointed "waiting for your answer"; a paused session (or a mix) reads
// "waiting for you". For several sessions the line invites a pick.
func waitingSessionsLine(ws []*v1.SessionSummary) string {
	n := len(ws)
	if n == 1 {
		if ws[0].WaitingInput {
			return "⚠ 1 session waiting for your answer"
		}
		return "⚠ 1 session waiting for you"
	}
	return fmt.Sprintf("⚠ %d sessions waiting for you", n)
}

// needsOnboarding reports whether a workspace looks un-onboarded (spec §19.2): it
// has no real spec.md AND no backlog tasks. It is conservative — on any unexpected
// read error it returns false so onboarding is not surfaced spuriously.
func needsOnboarding(workspace string) bool {
	if strings.TrimSpace(workspace) == "" {
		return false
	}
	return specIsEmpty(workspace) && !hasBacklogTasks(workspace)
}

// specIsEmpty reports whether the configured spec entry point is missing or
// trivially empty (only blank lines and markdown headings, no real content).
// The entry point is resolved via the workspace's .ycc/config.toml (spec_path),
// falling back to <workspace>/spec.md when unconfigured.
func specIsEmpty(workspace string) bool {
	data, err := os.ReadFile(docs.NewStore(workspace).SpecPath())
	if err != nil {
		if os.IsNotExist(err) {
			return true
		}
		return false // unexpected error: treat as not-empty (don't surface onboarding)
	}
	for _, ln := range strings.Split(string(data), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		return false // real content
	}
	return true
}

// hasBacklogTasks reports whether backlog/ exists and contains at least one task
// file matching the NNNN-*.md pattern (stray non-task .md files don't count).
func hasBacklogTasks(workspace string) bool {
	entries, err := os.ReadDir(filepath.Join(workspace, "backlog"))
	if err != nil {
		return false // missing dir (or unreadable): no tasks
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		stem := strings.TrimSuffix(name, ".md")
		dash := strings.IndexByte(stem, '-')
		if dash <= 0 {
			continue
		}
		if isAllDigits(stem[:dash]) {
			return true
		}
	}
	return false
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// locationLabel is the project name when attached to a daemon registry, else the
// basename of the workspace path — the bar's "where am I" segment.
func (m model) locationLabel() string {
	if m.project != "" {
		return m.project
	}
	if m.workspace != "" {
		return filepath.Base(m.workspace)
	}
	return ""
}
