package event

import (
	"path/filepath"
	"testing"
	"time"
)

// emit records an event and asserts the assigned seq matches the expected one,
// so tests both drive the log and verify monotonic sequence assignment.
func emit(t *testing.T, l *Log, wantSeq int, typ Type) {
	t.Helper()
	ev := l.Record("agent", typ, nil)
	if ev.Seq != wantSeq {
		t.Fatalf("Record assigned seq %d, want %d", ev.Seq, wantSeq)
	}
}

func collect(t *testing.T, ch <-chan Event, n int) []Event {
	t.Helper()
	var got []Event
	for i := 0; i < n; i++ {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed after %d events, wanted %d", len(got), n)
			}
			got = append(got, ev)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out after %d/%d events", len(got), n)
		}
	}
	return got
}

func TestLogPersistAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	emit(t, l,1, SessionStarted)
	emit(t, l,2, ModelTurn)
	if l.LastSeq() != 2 {
		t.Fatalf("LastSeq = %d, want 2", l.LastSeq())
	}
	l.Close()

	// Reopening loads the persisted events so seq numbering continues.
	l2, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if l2.LastSeq() != 2 {
		t.Fatalf("after reopen LastSeq = %d, want 2", l2.LastSeq())
	}
}

// A late subscriber with from_seq replays only newer events, then receives live
// ones — the reconnect-from-offset guarantee.
func TestSubscribeReplayFromOffset(t *testing.T) {
	l, err := OpenLog(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for i := 1; i <= 5; i++ {
		emit(t, l,i, ModelTurn)
	}

	ch, cancel := l.Subscribe(3) // want seq 4,5 replayed
	defer cancel()
	got := collect(t, ch, 2)
	if got[0].Seq != 4 || got[1].Seq != 5 {
		t.Fatalf("replay seqs = %d,%d, want 4,5", got[0].Seq, got[1].Seq)
	}

	// Live event arrives after subscription.
	emit(t, l,6, ToolCall)
	live := collect(t, ch, 1)
	if live[0].Seq != 6 {
		t.Fatalf("live seq = %d, want 6", live[0].Seq)
	}
}

// A fresh subscriber (from_seq 0) sees the entire history in order.
func TestSubscribeFromZero(t *testing.T) {
	l, err := OpenLog(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for i := 1; i <= 3; i++ {
		emit(t, l,i, ModelTurn)
	}
	ch, cancel := l.Subscribe(0)
	defer cancel()
	got := collect(t, ch, 3)
	for i, ev := range got {
		if ev.Seq != i+1 {
			t.Fatalf("event %d seq = %d", i, ev.Seq)
		}
	}
}

func TestReduce(t *testing.T) {
	events := []Event{
		{Seq: 1, Type: SessionStarted, Data: map[string]any{"mode": "work", "workspace": "/w"}},
		{Seq: 2, Type: UserInput, Data: map[string]any{"text": "do it"}},
		{Seq: 3, Type: ModelTurn},
		{Seq: 4, Type: ToolCall},
		{Seq: 5, Type: ModelTurn},
		{Seq: 6, Type: SessionIdle, Data: map[string]any{"report": "done"}},
	}
	p := Reduce(events)
	if p.Mode != "work" || p.Workspace != "/w" {
		t.Fatalf("mode/workspace = %q/%q", p.Mode, p.Workspace)
	}
	if p.Turns != 2 || p.ToolCalls != 1 {
		t.Fatalf("turns/toolcalls = %d/%d, want 2/1", p.Turns, p.ToolCalls)
	}
	if p.Status != StatusIdle || p.LastReport != "done" || p.LastSeq != 6 {
		t.Fatalf("status/report/lastseq = %q/%q/%d", p.Status, p.LastReport, p.LastSeq)
	}
}
