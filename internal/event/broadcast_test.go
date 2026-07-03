package event

import (
	"path/filepath"
	"testing"
	"time"
)

// A live subscriber receives a Broadcast (transient) event with Seq==0 and
// Transient==true, and it is never persisted, never appears in the in-memory
// replay slice, Snapshot, ReadLog on disk, or a fresh Subscribe(0) replay.
func TestBroadcastTransientDeliveryNotPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	emit(t, l, 1, ModelTurn)

	ch, cancel := l.Subscribe(0)
	defer cancel()
	// Drain the replayed persisted event first.
	got := collect(t, ch, 1)
	if got[0].Seq != 1 || got[0].Transient {
		t.Fatalf("persisted replay = %+v, want seq 1 non-transient", got[0])
	}

	ev := l.Broadcast("agent", TurnDelta, map[string]any{"text": "hello"})
	if ev.Seq != 0 || !ev.Transient || ev.Type != TurnDelta {
		t.Fatalf("Broadcast returned %+v, want seq 0 transient turn_delta", ev)
	}

	live := collect(t, ch, 1)
	if live[0].Seq != 0 || !live[0].Transient || live[0].Type != TurnDelta {
		t.Fatalf("subscriber got %+v, want seq 0 transient turn_delta", live[0])
	}
	if live[0].Data["text"] != "hello" {
		t.Fatalf("transient data lost: %+v", live[0].Data)
	}

	// Never appended to the in-memory replay slice.
	if snap := l.Snapshot(); len(snap) != 1 || snap[0].Seq != 1 {
		t.Fatalf("Snapshot = %+v, want only persisted seq 1", snap)
	}

	// Never written to events.jsonl on disk.
	onDisk, err := ReadLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(onDisk) != 1 || onDisk[0].Seq != 1 {
		t.Fatalf("ReadLog = %+v, want only persisted seq 1", onDisk)
	}

	// A fresh subscriber never replays the transient.
	ch2, cancel2 := l.Subscribe(0)
	defer cancel2()
	got2 := collect(t, ch2, 1)
	if got2[0].Seq != 1 || got2[0].Transient {
		t.Fatalf("fresh replay = %+v, want only persisted seq 1", got2[0])
	}
}

// Interleaving Broadcast with Record must keep the persisted seq order intact and
// lose no persisted events; the transient is delivered without perturbing seqs.
func TestBroadcastInterleavedWithRecord(t *testing.T) {
	l, err := OpenLog(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	ch, cancel := l.Subscribe(0)
	defer cancel()

	emit(t, l, 1, ModelTurn)
	l.Broadcast("agent", TurnDelta, map[string]any{"i": 1})
	emit(t, l, 2, ToolCall)
	l.Broadcast("agent", TurnDelta, map[string]any{"i": 2})
	emit(t, l, 3, ModelTurn)

	// Collect all five (2 transient + 3 persisted). Order between a persisted
	// event and a concurrently-broadcast transient is not guaranteed, but the
	// persisted events must arrive in seq order and none may be dropped.
	got := collect(t, ch, 5)
	var seqs []int
	transients := 0
	for _, ev := range got {
		if ev.Transient {
			transients++
			if ev.Seq != 0 {
				t.Fatalf("transient carried seq %d, want 0", ev.Seq)
			}
			continue
		}
		seqs = append(seqs, ev.Seq)
	}
	if transients != 2 {
		t.Fatalf("got %d transients, want 2", transients)
	}
	if len(seqs) != 3 || seqs[0] != 1 || seqs[1] != 2 || seqs[2] != 3 {
		t.Fatalf("persisted seqs = %v, want [1 2 3] in order", seqs)
	}
}

// Broadcast on a closed log is a no-op: it delivers to nobody and never panics.
func TestBroadcastAfterCloseIsNoop(t *testing.T) {
	l, err := OpenLog(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	ev := l.Broadcast("agent", TurnDelta, nil)
	if !ev.Transient || ev.Seq != 0 {
		t.Fatalf("Broadcast on closed log = %+v, want transient seq 0", ev)
	}
	// Snapshot must still be empty.
	if snap := l.Snapshot(); len(snap) != 0 {
		t.Fatalf("Snapshot after closed Broadcast = %+v, want empty", snap)
	}
}

// A slow / never-draining subscriber must never wedge Broadcast (transients are
// enqueued under lock and dropped under backpressure, never sent synchronously),
// and Broadcast must return promptly.
func TestBroadcastDoesNotBlockOnSlowSubscriber(t *testing.T) {
	l, err := OpenLog(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// A subscriber that we never drain.
	_, cancel := l.Subscribe(0)
	defer cancel()

	done := make(chan struct{})
	go func() {
		for i := 0; i < transientQueueMax*4; i++ {
			l.Broadcast("agent", TurnDelta, map[string]any{"i": i})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Broadcast wedged on a slow subscriber")
	}
}

// A cancelled subscriber must not block Broadcast and must be deregistered so it
// stops receiving transients.
func TestBroadcastAfterCancelDeregisters(t *testing.T) {
	l, err := OpenLog(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	_, cancel := l.Subscribe(0)
	cancel()
	// Give the pump goroutine a moment to deregister on cancel.
	time.Sleep(50 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		l.Broadcast("agent", TurnDelta, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Broadcast wedged after subscriber cancelled")
	}

	l.mu.Lock()
	n := len(l.subs)
	l.mu.Unlock()
	if n != 0 {
		t.Fatalf("cancelled subscriber not deregistered: %d subs remain", n)
	}
}
