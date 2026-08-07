// This file owns session and menu status summaries.
package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// statusBar renders the single-row session status line as a set of colored,
// glyph-prefixed segments — an activity spinner / state dot, a mode pill, the
// coordinator model and thinking level, the elapsed clock, a live token/cost
// readout (task 0062), and the location/id — joined by dim chevrons.
//
// It is ALWAYS exactly one physical row. Each segment carries a priority (lower =
// keep longer); when the joined bar would exceed the terminal width we drop the
// lowest-priority segments first, then apply an ANSI-aware truncation as a final
// clamp. This preserves the fixed-height frame (a wrap here corrupts Bubble Tea's
// line accounting; see TestSessionViewFitsTerminal) while degrading gracefully on
// narrow terminals.
func (m model) statusBar() string {
	type seg struct {
		text string
		prio int // lower = more important (dropped last)
	}
	var segs []seg

	// The quit guard warning rides at the very highest priority so it's never
	// dropped — the user pressed ctrl+c and needs to see why nothing quit (task 0109).
	if m.quitArmed {
		segs = append(segs, seg{errStyle.Render("⚠ " + quitGuardHint), -2})
	}
	// A transient inline error (failed RPC on an otherwise-live session) rides at
	// the highest priority so the width-greedy fitter never drops it (task 0104).
	if m.flashErr != "" {
		segs = append(segs, seg{errStyle.Render("✗ " + m.flashErr), -1})
	}
	// A transient inline notice (e.g. "copied ✓" after a yank) rides at the same
	// high priority so it's never dropped (task 0141).
	if m.flashNote != "" {
		segs = append(segs, seg{successStyle.Render(m.flashNote), -1})
	}
	// status: a state-colored dot. The header always shows the static dot; the
	// activity spinner now lives next to the input box at the bottom of the
	// session view (see inputRow / task 0076). The static dot covers
	// idle/paused/error so a stale error never animates (task 0051).
	dot := dimStyle
	switch m.status {
	case "running":
		dot = successStyle
	case "paused":
		dot = recoStyle
	case "error":
		dot = errStyle
	case "idle", "waiting for your answer":
		dot = pathStyle
	}
	glyph := dot.Render("●")
	segs = append(segs, seg{glyph + " " + typeStyle.Render(m.status), 0})

	if m.mode != "" {
		segs = append(segs, seg{dimStyle.Render("mode ") + typeStyle.Render(m.mode), 1})
	}
	// The backlog task currently in focus (task_focus): which task the work
	// agent is on right now. The title tags along, truncated, when present.
	if m.focusTask != "" {
		label := m.focusTask
		if m.focusTaskTitle != "" {
			label += " " + trunc(m.focusTaskTitle, 32)
		}
		segs = append(segs, seg{dimStyle.Render("task ") + typeStyle.Render(label), 1})
	}
	// Surface daemon loop lifecycle (or an attended session's deferred arm) at high
	// priority so unattended work is never invisible.
	loopLabel := ""
	if m.looping {
		loopLabel = "⟳ loop"
		if m.loopInfo != nil && m.loopInfo.State == "stopping" {
			loopLabel = "⟳ loop (stopping)"
		}
	} else if m.loopArmed {
		loopLabel = "⟳ loop (armed)"
	}
	if loopLabel != "" {
		segs = append(segs, seg{recoStyle.Render(loopLabel), 1})
	}
	// Spend guard (task 0137, spec §20.6): a visually distinct, high-priority
	// segment once the session crosses ~80% (warn) or the cap (err). Kept above
	// the normal Σ readout so a budget breach is unmistakable.
	if m.budgetExceeded {
		segs = append(segs, seg{errStyle.Render("⚠ budget reached"), 1})
	} else if m.budgetPct > 0 {
		segs = append(segs, seg{recoStyle.Render(fmt.Sprintf("⚠ budget %d%%", int(m.budgetPct*100))), 1})
	}
	// live token/cost readout — the headline new datum, kept at high priority.
	if tokens, cost, st := m.sessionUsage(); tokens > 0 {
		readout := dimStyle.Render("Σ ") + typeStyle.Render(fmtTokens(tokens))
		switch st {
		case "priced":
			readout += " " + successStyle.Render(fmt.Sprintf("$%.4f", cost))
		case "partial":
			readout += " " + recoStyle.Render(fmt.Sprintf("$%.4f*", cost))
		}
		segs = append(segs, seg{readout, 2})
	}
	// Keep the active coordinator model beside its reasoning level so the bar
	// answers at a glance which model is producing the session's top-level turns.
	// roleCoord is seeded from ListModels and follows live role_config_changed events.
	if m.roleCoord != "" {
		segs = append(segs, seg{dimStyle.Render("model ") + typeStyle.Render(m.roleCoord), 3})
	}
	if lvl := m.thinkLevels["coordinator"]; lvl != "" {
		segs = append(segs, seg{pathStyle.Render("◆") + " " + dimStyle.Render(lvl), 4})
	}
	if !m.sessionStart.IsZero() {
		segs = append(segs, seg{dimStyle.Render("⏱ " + fmtElapsed(time.Since(m.sessionStart))), 5})
	}
	if loc := m.locationLabel(); loc != "" {
		segs = append(segs, seg{dimStyle.Render(loc), 6})
	}
	if m.sessionID != "" {
		segs = append(segs, seg{dimStyle.Render(short(m.sessionID)), 7})
	}

	prefix := ""
	if m.pending != "" {
		prefix = askStyle.Render(" ? answer below ")
	}
	sep := dimStyle.Render(" › ")
	// render joins the chosen segments (in their original visual order) into the bar.
	render := func(chosen []seg) string {
		parts := make([]string, len(chosen))
		for i, s := range chosen {
			parts[i] = s.text
		}
		return prefix + " " + strings.Join(parts, sep) + " "
	}

	// Greedily include segments by priority while the rendered bar fits the width,
	// then emit the kept segments in visual order. A zero width (before the first
	// WindowSizeMsg) keeps everything.
	if m.w > 0 {
		order := make([]int, len(segs))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool { return segs[order[a]].prio < segs[order[b]].prio })
		keep := make([]bool, len(segs))
		for _, idx := range order {
			keep[idx] = true
			chosen := chosenSegs(segs, keep)
			if lipgloss.Width(render(chosen)) > m.w {
				keep[idx] = false // this segment doesn't fit; skip it (lower-priority ones may still fit)
			}
		}
		bar := render(chosenSegs(segs, keep))
		return ansi.Truncate(bar, m.w, dimStyle.Render("…"))
	}
	all := make([]seg, len(segs))
	copy(all, segs)
	return render(all)
}

// chosenSegs returns the segments flagged keep[i], preserving visual order. A tiny
// helper kept out of statusBar so the drop loop reads cleanly.
func chosenSegs[T any](segs []T, keep []bool) []T {
	out := make([]T, 0, len(segs))
	for i, s := range segs {
		if keep[i] {
			out = append(out, s)
		}
	}
	return out
}

// fitSeg is one width-fit segment: pre-styled text plus a drop priority (lower =
// kept first, dropped last). Used by the home-menu context header (task 0139).
type fitSeg struct {
	text string
	prio int
}

// fitSegmentStrip greedily fits priority-ordered segments into width w, joining
// the kept ones (in original visual order) with sep. It mirrors the status bar's
// priority-fit approach (task 0139): a zero/negative width keeps everything, and
// the result is ANSI-truncated to w as a final clamp so the strip never spills
// past one physical row on a narrow terminal.
func fitSegmentStrip(segs []fitSeg, sep string, w int) string {
	render := func(chosen []fitSeg) string {
		parts := make([]string, len(chosen))
		for i, s := range chosen {
			parts[i] = s.text
		}
		return strings.Join(parts, sep)
	}
	if w <= 0 {
		return render(segs)
	}
	order := make([]int, len(segs))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return segs[order[a]].prio < segs[order[b]].prio })
	keep := make([]bool, len(segs))
	for _, idx := range order {
		keep[idx] = true
		if lipgloss.Width(render(chosenSegs(segs, keep))) > w {
			keep[idx] = false // doesn't fit; lower-priority segments may still fit
		}
	}
	return ansi.Truncate(render(chosenSegs(segs, keep)), w, dimStyle.Render("…"))
}

// menuReadyCount reports how many backlog tasks a work session could pick up
// right now: ready and either todo or a resumable in_progress (task 0139).
func (m model) menuReadyCount() int {
	n := 0
	for _, t := range m.backlogTasks {
		if t.Ready && (t.Status == "todo" || t.Status == "in_progress") {
			n++
		}
	}
	return n
}

// menuHeader builds the one-line project-context header for the home menu (task
// 0139): project · git branch (+dirty marker) · N ready / M blocked · $ today.
// Each segment drops out when its data is unavailable (non-git workspace, empty
// backlog, no priced usage), and the strip is width-fit to exactly one physical
// row like the session status bar so it never corrupts the frame.
func (m model) menuHeader() string {
	var segs []fitSeg
	// project name — highest priority, always present.
	name := filepath.Base(m.workspace)
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = m.workspace
	}
	segs = append(segs, fitSeg{typeStyle.Render(name), 0})
	// git branch + dirty marker (dropped when the workspace isn't a git repo).
	if m.gitBranch != "" {
		git := dimStyle.Render("⎇ ") + typeStyle.Render(m.gitBranch)
		if m.gitDirty {
			git += recoStyle.Render("*")
		}
		segs = append(segs, fitSeg{git, 2})
	}
	// backlog readiness (dropped when there are no backlog tasks at all).
	if len(m.backlogTasks) > 0 {
		ready := m.menuReadyCount()
		blocked := m.blockedTaskCount()
		line := typeStyle.Render(fmt.Sprintf("%d", ready)) + dimStyle.Render(" ready")
		if blocked > 0 {
			line += dimStyle.Render(" / ") + warnStyle.Render(fmt.Sprintf("%d blocked", blocked))
		}
		segs = append(segs, fitSeg{line, 1})
	}
	// today's spend (dropped until a priced fetch reports a positive cost).
	if m.todaySpendLoaded && m.todaySpend > 0 {
		var spend string
		if m.todaySpendStatus == "partial" {
			spend = recoStyle.Render(fmt.Sprintf("$%.2f* today", m.todaySpend))
		} else {
			spend = successStyle.Render(fmt.Sprintf("$%.2f today", m.todaySpend))
		}
		segs = append(segs, fitSeg{spend, 3})
	}
	return "  " + fitSegmentStrip(segs, dimStyle.Render(" · "), m.w-2)
}

// fmtTokens renders a token count compactly: "842", "12.3k", "1.2M".
func fmtTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return strconv.Itoa(n)
	}
}

// fmtElapsed renders a session/turn duration as mm:ss, or h:mm:ss past an hour.
func fmtElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Seconds())
	h := total / 3600
	mn := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, mn, s)
	}
	return fmt.Sprintf("%02d:%02d", mn, s)
}

// sessionUsage sums the running per-model usage and prices it (task 0062, spec
// §20). tokens is the total token count across every model. cost is the dollar
// sum over models that have configured pricing; unpriced models contribute tokens
// but never an invented cost. status reports the pricing coverage:
//   - "priced":   every model that spent tokens is priced
//   - "partial":  some but not all spending models are priced
//   - "unpriced": no spending model is priced (or there is no usage)
func (m model) sessionUsage() (tokens int, cost float64, status string) {
	var priced, unpriced int
	for name, u := range m.usageByModel {
		t := u.Total
		if t == 0 {
			t = u.Input + u.Output + u.CacheRead + u.CacheWrite
		}
		if t == 0 {
			continue // a model that recorded no tokens doesn't affect pricing status
		}
		tokens += t
		if p, ok := m.pricing[name]; ok {
			if c, ok := p.Cost(u); ok {
				cost += c
				priced++
				continue
			}
		}
		unpriced++
	}
	switch {
	case priced > 0 && unpriced == 0:
		status = "priced"
	case priced > 0:
		status = "partial"
	default:
		status = "unpriced"
	}
	return tokens, cost, status
}
