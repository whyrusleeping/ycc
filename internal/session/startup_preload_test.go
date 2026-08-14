package session

import (
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
)

// A work session that explicitly names one task enters its first provider turn
// with the routine backlog reads already answered. The synthetic events follow
// the real opening user input, so replay reconstructs byte-for-byte equivalent
// coordinator history.
func TestStartWorkPreloadsExplicitTaskIntoHistoryAndEvents(t *testing.T) {
	workspace := t.TempDir()
	store := docs.NewStore(workspace)
	task, err := store.Create("preloaded task", "## Description\n\nbody from backlog\n", 2, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	m := NewManager(testRegistry(), t.TempDir())
	defer m.ReclaimAll()
	s, err := m.Start(Config{
		Workspace: workspace,
		Mode:      "work",
		Prompt:    "Work on task " + task.ID + ": preloaded task.",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	live := s.currentLoop().History()
	if len(live) != 4 || live[0].Role != "user" || live[1].Role != "assistant" || len(live[1].ToolCalls) != 2 || live[2].Role != "tool" || live[3].Role != "tool" {
		t.Fatalf("live startup history = %+v", live)
	}
	if live[1].ToolCalls[0].Function.Name != "list_backlog" || live[1].ToolCalls[1].Function.Name != "get_task" {
		t.Fatalf("synthetic calls = %+v", live[1].ToolCalls)
	}

	deadline := time.Now().Add(2 * time.Second)
	var events []event.Event
	for time.Now().Before(deadline) {
		events = s.log.Snapshot()
		results := 0
		for _, ev := range events {
			if ev.Type == event.ToolResult && ev.Data["synthetic"] == true {
				results++
			}
		}
		if results == 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	s.Stop()

	var userSeq, syntheticTurnSeq int
	var syntheticCalls, syntheticResults int
	for _, ev := range events {
		if ev.Type == event.UserInput && ev.Actor == "user" {
			userSeq = ev.Seq
		}
		if ev.Type == event.ModelTurn && ev.Data["synthetic"] == true {
			syntheticTurnSeq = ev.Seq
		}
		if ev.Type == event.ToolCall && ev.Data["synthetic"] == true {
			syntheticCalls++
		}
		if ev.Type == event.ToolResult && ev.Data["synthetic"] == true {
			syntheticResults++
		}
	}
	if userSeq == 0 || syntheticTurnSeq <= userSeq || syntheticCalls != 2 || syntheticResults != 2 {
		t.Fatalf("event ordering/counts: user=%d turn=%d calls=%d results=%d events=%+v", userSeq, syntheticTurnSeq, syntheticCalls, syntheticResults, events)
	}

	replayed := engine.ReplayHistory(events)
	if len(replayed) < 4 {
		t.Fatalf("replayed history = %+v", replayed)
	}
	for i := 0; i < 4; i++ {
		if replayed[i].Role != live[i].Role || replayed[i].Content != live[i].Content || replayed[i].ToolCallID != live[i].ToolCallID {
			t.Fatalf("replay[%d] = %+v, live = %+v", i, replayed[i], live[i])
		}
	}
	if len(replayed[1].ToolCalls) != 2 || replayed[1].ToolCalls[0].Function.Name != "list_backlog" || replayed[1].ToolCalls[1].Function.Name != "get_task" {
		t.Fatalf("replayed synthetic calls = %+v", replayed[1].ToolCalls)
	}
}
