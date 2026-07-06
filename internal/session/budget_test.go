package session

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
)

// newBudgetSession builds a Session backed by a real event log + registry so the
// spend guard (checkBudget) can reduce the session's own usage and emit events.
func newBudgetSession(t *testing.T, level string, cfg *config.Config) *Session {
	t.Helper()
	reg := config.NewRegistry(cfg)
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	lg, err := event.OpenLog(logPath)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	t.Cleanup(func() { lg.Close() })
	em := event.NewEmitter(lg, "coordinator")
	return &Session{
		ID:      "test",
		log:     lg,
		emitter: em,
		inter:   newInteraction(level, em),
		reg:     reg,
		inputCh: make(chan string, 4),
		status:  event.StatusRunning,
	}
}

// budgetConfig assembles a one-model config with the given caps. When priced the
// model gets $1000/Mtok input pricing (so 1000 input tokens = $1.00), otherwise
// it is unpriced (contributes tokens but no dollars).
func budgetConfig(b config.Budget, priced bool) *config.Config {
	m := config.Model{Backend: "ollama", BaseURL: "http://localhost:1", Model: "m"}
	if priced {
		p := 1000.0
		m.PriceInput = &p
	}
	return &config.Config{
		Models: map[string]config.Model{"a": m},
		Roles:  config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
		Budget: b,
	}
}

// spendTokens records a coordinator model_turn attributing n input+total tokens to
// model "a" so the guard's reduction sees them.
func (s *Session) spendTokens(n int) {
	s.emitter.Emit(event.ModelTurn, map[string]any{
		"model_name": "a",
		"usage":      event.Usage{Input: n, Total: n},
	})
}

func countEvents(s *Session, t event.Type) int {
	n := 0
	for _, ev := range s.log.Snapshot() {
		if ev.Type == t {
			n++
		}
	}
	return n
}

func lastLogEvent(s *Session, t event.Type) *event.Event {
	snap := s.log.Snapshot()
	for i := len(snap) - 1; i >= 0; i-- {
		if snap[i].Type == t {
			ev := snap[i]
			return &ev
		}
	}
	return nil
}

// (a) The ~80% warning fires exactly once even across repeated checkpoints.
func TestBudgetWarningOnce(t *testing.T) {
	s := newBudgetSession(t, "judgement", budgetConfig(config.Budget{SessionTokens: 1000}, false))
	s.spendTokens(800) // 80% of the token cap

	if msgs := s.checkBudget(context.Background()); msgs != nil {
		t.Fatalf("warning checkBudget returned msgs %v, want nil", msgs)
	}
	if n := countEvents(s, event.BudgetWarning); n != 1 {
		t.Fatalf("budget_warning count = %d, want 1", n)
	}
	// A second check at the same spend must not re-warn.
	s.checkBudget(context.Background())
	if n := countEvents(s, event.BudgetWarning); n != 1 {
		t.Fatalf("budget_warning count after re-check = %d, want 1", n)
	}
}

// (b) An autonomous breach injects the wrap-up instruction and records a
// budget_exceeded{action:"halt"} user-actor event, exactly once.
func TestBudgetAutonomousHalt(t *testing.T) {
	s := newBudgetSession(t, "autonomous", budgetConfig(config.Budget{SessionTokens: 1000}, false))
	s.spendTokens(1200) // over the cap

	msgs := s.checkBudget(context.Background())
	if len(msgs) != 1 {
		t.Fatalf("halt checkBudget returned %d msgs, want 1", len(msgs))
	}

	ev := lastLogEvent(s, event.BudgetExceeded)
	if ev == nil {
		t.Fatal("no budget_exceeded event recorded")
	}
	if ev.Actor != "user" {
		t.Fatalf("budget_exceeded actor = %q, want user (for replay)", ev.Actor)
	}
	if ev.Data["action"] != "halt" {
		t.Fatalf("budget_exceeded action = %v, want halt", ev.Data["action"])
	}
	if _, ok := ev.Data["text"].(string); !ok {
		t.Fatalf("budget_exceeded halt carries no text: %+v", ev.Data)
	}

	// Fires once: a second check injects nothing more.
	if got := s.checkBudget(context.Background()); got != nil {
		t.Fatalf("second checkBudget = %v, want nil", got)
	}
	if n := countEvents(s, event.BudgetExceeded); n != 1 {
		t.Fatalf("budget_exceeded count = %d, want 1", n)
	}
}

// (c) An attended breach raises a Confirm gate; declining halts.
func TestBudgetAttendedConfirmDecline(t *testing.T) {
	s := newBudgetSession(t, "judgement", budgetConfig(config.Budget{SessionTokens: 1000}, false))
	s.spendTokens(1000)

	type result struct{ msgs []string }
	done := make(chan result, 1)
	go func() { done <- result{s.checkBudget(context.Background())} }()

	waitPending(t, s.inter)
	if ok := s.inter.Answer("No"); !ok {
		t.Fatal("Answer(No) not accepted")
	}
	r := <-done
	if len(r.msgs) != 1 {
		t.Fatalf("declined breach returned %d msgs, want 1 (halt)", len(r.msgs))
	}
	ev := lastLogEvent(s, event.BudgetExceeded)
	if ev == nil || ev.Data["action"] != "halt" || ev.Actor != "user" {
		t.Fatalf("declined breach event = %+v, want user halt", ev)
	}
}

// (c) An attended breach confirmed with "yes" continues and does not re-ask.
func TestBudgetAttendedConfirmContinue(t *testing.T) {
	s := newBudgetSession(t, "judgement", budgetConfig(config.Budget{SessionTokens: 1000}, false))
	s.spendTokens(1000)

	done := make(chan []string, 1)
	go func() { done <- s.checkBudget(context.Background()) }()

	waitPending(t, s.inter)
	if ok := s.inter.Answer("Yes"); !ok {
		t.Fatal("Answer(Yes) not accepted")
	}
	if msgs := <-done; msgs != nil {
		t.Fatalf("confirmed breach returned %v, want nil", msgs)
	}
	ev := lastLogEvent(s, event.BudgetExceeded)
	if ev == nil || ev.Data["action"] != "continue" {
		t.Fatalf("confirmed breach event = %+v, want action continue", ev)
	}

	// Asked at most once: a further check does not raise a new question.
	before := countEvents(s, event.QuestionAsked)
	if got := s.checkBudget(context.Background()); got != nil {
		t.Fatalf("post-continue checkBudget = %v, want nil", got)
	}
	if after := countEvents(s, event.QuestionAsked); after != before {
		t.Fatalf("re-asked confirm: question_asked %d -> %d", before, after)
	}
}

// (d) An unpriced model never breaches a cost-only cap (no invented dollars), but
// a token cap is still enforced.
func TestBudgetUnpricedCostCapNoBreach(t *testing.T) {
	s := newBudgetSession(t, "autonomous", budgetConfig(config.Budget{SessionCost: 1.0}, false))
	s.spendTokens(10_000_000) // huge token spend, but the model is unpriced → $0

	if msgs := s.checkBudget(context.Background()); msgs != nil {
		t.Fatalf("unpriced cost-cap checkBudget = %v, want nil (no invented dollars)", msgs)
	}
	if n := countEvents(s, event.BudgetExceeded); n != 0 {
		t.Fatalf("budget_exceeded on unpriced cost cap = %d, want 0", n)
	}
}

func TestBudgetTokenCapEnforcedUnpriced(t *testing.T) {
	s := newBudgetSession(t, "autonomous", budgetConfig(config.Budget{SessionTokens: 1000}, false))
	s.spendTokens(2000)
	if msgs := s.checkBudget(context.Background()); len(msgs) != 1 {
		t.Fatalf("token cap breach returned %d msgs, want 1", len(msgs))
	}
}

// A priced model breaches a cost cap on real dollars.
func TestBudgetPricedCostCapBreach(t *testing.T) {
	s := newBudgetSession(t, "autonomous", budgetConfig(config.Budget{SessionCost: 1.0}, true))
	s.spendTokens(2000) // 2000 input tok @ $1000/Mtok = $2.00 > $1.00 cap
	if msgs := s.checkBudget(context.Background()); len(msgs) != 1 {
		t.Fatalf("priced cost breach returned %d msgs, want 1", len(msgs))
	}
}

// No caps configured → the guard is a cheap no-op.
func TestBudgetNoCapsNoop(t *testing.T) {
	s := newBudgetSession(t, "autonomous", budgetConfig(config.Budget{}, true))
	s.spendTokens(10_000_000)
	if msgs := s.checkBudget(context.Background()); msgs != nil {
		t.Fatalf("no-caps checkBudget = %v, want nil", msgs)
	}
	if n := countEvents(s, event.BudgetWarning) + countEvents(s, event.BudgetExceeded); n != 0 {
		t.Fatalf("no-caps emitted %d budget events, want 0", n)
	}
}
