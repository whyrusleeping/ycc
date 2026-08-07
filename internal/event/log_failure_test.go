package event

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type faultLogFile struct {
	logFile
	writeErr error
	syncErr  error
}

func (f *faultLogFile) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.logFile.Write(p)
}

func (f *faultLogFile) Sync() error {
	if f.syncErr != nil {
		return f.syncErr
	}
	return f.logFile.Sync()
}

func installFaultFile(l *Log, writeErr, syncErr error) {
	l.mu.Lock()
	l.f = &faultLogFile{logFile: l.f, writeErr: writeErr, syncErr: syncErr}
	l.mu.Unlock()
}

func receiveTerminalFailure(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("subscription closed without terminal failure event")
		}
		if ev.Seq != 0 || !ev.Transient || ev.Type != SessionError {
			t.Fatalf("terminal event = %+v, want transient seq-0 session_error", ev)
		}
		if ev.Data["kind"] != "event_log" || !strings.Contains(ev.Data["msg"].(string), "event log persistence failed") {
			t.Fatalf("terminal failure data = %#v", ev.Data)
		}
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for terminal failure event")
		return Event{}
	}
}

func requireSubscriptionClosed(t *testing.T, ch <-chan Event) {
	t.Helper()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("subscription delivered an event after terminal failure")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscription did not close after terminal failure")
	}
}

func TestLogWriteFailureIsTerminalAndRestartSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	emit(t, l, 1, SessionStarted)

	// Subscribe before the failure. The durable history must still arrive before
	// the transient terminal error, even if the pump has not drained replay yet.
	ch, cancel := l.Subscribe(0)
	defer cancel()

	writeErr := errors.New("injected write failure")
	installFaultFile(l, writeErr, nil)
	callbacks := 0
	l.OnFailure(func(got error) {
		// Calling back into Log proves the owner hook is not invoked under l.mu.
		if !errors.Is(l.Err(), writeErr) || !errors.Is(got, writeErr) {
			t.Errorf("callback error = %v, Log.Err = %v", got, l.Err())
		}
		callbacks++
	})

	failed := l.Record("agent", ModelTurn, map[string]any{"text": "not durable"})
	if failed.Seq != 0 {
		t.Fatalf("failed Record seq = %d, want 0", failed.Seq)
	}
	if callbacks != 1 {
		t.Fatalf("failure callbacks = %d, want 1", callbacks)
	}
	if !errors.Is(l.Err(), writeErr) {
		t.Fatalf("Err = %v, want injected write error", l.Err())
	}
	if l.LastSeq() != 1 {
		t.Fatalf("LastSeq after failed append = %d, want 1", l.LastSeq())
	}
	if snap := l.Snapshot(); len(snap) != 1 || snap[0].Seq != 1 {
		t.Fatalf("Snapshot after failed append = %+v, want only seq 1", snap)
	}
	if again := l.Record("agent", ToolCall, nil); again.Seq != 0 {
		t.Fatalf("Record after terminal failure seq = %d, want 0", again.Seq)
	}
	if callbacks != 1 {
		t.Fatalf("failure callback repeated: got %d calls", callbacks)
	}

	replay := collect(t, ch, 1)
	if replay[0].Seq != 1 || replay[0].Transient {
		t.Fatalf("pre-failure replay = %+v, want durable seq 1", replay[0])
	}
	receiveTerminalFailure(t, ch)
	requireSubscriptionClosed(t, ch)

	// The failed write added no bytes. Reopen therefore resumes from the last
	// durable sequence rather than from the rejected candidate.
	l2, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	emit(t, l2, 2, ModelTurn)
}

func TestLogSyncFailureIsNotReportedAsRecorded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := l.Subscribe(0)
	defer cancel()

	syncErr := errors.New("injected sync failure")
	installFaultFile(l, nil, syncErr)
	failed := l.Record("agent", ModelTurn, map[string]any{"text": "not confirmed durable"})
	if failed.Seq != 0 {
		t.Fatalf("failed Record seq = %d, want 0", failed.Seq)
	}
	if !errors.Is(l.Err(), syncErr) {
		t.Fatalf("Err = %v, want injected sync error", l.Err())
	}
	if l.LastSeq() != 0 || len(l.Snapshot()) != 0 {
		t.Fatalf("sync-failed event exposed in memory: LastSeq=%d Snapshot=%+v", l.LastSeq(), l.Snapshot())
	}
	if again := l.Record("agent", ToolCall, nil); again.Seq != 0 {
		t.Fatalf("Record after sync failure seq = %d, want 0", again.Seq)
	}
	receiveTerminalFailure(t, ch)
	requireSubscriptionClosed(t, ch)
}

func TestLogEncodingFailureIsTerminal(t *testing.T) {
	l, err := OpenLog(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	failed := l.Record("agent", ModelTurn, map[string]any{"unsupported": func() {}})
	if failed.Seq != 0 {
		t.Fatalf("failed Record seq = %d, want 0", failed.Seq)
	}
	if l.Err() == nil || !strings.Contains(l.Err().Error(), "encode event") {
		t.Fatalf("Err = %v, want encoding failure", l.Err())
	}
	if l.LastSeq() != 0 || len(l.Snapshot()) != 0 {
		t.Fatalf("encoding-failed event exposed in memory: LastSeq=%d Snapshot=%+v", l.LastSeq(), l.Snapshot())
	}
}
