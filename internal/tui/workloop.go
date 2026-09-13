// This file owns work-loop controls and digest rendering.
package tui

import (
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	tea "charm.land/bubbletea/v2"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// startWorkLoop asks the daemon to own the unattended backlog loop. If another
// client already started one, fetch its snapshot instead of treating that as an
// error so reconnecting TUIs attach seamlessly.
func (m model) startWorkLoop() tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.StartWorkLoop(m.ctx, connect.NewRequest(&v1.StartWorkLoopRequest{Project: m.project}))
		if err == nil {
			return workLoopMsg{info: resp.Msg.Loop, initiated: true}
		}
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			return workLoopMsg{err: err, initiated: true}
		}
		got, getErr := m.client.GetWorkLoop(m.ctx, connect.NewRequest(&v1.GetWorkLoopRequest{Project: m.project}))
		if getErr != nil {
			return workLoopMsg{err: getErr, initiated: true}
		}
		return workLoopMsg{info: got.Msg.Loop, alreadyRunning: true, initiated: true}
	}
}

// stopWorkLoop gracefully drains the daemon loop: its current task may finish but
// no next task is picked.
func (m model) stopWorkLoop() tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.StopWorkLoop(m.ctx, connect.NewRequest(&v1.StopWorkLoopRequest{Project: m.project}))
		if err != nil {
			return workLoopMsg{err: err, initiated: true}
		}
		return workLoopMsg{info: resp.Msg.Loop, initiated: true}
	}
}

// fetchWorkLoop captures the current generation so an older GetWorkLoop response
// cannot overwrite a newer start/stop/attach transition.
func (m model) fetchWorkLoop() tea.Cmd {
	seq := m.loopSeq
	return func() tea.Msg {
		resp, err := m.client.GetWorkLoop(m.ctx, connect.NewRequest(&v1.GetWorkLoopRequest{Project: m.project}))
		if err != nil {
			return workLoopMsg{err: err, continuePoll: m.looping, seq: seq}
		}
		info := resp.Msg.Loop
		attach := !m.looping && info != nil && (info.State == "running" || info.State == "waiting" || info.State == "stopping")
		return workLoopMsg{info: info, alreadyRunning: attach, seq: seq}
	}
}

// fetchWorkLoopDigest is a pure historical read for the digest modal. Unlike a
// start/stop response it must not alter lifecycle state or polling generations.
func (m model) fetchWorkLoopDigest() tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.GetWorkLoop(m.ctx, connect.NewRequest(&v1.GetWorkLoopRequest{Project: m.project}))
		if err != nil {
			return workLoopMsg{err: err, openDigest: true}
		}
		return workLoopMsg{info: resp.Msg.Loop, openDigest: true}
	}
}

func (m model) loopRefreshTick() tea.Cmd {
	seq := m.loopSeq
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return loopTickMsg{seq} })
}

func workLoopWaitingStatus(info *v1.WorkLoopInfo) string {
	status := "loop waiting for provider"
	if info.WaitKind != "" {
		status += " (" + info.WaitKind + ")"
	}
	if resumeAt, err := time.Parse(time.RFC3339, info.ResumeAt); err == nil {
		status += "; resumes " + resumeAt.Local().Format("15:04")
	}
	return status
}

// loopSessRec mirrors the per-session summary supplied by WorkLoopInfo.
type loopSessRec struct {
	id          string
	focus       string
	attempt     int32
	evidence    string
	errKind     string
	errMessage  string
	tokens      int64
	cost        float64
	priceStatus string
}

// digestTask mirrors one daemon-provided WorkLoopDigestTask.
type digestTask struct {
	id, title, status string
	sha               string
	verdictTally      string
	tokens            int64
	cost              float64
	priceStatus       string
	reason            string
	attempts          int32
	latestEvidence    string
	remainingCriteria string
	nextStep          string
}

// loopDigest is the finished, re-openable batch digest surface.
type loopDigest struct {
	outcome       string
	startedAt     time.Time
	dur           time.Duration
	sessions      []loopSessRec
	completed     []digestTask
	blocked       []digestTask
	inReview      []digestTask
	created       []digestTask
	unfinished    []digestTask
	resourceLines []string
	totalTokens   int64
	totalCost     float64
	costStatus    string
}

func workLoopResourceLines(info *v1.WorkLoopInfo) []string {
	if info == nil || !info.ResourceEnvelopeCaptured {
		return []string{"resource envelope unavailable for this pre-upgrade snapshot"}
	}
	limit := func(tokens int64, cost float64, wallSecs int64) string {
		tokenText := "tokens unbounded"
		if tokens > 0 {
			tokenText = commasTUI(tokens) + " tokens"
		}
		costText := "cost unbounded"
		if cost > 0 {
			costText = fmt.Sprintf("$%.2f priced cost", cost)
		}
		timeText := "wall time unbounded"
		if wallSecs > 0 {
			timeText = fmtElapsed(time.Duration(wallSecs)*time.Second) + " wall time"
		}
		return strings.Join([]string{tokenText, costText, timeText}, " · ")
	}
	pricing := "cost-limit pricing semantics unavailable"
	if info.CostLimitsPricedOnly {
		pricing = "cost caps/totals count priced models only; tokens count all models"
	}
	return []string{
		"session: " + limit(info.SessionTokenLimit, info.SessionCostLimit, info.SessionTimeLimitSecs),
		"loop: " + limit(info.LoopTokenLimit, info.LoopCostLimit, info.LoopTimeLimitSecs),
		pricing,
		"attempts: no fixed limit; ready work continues until stopped, budget-limited, or blocked",
	}
}

func workLoopResourceSummary(info *v1.WorkLoopInfo) string {
	if info == nil || !info.ResourceEnvelopeCaptured {
		return "resource envelope unavailable"
	}
	compact := func(tokens int64, cost float64, wallSecs int64) string {
		t := "∞ tok"
		if tokens > 0 {
			t = fmtTokens(int(tokens)) + " tok"
		}
		c := "∞ cost"
		if cost > 0 {
			c = fmt.Sprintf("$%.2f priced", cost)
		}
		wall := "∞ wall"
		if wallSecs > 0 {
			wall = fmtElapsed(time.Duration(wallSecs)*time.Second) + " wall"
		}
		return strings.Join([]string{t, c, wall}, "/")
	}
	return fmt.Sprintf("session %s; loop %s; attempts ∞",
		compact(info.SessionTokenLimit, info.SessionCostLimit, info.SessionTimeLimitSecs),
		compact(info.LoopTokenLimit, info.LoopCostLimit, info.LoopTimeLimitSecs))
}

// digestFromWorkLoop maps the daemon's durable loop snapshot onto the existing
// digest browser model. The daemon owns all classification, pricing, and reasons.
func digestFromWorkLoop(info *v1.WorkLoopInfo) *loopDigest {
	if info == nil {
		return nil
	}
	d := &loopDigest{
		outcome: info.Outcome, totalTokens: info.TotalTokens,
		totalCost: info.TotalCost, costStatus: info.CostStatus,
		resourceLines: workLoopResourceLines(info),
	}
	if started, err := time.Parse(time.RFC3339, info.StartedAt); err == nil {
		d.startedAt = started
		d.dur = time.Since(started)
	}
	for _, s := range info.Sessions {
		d.sessions = append(d.sessions, loopSessRec{
			id: s.SessionId, focus: s.Focus, attempt: s.Attempt,
			evidence: s.Evidence, errKind: s.ErrorKind, errMessage: s.ErrorMessage,
			tokens: s.Tokens, cost: s.Cost, priceStatus: s.PriceStatus,
		})
	}
	mapTasks := func(src []*v1.WorkLoopDigestTask) []digestTask {
		out := make([]digestTask, 0, len(src))
		for _, t := range src {
			out = append(out, digestTask{
				id: t.Id, title: t.Title, status: t.Status, sha: t.Sha,
				verdictTally: t.VerdictTally, tokens: t.Tokens, cost: t.Cost,
				priceStatus: t.PriceStatus, reason: t.Reason, attempts: t.Attempts,
				latestEvidence: t.LatestEvidence, remainingCriteria: t.RemainingCriteria,
				nextStep: t.NextStep,
			})
		}
		return out
	}
	d.completed = mapTasks(info.Completed)
	d.blocked = mapTasks(info.Blocked)
	d.inReview = mapTasks(info.InReview)
	d.created = mapTasks(info.Created)
	d.unfinished = mapTasks(info.Unfinished)
	return d
}

// shortSHA truncates a commit sha to its conventional 7-char prefix for display.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// digestRows builds the digest's list rows (shared browser surface) alongside a
// parallel nav slice giving each row's task id ("" for informational rows), so
// updateDigest and digestView agree on which row maps to which task.
func (m model) digestRows() (rows []browserRow, nav []string) {
	d := m.loopDigest
	if d == nil {
		return nil, nil
	}
	add := func(text, suffix, id string) {
		rows = append(rows, browserRow{text: text, suffix: suffix})
		nav = append(nav, id)
	}
	add(d.outcome, "", "")
	add(dimStyle.Render(fmt.Sprintf("%d session(s) · %s", len(d.sessions), fmtElapsed(d.dur))), "", "")
	add(dimStyle.Render(fmt.Sprintf("total: %s tok · %s", commasTUI(d.totalTokens),
		costCellTUI(&v1.UsageRow{Cost: d.totalCost, PriceStatus: d.costStatus}))), "", "")
	add(dimStyle.Render("resource envelope"), "", "")
	for _, line := range d.resourceLines {
		add(dimStyle.Render(line), "", "")
	}
	if len(d.sessions) > 0 {
		add(dimStyle.Render(fmt.Sprintf("attempts (%d)", len(d.sessions))), "", "")
		for _, s := range d.sessions {
			label := s.focus
			if label == "" {
				label = shortSHA(s.id)
			}
			if s.attempt > 0 {
				label += fmt.Sprintf(" attempt %d", s.attempt)
			}
			if s.errKind != "" {
				label += " failed (" + s.errKind + ")"
			}
			evidence := s.evidence
			if evidence == "" {
				evidence = s.errMessage
			}
			add("↻ "+label, "  "+dimStyle.Render(oneLine(evidence, 72)), s.focus)
		}
	}

	section := func(title, marker string, tasks []digestTask, suf func(digestTask) string) {
		if len(tasks) == 0 {
			return
		}
		add(dimStyle.Render(fmt.Sprintf("%s (%d)", title, len(tasks))), "", "")
		for _, t := range tasks {
			add(fmt.Sprintf("%s %s  %s", marker, t.id, oneLine(t.title, 48)), suf(t), t.id)
		}
	}
	tokCost := func(t digestTask) string {
		return fmt.Sprintf("%s tok · %s", commasTUI(t.tokens),
			costCellTUI(&v1.UsageRow{Cost: t.cost, PriceStatus: t.priceStatus}))
	}
	actionable := func(t digestTask) string {
		parts := []string{}
		if t.attempts > 0 {
			parts = append(parts, fmt.Sprintf("%d attempt(s)", t.attempts))
		}
		if t.latestEvidence != "" {
			parts = append(parts, "latest: "+oneLine(t.latestEvidence, 48))
		}
		if t.remainingCriteria != "" {
			parts = append(parts, "remaining: "+oneLine(t.remainingCriteria, 48))
		}
		if t.nextStep != "" {
			parts = append(parts, "next: "+oneLine(t.nextStep, 48))
		}
		return strings.Join(parts, " · ")
	}
	section("completed", "✔", d.completed, func(t digestTask) string {
		parts := []string{}
		if t.sha != "" {
			parts = append(parts, shortSHA(t.sha))
		}
		if t.verdictTally != "" {
			parts = append(parts, t.verdictTally)
		}
		parts = append(parts, tokCost(t))
		return "  " + dimStyle.Render(strings.Join(parts, " · "))
	})
	section("blocked", "⛔", d.blocked, func(t digestTask) string {
		reason := t.reason
		if reason == "" {
			reason = "(no reason recorded — open to view)"
		}
		if context := actionable(t); context != "" {
			reason += " · " + context
		}
		return "  " + dimStyle.Render(oneLine(reason, 120))
	})
	section("in review", "◌", d.inReview, func(t digestTask) string {
		return "  " + dimStyle.Render(tokCost(t))
	})
	section("unfinished", "↻", d.unfinished, func(t digestTask) string {
		return "  " + dimStyle.Render(oneLine(actionable(t), 120))
	})
	section("created during run", "+", d.created, func(t digestTask) string {
		parts := []string{t.status}
		if context := actionable(t); context != "" {
			parts = append(parts, context)
		}
		return "  " + dimStyle.Render(oneLine(strings.Join(parts, " · "), 120))
	})
	return rows, nav
}

// digestView renders the batch digest as a bordered modal card via the shared
// list component.
func (m model) digestView() string {
	rows, _ := m.digestRows()
	b := browser{
		title:  " ycc — loop digest ",
		rows:   rows,
		cursor: m.digestCursor,
		hint:   "↑/↓ · enter open task · esc close",
		empty:  "no completed loop run yet",
	}
	return m.browserCard(b)
}

// updateDigest handles the batch digest modal: list navigation, and Enter on a
// task row jumps into the backlog browser's detail for that task — the fast path
// to answer a blocked task + re-queue, or just inspect what happened.
func (m model) updateDigest(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	_, nav := m.digestRows()
	switch key.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "esc", "q":
		m.digest = false
		return m, nil
	case "up":
		m.digestCursor = navUp(m.digestCursor)
		return m, nil
	case "down":
		m.digestCursor = navDown(m.digestCursor, len(nav))
		return m, nil
	case "enter":
		if m.digestCursor >= 0 && m.digestCursor < len(nav) && nav[m.digestCursor] != "" {
			id := nav[m.digestCursor]
			m.digest = false
			m.backlog, m.backlogCursor, m.backlogDetail = true, 0, nil
			m.backlogShowDone = true
			m.backlogBlockedOnly = false
			return m, tea.Batch(m.fetchBacklog, m.fetchTask(id))
		}
		return m, nil
	}
	return m, nil
}
