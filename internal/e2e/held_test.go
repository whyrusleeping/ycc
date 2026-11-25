package e2e

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// TestE2EBacklogHeldBy covers the backlog's held_by contract (spec §9.1): a
// session that claimed a task and is parked on a question holds it, so
// ListBacklog and GetTask name it; once the session goes idle the hold ends.
// Session coordination stays off throughout.
func TestE2EBacklogHeldBy(t *testing.T) {
	e := newEnv(t, []scriptedTurn{
		{ToolCalls: []scriptedToolCall{{
			ID: "call_claim", Name: "update_task", Arguments: `{"task_id":"0001","status":"in_progress"}`,
		}}},
		{ToolCalls: []scriptedToolCall{{
			ID: "call_ask", Name: "ask_user", Arguments: `{"question":"Which cache size?","options":["small","large"]}`,
		}}},
		{Text: "Going with that.", Finish: "stop"},
	})
	e.startDaemon()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := e.client()
	if _, err := c.CreateTask(ctx, connect.NewRequest(&v1.CreateTaskRequest{Title: "Cache eviction"})); err != nil {
		t.Fatal(err)
	}

	p := e.spawn(e.environ("YCC_TOKEN="+e.token), "--addr", e.addr, "start", "--mode", "chat", "work on the cache task")
	id := p.sessionID()
	p.waitFor("question_asked")

	holder := func() *v1.TaskHolder {
		t.Helper()
		list, err := c.ListBacklog(ctx, connect.NewRequest(&v1.ListBacklogRequest{}))
		if err != nil || len(list.Msg.Tasks) != 1 {
			t.Fatalf("ListBacklog = %v, %v", list, err)
		}
		return list.Msg.Tasks[0].GetHeldBy()
	}
	if h := holder(); h.GetSessionId() != id || h.GetLoop() || h.GetTitle() == "" {
		t.Fatalf("held_by while the session waits = %+v, want session %s (not a loop session, titled)", h, id)
	}
	task, err := c.GetTask(ctx, connect.NewRequest(&v1.GetTaskRequest{Id: "0001"}))
	if err != nil || task.Msg.Task.GetHeldBy().GetSessionId() != id {
		t.Fatalf("GetTask held_by = %+v, %v", task.Msg.GetTask().GetHeldBy(), err)
	}

	e.answer(id, 0)
	p.waitFor("Going with that.")
	deadline := time.Now().Add(10 * time.Second)
	for holder() != nil {
		if time.Now().After(deadline) {
			t.Fatalf("idle session still holds the task: %+v", holder())
		}
		time.Sleep(100 * time.Millisecond)
	}
}
