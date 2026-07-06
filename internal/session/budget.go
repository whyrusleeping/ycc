package session

// Spend guard (task 0137, spec §20.6): converts the existing per-turn usage/cost
// telemetry into an enforced, optional ceiling. Session caps are checked at the
// engine's safe checkpoint (Session.Checkpoint) so a breach never kills a tool
// mid-write: the current turn/tool completes and the guard acts before the next
// turn. Absent config ([budget] unset) means every cap is 0 → this is a no-op and
// behaviour is exactly as before.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/usage"
)

// checkBudget enforces the configured per-session spend caps at a safe checkpoint.
// It returns any wrap-up instruction(s) to inject before the next turn (a
// graceful autonomous/loop halt), or nil. Behaviour:
//
//   - No session caps configured → cheap no-op.
//   - Spent >= ~80% of a configured cap and not yet warned → emit budget_warning
//     once (visible in the status bar / transcript) and keep going.
//   - Spent >= a configured cap and not yet handled:
//   - attended (non-autonomous): raise a Confirm gate. "yes" → record
//     budget_exceeded{action:"continue"} and continue (asked at most once);
//     "no" (or no human) → graceful halt.
//   - autonomous / loop / declined confirm → record budget_exceeded{action:
//     "halt"} as a USER-actor event carrying the wrap-up instruction (so reopen
//     replays it as a user message) and inject that instruction so the agent
//     brings the current task to a safe stopping point, then finishes.
//
// Unpriced models contribute tokens but no dollars (usage.Aggregate), so a
// cost-only cap never breaches on an unpriced model — matching §20.4's
// degrade-gracefully rule (no invented dollars). A token cap still applies.
func (s *Session) checkBudget(ctx context.Context) []string {
	if s.reg == nil {
		return nil // minimally-constructed session (tests) — no registry, no caps
	}
	caps := s.reg.Budget()
	if caps.SessionCost == 0 && caps.SessionTokens == 0 {
		return nil // no session caps configured — unlimited
	}

	s.mu.Lock()
	warned, breached := s.budgetWarned, s.budgetBreached
	s.mu.Unlock()
	if breached {
		return nil // already handled once this session
	}

	entries := usage.ReduceEvents(s.ID, s.log.Snapshot())
	res := usage.Aggregate(entries, s.reg, usage.Options{})
	tokens := int64(res.Total.Tokens.Total)
	cost := res.Total.Cost

	// pct is the fraction of the tightest configured cap that has been spent.
	pct := 0.0
	if caps.SessionTokens > 0 {
		if p := float64(tokens) / float64(caps.SessionTokens); p > pct {
			pct = p
		}
	}
	if caps.SessionCost > 0 {
		if p := cost / caps.SessionCost; p > pct {
			pct = p
		}
	}

	data := func(action string) map[string]any {
		d := map[string]any{"tokens": tokens, "cost": cost, "pct": pct}
		if caps.SessionTokens > 0 {
			d["token_cap"] = caps.SessionTokens
		}
		if caps.SessionCost > 0 {
			d["cost_cap"] = caps.SessionCost
		}
		if action != "" {
			d["action"] = action
		}
		return d
	}

	switch {
	case pct >= 1.0:
		status := budgetStatus(tokens, cost, caps)
		if s.inter.Level() != "autonomous" {
			ok, err := s.inter.Confirm(ctx, "Session budget reached ("+status+") — continue past the budget?")
			if err != nil || ctx.Err() != nil {
				// ctx cancelled while asking (Confirm returns false,nil on cancel):
				// the session is stopping anyway. Don't set breached so a reopened
				// session re-checks cleanly.
				return nil
			}
			if ok {
				s.mu.Lock()
				s.budgetBreached = true
				s.mu.Unlock()
				s.emitter.Emit(event.BudgetExceeded, data("continue"))
				return nil
			}
			// declined → fall through to the graceful halt path
		}
		s.mu.Lock()
		s.budgetBreached = true
		s.mu.Unlock()
		instr := budgetHaltInstruction(status)
		d := data("halt")
		d["text"] = instr
		// Emit as actor "user" so reopen (engine.ReplayHistory) reconstructs it as
		// a user message — the wrap-up is durable in the log and history stays
		// valid on resume (mirrors job_notified).
		s.emitter.EmitAs("user", event.BudgetExceeded, d)
		return []string{instr}
	case pct >= 0.8 && !warned:
		s.mu.Lock()
		s.budgetWarned = true
		s.mu.Unlock()
		s.emitter.Emit(event.BudgetWarning, data(""))
		return nil
	}
	return nil
}

// budgetHaltInstruction is the user-role wrap-up message injected on a graceful
// budget halt: stop taking on new work and bring the current task to the nearest
// safe stopping point, then finish.
func budgetHaltInstruction(status string) string {
	return "Session budget reached (" + status + "). Stop taking on new work. Bring the current task to the " +
		"nearest safe stopping point: if it is essentially complete, finish and commit it; otherwise mark it " +
		"in_review or blocked (update_task) with a brief work-log note recording the budget halt. Then call finish."
}

// budgetStatus renders "<tokens> tokens (cap <cap>) / $<cost> (cap $<cap>)",
// including only the dimensions that have a cap configured.
func budgetStatus(tokens int64, cost float64, caps config.Budget) string {
	var parts []string
	if caps.SessionTokens > 0 {
		parts = append(parts, fmt.Sprintf("%s tokens (cap %s)", commaInt(tokens), commaInt(caps.SessionTokens)))
	}
	if caps.SessionCost > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f (cap $%.2f)", cost, caps.SessionCost))
	}
	return strings.Join(parts, " / ")
}

// seedBudgetFromLog pre-sets the warned/breached flags from a replayed event log
// so a reopened session that already crossed the line does not re-fire the
// warning or re-ask the Confirm gate (task 0137).
func (s *Session) seedBudgetFromLog(events []event.Event) {
	warned, breached := false, false
	for _, ev := range events {
		switch ev.Type {
		case event.BudgetWarning:
			warned = true
		case event.BudgetExceeded:
			breached = true
			warned = true
		}
	}
	s.mu.Lock()
	s.budgetWarned = warned
	s.budgetBreached = breached
	s.mu.Unlock()
}

// commaInt formats an integer with thousands separators (local helper so budget
// messages read naturally without exporting usage.commas).
func commaInt(n int64) string {
	s := strconv.FormatInt(n, 10)
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
