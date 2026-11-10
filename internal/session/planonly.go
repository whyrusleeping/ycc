package session

import (
	"errors"
	"fmt"

	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
)

// ErrPlanOnlyMode rejects plan only outside chat: work and pm sessions have
// their own fixed workflow boundaries.
var ErrPlanOnlyMode = errors.New("plan only applies to chat sessions")

// SetPlanOnly turns the chat's plan-only guardrail on or off. The change is
// recorded at once and the tools enforce it from their next call; the
// coordinator is told at its next turn boundary (planOnlyNote). Setting the
// current value is a no-op.
func (s *Session) SetPlanOnly(on bool) error {
	if err := s.logFailure(); err != nil {
		return fmt.Errorf("session event log failed: %w", err)
	}
	if s.Mode != "chat" {
		return ErrPlanOnlyMode
	}
	s.planMu.Lock()
	defer s.planMu.Unlock()
	if s.planOnly == on {
		return nil
	}
	s.emitter.EmitAs("user", event.PlanOnlyChanged, map[string]any{"plan_only": on})
	if err := s.logFailure(); err != nil {
		return fmt.Errorf("session event log failed: %w", err)
	}
	s.planOnly = on
	if s.deps != nil {
		s.deps.SetPlanOnly(on)
	}
	return nil
}

// PlanOnly reports the session's current plan-only choice.
func (s *Session) PlanOnly() bool {
	s.planMu.Lock()
	defer s.planMu.Unlock()
	return s.planOnly
}

// planOnlyNote returns the note owed to the coordinator when its conversation
// was last told a different plan-only state, recording the delivery so replay
// places the note where the live loop posts it. A toggle undone before the
// next checkpoint owes nothing.
func (s *Session) planOnlyNote() ([]string, error) {
	s.planMu.Lock()
	defer s.planMu.Unlock()
	if s.planOnly == s.planOnlyTold {
		return nil, nil
	}
	text, err := s.recordPlanOnlyNoteLocked()
	if err != nil {
		return nil, err
	}
	return []string{text}, nil
}

func (s *Session) recordPlanOnlyNoteLocked() (string, error) {
	text := orchestrator.PlanOnlyNote(s.planOnly)
	s.emitter.EmitAs("user", event.PlanOnlyDelivered, map[string]any{"plan_only": s.planOnly, "text": text})
	if err := s.logFailure(); err != nil {
		return "", fmt.Errorf("session event log failed: %w", err)
	}
	s.planOnlyTold = s.planOnly
	return text, nil
}

// replanAfterContextReset re-tells a rolled-over conversation that plan only
// is on: the compact view starts from the default (off). Called right after
// the selected view replaced the loop's history, so replay appends the note
// to the summary exactly as here.
func (s *Session) replanAfterContextReset(loop *engine.Loop) error {
	s.planMu.Lock()
	defer s.planMu.Unlock()
	s.planOnlyTold = false
	if !s.planOnly {
		return nil
	}
	text, err := s.recordPlanOnlyNoteLocked()
	if err != nil {
		return err
	}
	loop.Post(text)
	return nil
}

// restorePlanOnly folds a replayed log into the session's plan-only state.
func (s *Session) restorePlanOnly(events []event.Event) {
	on, told := false, false
	for _, ev := range events {
		switch ev.Type {
		case event.PlanOnlyChanged:
			on, _ = ev.Data["plan_only"].(bool)
		case event.PlanOnlyDelivered:
			told, _ = ev.Data["plan_only"].(bool)
		case event.ContextViewChanged:
			if summary, _ := ev.Data["summary"].(string); summary != "" {
				told = false
			}
		}
	}
	s.planMu.Lock()
	s.planOnly, s.planOnlyTold = on, told
	s.planMu.Unlock()
	if s.deps != nil {
		s.deps.SetPlanOnly(on)
	}
}
