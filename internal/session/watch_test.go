package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/notify"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
)

// watchHarness drives the watch scheduler with a fake clock and fake run
// sessions; tests end runs by reporting through the real hook.
type watchHarness struct {
	t   *testing.T
	m   *Manager
	ws  string
	mu  sync.Mutex
	now time.Time
	ctx []string
	ses []*Session
}

func newWatchHarness(t *testing.T) *watchHarness {
	t.Helper()
	ws := t.TempDir()
	h := &watchHarness{t: t, m: NewManager(nil, ws), ws: ws, now: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)}
	h.m.watchNow = func() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return h.now }
	h.m.watchPoll = 5 * time.Millisecond
	h.m.startWatchSession = func(primary string, def WatchDefinition, runCtx string) (*Session, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		s := newStopSession(t)
		s.ID = fmt.Sprintf("s_run%d", len(h.ses)+1)
		s.Mode = watchMode
		s.Workspace = primary
		h.m.mu.Lock()
		h.m.sessions[s.ID] = s
		h.m.mu.Unlock()
		h.ctx = append(h.ctx, runCtx)
		h.ses = append(h.ses, s)
		return s, nil
	}
	t.Cleanup(h.m.ReclaimAll)
	return h
}

func (h *watchHarness) setNow(t time.Time) { h.mu.Lock(); h.now = t; h.mu.Unlock() }

func (h *watchHarness) runs() int { h.mu.Lock(); defer h.mu.Unlock(); return len(h.ses) }

func (h *watchHarness) run(i int) (*Session, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ses[i], h.ctx[i]
}

func (h *watchHarness) create(def WatchDefinition) {
	h.t.Helper()
	if def.Name == "" {
		def.Name = "live"
	}
	if def.Brief == "" {
		def.Brief = "check the live system"
	}
	if def.Interval == 0 {
		def.Interval = Duration(time.Hour)
	}
	if _, err := h.m.CreateWatch("", def); err != nil {
		h.t.Fatalf("CreateWatch: %v", err)
	}
}

// report ends run i through the watch_report hook and waits for the follower
// to finish it.
func (h *watchHarness) report(i int, r orchestrator.WatchReport) {
	h.t.Helper()
	s, _ := h.run(i)
	if _, err := h.m.recordWatchReport(h.ws, s.ID, r); err != nil {
		h.t.Fatalf("recordWatchReport: %v", err)
	}
	s.setStatus(event.StatusIdle)
	waitFor(h.t, func() bool {
		h.m.watchMu.Lock()
		defer h.m.watchMu.Unlock()
		return !h.m.watchFollowed[s.ID]
	})
}

func (h *watchHarness) waitIdle() {
	h.t.Helper()
	waitFor(h.t, func() bool {
		h.m.watchMu.Lock()
		defer h.m.watchMu.Unlock()
		return len(h.m.watchBusy) == 0 && len(h.m.watchFollowed) == 0
	})
}

func (h *watchHarness) record() watchRecord {
	h.t.Helper()
	rec, err := loadWatch(h.ws, "live")
	if err != nil {
		h.t.Fatal(err)
	}
	return rec
}

func (h *watchHarness) reports() []WatchReportRecord {
	h.t.Helper()
	rs, err := h.m.WatchReports("", "live", 0)
	if err != nil {
		h.t.Fatal(err)
	}
	return rs // newest first
}

// Due times stay on the schedule's grid regardless of when runs happen, and a
// long outage produces one catch-up run that states the gap.
func TestWatchScheduleDoesNotDriftAndCollapsesMissedRuns(t *testing.T) {
	h := newWatchHarness(t)
	t0 := h.now
	h.create(WatchDefinition{})

	h.m.tickWatches()
	waitFor(t, func() bool { return h.runs() == 1 })
	h.report(0, orchestrator.WatchReport{Status: "ok", Summary: "fine", Notes: "baseline lag 2s"})
	if got := h.record().NextDue; !got.Equal(t0.Add(time.Hour)) {
		t.Fatalf("NextDue after first run = %v, want %v", got, t0.Add(time.Hour))
	}

	// A late tick (10 minutes after due) still schedules the next run on the grid.
	h.setNow(t0.Add(70 * time.Minute))
	h.m.tickWatches()
	waitFor(t, func() bool { return h.runs() == 2 })
	if _, ctx := h.run(1); !strings.Contains(ctx, "baseline lag 2s") || !strings.Contains(ctx, "10m0s late") {
		t.Fatalf("second run context lacks notes or lateness:\n%s", ctx)
	}
	h.report(1, orchestrator.WatchReport{Status: "ok", Summary: "fine", Notes: "n"})
	if got := h.record().NextDue; !got.Equal(t0.Add(2 * time.Hour)) {
		t.Fatalf("NextDue drifted: %v, want %v", got, t0.Add(2*time.Hour))
	}

	// Daemon down from 2h to 5h30m: one catch-up run, then back on the grid.
	h.setNow(t0.Add(5*time.Hour + 30*time.Minute))
	h.m.tickWatches()
	h.m.tickWatches()
	waitFor(t, func() bool { return h.runs() == 3 })
	if _, ctx := h.run(2); !strings.Contains(ctx, "3 scheduled run(s) did not happen") {
		t.Fatalf("catch-up context does not state the gap:\n%s", ctx)
	}
	if got := h.record().NextDue; !got.Equal(t0.Add(6 * time.Hour)) {
		t.Fatalf("NextDue after catch-up = %v, want %v", got, t0.Add(6*time.Hour))
	}
	if h.runs() != 3 {
		t.Fatalf("runs = %d, want exactly one catch-up run", h.runs())
	}
}

// An active run makes the next due run a recorded skip; a run parked on a user
// question does not hold the schedule, and later runs see its question.
func TestWatchQuestionParksRunWithoutBlockingSchedule(t *testing.T) {
	h := newWatchHarness(t)
	t0 := h.now
	h.create(WatchDefinition{})
	h.m.tickWatches()
	waitFor(t, func() bool { return h.runs() == 1 })

	h.setNow(t0.Add(time.Hour))
	h.m.tickWatches()
	if rs := h.reports(); len(rs) != 1 || rs[0].Kind != WatchReportSkipped {
		t.Fatalf("reports = %+v, want one skipped", rs)
	}
	if h.runs() != 1 {
		t.Fatalf("a second run started while the first was active")
	}

	// The first run asks the user and parks.
	s1, _ := h.run(0)
	answered := make(chan string, 1)
	go func() {
		ans, _ := s1.inter.Ask(context.Background(), "Disk is 99% full. Lower retention?", []string{"yes", "no"})
		answered <- ans
	}()
	waitFor(t, func() bool { return s1.PendingQuestion() })
	waitFor(t, func() bool {
		h.m.watchMu.Lock()
		defer h.m.watchMu.Unlock()
		return len(h.m.watchBusy) == 0
	})

	h.setNow(t0.Add(2 * time.Hour))
	h.m.tickWatches()
	waitFor(t, func() bool { return h.runs() == 2 })
	if _, ctx := h.run(1); !strings.Contains(ctx, "Lower retention?") || !strings.Contains(ctx, "do not ask again") {
		t.Fatalf("second run is not told about the pending question:\n%s", ctx)
	}
	h.report(1, orchestrator.WatchReport{Status: "warn", Summary: "disk still full; question pending", Notes: "n2"})

	// The answer resumes the parked run, which reports; the daemon records the
	// decision for later runs.
	if !s1.inter.Answer("yes, lower it to 48h") {
		t.Fatal("answer was not delivered")
	}
	<-answered
	h.report(0, orchestrator.WatchReport{Status: "ok", Summary: "lowered retention", Notes: "n3",
		Actions: []orchestrator.WatchAction{{Action: "set raw-v2 retention to 48h", Undo: "set it back to 14d"}}})

	rec := h.record()
	if len(rec.Decisions) != 1 || !strings.Contains(rec.Decisions[0].Answer, "48h") {
		t.Fatalf("decisions = %+v, want the user's answer", rec.Decisions)
	}
	if rec.Last == nil || rec.Last.Status != "warn" {
		t.Fatalf("a run that acted reported %+v, want status raised to warn", rec.Last)
	}
	h.setNow(t0.Add(3 * time.Hour))
	h.m.tickWatches()
	waitFor(t, func() bool { return h.runs() == 3 })
	if _, ctx := h.run(2); !strings.Contains(ctx, "lower it to 48h") || strings.Contains(ctx, "do not ask again") {
		t.Fatalf("third run context should carry the decision and no pending question:\n%s", ctx)
	}
}

// A passing probe records ok without a model run; a failing one starts a run
// that sees the probe output.
func TestWatchProbeGatesModelRun(t *testing.T) {
	h := newWatchHarness(t)
	t0 := h.now
	marker := filepath.Join(h.ws, "fail")
	h.create(WatchDefinition{Probe: fmt.Sprintf("if [ -e %q ]; then echo lag 90s; exit 1; fi; echo lag 1s", marker)})

	h.m.tickWatches()
	h.waitIdle()
	if rs := h.reports(); len(rs) != 1 || rs[0].Kind != WatchReportProbe || rs[0].Status != "ok" || !strings.Contains(rs[0].Summary, "lag 1s") {
		t.Fatalf("reports = %+v, want one passing probe", rs)
	}
	if h.runs() != 0 {
		t.Fatal("a passing probe started a model run")
	}

	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h.setNow(t0.Add(time.Hour))
	h.m.tickWatches()
	waitFor(t, func() bool { return h.runs() == 1 })
	if _, ctx := h.run(0); !strings.Contains(ctx, "exit 1") || !strings.Contains(ctx, "lag 90s") {
		t.Fatalf("run context lacks the failing probe:\n%s", ctx)
	}
}

// A run that ends without watch_report is recorded as failed; a run left
// unfinished by a daemon restart is closed out unless it is parked on a
// restorable question.
func TestWatchRecordsRunsThatNeverReport(t *testing.T) {
	h := newWatchHarness(t)
	h.create(WatchDefinition{})
	h.m.tickWatches()
	waitFor(t, func() bool { return h.runs() == 1 })
	s, _ := h.run(0)
	s.setStatus(event.StatusIdle)
	h.waitIdle()
	if rs := h.reports(); len(rs) != 1 || rs[0].Kind != WatchReportFailed || !strings.Contains(rs[0].Summary, "without calling watch_report") {
		t.Fatalf("reports = %+v, want a failed run", rs)
	}

	// Simulate a restart: an unfinished run whose session is gone.
	h.m.watchMu.Lock()
	rec, _ := loadWatch(h.ws, "live")
	rec.Runs = append(rec.Runs, watchRun{SessionID: "s_gone", ScheduledFor: h.now, Started: h.now})
	_ = saveWatch(h.ws, rec)
	h.m.watchMu.Unlock()
	h.m.tickWatches()
	h.waitIdle()
	rec = h.record()
	if i := rec.runIndex("s_gone"); i < 0 || rec.Runs[i].Finished.IsZero() {
		t.Fatalf("interrupted run was not finished: %+v", rec.Runs)
	}
	if rs := h.reports(); rs[0].SessionID != "s_gone" || rs[0].Kind != WatchReportFailed {
		t.Fatalf("latest report = %+v, want the interrupted run as failed", rs[0])
	}
}

// A probe whose shell waits on a child holding its output pipe must not hold
// up daemon shutdown.
func TestWatchProbeDoesNotBlockShutdown(t *testing.T) {
	h := newWatchHarness(t)
	h.create(WatchDefinition{Probe: "sleep 60; echo done"})
	if err := h.m.RunWatchNow("", "live"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	done := make(chan struct{})
	go func() { h.m.ReclaimAll(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ReclaimAll blocked on a running probe")
	}
}

// After a restart, reconciliation closes a run from its persisted log: an
// answer it recorded becomes a standing decision, and a report already in the
// history is not overwritten by a failed one.
func TestWatchRestartReconcileUsesPersistedLogAndHistory(t *testing.T) {
	h := newWatchHarness(t)
	h.create(WatchDefinition{})
	logPath := filepath.Join(h.ws, ".ycc", "sessions", "s_old", "events.jsonl")
	log, err := event.OpenLog(logPath)
	if err != nil {
		t.Fatal(err)
	}
	em := event.NewEmitter(log, "coordinator")
	em.Emit(event.QuestionAsked, map[string]any{"question": "Pause the history load?"})
	em.Emit(event.QuestionAnswered, map[string]any{"answer": "yes, pause it"})
	log.Close()

	h.m.watchMu.Lock()
	rec, _ := loadWatch(h.ws, "live")
	rec.NextDue = h.now.Add(time.Hour) // keep the tick from launching a run
	rec.Runs = append(rec.Runs, watchRun{SessionID: "s_old", ScheduledFor: h.now, Started: h.now})
	_ = saveWatch(h.ws, rec)
	_ = appendWatchReport(h.ws, "live", WatchReportRecord{At: h.now, Kind: WatchReportRun, SessionID: "s_old", Status: "ok", Summary: "paused"})
	h.m.watchMu.Unlock()

	h.m.tickWatches()
	h.waitIdle()
	rec = h.record()
	if i := rec.runIndex("s_old"); i < 0 || rec.Runs[i].Finished.IsZero() || !rec.Runs[i].Reported {
		t.Fatalf("run not closed as reported: %+v", rec.Runs)
	}
	if len(rec.Decisions) != 1 || rec.Decisions[0].Answer != "yes, pause it" {
		t.Fatalf("decisions = %+v, want the logged answer", rec.Decisions)
	}
	for _, r := range h.reports() {
		if r.Kind == WatchReportFailed {
			t.Fatalf("a reported run was also recorded as failed: %+v", r)
		}
	}
}

func TestWatchCompletionPausesScheduleAndNotifiesOnce(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   string
		notify   bool
		asked    bool
		wantKind string
	}{
		{name: "completion", status: "ok", notify: true, wantKind: notify.KindIdle},
		{name: "silent completion", status: "ok"},
		{name: "completion after question", status: "ok", notify: true, asked: true, wantKind: notify.KindIdle},
		{name: "alert completion", status: "alert", notify: true, wantKind: notify.KindAttention},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newWatchHarness(t)
			sink := newNotifySink(t)
			n := notify.New(config.Notify{URL: sink.URL})
			h.m.SetNotifier(n)
			h.create(WatchDefinition{})
			h.m.tickWatches()
			waitFor(t, func() bool { return h.runs() == 1 })
			s, _ := h.run(0)
			if tc.asked {
				s.emitter.Emit(event.QuestionAsked, map[string]any{"question": "Proceed?"})
				s.emitter.Emit(event.QuestionAnswered, map[string]any{"answer": "Yes"})
			}
			r := orchestrator.WatchReport{Status: tc.status, Summary: "results ready", Notes: "final baseline", Completed: true, Notify: tc.notify}
			h.report(0, r)
			// Retrying a completed tool call must not append or push twice.
			if _, err := h.m.recordWatchReport(h.ws, s.ID, r); err != nil {
				t.Fatal(err)
			}
			n.Flush()
			rec := h.record()
			if !rec.Paused || rec.PausedReason != "completed" || rec.Last == nil || !rec.Last.Completed || rec.Notes != "final baseline" {
				t.Fatalf("completion state = %+v", rec)
			}
			if rs := h.reports(); len(rs) != 1 || !rs[0].Completed {
				t.Fatalf("completion history = %+v", rs)
			}
			got := sink.all()
			if tc.wantKind == "" {
				if len(got) != 0 {
					t.Fatalf("silent completion pushed %+v", got)
				}
			} else if len(got) != 1 || got[0].tags != tc.wantKind || !strings.Contains(got[0].body, "results ready") {
				t.Fatalf("completion notification = %+v, want one %s", got, tc.wantKind)
			}
			// Reloaded persisted state keeps future scheduled runs paused.
			h.setNow(h.now.Add(3 * time.Hour))
			h.m.tickWatches()
			h.waitIdle()
			if h.runs() != 1 {
				t.Fatal("completed watch started another scheduled run")
			}
		})
	}
}

// A report saved in state but missing from the history (crash between the two
// writes) is appended on the next tick, exactly once.
func TestWatchPendingReportReachesHistoryOnce(t *testing.T) {
	h := newWatchHarness(t)
	h.create(WatchDefinition{})
	h.m.watchMu.Lock()
	rec, _ := loadWatch(h.ws, "live")
	rec.NextDue = h.now.Add(time.Hour)
	r := WatchReportRecord{At: h.now, Kind: WatchReportRun, SessionID: "s_old", Status: "ok", Summary: "fine"}
	rec.Runs = append(rec.Runs, watchRun{SessionID: "s_old", ScheduledFor: h.now, Started: h.now, Finished: h.now, Reported: true, Pending: &r})
	_ = saveWatch(h.ws, rec)
	h.m.watchMu.Unlock()

	h.m.tickWatches()
	h.m.tickWatches()
	if rs := h.reports(); len(rs) != 1 || rs[0].SessionID != "s_old" {
		t.Fatalf("history = %+v, want the pending report once", rs)
	}
	if rec := h.record(); rec.Runs[0].Pending != nil {
		t.Fatal("pending report not cleared")
	}
}
