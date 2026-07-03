package event

import (
	"path/filepath"
	"testing"
)

// Emitter.Broadcast no-ops (ok=false) when the underlying Recorder is not a
// Broadcaster (e.g. StdoutRecorder / FuncRecorder / nil), and never persists.
func TestEmitterBroadcastNoopsOnNonBroadcaster(t *testing.T) {
	cases := map[string]Recorder{
		"nil":    nil,
		"stdout": NewStdoutRecorder(discardWriter{}),
		"func":   NewFuncRecorder(func(Event) {}),
	}
	for name, rec := range cases {
		e := NewEmitter(rec, "agent")
		if e.CanBroadcast() {
			t.Fatalf("%s: CanBroadcast = true, want false", name)
		}
		ev, ok := e.Broadcast(TurnDelta, map[string]any{"text": "hi"})
		if ok {
			t.Fatalf("%s: Broadcast ok = true, want false", name)
		}
		if !ev.Transient || ev.Seq != 0 || ev.Type != TurnDelta || ev.Actor != "agent" {
			t.Fatalf("%s: fallback event = %+v, want transient seq0 turn_delta actor agent", name, ev)
		}
	}
}

// Emitter.Broadcast delivers via a *Log (a Broadcaster) and the transient is
// never persisted.
func TestEmitterBroadcastViaLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	ch, cancel := l.Subscribe(0)
	defer cancel()

	e := NewEmitter(l, "agent")
	if !e.CanBroadcast() {
		t.Fatal("CanBroadcast = false for *Log, want true")
	}
	ev, ok := e.Broadcast(TurnDelta, map[string]any{"text": "snap"})
	if !ok || !ev.Transient || ev.Seq != 0 {
		t.Fatalf("Broadcast = (%+v, %v), want transient seq0 ok", ev, ok)
	}

	got := collect(t, ch, 1)
	if got[0].Type != TurnDelta || got[0].Data["text"] != "snap" || !got[0].Transient {
		t.Fatalf("subscriber got %+v, want transient turn_delta snap", got[0])
	}

	if snap := l.Snapshot(); len(snap) != 0 {
		t.Fatalf("Snapshot = %+v, want empty (transient not persisted)", snap)
	}
	onDisk, err := ReadLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(onDisk) != 0 {
		t.Fatalf("ReadLog = %+v, want empty (transient not persisted)", onDisk)
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
