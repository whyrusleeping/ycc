package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

type contractStep struct {
	Event *struct {
		Seq       int64          `json:"seq"`
		Actor     string         `json:"actor"`
		Type      string         `json:"type"`
		Transient bool           `json:"transient"`
		TS        string         `json:"ts"`
		Data      map[string]any `json:"data"`
	} `json:"event"`
	Reconnect *struct{}                  `json:"reconnect"`
	Expect    map[string]json.RawMessage `json:"expect"`
}

func TestEventContract(t *testing.T) {
	files, err := filepath.Glob("../../testdata/event-contract/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("fixtures: %v (%d files)", err, len(files))
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var fixture struct {
				Version int            `json:"version"`
				Name    string         `json:"name"`
				Steps   []contractStep `json:"steps"`
			}
			if err := json.Unmarshal(raw, &fixture); err != nil {
				t.Fatal(err)
			}
			if fixture.Version != 1 || fixture.Name == "" {
				t.Fatalf("unsupported fixture: %+v", fixture)
			}
			m := readyStreamModel(t)
			for i, step := range fixture.Steps {
				if step.Event != nil {
					data, err := json.Marshal(step.Event.Data)
					if err != nil {
						t.Fatal(err)
					}
					m.applyLiveEvent(&v1.Event{Seq: step.Event.Seq, Actor: step.Event.Actor, Type: step.Event.Type, Transient: step.Event.Transient, Ts: step.Event.TS, DataJson: string(data)})
				} else if step.Reconnect != nil {
					m.liveTails = map[string]string{}
					m.retryNotes = map[string]string{}
				} else if step.Expect != nil {
					got := tuiContractFacts(&m)
					for key, want := range step.Expect {
						actual, ok := got[key]
						if !ok {
							t.Fatalf("step %d: unknown fact %s", i, key)
						}
						var expected any
						if err := json.Unmarshal(want, &expected); err != nil {
							t.Fatal(err)
						}
						// JSON round trip normalizes integer types for semantic comparison.
						buf, _ := json.Marshal(actual)
						var normalized any
						_ = json.Unmarshal(buf, &normalized)
						if !reflect.DeepEqual(normalized, expected) {
							t.Errorf("step %d %s: got %s want %s", i, key, buf, want)
						}
					}
				} else {
					t.Fatalf("step %d: empty step", i)
				}
			}
		})
	}
}

func tuiContractFacts(m *model) map[string]any {
	phase := m.status
	switch phase {
	case "paused", "idle", "error", "stopped":
	default:
		phase = "running"
	}
	var pending any
	if m.wizActive {
		prompts := []string{}
		opts := [][]string{}
		for _, q := range m.wizQuestions {
			prompts = append(prompts, q.prompt)
			options := append([]string{}, q.options...)
			opts = append(opts, options)
		}
		pending = map[string]any{"prompts": prompts, "options": opts}
	} else if m.pending != "" {
		pending = map[string]any{"prompts": []string{m.pending}, "options": [][]string{append([]string{}, m.pickerOpts...)}}
	}
	inputs := []any{}
	answers := []any{}
	reviews := []any{}
	for i, ev := range m.evs {
		switch ev.Type {
		case "user_input":
			delivery := "delivered"
			if dataField(ev, "queued") == "true" && !m.deliveredSeqs[ev.Seq] {
				delivery = "queued"
			}
			inputs = append(inputs, map[string]any{"seq": ev.Seq, "text": dataField(ev, "text"), "delivery": delivery})
		case "question_answered":
			questionSeq := int64(0)
			for j := i - 1; j >= 0; j-- {
				if m.evs[j].Actor == ev.Actor && m.evs[j].Type == "question_asked" {
					questionSeq = m.evs[j].Seq
					break
				}
			}
			provenance := "human"
			if dataField(ev, "auto") == "true" {
				provenance = "automatic"
			}
			answers = append(answers, map[string]any{"question_seq": questionSeq, "provenance": provenance})
		case "review_submitted":
			verdict := dataField(ev, "verdict")
			if verdict != "accept" && verdict != "revise" {
				verdict = "unknown"
			}
			reviews = append(reviews, map[string]any{"seq": ev.Seq, "verdict": verdict})
		}
	}
	tails := map[string]string{}
	for actor, text := range m.liveTails {
		tails[actor] = text
	}
	return map[string]any{"phase": phase, "pause_requested": m.pausePending, "cursor": m.lastSeq, "pending_question": pending, "inputs": inputs, "answers": answers, "reviews": reviews, "tails": tails}
}
