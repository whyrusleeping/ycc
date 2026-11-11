package orchestrator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
)

func TestReviewSubmissionEvidence(t *testing.T) {
	for _, verdict := range []string{"accept", "revise", "unknown"} {
		t.Run(verdict, func(t *testing.T) {
			ws := t.TempDir()
			repo, err := git.Open(ws)
			if err != nil {
				t.Fatal(err)
			}
			store := docs.NewStore(ws)
			if _, err := store.Create("task", "## Work log\n", 1, nil, nil); err != nil {
				t.Fatal(err)
			}
			var response *gollama.ResponseMessageGenerate
			if verdict == "unknown" {
				// A reviewer who never calls submit_review yields an unknown verdict.
				response = text("not sure")
			} else {
				findings := make([]finding, 12)
				for i := range findings {
					findings[i] = finding{Severity: "major", Message: strings.Repeat("x", 330)}
				}
				report, err := json.Marshal(map[string]any{"verdict": verdict, "summary": "summary", "findings": findings})
				if err != nil {
					t.Fatal(err)
				}
				response = call("submit_review", string(report))
			}
			turner := &scripted{resp: []*gollama.ResponseMessageGenerate{response}}
			rec := &captureRec{}
			d := &Deps{Workspace: ws, Docs: store, Repo: repo, Emitter: event.NewEmitter(rec, "coordinator"), Asker: noopAsker{},
				ReviewTier: func(string) ReviewPlan {
					return ReviewPlan{Tier: "standard", Specs: []AgentSpec{{Name: "rev", Model: "m", NewClient: func() engine.Turner { return turner }}}}
				}}
			_, err = spawnReviewers(d).Call(context.Background(), map[string]any{"task_id": "0001"})
			if err != nil {
				t.Fatal(err)
			}
			for _, ev := range rec.events {
				if ev.Type != event.ReviewSubmitted {
					continue
				}
				if ev.Data["verdict"] != verdict || ev.Data["reviewer"] != "rev" || ev.Data["logical_model"] != "rev" ||
					ev.Data["reviewed_snapshot_id"] == "" || ev.Data["reviewed_baseline_id"] == "" || turner.i != 1 {
					t.Fatalf("review evidence: %+v (turns=%d)", ev.Data, turner.i)
				}
				severity, ok := ev.Data["findings_by_severity"].(map[string]int)
				if !ok {
					t.Fatalf("missing severity counts: %+v", ev.Data)
				}
				items, ok := ev.Data["finding_items"].([]finding)
				if !ok {
					t.Fatalf("missing finding items: %+v", ev.Data)
				}
				if verdict == "unknown" {
					if ev.Data["summary"] != "not sure" || len(severity) != 0 || len(items) != 0 || ev.Data["findings"] != 0 {
						t.Fatalf("unknown review evidence: %+v", ev.Data)
					}
				} else {
					if severity["major"] != 12 || len(items) != 10 || ev.Data["findings"] != 12 ||
						items[0].Severity != "major" || utf8.RuneCountInString(items[0].Message) != 300 ||
						ev.Data["snapshot_id"] != ev.Data["reviewed_snapshot_id"] {
						t.Fatalf("bounded review evidence: %+v", ev.Data)
					}
				}
				return
			}
			t.Fatal("missing review submission")
		})
	}
}

// A direct work session that resumes an already in_progress task may never call
// update_task; reviewing it still focuses the task (0544) so the session title
// and loop record name it. re_review of the same task does not duplicate it.
func TestReviewEstablishesTaskFocus(t *testing.T) {
	ws := t.TempDir()
	repo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	store := docs.NewStore(ws)
	if _, err := store.CreateWithStatus("resumed task", "## Work log\n", 1, nil, nil, docs.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	turner := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("submit_review", `{"verdict":"revise","summary":"fix it"}`),
		call("submit_review", `{"verdict":"accept","summary":"ok"}`),
	}}
	rec := &captureRec{}
	d := &Deps{Workspace: ws, Docs: store, Repo: repo, Emitter: event.NewEmitter(rec, "coordinator"), Asker: noopAsker{},
		Reviewers: []AgentSpec{{Name: "rev", Model: "m", NewClient: func() engine.Turner { return turner }}}}
	ctx := context.Background()
	// An unpadded alias still focuses the canonical id.
	if _, err := spawnReviewers(d).Call(ctx, map[string]any{"task_id": "1"}); err != nil {
		t.Fatal(err)
	}
	if got := rec.focusTasks(); len(got) != 1 || got[0] != "0001" {
		t.Fatalf("focus after spawn_reviewers = %v, want [0001]", got)
	}
	if _, err := reReview(d).Call(ctx, map[string]any{"task_id": "0001"}); err != nil {
		t.Fatal(err)
	}
	if got := rec.focusTasks(); len(got) != 1 {
		t.Fatalf("re_review re-emitted focus for the same task: %v", got)
	}
}
