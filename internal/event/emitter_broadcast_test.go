package event

import (
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

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
