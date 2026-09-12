package session

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/usage"
)

// newBudgetSession builds a Session backed by a real event log + registry so the
// spend guard (checkBudget) can reduce the session's own usage and emit events.
func newBudgetSession(t *testing.T, unattended bool, cfg *config.Config) *Session {
	t.Helper()
	reg := config.NewRegistry(cfg)
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	lg, err := event.OpenLog(logPath)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	t.Cleanup(func() { lg.Close() })
	return budgetSessionWithLog(unattended, reg, lg)
}

func budgetSessionWithLog(unattended bool, reg *config.Registry, lg *event.Log) *Session {
	em := event.NewEmitter(lg, "coordinator")
	return &Session{
		ID:         "test",
		log:        lg,
		emitter:    em,
		inter:      newInteraction(unattended, em),
		unattended: unattended,
		reg:        reg,
		messageCh:  make(chan engine.UserMessage, 4),
		status:     event.StatusRunning,
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
	s := newBudgetSession(t, false, budgetConfig(config.Budget{SessionTokens: 1000}, false))
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

// (b) An unattended breach injects the wrap-up instruction and records a
// budget_exceeded{action:"halt"} user-actor event, exactly once.
func TestBudgetAutonomousHalt(t *testing.T) {
	s := newBudgetSession(t, true, budgetConfig(config.Budget{SessionTokens: 1000}, false))
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
	s := newBudgetSession(t, false, budgetConfig(config.Budget{SessionTokens: 1000}, false))
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
	s := newBudgetSession(t, false, budgetConfig(config.Budget{SessionTokens: 1000}, false))
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
	s := newBudgetSession(t, true, budgetConfig(config.Budget{SessionCost: 1.0}, false))
	s.spendTokens(10_000_000) // huge token spend, but the model is unpriced → $0

	if msgs := s.checkBudget(context.Background()); msgs != nil {
		t.Fatalf("unpriced cost-cap checkBudget = %v, want nil (no invented dollars)", msgs)
	}
	if n := countEvents(s, event.BudgetExceeded); n != 0 {
		t.Fatalf("budget_exceeded on unpriced cost cap = %d, want 0", n)
	}
}

func TestBudgetTokenCapEnforcedUnpriced(t *testing.T) {
	s := newBudgetSession(t, true, budgetConfig(config.Budget{SessionTokens: 1000}, false))
	s.spendTokens(2000)
	if msgs := s.checkBudget(context.Background()); len(msgs) != 1 {
		t.Fatalf("token cap breach returned %d msgs, want 1", len(msgs))
	}
}

// A priced model breaches a cost cap on real dollars.
func TestBudgetPricedCostCapBreach(t *testing.T) {
	s := newBudgetSession(t, true, budgetConfig(config.Budget{SessionCost: 1.0}, true))
	s.spendTokens(2000) // 2000 input tok @ $1000/Mtok = $2.00 > $1.00 cap
	if msgs := s.checkBudget(context.Background()); len(msgs) != 1 {
		t.Fatalf("priced cost breach returned %d msgs, want 1", len(msgs))
	}
}

// No caps configured → the guard is a cheap no-op.
func TestBudgetNoCapsNoop(t *testing.T) {
	s := newBudgetSession(t, true, budgetConfig(config.Budget{}, true))
	s.spendTokens(10_000_000)
	if msgs := s.checkBudget(context.Background()); msgs != nil {
		t.Fatalf("no-caps checkBudget = %v, want nil", msgs)
	}
	if n := countEvents(s, event.BudgetWarning) + countEvents(s, event.BudgetExceeded); n != 0 {
		t.Fatalf("no-caps emitted %d budget events, want 0", n)
	}
}

func referenceBudgetTotal(s *Session) usage.Row {
	entries := usage.ReduceEvents(s.ID, s.log.Snapshot())
	return usage.Aggregate(entries, s.reg, usage.Options{}).Total
}

func requireBudgetTotal(t *testing.T, got, want usage.Row) {
	t.Helper()
	if got.Tokens != want.Tokens || math.Abs(got.Cost-want.Cost) > 1e-12 || got.Status != want.Status {
		t.Fatalf("incremental total = %+v, reference = %+v", got, want)
	}
}

func TestIncrementalBudgetMatchesReferenceAcrossActorsAndCheckpoints(t *testing.T) {
	s := newBudgetSession(t, true, budgetConfig(config.Budget{SessionTokens: 1_000_000}, true))
	s.emitter.EmitAs("coordinator", event.ModelTurn, map[string]any{
		"model_name": "a",
		"usage":      event.Usage{Input: 100, Output: 20, CacheRead: 30, Total: 150},
	})
	s.emitter.EmitAs("implementer", event.ModelTurn, map[string]any{
		"model_name": "a",
		"usage": map[string]any{
			"input": float64(200), "output": float64(40), "cache_write": float64(10), "total": float64(250),
		},
	})
	// Legacy turns may be missing model_name. They still contribute tokens and
	// remain unpriced, exactly as in the full usage reduction.
	s.emitter.EmitAs("reviewer:legacy", event.ModelTurn, map[string]any{
		"usage": map[string]any{"input": int64(7), "total": int64(7)},
	})

	requireBudgetTotal(t, s.incrementalBudgetTotal(), referenceBudgetTotal(s))
	cursor := s.budgetCursor
	// Unchanged checkpoints must neither rescan nor count any actor twice.
	requireBudgetTotal(t, s.incrementalBudgetTotal(), referenceBudgetTotal(s))
	if s.budgetCursor != cursor {
		t.Fatalf("unchanged checkpoint advanced cursor %d -> %d", cursor, s.budgetCursor)
	}

	s.emitter.EmitAs("reviewer:a", event.ModelTurn, map[string]any{
		"model_name": "a",
		"usage":      &event.Usage{Input: 50, Output: 5, Total: 55},
	})
	requireBudgetTotal(t, s.incrementalBudgetTotal(), referenceBudgetTotal(s))
	requireBudgetTotal(t, s.incrementalBudgetTotal(), referenceBudgetTotal(s))
}

func TestIncrementalBudgetReopenReconstructsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	cfg := budgetConfig(config.Budget{SessionTokens: 1_000_000}, true)
	reg := config.NewRegistry(cfg)

	lg, err := event.OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	s := budgetSessionWithLog(true, reg, lg)
	s.spendTokens(100)
	s.emitter.EmitAs("implementer", event.ModelTurn, map[string]any{
		"model_name": "a", "usage": event.Usage{Input: 200, Total: 200},
	})
	s.emitter.EmitAs("reviewer:legacy", event.ModelTurn, map[string]any{
		"usage": map[string]any{"total": 9}, // no model or token-class metadata
	})
	want := referenceBudgetTotal(s)
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}

	reopenedLog, err := event.OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedLog.Close()
	reopened := budgetSessionWithLog(true, reg, reopenedLog)
	events := reopenedLog.Snapshot()
	reopened.seedBudgetFromLog(events)
	if reopened.budgetCursor != len(events) {
		t.Fatalf("reopen cursor = %d, want %d", reopened.budgetCursor, len(events))
	}
	requireBudgetTotal(t, reopened.incrementalBudgetTotal(), want)
	requireBudgetTotal(t, reopened.incrementalBudgetTotal(), want)

	reopened.spendTokens(50)
	requireBudgetTotal(t, reopened.incrementalBudgetTotal(), referenceBudgetTotal(reopened))
}

func TestIncrementalBudgetThresholdCrossingAfterRepeatedChecks(t *testing.T) {
	s := newBudgetSession(t, true, budgetConfig(config.Budget{SessionTokens: 1000}, false))
	s.spendTokens(799)
	for i := 0; i < 3; i++ {
		if msgs := s.checkBudget(context.Background()); msgs != nil {
			t.Fatalf("check %d before warning = %v", i, msgs)
		}
	}
	if got := countEvents(s, event.BudgetWarning); got != 0 {
		t.Fatalf("warnings before threshold = %d, want 0", got)
	}

	s.spendTokens(1)
	if msgs := s.checkBudget(context.Background()); msgs != nil {
		t.Fatalf("warning check returned %v", msgs)
	}
	if got := countEvents(s, event.BudgetWarning); got != 1 {
		t.Fatalf("warnings at threshold = %d, want 1", got)
	}

	s.spendTokens(200)
	if msgs := s.checkBudget(context.Background()); len(msgs) != 1 {
		t.Fatalf("breach check returned %d messages, want 1", len(msgs))
	}
	if got := countEvents(s, event.BudgetExceeded); got != 1 {
		t.Fatalf("breaches at threshold = %d, want 1", got)
	}
}

var benchmarkBudgetTotal usage.Row

func BenchmarkBudgetCheckLargeHistory(b *testing.B) {
	const eventCount = 20_000
	path := filepath.Join(b.TempDir(), "events.jsonl")
	var data bytes.Buffer
	enc := json.NewEncoder(&data)
	for i := 1; i <= eventCount; i++ {
		ev := event.Event{
			Seq:   i,
			Actor: []string{"coordinator", "implementer", "reviewer:a"}[i%3],
			Type:  event.ModelTurn,
			Data: map[string]any{
				"model_name": "a",
				"usage":      event.Usage{Input: 1, Total: 1},
			},
		}
		if err := enc.Encode(ev); err != nil {
			b.Fatal(err)
		}
	}
	if err := os.WriteFile(path, data.Bytes(), 0o600); err != nil {
		b.Fatal(err)
	}
	lg, err := event.OpenLog(path)
	if err != nil {
		b.Fatal(err)
	}
	defer lg.Close()
	reg := config.NewRegistry(budgetConfig(config.Budget{SessionTokens: 1_000_000}, true))
	s := budgetSessionWithLog(true, reg, lg)
	events := lg.Snapshot()
	s.seedBudgetFromLog(events)

	b.Run("incremental_unchanged_checkpoint", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if msgs := s.checkBudget(context.Background()); msgs != nil {
				b.Fatal(msgs)
			}
		}
	})
	b.Run("reference_full_reduction", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			benchmarkBudgetTotal = usage.Aggregate(usage.ReduceEvents(s.ID, events), reg, usage.Options{}).Total
		}
	})
}
