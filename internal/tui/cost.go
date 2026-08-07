// This file owns usage queries, cost grouping, and cost views.
package tui

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"

	tea "charm.land/bubbletea/v2"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// fetchUsage loads the token/cost breakdown for the cost view (spec §20.5, task
// 0039). It respects the selected project, task drill-down, and group-by dimension.
func (m model) fetchUsage() tea.Msg {
	resp, err := m.client.GetUsage(m.ctx, connect.NewRequest(&v1.GetUsageRequest{
		Project: m.project, GroupBy: m.costGroupBy, Task: m.costTask,
	}))
	if err != nil {
		return errMsg{err}
	}
	msg := usageMsg{gen: m.costGen, rows: resp.Msg.Rows, total: resp.Msg.Total, workspace: resp.Msg.Workspace}
	// Subscription telemetry is best effort: local usage still renders if this
	// provider-internal endpoint is unavailable or an older daemon lacks the RPC.
	if sub, subErr := m.client.GetSubscriptionUsage(m.ctx, connect.NewRequest(&v1.GetSubscriptionUsageRequest{Refresh: true})); subErr == nil {
		msg.accounts = sub.Msg.Accounts
	}
	return msg
}

// costGroupOrder is the cycle of group-by dimensions the cost view rotates through
// with the "g" key (mirrors the CLI's -by options in cmd/ycc).
var costGroupOrder = []string{"task", "model", "session", "day", "agent"}

// costDrillGroupOrder omits task because every row in a drill-down is already
// scoped to the focused task (task 0174).
var costDrillGroupOrder = []string{"agent", "model", "session", "day"}

// updateCost handles navigation, grouping, and task drill-down in the modal cost
// view. Esc/q backs out of a drill-down before dismissing the modal.
func (m model) updateCost(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "esc", "q":
		if m.costTask != "" {
			m.costTask = ""
			m.costGroupBy = []string{"task"}
			m.costCursor = m.costTaskCursor
			m.costRows, m.costTotal = nil, nil
			m.costMsg = "loading…"
			m.costGen++
			return m, m.fetchUsage
		}
		m.cost = false
		return m, nil
	case "up":
		m.costCursor = navUp(m.costCursor)
		return m, nil
	case "down":
		m.costCursor = navDown(m.costCursor, len(m.costRows))
		return m, nil
	case "enter":
		if m.costTask != "" || len(m.costGroupBy) == 0 || m.costGroupBy[0] != "task" || m.costCursor < 0 || m.costCursor >= len(m.costRows) {
			return m, nil
		}
		task := m.costRows[m.costCursor].Task
		if task == "" {
			return m, nil
		}
		m.costTaskCursor = m.costCursor
		m.costTask = task
		m.costGroupBy = []string{"agent"}
		m.costCursor = 0
		m.costRows, m.costTotal = nil, nil
		m.costMsg = "loading…"
		m.costGen++
		return m, m.fetchUsage
	case "g":
		order := costGroupOrder
		cur := "task"
		if m.costTask != "" {
			order = costDrillGroupOrder
			cur = "agent"
		}
		if len(m.costGroupBy) > 0 {
			cur = m.costGroupBy[0]
		}
		next := order[0]
		for i, d := range order {
			if d == cur {
				next = order[(i+1)%len(order)]
				break
			}
		}
		m.costGroupBy = []string{next}
		m.costCursor = 0
		m.costMsg = "loading…"
		m.costGen++
		return m, m.fetchUsage
	}
	return m, nil
}

// costCellTUI renders the cost column for a usage row, mirroring cmd/ycc's
// costCell: unpriced rows show "—", partial pricing appends "*".
func costCellTUI(r *v1.UsageRow) string {
	switch r.PriceStatus {
	case "unpriced":
		return "—"
	case "partial":
		return fmt.Sprintf("$%.4f*", r.Cost)
	default:
		return fmt.Sprintf("$%.4f", r.Cost)
	}
}

// costTitleTUI capitalises a group-by dimension for the table header (mirrors
// cmd/ycc's costTitle).
func costTitleTUI(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// commasTUI formats an int64 with thousands separators (mirrors cmd/ycc's commas).
func commasTUI(n int64) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var out []byte
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

// costGroupValue resolves the group label for one row + dimension, mirroring the
// CLI's placeholder treatment for unattributed/unknown values.
func costGroupValue(r *v1.UsageRow, dim string) string {
	switch dim {
	case "task":
		if r.Task == "" {
			return "(unattributed)"
		}
		return r.Task
	case "model":
		if r.Model == "" {
			return "(unknown)"
		}
		return r.Model
	case "session":
		return r.Session
	case "agent":
		if r.Agent == "" {
			return "(unknown)"
		}
		return r.Agent
	case "day":
		return r.Day
	}
	return ""
}

func subscriptionUsageTUI(accounts []*v1.SubscriptionUsageAccount) string {
	if len(accounts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Subscription allowance\n")
	for _, account := range accounts {
		heading := account.Provider
		if account.Plan != "" {
			heading += " · " + account.Plan
		}
		if len(account.Models) > 0 {
			heading += "  [" + strings.Join(account.Models, ", ") + "]"
		}
		if account.State != "fresh" {
			heading += "  " + account.State
		}
		b.WriteString(heading + "\n")
		if len(account.Windows) == 0 {
			msg := account.Message
			if msg == "" {
				msg = "allowance unavailable"
			}
			b.WriteString("  " + msg + "\n")
			continue
		}
		for _, window := range account.Windows {
			used := math.Max(0, math.Min(100, window.UsedPercent))
			filled := int(math.Round(used / 10))
			bar := strings.Repeat("█", filled) + strings.Repeat("░", 10-filled)
			reset := ""
			if window.ResetsAtUnix > 0 {
				reset = " · resets " + time.Unix(window.ResetsAtUnix, 0).Local().Format("Jan 2 15:04")
			}
			b.WriteString(fmt.Sprintf("  %-18s %s %5.1f%%%s\n", window.Label, bar, used, reset))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// costView renders provider subscription allowance followed by the local
// token/cost breakdown as a bordered modal card (spec §20.5).
func (m model) costView() string {
	groupBy := m.costGroupBy
	if len(groupBy) == 0 {
		groupBy = []string{"task"}
	}

	title := " ycc — usage"
	if m.costWorkspace != "" {
		title += " · " + m.costWorkspace
	}
	if m.costTask != "" {
		title += " · task " + m.costTask
	}
	title += " "

	hint := fmt.Sprintf("g group-by:%s · ↑/↓ select", groupBy[0])
	if m.costTask != "" {
		hint += " · esc back"
	} else {
		if groupBy[0] == "task" {
			if m.costCursor >= 0 && m.costCursor < len(m.costRows) && m.costRows[m.costCursor].Task == "" {
				hint += " · enter n/a for (unattributed)"
			} else {
				hint += " · enter breakdown"
			}
		}
		hint += " · esc close"
	}
	subText := subscriptionUsageTUI(m.subUsageAccounts)

	if len(m.costRows) == 0 {
		msg := m.costMsg
		if msg == "" {
			msg = "(no local usage recorded)"
		}
		if subText != "" {
			msg = subText + "\n\n" + dimStyle.Render(msg)
		} else {
			msg = dimStyle.Render(msg)
		}
		return m.modalCard(title, msg, hint)
	}

	// Build aligned columns with a tabwriter, then apply selection styling per
	// rendered line (the writer pads on raw widths, so style after flushing).
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 2, 2, ' ', 0)
	header := make([]string, 0, len(groupBy)+5)
	for _, d := range groupBy {
		header = append(header, costTitleTUI(d))
	}
	header = append(header, "Input", "Output", "Cache", "Total", "Cost")
	fmt.Fprintln(tw, "  "+strings.Join(header, "\t"))

	partial := false
	for _, r := range m.costRows {
		cells := make([]string, 0, len(groupBy)+5)
		for _, d := range groupBy {
			cells = append(cells, costGroupValue(r, d))
		}
		cache := r.CacheRead + r.CacheWrite
		cells = append(cells, commasTUI(r.Input), commasTUI(r.Output), commasTUI(cache), commasTUI(r.Total), costCellTUI(r))
		fmt.Fprintln(tw, "  "+strings.Join(cells, "\t"))
		if r.PriceStatus == "partial" {
			partial = true
		}
	}
	if total := m.costTotal; total != nil {
		cells := make([]string, len(groupBy))
		cells[0] = "TOTAL"
		cache := total.CacheRead + total.CacheWrite
		cells = append(cells, commasTUI(total.Input), commasTUI(total.Output), commasTUI(cache), commasTUI(total.Total), costCellTUI(total))
		fmt.Fprintln(tw, "  "+strings.Join(cells, "\t"))
		if total.PriceStatus == "partial" {
			partial = true
		}
	}
	tw.Flush()

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	headerLine, dataLines := lines[0], lines[1:]
	totalLine := ""
	if m.costTotal != nil && len(dataLines) > 0 {
		totalLine, dataLines = dataLines[len(dataLines)-1], dataLines[:len(dataLines)-1]
	}
	// Window the data rows around the cursor so the card never overruns the
	// terminal vertically (mirrors browserCard). Fixed chrome: modalCard's 6
	// non-content rows plus the pinned header, TOTAL, footnote, and subscription
	// allowance lines.
	budget := len(dataLines)
	if m.h > 0 {
		chrome := 6 + 1 // modalCard chrome + header row
		if totalLine != "" {
			chrome++
		}
		if partial {
			chrome++
		}
		if subText != "" {
			chrome += strings.Count(subText, "\n") + 3 // allowance + blank + local heading
		}
		budget = m.h - chrome
		if budget < 1 {
			budget = 1
		}
	}
	start, end := listWindow(m.costCursor, len(dataLines), budget)
	if start > 0 || end < len(dataLines) {
		hint = fmt.Sprintf("%s · %d–%d/%d", hint, start+1, end, len(dataLines))
	}
	var sb strings.Builder
	if subText != "" {
		sb.WriteString(subText + "\n\nLocal token usage\n")
	}
	sb.WriteString(dimStyle.Render(headerLine) + "\n")
	for i, line := range dataLines[start:end] {
		if start+i == m.costCursor {
			// Highlight the cursor row.
			sb.WriteString(selStyle.Render("▸"+line[1:]) + "\n")
			continue
		}
		sb.WriteString(line + "\n")
	}
	if totalLine != "" {
		sb.WriteString(dimStyle.Render(totalLine) + "\n")
	}
	if partial {
		sb.WriteString(dimStyle.Render("  * partial pricing (some models unpriced)") + "\n")
	}
	return m.modalCard(title, strings.TrimRight(sb.String(), "\n"), hint)
}
