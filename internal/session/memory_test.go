package session

import (
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
)

func TestMemorySourceFromDurableEvents(t *testing.T) {
	base := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	events := []event.Event{
		{Seq: 10, TS: base, Actor: "user", Type: event.UserInput},
		{Seq: 11, TS: base.Add(time.Minute), Actor: "coordinator", Type: event.ToolResult},
		{Seq: 12, TS: base.Add(2 * time.Minute), Actor: "coordinator", Type: event.ToolCall},
	}

	guidance := memorySourceFromEvents("s_source", events, docs.MemoryUserGuidance)
	if guidance.SessionID != "s_source" || guidance.EventSeq != 10 || guidance.Actor != "user" || !guidance.EventTime.Equal(base) {
		t.Fatalf("user guidance provenance = %+v", guidance)
	}
	observation := memorySourceFromEvents("s_source", events, docs.MemoryObservation)
	if observation.EventSeq != 11 || observation.Actor != "coordinator" {
		t.Fatalf("observation provenance = %+v", observation)
	}
	inference := memorySourceFromEvents("s_source", events, docs.MemoryInference)
	if inference.EventSeq != 12 {
		t.Fatalf("inference provenance = %+v", inference)
	}
}
