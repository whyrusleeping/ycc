package orchestrator

import (
	"context"
	"strings"
	"testing"
)

// recordAsker records the questions it is asked and returns canned answers.
type recordAsker struct {
	single []string
	batch  [][]Question
}

func (a *recordAsker) Ask(_ context.Context, q string, _ []string) (string, error) {
	a.single = append(a.single, q)
	return "single:" + q, nil
}

func (a *recordAsker) AskMany(_ context.Context, qs []Question) ([]string, error) {
	a.batch = append(a.batch, qs)
	out := make([]string, len(qs))
	for i, q := range qs {
		out[i] = "ans:" + q.Prompt
	}
	return out, nil
}

func (a *recordAsker) Confirm(context.Context, string) (bool, error) { return true, nil }

// ask_user with a `questions` array routes to AskMany and maps each answer to
// its question in the result.
func TestAskUserMultiQuestion(t *testing.T) {
	asker := &recordAsker{}
	d := &Deps{Asker: asker}
	res, err := askUser(d).Call(context.Background(), map[string]any{
		"questions": []any{
			map[string]any{"question": "db?", "options": []any{"postgres", "sqlite"}},
			map[string]any{"question": "name?"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Content)
	}
	if len(asker.batch) != 1 || len(asker.batch[0]) != 2 {
		t.Fatalf("AskMany not called as expected: %+v", asker.batch)
	}
	if len(asker.single) != 0 {
		t.Fatalf("Ask should not be called for multi-question form: %v", asker.single)
	}
	for _, want := range []string{"Q1: db?", "A1: ans:db?", "Q2: name?", "A2: ans:name?"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("result missing %q:\n%s", want, res.Content)
		}
	}
}

// Single-question form still routes to Ask (backward compatibility).
func TestAskUserSingleQuestion(t *testing.T) {
	asker := &recordAsker{}
	d := &Deps{Asker: asker}
	res, err := askUser(d).Call(context.Background(), map[string]any{"question": "proceed?"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if len(asker.single) != 1 || asker.single[0] != "proceed?" {
		t.Fatalf("Ask not called: %v", asker.single)
	}
	if res.Content != "single:proceed?" {
		t.Fatalf("content = %q", res.Content)
	}
}

// An empty questions list (after dropping blank prompts) is rejected, and a call
// with neither question nor questions is rejected.
func TestAskUserValidation(t *testing.T) {
	asker := &recordAsker{}
	d := &Deps{Asker: asker}

	// questions list with only blank prompts → validation error.
	res, _ := askUser(d).Call(context.Background(), map[string]any{
		"questions": []any{map[string]any{"question": "  "}},
	})
	if !res.IsError {
		t.Fatalf("expected error for blank-prompt questions, got %q", res.Content)
	}

	// neither question nor questions → validation error.
	res, _ = askUser(d).Call(context.Background(), map[string]any{})
	if !res.IsError {
		t.Fatalf("expected error for empty params, got %q", res.Content)
	}

	if len(asker.batch) != 0 || len(asker.single) != 0 {
		t.Fatal("asker should not be called on validation failure")
	}
}
