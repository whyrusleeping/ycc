package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobshim"
)

func readRestartState(t *testing.T, path string) restartState {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var st restartState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func writeTestRestartState(t *testing.T, path string, st restartState) {
	t.Helper()
	if st.Version == 0 {
		st.Version = restartStateVersion
	}
	if err := writeRestartState(path, &st); err != nil {
		t.Fatal(err)
	}
}

// recoveringManager is the next daemon's startup phase 3 over m's projects,
// with execution held: passive job supervision, lease restoration, and
// restart manifest recovery. The caller releases execution.
func recoveringManager(t *testing.T, m *Manager, launcher *jobshim.Launcher, path string, steps []*[2]string) *Manager {
	t.Helper()
	m2, _ := shutdownTestManager(t, steps)
	m2.SetProjects(m.projects)
	if launcher != nil {
		m2.SetJobLauncher(launcher)
	}
	m2.SetRestartStateFile(path)
	m2.HoldExecution()
	m2.SupervisePassiveJobs()
	m2.RestoreLeases()
	m2.RecoverRestart()
	return m2
}

func restartMarkers(evs []event.Event, restartID string) int {
	n := 0
	for _, ev := range evs {
		if ev.Type == event.SessionReopened && str(ev.Data, "restart_id") == restartID {
			n++
		}
	}
	return n
}

// A graceful restart names a session blocked in wait in the manifest
// (owner-only), and the next startup reopens it before execution is released,
// recording session_reopened with the restart id; the wait then resumes. A
// repeated startup with the same manifest (a crash after recovery) reopens
// nothing twice.
func TestRestartManifestReopensWaitingSession(t *testing.T) {
	launcher := supervisedTestLauncher(t)
	m, ws := shutdownTestManager(t, []*[2]string{
		{"Bash", `{"command":"sleep 1; echo survived","run_in_background":true}`},
		{"wait", `{"job_ids":["job_1"],"timeout_s":60}`},
	})
	m.SetJobLauncher(launcher)
	path := filepath.Join(t.TempDir(), "restart-state.json")
	m.SetRestartStateFile(path)
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(eventsOf(s, event.WaitStarted)) == 1 })
	m.ShutdownForRestart()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("no restart manifest: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("manifest mode = %v, want 0600", fi.Mode().Perm())
	}
	saved, _ := os.ReadFile(path)
	st := readRestartState(t, path)
	last := logEvents(t, ws, s.ID)
	if st.RestartID == "" || len(st.Waiting) != 1 || len(st.Interrupted) != 0 ||
		st.Waiting[0].SessionID != s.ID || st.Waiting[0].Kind != restartKindWait || st.Waiting[0].LastSeq != last[len(last)-1].Seq {
		t.Fatalf("manifest = %+v", st)
	}

	m2 := recoveringManager(t, m, launcher, path, nil)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("manifest left after recovery: %v", err)
	}
	s2, ok := m2.Get(s.ID)
	if !ok {
		t.Fatal("recovery did not reopen the waiting session")
	}
	evs := s2.Log().Snapshot()
	if n := restartMarkers(evs, st.RestartID); n != 1 || !boolVal(evs[len(evs)-1].Data, "wait_restored") {
		t.Fatalf("before release: %d restart markers, last event %v", n, evs[len(evs)-1])
	}
	m2.ReleaseExecution()
	waitLong(t, "the restored wait's turn to finish", func() bool { return len(eventsOf(s2, event.SessionIdle)) == 1 })
	if _, res := restoredWaitResult(s2.Log().Snapshot()); res == nil || !strings.Contains(str(res.Data, "result"), "survived") {
		t.Fatalf("restored wait result = %v", res)
	}
	if n := restartMarkers(s2.Log().Snapshot(), st.RestartID); n != 1 {
		t.Fatalf("%d session_reopened markers for the restart, want 1", n)
	}
	m2.ReclaimAll()

	// The same manifest again: the log already records this restart's reopen.
	if err := os.WriteFile(path, saved, 0o600); err != nil {
		t.Fatal(err)
	}
	m3 := recoveringManager(t, m, launcher, path, nil)
	if _, ok := m3.Get(s.ID); ok {
		t.Fatal("a repeated startup reopened the session again")
	}
	if n := restartMarkers(logEvents(t, ws, s.ID), st.RestartID); n != 1 {
		t.Fatalf("%d session_reopened markers after a repeated startup, want 1", n)
	}
}

// A session idle awaiting a supervised job is listed as waiting (idle) and
// reopens into wake-eligible idle; the job's completion wakes it.
func TestRestartManifestReopensAwaitingSession(t *testing.T) {
	launcher := supervisedTestLauncher(t)
	m, _ := shutdownTestManager(t, []*[2]string{
		{"Bash", `{"command":"sleep 1; echo survived","run_in_background":true}`},
	})
	m.SetJobLauncher(launcher)
	path := filepath.Join(t.TempDir(), "restart-state.json")
	m.SetRestartStateFile(path)
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		ev, ok := s.Log().LastByActor("coordinator", event.SessionIdle)
		return ok && boolVal(ev.Data, "awaiting_jobs")
	})
	m.ShutdownForRestart()
	st := readRestartState(t, path)
	if len(st.Waiting) != 1 || st.Waiting[0].Kind != restartKindIdle {
		t.Fatalf("manifest = %+v", st)
	}
	m2 := recoveringManager(t, m, launcher, path, nil)
	s2, ok := m2.Get(s.ID)
	if !ok || !s2.AwaitingJobs() {
		t.Fatalf("recovery: live=%v, want the session reopened awaiting its job", ok)
	}
	m2.ReleaseExecution()
	waitLong(t, "the survivor's completion to wake the session", func() bool {
		return len(eventsOf(s2, event.JobNotified)) == 1 && s2.Status() == event.StatusIdle && !s2.AwaitingJobs()
	})
}

// A session whose turn the shutdown cut short is listed as interrupted. Its
// history owes the model a response, so the reopen goes on with that turn,
// without a restart note.
func TestRestartManifestContinuesInterruptedTurn(t *testing.T) {
	m, _ := shutdownTestManager(t, []*[2]string{nil})
	path := filepath.Join(t.TempDir(), "restart-state.json")
	m.SetRestartStateFile(path)
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return s.Status() == event.StatusRunning && len(eventsOf(s, event.UserInput)) == 1 })
	m.ShutdownForRestart()
	st := readRestartState(t, path)
	if len(st.Interrupted) != 1 || st.Interrupted[0].SessionID != s.ID || len(st.Waiting) != 0 {
		t.Fatalf("manifest = %+v", st)
	}
	m2 := recoveringManager(t, m, nil, path, nil)
	s2, ok := m2.Get(s.ID)
	if !ok {
		t.Fatal("recovery did not reopen the interrupted session")
	}
	m2.ReleaseExecution()
	waitFor(t, func() bool { return len(eventsOf(s2, event.SessionIdle)) == 1 })
	evs := s2.Log().Snapshot()
	if restartMarkers(evs, st.RestartID) != 1 || countType(evs, event.RestartContext) != 0 {
		t.Fatalf("restart markers=%d restart_context=%d, want 1 and 0", restartMarkers(evs, st.RestartID), countType(evs, event.RestartContext))
	}
}

// interruptedWithoutPendingResponse leaves a closed session whose log ends on
// a coordinator's final text with no session_idle (the restart cut the turn
// after the model answered): its history owes no response. It returns the
// log's last seq.
func interruptedWithoutPendingResponse(t *testing.T) (*Manager, string, string, int) {
	t.Helper()
	m, ws := shutdownTestManager(t, nil)
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(eventsOf(s, event.SessionIdle)) == 1 })
	m.ReclaimAll()
	appendToLog(t, ws, s.ID, func(l *event.Log) {
		l.Record("user", event.UserInput, map[string]any{"text": "and then?"})
		l.Record("coordinator", event.ModelTurn, map[string]any{"text": "half an answer", "tool_calls": 0})
	})
	evs := logEvents(t, ws, s.ID)
	return m, ws, s.ID, evs[len(evs)-1].Seq
}

// An interrupted session whose history owes no response goes on from one
// synthetic restart_context note, recorded after its restart marker and
// before execution is released, and never as user input.
func TestRestartManifestNudgesInterruptedSessionOnce(t *testing.T) {
	m, ws, id, last := interruptedWithoutPendingResponse(t)
	path := filepath.Join(t.TempDir(), "restart-state.json")
	st := restartState{RestartID: "r1", Interrupted: []restartEntry{{Project: "proj", SessionID: id, LastSeq: last}}}
	writeTestRestartState(t, path, st)
	m2 := recoveringManager(t, m, nil, path, nil)
	s2, ok := m2.Get(id)
	if !ok {
		t.Fatal("recovery did not reopen the interrupted session")
	}
	evs := s2.Log().Snapshot()
	marker, note := indexOf(evs, isType(event.SessionReopened)), indexOf(evs, isType(event.RestartContext))
	if note < 0 || marker != note+1 || str(evs[note].Data, "restart_id") != "r1" || countType(evs, event.UserInput) != 2 {
		t.Fatalf("before release: restart_context at %d, marker at %d", note, marker)
	}
	m2.ReleaseExecution()
	waitFor(t, func() bool { return len(eventsOf(s2, event.SessionIdle)) == 2 })
	h := s2.currentLoop().History()
	found := false
	for _, msg := range h {
		if msg.Role == "user" && msg.Content == restartContextText {
			found = true
		}
	}
	if !found {
		t.Fatal("history lacks the restart note")
	}
	m2.ReclaimAll()

	// A crash after recovery replays the manifest: nothing is recorded again.
	writeTestRestartState(t, path, st)
	m3 := recoveringManager(t, m, nil, path, nil)
	if _, ok := m3.Get(id); ok {
		t.Fatal("a repeated startup reopened the session again")
	}
	if n := countType(logEvents(t, ws, id), event.RestartContext); n != 1 {
		t.Fatalf("%d restart_context events, want 1", n)
	}
}

// An entry whose log moved on past its last_seq (beyond records of job
// outcomes and lease releases) no longer authorizes a reopen.
func TestRestartManifestSkipsAdvancedLog(t *testing.T) {
	m, _, id, last := interruptedWithoutPendingResponse(t)
	path := filepath.Join(t.TempDir(), "restart-state.json")
	writeTestRestartState(t, path, restartState{RestartID: "r1", Interrupted: []restartEntry{{SessionID: id, LastSeq: last - 1}}})
	m2 := recoveringManager(t, m, nil, path, nil)
	if _, ok := m2.Get(id); ok {
		t.Fatal("recovery reopened a session whose log moved on")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("manifest left after recovery: %v", err)
	}
	if restartEntryConsumed([]event.Event{{Seq: 4, Type: event.ModelTurn}, {Seq: 5, Type: event.JobFinished}, {Seq: 6, Type: event.LeaseReleased}}, "r1", 4) != "" {
		t.Fatal("bookkeeping records counted as the log moving on")
	}
}

// An entry earlier startups kept failing to prepare is quarantined rather
// than tried again, and an unreadable manifest is moved aside; neither
// reopens anything.
func TestRestartManifestQuarantine(t *testing.T) {
	m, _, id, last := interruptedWithoutPendingResponse(t)
	path := filepath.Join(t.TempDir(), "restart-state.json")
	writeTestRestartState(t, path, restartState{RestartID: "r1",
		Interrupted: []restartEntry{{SessionID: id, LastSeq: last, Attempts: restartMaxAttempts}}})
	m2 := recoveringManager(t, m, nil, path, nil)
	if _, ok := m2.Get(id); ok {
		t.Fatal("recovery reopened a quarantined entry")
	}
	if data, err := os.ReadFile(path + ".quarantined"); err != nil || !strings.Contains(string(data), id) {
		t.Fatalf("quarantine record = %q, %v", data, err)
	}

	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	m3 := recoveringManager(t, m, nil, path, nil)
	if len(m3.List()) != 0 {
		t.Fatal("an unreadable manifest reopened sessions")
	}
	if _, err := os.Stat(path + ".unreadable"); err != nil {
		t.Fatalf("unreadable manifest not moved aside: %v", err)
	}
}

// After a crash there is no manifest, so startup reopens nothing and no model
// turn starts by itself; a session parked on ask_user is not listed either.
func TestNoManifestNoAutomaticTurns(t *testing.T) {
	m, ws := shutdownTestManager(t, []*[2]string{nil})
	path := filepath.Join(t.TempDir(), "restart-state.json")
	m.SetRestartStateFile(path)
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return s.Status() == event.StatusRunning })
	m.ReclaimAll() // as a crash would leave it: no graceful shutdown
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a manifest was written without a graceful shutdown: %v", err)
	}
	before := len(logEvents(t, ws, s.ID))
	m2 := recoveringManager(t, m, nil, path, nil)
	m2.ReleaseExecution()
	time.Sleep(100 * time.Millisecond)
	if len(m2.List()) != 0 || len(logEvents(t, ws, s.ID)) != before {
		t.Fatal("startup without a manifest reopened a session")
	}

	ma, _ := shutdownTestManager(t, []*[2]string{{"ask_user", `{"question":"which?","options":["a","b"]}`}})
	pa := filepath.Join(t.TempDir(), "restart-state.json")
	ma.SetRestartStateFile(pa)
	q, err := ma.Start(Config{Project: "proj", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, q.PendingQuestion)
	ma.ShutdownForRestart()
	if _, err := os.Stat(pa); !os.IsNotExist(err) {
		t.Fatalf("a session parked on ask_user was listed for reopening: %v", err)
	}
}

// A lease without a job survives a planned restart: suspension does not
// release it, the manifest carries it, and the next daemon holds it for the
// closed holder until a reopen takes it over. One that expired meanwhile is
// released once.
func TestRestartManifestCarriesUnboundLease(t *testing.T) {
	m, ws := shutdownTestManager(t, nil)
	path := filepath.Join(t.TempDir(), "restart-state.json")
	m.SetRestartStateFile(path)
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(eventsOf(s, event.SessionIdle)) == 1 })
	if _, _, err := m.acquireLease(s, ws, "deploy", "rolling out", time.Hour, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	m.ShutdownForRestart()
	if got := releasesIn(logEvents(t, ws, s.ID), "deploy"); len(got) != 0 {
		t.Fatalf("restart suspension released the lease: %v", got)
	}
	st := readRestartState(t, path)
	if len(st.UnboundLeases) != 1 || st.UnboundLeases[0].Name != "deploy" || st.UnboundLeases[0].SessionID != s.ID ||
		st.UnboundLeases[0].AcquiredSeq == 0 ||
		len(st.Waiting)+len(st.Interrupted) != 0 {
		t.Fatalf("manifest = %+v", st)
	}
	saved, _ := os.ReadFile(path)

	m2 := recoveringManager(t, m, nil, path, nil)
	m2.ReleaseExecution()
	held := m2.projectLeases(ws, time.Now())
	if len(held) != 1 || held[0].SessionID != s.ID {
		t.Fatalf("leases after restart = %+v", held)
	}
	other, err := m2.Start(Config{Project: "proj", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m2.acquireLease(other, ws, "deploy", "mine", time.Hour, "", time.Now()); err == nil {
		t.Fatal("another session took the carried lease")
	}
	if footer := m2.otherSessionsNote(ws, other.ID, time.Now()); !strings.Contains(footer, s.ID+" (chat, closed; it still holds a lease") {
		t.Fatalf("footer = %q", footer)
	}
	s2, err := m2.Reopen("proj", s.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Released and acquired anew: a replayed manifest must not bring the
	// old acquisition back over the new one.
	if _, err := m2.releaseLease(s2, ws, "deploy", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m2.acquireLease(s2, ws, "deploy", "again", 2*time.Hour, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := m2.Stop(s2.ID); err != nil {
		t.Fatal(err)
	}
	if got := releasesIn(logEvents(t, ws, s.ID), "deploy"); len(got) != 2 || got[0] != leaseReleased || got[1] != leaseSessionEnded {
		t.Fatalf("releases after the reopened holder stopped = %v", got)
	}
	// A replay while a newer acquisition, even one with the same expiry, is
	// (as far as the log shows) still held does not restore the old one.
	appendToLog(t, ws, s.ID, func(l *event.Log) {
		l.Record("coordinator", event.LeaseAcquired, map[string]any{"name": "deploy", "reason": "newer",
			"expires_at": st.UnboundLeases[0].ExpiresAt.UTC().Format(time.RFC3339)})
	})
	if err := os.WriteFile(path, saved, 0o600); err != nil {
		t.Fatal(err)
	}
	if mr := recoveringManager(t, m, nil, path, nil); len(mr.projectLeases(ws, time.Now())) != 0 {
		t.Fatal("a replayed manifest restored an old acquisition over a newer one")
	} else {
		mr.ReclaimAll()
	}
	appendToLog(t, ws, s.ID, func(l *event.Log) {
		l.Record("coordinator", event.LeaseReleased, map[string]any{"name": "deploy", "why": leaseSessionEnded})
	})
	m2.ReclaimAll()

	// A manifest a startup failed to remove is replayed by the next one: the
	// holder's log shows the lease released, so it is not held again.
	if err := os.WriteFile(path, saved, 0o600); err != nil {
		t.Fatal(err)
	}
	if mr := recoveringManager(t, m, nil, path, nil); len(mr.projectLeases(ws, time.Now())) != 0 {
		t.Fatal("a replayed manifest resurrected a released lease")
	} else {
		mr.ReclaimAll()
	}

	// A lease that expired while the daemon was down is released, recorded
	// once even when a later startup replays the manifest.
	past := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	acquired := 0
	appendToLog(t, ws, s.ID, func(l *event.Log) {
		acquired = l.Record("coordinator", event.LeaseAcquired, map[string]any{"name": "deploy", "reason": "short",
			"expires_at": past.Format(time.RFC3339)}).Seq
	})
	var st2 restartState
	if err := json.Unmarshal(saved, &st2); err != nil {
		t.Fatal(err)
	}
	st2.UnboundLeases[0].ExpiresAt, st2.UnboundLeases[0].AcquiredSeq = past, acquired
	for i := 0; i < 2; i++ {
		writeTestRestartState(t, path, st2)
		m3 := recoveringManager(t, m, nil, path, nil)
		m3.ReleaseExecution()
		if len(m3.projectLeases(ws, time.Now())) != 0 {
			t.Fatal("an expired lease was held again")
		}
		m3.ReclaimAll()
	}
	if got := releasesIn(logEvents(t, ws, s.ID), "deploy"); len(got) != 4 || got[3] != leaseExpired {
		t.Fatalf("releases = %v, want one expiry after the earlier three", got)
	}
}

// A startup cut off after recording the restart note but before the reopen
// marker leaves the entry authorized: the next startup reopens the session,
// which goes on from that note without a second one.
func TestRestartManifestResumesAfterNoteOnlyPrefix(t *testing.T) {
	m, ws, id, last := interruptedWithoutPendingResponse(t)
	path := filepath.Join(t.TempDir(), "restart-state.json")
	writeTestRestartState(t, path, restartState{RestartID: "r1", Interrupted: []restartEntry{{SessionID: id, LastSeq: last, Attempts: 1}}})
	appendToLog(t, ws, id, func(l *event.Log) {
		l.Record("coordinator", event.RestartContext, map[string]any{"text": restartContextText, "restart_id": "r1"})
	})
	m2 := recoveringManager(t, m, nil, path, nil)
	s2, ok := m2.Get(id)
	if !ok {
		t.Fatal("recovery did not reopen the session its note was recorded for")
	}
	m2.ReleaseExecution()
	waitFor(t, func() bool { return len(eventsOf(s2, event.SessionIdle)) == 2 })
	evs := s2.Log().Snapshot()
	if countType(evs, event.RestartContext) != 1 || restartMarkers(evs, "r1") != 1 {
		t.Fatalf("restart_context=%d markers=%d, want 1 each", countType(evs, event.RestartContext), restartMarkers(evs, "r1"))
	}
}

// A malformed entry is quarantined on its own; the others are recovered.
// An entry whose last_seq the log does not have is not acted on.
func TestRestartManifestMalformedEntryIsolated(t *testing.T) {
	m, _, id, last := interruptedWithoutPendingResponse(t)
	path := filepath.Join(t.TempDir(), "restart-state.json")
	data := `{"version":1,"restart_id":"r1","waiting_sessions":[{"session_id":"bad","last_seq":"x"}],` +
		`"interrupted_sessions":[{"session_id":"` + id + `","last_seq":` + strconv.Itoa(last) + `}],"unbound_leases":[]}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	m2 := recoveringManager(t, m, nil, path, nil)
	if _, ok := m2.Get(id); !ok {
		t.Fatal("a malformed entry kept a valid one from being recovered")
	}
	if q, err := os.ReadFile(path + ".quarantined"); err != nil || !strings.Contains(string(q), `"bad"`) {
		t.Fatalf("quarantine = %q, %v", q, err)
	}
	if why := restartEntryConsumed([]event.Event{{Seq: 1, Type: event.SessionStarted}}, "r1", 9); !strings.Contains(why, "no event at seq 9") {
		t.Fatalf("an entry past the log's end authorized a reopen: %q", why)
	}
}

// A lease listing during restart suspension, after the holder was cancelled,
// does not release the lease the manifest is about to carry.
func TestRestartSuspensionKeepsUnboundLeaseListed(t *testing.T) {
	m, ws := shutdownTestManager(t, nil)
	path := filepath.Join(t.TempDir(), "restart-state.json")
	m.SetRestartStateFile(path)
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(eventsOf(s, event.SessionIdle)) == 1 })
	if _, _, err := m.acquireLease(s, ws, "deploy", "rolling out", time.Hour, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	m.BeginShutdown()
	s.cancelForRestart()
	if held := m.projectLeases(ws, time.Now()); len(held) != 1 {
		t.Fatalf("leases while suspending = %v", held)
	}
	m.ShutdownForRestart()
	if st := readRestartState(t, path); len(st.UnboundLeases) != 1 {
		t.Fatalf("manifest = %+v", st)
	}
}

// A session sampled idle awaiting its jobs whose job wake recorded a
// delivery before suspension cut the new turn short is interrupted, not
// dropped.
func TestRestartEntryAwaitingWokenIsInterrupted(t *testing.T) {
	m, _ := shutdownTestManager(t, nil)
	l, err := event.OpenLog(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.Record("coordinator", event.SessionStarted, map[string]any{"mode": "chat"})
	l.Record("user", event.UserInput, map[string]any{"text": "go"})
	l.Record("coordinator", event.ModelTurn, map[string]any{"text": "started a job", "tool_calls": 0})
	l.Record("coordinator", event.SessionIdle, map[string]any{"report": "started a job", "awaiting_jobs": true})
	l.Record("coordinator", event.JobNotified, map[string]any{"id": "job_1", "text": "job_1 finished"})
	if _, interrupted, ok := m.restartEntryFor(restartCandidate{s: &Session{ID: "s_x", log: l}}); !ok || !interrupted {
		t.Fatalf("classified ok=%v interrupted=%v, want interrupted", ok, interrupted)
	}
}

// When the manifest cannot be written, the leases suspension kept for it end,
// recorded in their holders' logs.
func TestUnwrittenManifestEndsKeptLeases(t *testing.T) {
	m, ws := shutdownTestManager(t, nil)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	m.SetRestartStateFile(filepath.Join(blocker, "restart-state.json"))
	s, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(eventsOf(s, event.SessionIdle)) == 1 })
	if _, _, err := m.acquireLease(s, ws, "deploy", "rolling out", time.Hour, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	m.ShutdownForRestart()
	if got := releasesIn(logEvents(t, ws, s.ID), "deploy"); len(got) != 1 || got[0] != leaseSessionEnded {
		t.Fatalf("releases = %v, want session_ended", got)
	}
}

// A session whose turn ended in an error (a refusal, say) after it was sampled
// running is held for a human, not reopened as interrupted.
func TestRestartEntryErroredTurnStaysClosed(t *testing.T) {
	m, _ := shutdownTestManager(t, nil)
	l, err := event.OpenLog(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.Record("coordinator", event.SessionStarted, map[string]any{"mode": "chat"})
	l.Record("user", event.UserInput, map[string]any{"text": "go"})
	l.Record("coordinator", event.SessionError, map[string]any{"msg": "refused", "kind": "refusal"})
	if _, _, ok := m.restartEntryFor(restartCandidate{s: &Session{ID: "s_x", log: l}}); ok {
		t.Fatal("an errored turn was listed for reopening")
	}
}

// An entry already prepared for this restart is skipped before its attempts
// are counted, so it is never quarantined.
func TestPreparedRestartEntryNotQuarantined(t *testing.T) {
	m, ws, id, last := interruptedWithoutPendingResponse(t)
	path := filepath.Join(t.TempDir(), "restart-state.json")
	writeTestRestartState(t, path, restartState{RestartID: "r1", Interrupted: []restartEntry{{SessionID: id, LastSeq: last, Attempts: restartMaxAttempts}}})
	appendToLog(t, ws, id, func(l *event.Log) {
		l.Record("coordinator", event.SessionReopened, map[string]any{"restart_id": "r1"})
	})
	m2 := recoveringManager(t, m, nil, path, nil)
	if _, ok := m2.Get(id); ok {
		t.Fatal("an already prepared session was reopened again")
	}
	if _, err := os.Stat(path + ".quarantined"); !os.IsNotExist(err) {
		t.Fatalf("a prepared entry was quarantined: %v", err)
	}
}

// A session whose preparation could not be logged stays in the manifest
// alone: the generation's leases are not kept for a later startup, since
// admission opens and they may be released meanwhile.
func TestFailedPreparationKeepsOnlyItsEntry(t *testing.T) {
	m, ws, id, last := interruptedWithoutPendingResponse(t)
	path := filepath.Join(t.TempDir(), "restart-state.json")
	lease := leaseRecord{Primary: canonicalPath(ws), Name: "deploy", Reason: "r", SessionID: id, Workspace: ws,
		AcquiredAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	writeTestRestartState(t, path, restartState{RestartID: "r1",
		Interrupted: []restartEntry{{SessionID: id, LastSeq: last}}, UnboundLeases: []leaseRecord{lease}})
	m2, _ := shutdownTestManager(t, nil)
	m2.SetProjects(m.projects)
	m2.SetRestartStateFile(path)
	m2.HoldExecution()
	s, err := m2.Reopen("proj", id)
	if err != nil {
		t.Fatal(err)
	}
	// Every append after this fails, as on a full disk.
	s.emitter = event.NewEmitter(event.NewFuncRecorder(func(ev event.Event) {
		ev.Data["unencodable"] = func() {}
		s.log.Record(ev.Actor, ev.Type, ev.Data)
	}), "coordinator")
	m2.RecoverRestart()
	st := readRestartState(t, path)
	if len(st.Interrupted) != 1 || st.Interrupted[0].SessionID != id || st.Interrupted[0].Attempts != 1 || len(st.UnboundLeases) != 0 {
		t.Fatalf("kept manifest = %+v", st)
	}
}

// A subagent's failure, recorded under its own actor, neither holds an
// interrupted coordinator nor ends its wait.
func TestSubagentErrorDoesNotHoldCoordinator(t *testing.T) {
	m, _ := shutdownTestManager(t, nil)
	l, err := event.OpenLog(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.Record("coordinator", event.SessionStarted, map[string]any{"mode": "chat"})
	l.Record("user", event.UserInput, map[string]any{"text": "go"})
	l.Record("agent_1", event.SessionError, map[string]any{"msg": "subagent provider request failed"})
	if _, interrupted, ok := m.restartEntryFor(restartCandidate{s: &Session{ID: "s_x", log: l}}); !ok || !interrupted {
		t.Fatalf("classified ok=%v interrupted=%v, want interrupted", ok, interrupted)
	}
	evs := []event.Event{
		{Seq: 1, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"tool_calls": 1}},
		waitCall("call_1"), waitStarted("call_1"),
		{Seq: 9, Actor: "agent_1", Type: event.SessionError, Data: map[string]any{"msg": "x"}},
	}
	if findResumableWait(evs) == nil {
		t.Fatal("a subagent's error ended the coordinator's restorable wait")
	}
}
