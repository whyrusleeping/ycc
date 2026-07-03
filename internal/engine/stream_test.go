package engine

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/tools"
)

// streamAttempt scripts one TurnStream call: the snapshots it feeds via onDelta
// (each is the FULL accumulated text so far) and the response/error it returns.
type streamAttempt struct {
	snaps []string
	resp  *gollama.ResponseMessageGenerate
	err   error
}

// scriptStreamTurner is a fake StreamTurner: each TurnStream call consumes the
// next scripted attempt, feeding its snapshots to onDelta before returning. Turn
// mirrors the same script for the non-streaming path.
type scriptStreamTurner struct {
	attempts    []streamAttempt
	n           int
	turnCalls   int
	streamCalls int
	beforeDelta func() // optional hook run before each onDelta (e.g. to sleep)
}

func (s *scriptStreamTurner) next() streamAttempt {
	a := s.attempts[s.n]
	if s.n < len(s.attempts)-1 {
		s.n++
	}
	return a
}

func (s *scriptStreamTurner) Turn(opts gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	s.turnCalls++
	a := s.next()
	return a.resp, a.err
}

func (s *scriptStreamTurner) TurnStream(opts gollama.RequestOptions, onDelta func(text string)) (*gollama.ResponseMessageGenerate, error) {
	s.streamCalls++
	a := s.attempts[s.n]
	if s.n < len(s.attempts)-1 {
		s.n++
	}
	for _, snap := range a.snaps {
		if s.beforeDelta != nil {
			s.beforeDelta()
		}
		onDelta(snap)
	}
	return a.resp, a.err
}

func newLoopWithRec(t *testing.T, turner Turner, rec event.Recorder) *Loop {
	t.Helper()
	reg := tools.New()
	reg.Add(tools.Worker(&tools.Workspace{Root: t.TempDir()})...)
	return &Loop{
		Client:  turner,
		Model:   "test",
		Tools:   reg,
		Emitter: event.NewEmitter(rec, "agent"),
	}
}

// collectDeltas subscribes to the log and gathers transient turn_delta events
// until cancelled. It returns a stop func that stops collection and returns the
// captured deltas.
func collectDeltas(t *testing.T, l *event.Log) func() []event.Event {
	t.Helper()
	ch, cancel := l.Subscribe(0)
	var mu sync.Mutex
	var deltas []event.Event
	done := make(chan struct{})
	go func() {
		for ev := range ch {
			if ev.Transient && ev.Type == event.TurnDelta {
				mu.Lock()
				deltas = append(deltas, ev)
				mu.Unlock()
			}
		}
		close(done)
	}()
	return func() []event.Event {
		// Let any in-flight transients drain, then deregister and wait.
		time.Sleep(80 * time.Millisecond)
		cancel()
		<-done
		mu.Lock()
		defer mu.Unlock()
		out := make([]event.Event, len(deltas))
		copy(out, deltas)
		return out
	}
}

// A streaming client lights up incremental turn_delta broadcasts carrying full
// snapshots, and the turn ends with a clearing done delta. None of it is
// persisted.
func TestLoopStreamsTurnDeltaSnapshots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := event.OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	stop := collectDeltas(t, l)

	client := &scriptStreamTurner{
		attempts: []streamAttempt{{
			snaps: []string{"Hel", "Hello world"},
			resp:  assistantText("Hello world"),
		}},
		// Sleep past the throttle interval so both snapshots are delivered.
		beforeDelta: func() { time.Sleep(turnDeltaInterval + 30*time.Millisecond) },
	}
	loop := newLoopWithRec(t, client, l)
	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Report != "Hello world" {
		t.Fatalf("report = %q, want %q", res.Report, "Hello world")
	}
	if client.streamCalls != 1 || client.turnCalls != 0 {
		t.Fatalf("streamCalls=%d turnCalls=%d, want stream path only", client.streamCalls, client.turnCalls)
	}

	deltas := stop()

	// Snapshots are full accumulated text; the first two carry text, the last is
	// the clearing done delta.
	var texts []string
	var sawDone bool
	for _, d := range deltas {
		if d.Seq != 0 || !d.Transient {
			t.Fatalf("delta not transient/seq0: %+v", d)
		}
		if done, _ := d.Data["done"].(bool); done {
			sawDone = true
			if txt, _ := d.Data["text"].(string); txt != "" {
				t.Fatalf("done delta carried text %q, want empty", txt)
			}
			continue
		}
		texts = append(texts, d.Data["text"].(string))
	}
	if !sawDone {
		t.Fatalf("no clearing done delta; deltas=%+v", deltas)
	}
	if len(texts) < 2 || texts[0] != "Hel" || texts[len(texts)-1] != "Hello world" {
		t.Fatalf("snapshot texts = %v, want first=Hel last=Hello world", texts)
	}

	// Deltas are never persisted (spec §5.2 / 0128 invariant).
	for _, ev := range l.Snapshot() {
		if ev.Type == event.TurnDelta {
			t.Fatalf("turn_delta leaked into in-memory replay: %+v", ev)
		}
	}
	onDisk, err := event.ReadLog(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range onDisk {
		if ev.Type == event.TurnDelta {
			t.Fatalf("turn_delta persisted to events.jsonl: %+v", ev)
		}
	}
}

// Rapid snapshots are throttled to at most ~1 per interval (plus the always-
// delivered first), collapsing a burst to a single non-done delta.
func TestLoopStreamThrottlesRapidSnapshots(t *testing.T) {
	l, err := event.OpenLog(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	stop := collectDeltas(t, l)

	client := &scriptStreamTurner{
		attempts: []streamAttempt{{
			snaps: []string{"a", "ab", "abc", "abcd", "abcde"},
			resp:  assistantText("abcde"),
		}},
		// No sleep: all snapshots arrive within one throttle window.
	}
	loop := newLoopWithRec(t, client, l)
	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	deltas := stop()
	var textDeltas int
	var first string
	for _, d := range deltas {
		if done, _ := d.Data["done"].(bool); done {
			continue
		}
		if textDeltas == 0 {
			first = d.Data["text"].(string)
		}
		textDeltas++
	}
	if textDeltas != 1 {
		t.Fatalf("got %d text deltas from a rapid burst, want 1 (throttled)", textDeltas)
	}
	if first != "a" {
		t.Fatalf("first delta = %q, want the first snapshot %q", first, "a")
	}
}

// A plain (non-streaming) Turner produces zero deltas and takes the Turn path.
func TestLoopPlainTurnerNoDeltas(t *testing.T) {
	l, err := event.OpenLog(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	stop := collectDeltas(t, l)

	client := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{assistantText("done")}}
	loop := newLoopWithRec(t, client, l)
	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Report != "done" {
		t.Fatalf("report = %q, want done", res.Report)
	}
	if client.calls != 1 {
		t.Fatalf("Turn calls = %d, want 1", client.calls)
	}
	if deltas := stop(); len(deltas) != 0 {
		t.Fatalf("plain Turner produced %d deltas, want 0", len(deltas))
	}
}

// A streaming turn that fails still clears the tail with a done delta and no
// stale streamed text survives; the error is surfaced as session_error.
func TestLoopStreamErrorClearsTail(t *testing.T) {
	l, err := event.OpenLog(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	stop := collectDeltas(t, l)

	client := &scriptStreamTurner{
		attempts: []streamAttempt{{
			snaps: []string{"partial"},
			err:   errors.New("boom: bad request"), // non-retryable
		}},
	}
	loop := newLoopWithRec(t, client, l)
	if _, err := loop.Run(context.Background()); err == nil {
		t.Fatal("Run: want error, got nil")
	}

	deltas := stop()
	var sawDone bool
	for _, d := range deltas {
		if done, _ := d.Data["done"].(bool); done {
			sawDone = true
		}
	}
	if !sawDone {
		t.Fatalf("no clearing done delta on error path; deltas=%+v", deltas)
	}
}

// When the recorder cannot broadcast (a plain non-Broadcaster recorder), a
// streaming client transparently falls back to the non-streaming Turn path.
func TestLoopStreamFallsBackWithoutBroadcaster(t *testing.T) {
	client := &scriptStreamTurner{
		attempts: []streamAttempt{{
			snaps: []string{"x"},
			resp:  assistantText("ok"),
		}},
	}
	// event.NewEmitter(nil, ...) yields a non-broadcasting emitter.
	loop := newLoopWithRec(t, client, nil)
	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if client.streamCalls != 0 || client.turnCalls != 1 {
		t.Fatalf("streamCalls=%d turnCalls=%d, want Turn fallback", client.streamCalls, client.turnCalls)
	}
}
