package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/notify"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
)

// Watches are daemon-owned recurring checks (docs/design/watches.md). Each run
// is a fresh watch-mode session seeded with the brief, the previous run's
// notes, the user's standing decisions, pending questions, and a snapshot of
// other activity in the project.

// OriginWatch marks sessions started by the watch scheduler.
const OriginWatch = "watch"

const (
	watchMode             = "watch"
	watchMinInterval      = 5 * time.Minute
	watchTick             = 30 * time.Second
	watchPollInterval     = time.Second
	watchProbeTimeout     = 5 * time.Minute
	watchRunTimeout       = time.Hour // active (not parked) time for one run
	watchLateThreshold    = 2 * time.Minute
	watchRecentRuns       = 20
	watchDecisionLimit    = 20
	watchProbeOutputLimit = 4096
	watchBriefLimit       = 16 * 1024
	watchFileName         = "watch.json"
	watchReportsFileName  = "reports.jsonl"
)

// Report kinds.
const (
	WatchReportRun     = "run"     // a model run's watch_report
	WatchReportProbe   = "probe"   // a passing probe recorded without a model run
	WatchReportSkipped = "skipped" // a due run skipped because the previous one was still active
	WatchReportFailed  = "failed"  // a run that could not start or ended without a report
)

var (
	ErrUnknownWatch = errors.New("unknown watch")
	ErrWatchExists  = errors.New("watch already exists")
	ErrWatchBusy    = errors.New("watch run already active")
	ErrInvalidWatch = errors.New("invalid watch")
)

var watchNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// WatchDefinition is the user-controlled part of a watch.
type WatchDefinition struct {
	Name           string    `json:"name"`
	Brief          string    `json:"brief"`
	Interval       Duration  `json:"interval"`
	Model          string    `json:"model,omitempty"`
	Probe          string    `json:"probe,omitempty"`
	ProbeAlways    bool      `json:"probe_always,omitempty"`
	Expires        time.Time `json:"expires,omitzero"`
	AllowedActions []string  `json:"allowed_actions,omitempty"`
}

// Duration is a time.Duration persisted as a Go duration string.
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// WatchDecision is a user answer to a watch question, carried to later runs.
type WatchDecision struct {
	At        time.Time `json:"at"`
	SessionID string    `json:"session_id"`
	Seq       int       `json:"seq"`
	Question  string    `json:"question"`
	Answer    string    `json:"answer"`
}

// WatchReportRecord is one entry in a watch's report history.
type WatchReportRecord struct {
	At           time.Time                  `json:"at"`
	Kind         string                     `json:"kind"`
	SessionID    string                     `json:"session_id,omitempty"`
	Status       string                     `json:"status,omitempty"`
	Summary      string                     `json:"summary"`
	Actions      []orchestrator.WatchAction `json:"actions,omitempty"`
	ScheduledFor time.Time                  `json:"scheduled_for,omitzero"`
}

type watchRun struct {
	SessionID    string    `json:"session_id"`
	ScheduledFor time.Time `json:"scheduled_for"`
	Started      time.Time `json:"started"`
	Finished     time.Time `json:"finished,omitzero"`
	Reported     bool      `json:"reported,omitempty"`
	// Pending is a report saved in state but not yet confirmed in reports.jsonl;
	// flushWatchPending appends it idempotently (also after a restart).
	Pending *WatchReportRecord `json:"pending,omitempty"`
}

// watchRecord is the persisted definition plus scheduling state.
type watchRecord struct {
	Definition   WatchDefinition    `json:"definition"`
	Created      time.Time          `json:"created"`
	Paused       bool               `json:"paused,omitempty"`
	PausedReason string             `json:"paused_reason,omitempty"` // user | expired
	NextDue      time.Time          `json:"next_due"`
	Notes        string             `json:"notes,omitempty"`
	Decisions    []WatchDecision    `json:"decisions,omitempty"`
	Runs         []watchRun         `json:"runs,omitempty"`
	Last         *WatchReportRecord `json:"last,omitempty"`
}

// WatchInfo is a watch's client-facing state.
type WatchInfo struct {
	Project      string
	Definition   WatchDefinition
	Created      time.Time
	Paused       bool
	PausedReason string
	NextDue      time.Time
	Notes        string
	Decisions    []WatchDecision
	Last         *WatchReportRecord
	// Running is true while a probe or a run's first pass is active.
	Running bool
	// ParkedSessions are runs waiting on a user answer.
	ParkedSessions []string
}

func watchesDir(primary string) string { return filepath.Join(primary, ".ycc", "watches") }

func watchDir(primary, name string) string { return filepath.Join(watchesDir(primary), name) }

func watchKey(primary, name string) string { return primary + "\x00" + name }

func listWatchNames(primary string) []string {
	entries, err := os.ReadDir(watchesDir(primary))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && watchNameRE.MatchString(e.Name()) {
			if _, err := os.Stat(filepath.Join(watchesDir(primary), e.Name(), watchFileName)); err == nil {
				names = append(names, e.Name())
			}
		}
	}
	sort.Strings(names)
	return names
}

func loadWatch(primary, name string) (watchRecord, error) {
	var rec watchRecord
	data, err := os.ReadFile(filepath.Join(watchDir(primary, name), watchFileName))
	if errors.Is(err, os.ErrNotExist) {
		return rec, fmt.Errorf("%w %q", ErrUnknownWatch, name)
	}
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return rec, fmt.Errorf("watch %q: %w", name, err)
	}
	return rec, nil
}

func saveWatch(primary string, rec watchRecord) error {
	dir := watchDir(primary, rec.Definition.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, watchFileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func appendWatchReport(primary, name string, r WatchReportRecord) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(watchDir(primary, name), watchReportsFileName), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}

func readWatchReports(primary, name string) ([]WatchReportRecord, error) {
	f, err := os.Open(filepath.Join(watchDir(primary, name), watchReportsFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []WatchReportRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var r WatchReportRecord
		if json.Unmarshal(sc.Bytes(), &r) == nil {
			out = append(out, r)
		}
	}
	return out, sc.Err()
}

func (m *Manager) watchClock() time.Time {
	if m.watchNow != nil {
		return m.watchNow()
	}
	return time.Now()
}

func (m *Manager) validateWatchDefinition(def *WatchDefinition, creating bool) error {
	def.Name = strings.TrimSpace(def.Name)
	def.Brief = strings.TrimSpace(def.Brief)
	def.Model = strings.TrimSpace(def.Model)
	def.Probe = strings.TrimSpace(def.Probe)
	if !watchNameRE.MatchString(def.Name) {
		return fmt.Errorf("%w: name must be 1-64 lowercase letters, digits, '-' or '_', starting with a letter or digit", ErrInvalidWatch)
	}
	if def.Brief == "" {
		return fmt.Errorf("%w: brief is required", ErrInvalidWatch)
	}
	if len(def.Brief) > watchBriefLimit {
		return fmt.Errorf("%w: brief exceeds %d bytes", ErrInvalidWatch, watchBriefLimit)
	}
	if time.Duration(def.Interval) < watchMinInterval {
		return fmt.Errorf("%w: interval must be at least %s", ErrInvalidWatch, watchMinInterval)
	}
	if def.Model != "" {
		if !m.reg.Has(def.Model) {
			return fmt.Errorf("%w %q", ErrUnknownModel, def.Model)
		}
		if !m.reg.Enabled(def.Model) {
			return fmt.Errorf("%w %q", ErrDisabledModel, def.Model)
		}
	}
	if creating && !def.Expires.IsZero() && !def.Expires.After(m.watchClock()) {
		return fmt.Errorf("%w: expiry is in the past", ErrInvalidWatch)
	}
	var actions []string
	for _, a := range def.AllowedActions {
		if a = strings.TrimSpace(a); a != "" {
			actions = append(actions, a)
		}
	}
	def.AllowedActions = actions
	return nil
}

func (m *Manager) watchPrimary(project string) (string, string, error) {
	ws, err := m.resolveProjectWorkspace(project)
	if err != nil {
		return "", "", err
	}
	primary := m.primaryTreeFor(filepath.Clean(ws))
	return primary, m.projectLabel(primary), nil
}

// CreateWatch persists a new watch. Its first run is due immediately.
func (m *Manager) CreateWatch(project string, def WatchDefinition) (WatchInfo, error) {
	primary, label, err := m.watchPrimary(project)
	if err != nil {
		return WatchInfo{}, err
	}
	return m.createWatchAt(primary, label, def)
}

func (m *Manager) createWatchAt(primary, label string, def WatchDefinition) (WatchInfo, error) {
	if err := m.validateWatchDefinition(&def, true); err != nil {
		return WatchInfo{}, err
	}
	m.watchMu.Lock()
	defer m.watchMu.Unlock()
	if _, err := loadWatch(primary, def.Name); err == nil {
		return WatchInfo{}, fmt.Errorf("%w %q", ErrWatchExists, def.Name)
	}
	now := m.watchClock()
	rec := watchRecord{Definition: def, Created: now, NextDue: now}
	if err := saveWatch(primary, rec); err != nil {
		return WatchInfo{}, err
	}
	m.wakeWatches()
	return m.watchInfoLocked(primary, label, rec), nil
}

// UpdateWatch replaces a watch's definition, keeping its notes, decisions, and
// history. A shorter interval brings the next run forward.
func (m *Manager) UpdateWatch(project string, def WatchDefinition) (WatchInfo, error) {
	primary, label, err := m.watchPrimary(project)
	if err != nil {
		return WatchInfo{}, err
	}
	if err := m.validateWatchDefinition(&def, false); err != nil {
		return WatchInfo{}, err
	}
	m.watchMu.Lock()
	defer m.watchMu.Unlock()
	rec, err := loadWatch(primary, def.Name)
	if err != nil {
		return WatchInfo{}, err
	}
	if next := m.watchClock().Add(time.Duration(def.Interval)); next.Before(rec.NextDue) {
		rec.NextDue = next
	}
	rec.Definition = def
	if rec.Paused && rec.PausedReason == "expired" && (def.Expires.IsZero() || def.Expires.After(m.watchClock())) {
		rec.Paused, rec.PausedReason = false, ""
	}
	if err := saveWatch(primary, rec); err != nil {
		return WatchInfo{}, err
	}
	return m.watchInfoLocked(primary, label, rec), nil
}

// DeleteWatch removes a watch and its history. Run session logs remain.
func (m *Manager) DeleteWatch(project, name string) error {
	primary, _, err := m.watchPrimary(project)
	if err != nil {
		return err
	}
	m.watchMu.Lock()
	defer m.watchMu.Unlock()
	if _, err := loadWatch(primary, name); err != nil {
		return err
	}
	return os.RemoveAll(watchDir(primary, name))
}

// SetWatchPaused pauses or resumes a watch. Resuming does not replay runs
// missed while paused.
func (m *Manager) SetWatchPaused(project, name string, paused bool) (WatchInfo, error) {
	primary, label, err := m.watchPrimary(project)
	if err != nil {
		return WatchInfo{}, err
	}
	m.watchMu.Lock()
	defer m.watchMu.Unlock()
	rec, err := loadWatch(primary, name)
	if err != nil {
		return WatchInfo{}, err
	}
	now := m.watchClock()
	if paused {
		rec.Paused, rec.PausedReason = true, "user"
	} else {
		if !rec.Definition.Expires.IsZero() && !rec.Definition.Expires.After(now) {
			return WatchInfo{}, fmt.Errorf("%w: watch %q has expired; update its expiry to resume it", ErrInvalidWatch, name)
		}
		rec.Paused, rec.PausedReason = false, ""
		if rec.NextDue.Before(now) {
			rec.NextDue = now
		}
		m.wakeWatches()
	}
	if err := saveWatch(primary, rec); err != nil {
		return WatchInfo{}, err
	}
	return m.watchInfoLocked(primary, label, rec), nil
}

// RunWatchNow starts a run immediately without changing the schedule.
func (m *Manager) RunWatchNow(project, name string) error {
	primary, _, err := m.watchPrimary(project)
	if err != nil {
		return err
	}
	m.watchMu.Lock()
	if _, err := loadWatch(primary, name); err != nil {
		m.watchMu.Unlock()
		return err
	}
	key := watchKey(primary, name)
	if m.watchBusy[key] {
		m.watchMu.Unlock()
		return fmt.Errorf("%w for %q", ErrWatchBusy, name)
	}
	if m.watchStop {
		m.watchMu.Unlock()
		return errors.New("daemon is shutting down")
	}
	m.watchBusy[key] = true
	m.watchWG.Add(1)
	m.watchMu.Unlock()
	go m.executeWatch(primary, name, m.watchClock(), 0, 0)
	return nil
}

// ListWatches returns a project's watches.
func (m *Manager) ListWatches(project string) ([]WatchInfo, error) {
	primary, label, err := m.watchPrimary(project)
	if err != nil {
		return nil, err
	}
	m.watchMu.Lock()
	defer m.watchMu.Unlock()
	var out []WatchInfo
	for _, name := range listWatchNames(primary) {
		rec, err := loadWatch(primary, name)
		if err != nil {
			continue
		}
		out = append(out, m.watchInfoLocked(primary, label, rec))
	}
	return out, nil
}

// GetWatch returns one watch.
func (m *Manager) GetWatch(project, name string) (WatchInfo, error) {
	primary, label, err := m.watchPrimary(project)
	if err != nil {
		return WatchInfo{}, err
	}
	m.watchMu.Lock()
	defer m.watchMu.Unlock()
	rec, err := loadWatch(primary, name)
	if err != nil {
		return WatchInfo{}, err
	}
	return m.watchInfoLocked(primary, label, rec), nil
}

// WatchReports returns up to limit reports, newest first (limit <= 0: all).
func (m *Manager) WatchReports(project, name string, limit int) ([]WatchReportRecord, error) {
	primary, _, err := m.watchPrimary(project)
	if err != nil {
		return nil, err
	}
	m.watchMu.Lock()
	defer m.watchMu.Unlock()
	if _, err := loadWatch(primary, name); err != nil {
		return nil, err
	}
	reports, err := readWatchReports(primary, name)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(reports)-1; i < j; i, j = i+1, j-1 {
		reports[i], reports[j] = reports[j], reports[i]
	}
	if limit > 0 && len(reports) > limit {
		reports = reports[:limit]
	}
	return reports, nil
}

func (m *Manager) watchInfoLocked(primary, label string, rec watchRecord) WatchInfo {
	info := WatchInfo{
		Project: label, Definition: rec.Definition, Created: rec.Created, Paused: rec.Paused,
		PausedReason: rec.PausedReason, NextDue: rec.NextDue, Notes: rec.Notes,
		Decisions: rec.Decisions, Last: rec.Last, Running: m.watchBusy[watchKey(primary, rec.Definition.Name)],
	}
	for _, p := range m.pendingWatchQuestions(primary, rec) {
		info.ParkedSessions = append(info.ParkedSessions, p.sessionID)
	}
	return info
}

// StartWatches runs the watch scheduler until ReclaimAll.
func (m *Manager) StartWatches() {
	m.watchMu.Lock()
	if m.watchStop || m.watchStarted {
		m.watchMu.Unlock()
		return
	}
	m.watchStarted = true
	m.watchRunning.Store(true)
	m.watchWG.Add(1)
	m.watchMu.Unlock()
	go func() {
		defer m.watchWG.Done()
		t := time.NewTicker(watchTick)
		defer t.Stop()
		for {
			m.tickWatches()
			select {
			case <-m.watchCtx.Done():
				return
			case <-t.C:
			case <-m.watchWake:
			}
		}
	}()
}

func (m *Manager) wakeWatches() {
	select {
	case m.watchWake <- struct{}{}:
	default:
	}
}

// stopWatches cancels the scheduler and run followers and joins them. Live run
// sessions are reaped with the rest of the session map.
func (m *Manager) stopWatches() {
	m.watchMu.Lock()
	if !m.watchStop {
		m.watchStop = true
		m.watchRunning.Store(false)
		m.watchCancel()
	}
	m.watchMu.Unlock()
	m.watchWG.Wait()
}

func (m *Manager) tickWatches() {
	now := m.watchClock()
	for _, p := range m.projects.List() {
		primary := filepath.Clean(p.Path)
		for _, name := range listWatchNames(primary) {
			m.considerWatch(primary, name, now)
		}
	}
}

// considerWatch starts a due run. Due times advance on the schedule's own grid,
// so cadence does not drift with run duration; several missed slots collapse
// into one run.
func (m *Manager) considerWatch(primary, name string, now time.Time) {
	m.watchMu.Lock()
	defer m.watchMu.Unlock()
	if m.watchStop {
		return
	}
	rec, err := loadWatch(primary, name)
	if err != nil {
		return
	}
	if m.reconcileWatchRuns(primary, &rec, now) {
		_ = saveWatch(primary, rec)
	}
	if rec.Paused {
		return
	}
	if exp := rec.Definition.Expires; !exp.IsZero() && !now.Before(exp) {
		rec.Paused, rec.PausedReason = true, "expired"
		_ = saveWatch(primary, rec)
		return
	}
	if now.Before(rec.NextDue) {
		return
	}
	interval := time.Duration(rec.Definition.Interval)
	scheduled := rec.NextDue
	missed := 0
	next := scheduled.Add(interval)
	for !next.After(now) {
		next = next.Add(interval)
		missed++
	}
	rec.NextDue = next
	key := watchKey(primary, name)
	if m.watchBusy[key] {
		_ = appendWatchReport(primary, name, WatchReportRecord{
			At: now, Kind: WatchReportSkipped, ScheduledFor: scheduled,
			Summary: "Skipped: the previous run was still active.",
		})
		_ = saveWatch(primary, rec)
		return
	}
	if err := saveWatch(primary, rec); err != nil {
		return
	}
	m.watchBusy[key] = true
	m.watchWG.Add(1)
	go m.executeWatch(primary, name, scheduled, now.Sub(scheduled), missed)
}

// flushWatchPending appends saved-but-unconfirmed reports to the history,
// skipping any already there (a crash after the append, before state was
// saved). It reports whether rec changed. Caller holds watchMu.
func flushWatchPending(primary, name string, rec *watchRecord) bool {
	changed := false
	var history []WatchReportRecord
	read := false
	for i := range rec.Runs {
		p := rec.Runs[i].Pending
		if p == nil {
			continue
		}
		if !read {
			history, _ = readWatchReports(primary, name)
			read = true
		}
		found := false
		for _, h := range history {
			if h.SessionID == p.SessionID && h.Kind == p.Kind && h.At.Equal(p.At) {
				found = true
				break
			}
		}
		if !found && appendWatchReport(primary, name, *p) != nil {
			continue
		}
		rec.Runs[i].Pending = nil
		changed = true
	}
	return changed
}

// reconcileWatchRuns attaches a follower to unfollowed live runs (a parked run
// reopened by an answer after a daemon restart) and finishes runs whose session
// is gone without a restorable question (interrupted by a restart). It reports
// whether rec changed. Caller holds watchMu.
func (m *Manager) reconcileWatchRuns(primary string, rec *watchRecord, now time.Time) bool {
	name := rec.Definition.Name
	changed := flushWatchPending(primary, name, rec)
	var history []WatchReportRecord
	historyRead := false
	for i := range rec.Runs {
		run := &rec.Runs[i]
		if !run.Finished.IsZero() {
			continue
		}
		s, live := m.Get(run.SessionID)
		var evs []event.Event
		if live {
			evs = s.Log().Snapshot()
		} else {
			evs, _ = event.ReadLog(filepath.Join(primary, ".ycc", "sessions", run.SessionID, "events.jsonl"))
		}
		// Answers reach later runs as soon as they are given, not only when the
		// answered run reports.
		before := len(rec.Decisions)
		rec.harvestDecisions(run.SessionID, evs)
		if len(rec.Decisions) != before {
			changed = true
		}
		if m.watchFollowed[run.SessionID] {
			continue
		}
		if live {
			m.watchFollowed[run.SessionID] = true
			m.watchWG.Add(1)
			go func() {
				defer m.watchWG.Done()
				m.followWatchRun(primary, name, s, func() {})
			}()
			continue
		}
		if evs != nil && findResumableQuestion(evs) != nil {
			continue // parked; answering it reopens the run
		}
		run.Finished = now
		changed = true
		if !run.Reported {
			if !historyRead {
				history, _ = readWatchReports(primary, name)
				historyRead = true
			}
			for _, h := range history {
				if h.SessionID == run.SessionID && h.Kind == WatchReportRun {
					run.Reported = true
				}
			}
		}
		if !run.Reported {
			r := WatchReportRecord{
				At: now, Kind: WatchReportFailed, SessionID: run.SessionID, Status: orchestrator.WatchWarn,
				Summary: "The run was interrupted before it reported (daemon restart or stop).", ScheduledFor: run.ScheduledFor,
			}
			_ = appendWatchReport(primary, name, r)
			rec.Last = &r
		}
	}
	return changed
}

type watchProbeResult struct {
	command string
	exit    int
	output  string
	err     string
}

func runWatchProbe(ctx context.Context, dir, command string) watchProbeResult {
	ctx, cancel := context.WithTimeout(ctx, watchProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = dir
	// Kill the whole process group on timeout/shutdown, and bound the wait for
	// descendants that keep the output pipe open, so a probe cannot hang the
	// scheduler or daemon shutdown.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 10 * time.Second
	buf := &tailBuffer{limit: watchProbeOutputLimit}
	cmd.Stdout = buf
	cmd.Stderr = buf
	err := cmd.Run()
	res := watchProbeResult{command: command, output: buf.String()}
	if err != nil {
		res.exit = -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.exit = ee.ExitCode()
		}
		if ctx.Err() == context.DeadlineExceeded {
			res.err = fmt.Sprintf("timed out after %s", watchProbeTimeout)
		} else if res.exit == -1 {
			res.err = err.Error()
		}
	}
	return res
}

// tailBuffer retains only the last limit bytes written to it.
type tailBuffer struct {
	mu        sync.Mutex
	limit     int
	buf       []byte
	truncated bool
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	if len(p) >= t.limit {
		t.buf = append(t.buf[:0], p[len(p)-t.limit:]...)
		t.truncated = true
		return n, nil
	}
	if over := len(t.buf) + len(p) - t.limit; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
		t.truncated = true
	}
	t.buf = append(t.buf, p...)
	return n, nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.truncated {
		return "…" + string(t.buf)
	}
	return string(t.buf)
}

// executeWatch runs one due watch: probe, then (unless the probe passed) a
// model run that it follows to completion.
func (m *Manager) executeWatch(primary, name string, scheduled time.Time, late time.Duration, missed int) {
	defer m.watchWG.Done()
	key := watchKey(primary, name)
	var once sync.Once
	release := func() {
		once.Do(func() {
			m.watchMu.Lock()
			delete(m.watchBusy, key)
			m.watchMu.Unlock()
		})
	}
	defer release()

	m.watchMu.Lock()
	rec, err := loadWatch(primary, name)
	m.watchMu.Unlock()
	if err != nil {
		return
	}
	var probe *watchProbeResult
	if rec.Definition.Probe != "" {
		res := runWatchProbe(m.watchCtx, primary, rec.Definition.Probe)
		if m.watchCtx.Err() != nil {
			return
		}
		probe = &res
		if res.exit == 0 && !rec.Definition.ProbeAlways {
			summary := "Probe passed."
			if line := firstLine(lastLine(res.output)); line != "" {
				summary = "Probe passed: " + line
			}
			m.recordWatchOutcome(primary, name, WatchReportRecord{
				At: m.watchClock(), Kind: WatchReportProbe, Status: orchestrator.WatchOK,
				Summary: summary, ScheduledFor: scheduled,
			})
			return
		}
	}

	m.watchMu.Lock()
	rec, err = loadWatch(primary, name)
	var runCtx string
	if err == nil {
		runCtx = m.watchRunContext(primary, rec, scheduled, late, missed, probe)
	}
	m.watchMu.Unlock()
	if err != nil {
		return
	}
	start := m.startWatchSession
	if start == nil {
		start = m.startWatchRunSession
	}
	// Hold watchMu across the launch so the run's own watch_report (which takes
	// watchMu) always finds the run recorded.
	m.watchMu.Lock()
	if m.watchStop {
		m.watchMu.Unlock()
		return
	}
	s, err := start(primary, rec.Definition, runCtx)
	if err != nil {
		m.watchMu.Unlock()
		m.recordWatchOutcome(primary, name, WatchReportRecord{
			At: m.watchClock(), Kind: WatchReportFailed, Status: orchestrator.WatchWarn,
			Summary: "The run could not start: " + err.Error(), ScheduledFor: scheduled,
		})
		return
	}
	if rec, err := loadWatch(primary, name); err == nil {
		rec.Runs = append(rec.Runs, watchRun{SessionID: s.ID, ScheduledFor: scheduled, Started: m.watchClock()})
		rec.Runs = trimWatchRuns(rec.Runs)
		_ = saveWatch(primary, rec)
	}
	m.watchFollowed[s.ID] = true
	m.watchMu.Unlock()
	m.followWatchRun(primary, name, s, release)
}

// trimWatchRuns keeps unfinished runs plus the most recent finished ones.
func trimWatchRuns(runs []watchRun) []watchRun {
	if len(runs) <= watchRecentRuns {
		return runs
	}
	drop := len(runs) - watchRecentRuns
	out := runs[:0]
	for _, r := range runs {
		if drop > 0 && !r.Finished.IsZero() {
			drop--
			continue
		}
		out = append(out, r)
	}
	return out
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

func (m *Manager) startWatchRunSession(primary string, def WatchDefinition, runCtx string) (*Session, error) {
	return m.start(Config{
		Workspace:        primary,
		Mode:             watchMode,
		Origin:           OriginWatch,
		Prompt:           def.Brief,
		CoordinatorModel: def.Model,
		loopContinuation: runCtx,
	}, false)
}

// followWatchRun waits for a run to end, then records its outcome and reclaims
// it. release frees the watch's schedule once the run parks on a question or
// ends, so a parked run never holds up later runs.
func (m *Manager) followWatchRun(primary, name string, s *Session, release func()) {
	defer func() {
		m.watchMu.Lock()
		delete(m.watchFollowed, s.ID)
		m.watchMu.Unlock()
	}()
	poll := watchPollInterval
	if m.watchPoll > 0 {
		poll = m.watchPoll
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	var active time.Duration
	last := time.Now()
	for {
		now := time.Now()
		pending := s.PendingQuestion()
		if pending {
			release()
		} else {
			active += now.Sub(last)
		}
		last = now
		status, awaiting := s.StatusWithJobContinuation()
		ended := ""
		switch {
		case status == event.StatusError:
			ended = "The run ended with a session error."
		case status == event.StatusStopped:
			ended = "The run was stopped."
		case status == event.StatusIdle && !awaiting && !pending && !s.hasLiveJobs():
			ended = "The run ended without calling watch_report."
		}
		if live, ok := m.Get(s.ID); !ok || live != s {
			ended = "The run was stopped."
		}
		if ended == "" && !pending && active > watchRunTimeout {
			_ = m.Stop(s.ID)
			ended = fmt.Sprintf("The run was stopped after %s without finishing.", watchRunTimeout)
		}
		if ended != "" {
			m.finishWatchRun(primary, name, s, ended)
			m.reclaimIfCurrent(s)
			release()
			return
		}
		select {
		case <-m.watchCtx.Done():
			return
		case <-ticker.C:
		}
	}
}

// finishWatchRun records a run's end: decisions are harvested and a run that
// never reported gets a failed report.
func (m *Manager) finishWatchRun(primary, name string, s *Session, failure string) {
	m.watchMu.Lock()
	defer m.watchMu.Unlock()
	rec, err := loadWatch(primary, name)
	if err != nil {
		return
	}
	idx := rec.runIndex(s.ID)
	if idx < 0 {
		return
	}
	rec.harvestDecisions(s.ID, s.Log().Snapshot())
	run := &rec.Runs[idx]
	run.Finished = m.watchClock()
	if !run.Reported {
		r := WatchReportRecord{
			At: run.Finished, Kind: WatchReportFailed, SessionID: s.ID, Status: orchestrator.WatchWarn,
			Summary: failure, ScheduledFor: run.ScheduledFor,
		}
		_ = appendWatchReport(primary, name, r)
		rec.Last = &r
	}
	_ = saveWatch(primary, rec)
}

func (m *Manager) recordWatchOutcome(primary, name string, r WatchReportRecord) {
	m.watchMu.Lock()
	defer m.watchMu.Unlock()
	rec, err := loadWatch(primary, name)
	if err != nil {
		return
	}
	_ = appendWatchReport(primary, name, r)
	rec.Last = &r
	_ = saveWatch(primary, rec)
}

func (rec *watchRecord) runIndex(sessionID string) int {
	for i, r := range rec.Runs {
		if r.SessionID == sessionID {
			return i
		}
	}
	return -1
}

// harvestDecisions appends the user's answers to this run's human questions,
// so standing decisions reach later runs whatever the model writes in notes.
func (rec *watchRecord) harvestDecisions(sessionID string, evs []event.Event) {
	seen := map[int]bool{}
	for _, d := range rec.Decisions {
		if d.SessionID == sessionID {
			seen[d.Seq] = true
		}
	}
	var asked []string
	for _, ev := range evs {
		if ev.Actor != "coordinator" {
			continue
		}
		switch ev.Type {
		case event.QuestionAsked:
			if boolVal(ev.Data, "auto") {
				asked = nil
				continue
			}
			asked = askedPrompts(ev.Data)
		case event.QuestionAnswered:
			if boolVal(ev.Data, "auto") || asked == nil {
				continue
			}
			answers := answeredTexts(ev.Data)
			if !seen[ev.Seq] {
				rec.Decisions = append(rec.Decisions, WatchDecision{
					At: ev.TS, SessionID: sessionID, Seq: ev.Seq,
					Question: clipText(strings.Join(asked, " / "), 600),
					Answer:   clipText(strings.Join(answers, " / "), 600),
				})
			}
			asked = nil
		}
	}
	if len(rec.Decisions) > watchDecisionLimit {
		rec.Decisions = rec.Decisions[len(rec.Decisions)-watchDecisionLimit:]
	}
}

func askedPrompts(data map[string]any) []string {
	if q := str(data, "question"); q != "" {
		return []string{q}
	}
	var out []string
	switch qs := data["questions"].(type) {
	case []any:
		for _, item := range qs {
			if qm, ok := item.(map[string]any); ok {
				out = append(out, str(qm, "question"))
			}
		}
	case []map[string]any:
		for _, qm := range qs {
			out = append(out, str(qm, "question"))
		}
	}
	if len(out) == 0 {
		out = []string{"(question)"}
	}
	return out
}

func answeredTexts(data map[string]any) []string {
	if a := str(data, "answer"); a != "" {
		return []string{a}
	}
	return strList(data["answers"])
}

func clipText(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// recordWatchReport is the watch_report hook for a watch-mode session.
func (m *Manager) recordWatchReport(absWS, sessionID string, r orchestrator.WatchReport) (string, error) {
	primary := m.primaryTreeFor(filepath.Clean(absWS))
	var evs []event.Event
	if s, live := m.Get(sessionID); live {
		evs = s.Log().Snapshot()
	}
	m.watchMu.Lock()
	var (
		rec  watchRecord
		name string
		idx  = -1
	)
	for _, n := range listWatchNames(primary) {
		cand, err := loadWatch(primary, n)
		if err != nil {
			continue
		}
		if i := cand.runIndex(sessionID); i >= 0 {
			rec, name, idx = cand, n, i
			break
		}
	}
	if idx < 0 {
		m.watchMu.Unlock()
		return "", errors.New("no watch owns this run (it may have been deleted)")
	}
	status := r.Status
	if len(r.Actions) > 0 && status == orchestrator.WatchOK {
		status = orchestrator.WatchWarn
	}
	run := &rec.Runs[idx]
	report := WatchReportRecord{
		At: m.watchClock(), Kind: WatchReportRun, SessionID: sessionID, Status: status,
		Summary: strings.TrimSpace(r.Summary), Actions: r.Actions, ScheduledFor: run.ScheduledFor,
	}
	flushWatchPending(primary, name, &rec)
	run.Reported = true
	run.Pending = &report
	rec.Last = &report
	rec.Notes = strings.TrimSpace(r.Notes)
	rec.harvestDecisions(sessionID, evs)
	// The report is durable once state is saved; a failed save records nothing,
	// so a retried watch_report cannot duplicate history. The history append
	// follows and is retried by later ticks if it does not complete.
	if err := saveWatch(primary, rec); err != nil {
		m.watchMu.Unlock()
		return "", err
	}
	if flushWatchPending(primary, name, &rec) {
		_ = saveWatch(primary, rec)
	}
	m.watchMu.Unlock()
	if status == orchestrator.WatchAlert && !askedHumanQuestion(evs) {
		m.Notify(notify.KindAttention, m.projectLabel(primary), sessionID, "watch "+name+": "+firstLine(report.Summary))
	}
	return fmt.Sprintf("Report recorded (status %s). This run ends now.", status), nil
}

func askedHumanQuestion(evs []event.Event) bool {
	for _, ev := range evs {
		if ev.Type == event.QuestionAsked && ev.Actor == "coordinator" && !boolVal(ev.Data, "auto") {
			return true
		}
	}
	return false
}

type pendingWatchQuestion struct {
	sessionID string
	asked     time.Time
	questions []string
}

// pendingWatchQuestions finds runs of this watch parked on a question, live or
// restorable from their logs after a restart.
func (m *Manager) pendingWatchQuestions(primary string, rec watchRecord) []pendingWatchQuestion {
	var out []pendingWatchQuestion
	for _, run := range rec.Runs {
		if !run.Finished.IsZero() {
			continue
		}
		var evs []event.Event
		if s, ok := m.Get(run.SessionID); ok {
			if !s.PendingQuestion() {
				continue
			}
			evs = s.Log().Snapshot()
		} else {
			var err error
			evs, err = event.ReadLog(filepath.Join(primary, ".ycc", "sessions", run.SessionID, "events.jsonl"))
			if err != nil {
				continue
			}
			if findResumableQuestion(evs) == nil {
				continue
			}
		}
		p := pendingWatchQuestion{sessionID: run.SessionID}
		for i := len(evs) - 1; i >= 0; i-- {
			if evs[i].Type == event.QuestionAsked && evs[i].Actor == "coordinator" {
				p.asked = evs[i].TS
				p.questions = askedPrompts(evs[i].Data)
				break
			}
		}
		out = append(out, p)
	}
	return out
}

// watchRunContext is the daemon-generated context seeded after the brief.
func (m *Manager) watchRunContext(primary string, rec watchRecord, scheduled time.Time, late time.Duration, missed int, probe *watchProbeResult) string {
	now := m.watchClock()
	def := rec.Definition
	var b strings.Builder
	fmt.Fprintf(&b, "[Watch run context, generated by the daemon]\nWatch %q runs every %s. This run was scheduled for %s and started %s.",
		def.Name, time.Duration(def.Interval), scheduled.Format(time.RFC3339), now.Format(time.RFC3339))
	if late > watchLateThreshold {
		fmt.Fprintf(&b, " It is %s late", late.Round(time.Minute))
		if missed > 0 {
			fmt.Fprintf(&b, "; %d scheduled run(s) did not happen (daemon unavailable), so coverage has a gap", missed)
		}
		b.WriteString(".")
	}
	b.WriteString("\n")

	b.WriteString("\n## Notes from the previous run\n")
	if rec.Notes != "" {
		b.WriteString(rec.Notes + "\n")
	} else {
		b.WriteString("(none — this may be the first run)\n")
	}
	if len(rec.Decisions) > 0 {
		b.WriteString("\n## The user's answers to earlier watch questions\n")
		for _, d := range rec.Decisions {
			fmt.Fprintf(&b, "- %s: Q: %s\n  A: %s\n", d.At.Format(time.RFC3339), d.Question, d.Answer)
		}
	}
	if last := rec.Last; last != nil {
		fmt.Fprintf(&b, "\n## Previous report (%s, %s", last.At.Format(time.RFC3339), last.Kind)
		if last.Status != "" {
			b.WriteString(", " + last.Status)
		}
		b.WriteString(")\n" + last.Summary + "\n")
	}
	if pend := m.pendingWatchQuestions(primary, rec); len(pend) > 0 {
		b.WriteString("\n## Questions from earlier runs still awaiting the user (do not ask again)\n")
		for _, p := range pend {
			fmt.Fprintf(&b, "- since %s (run %s): %s\n", p.asked.Format(time.RFC3339), p.sessionID, clipText(strings.Join(p.questions, " / "), 600))
		}
	}
	if len(def.AllowedActions) > 0 {
		b.WriteString("\n## Pre-authorized actions (allowed beyond the low-risk default)\n")
		for _, a := range def.AllowedActions {
			b.WriteString("- " + a + "\n")
		}
	}
	b.WriteString("\n## Other activity in this project\n")
	b.WriteString(m.watchActivitySnapshot(primary))
	if probe != nil {
		fmt.Fprintf(&b, "\n## Probe\n$ %s\nexit %d", probe.command, probe.exit)
		if probe.err != "" {
			b.WriteString(" (" + probe.err + ")")
		}
		b.WriteString("\n" + probe.output + "\n")
	}
	return b.String()
}

// watchActivitySnapshot lists the project's other live work so a watch run can
// tell what it must not act on.
func (m *Manager) watchActivitySnapshot(primary string) string {
	var b strings.Builder
	if lines := m.liveSessionLines(primary, "", m.watchClock()); lines != "" {
		b.WriteString("Live sessions (those idle for over an hour are omitted):\n" + lines)
	} else {
		b.WriteString("No other session has been active in the last hour.\n")
	}
	label := m.projectLabel(primary)
	var active []string
	for _, ws := range m.Workstreams(label) {
		if !ws.Status.Terminal() {
			line := fmt.Sprintf("- %s (%s) branch %s", ws.ID, ws.Status, ws.Branch)
			if ws.TaskID != "" {
				line += " task " + ws.TaskID
			}
			active = append(active, line)
		}
	}
	if len(active) > 0 {
		b.WriteString("Workstreams:\n" + strings.Join(active, "\n") + "\n")
	}
	if tasks, err := m.backlogStore(primary).ListMetadata(); err == nil {
		var inProgress []string
		for _, t := range tasks {
			if t.Status == docs.StatusInProgress {
				inProgress = append(inProgress, fmt.Sprintf("- %s %s", t.ID, t.Title))
			}
		}
		if len(inProgress) > 0 {
			b.WriteString("In-progress backlog tasks (owned by their sessions, even when none is live):\n" + strings.Join(inProgress, "\n") + "\n")
		}
	}
	return b.String()
}

// quietSessionAge is how long a live session that is not working may go without
// activity before session listings leave it out as noise.
const quietSessionAge = time.Hour

// liveSessionLines lists the live sessions of project primary except exclude,
// one line each with mode, state, focused tasks and running jobs. Only a session
// that is still working holds its focused tasks; an idle one's are past focus,
// and one quiet for over quietSessionAge is omitted.
func (m *Manager) liveSessionLines(primary, exclude string, now time.Time) string {
	primary = canonicalPath(primary)
	sessions := m.List()
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID < sessions[j].ID })
	var b strings.Builder
	for _, s := range sessions {
		if s.ID == exclude || canonicalPath(m.primaryTreeFor(s.Workspace)) != primary {
			continue
		}
		evs := s.Log().Snapshot()
		working := s.activelyWorking()
		if !working && len(evs) > 0 && now.Sub(evs[len(evs)-1].TS) > quietSessionAge {
			continue
		}
		kind := s.Mode
		if s.Mode == watchMode {
			kind = "watch run"
		}
		fmt.Fprintf(&b, "- %s (%s, %s)", s.ID, kind, sessionActivityState(s, evs))
		if tasks := focusTasks(evs); len(tasks) > 0 {
			if working {
				b.WriteString(" working on " + strings.Join(tasks, ", "))
			} else {
				b.WriteString(" earlier focus " + strings.Join(tasks, ", ") + " (not held)")
			}
		}
		if canonicalPath(s.Workspace) != primary {
			b.WriteString(" in worktree " + s.Workspace)
		}
		b.WriteString("\n")
		jn := 0
		for _, j := range s.JobInfos() {
			if j.Status != jobs.Running {
				continue
			}
			if jn++; jn > 10 {
				b.WriteString("    … more running jobs\n")
				break
			}
			fmt.Fprintf(&b, "    job %s (%s, since %s): %s\n", j.ID, j.Kind, j.Started.Format(time.RFC3339), clipText(j.Label, 200))
		}
	}
	return b.String()
}

// sessionActivityState describes what a live session is doing now; an idle
// session reports when its log last changed.
func sessionActivityState(s *Session, evs []event.Event) string {
	status, awaiting := s.StatusWithJobContinuation()
	switch {
	case s.PendingQuestion():
		return "waiting on a user question"
	case awaiting || (status == event.StatusIdle && s.hasLiveJobs()):
		return "waiting on its background work"
	case status == event.StatusIdle && len(evs) > 0:
		return "idle since " + evs[len(evs)-1].TS.UTC().Format("2006-01-02 15:04 UTC")
	}
	return string(status)
}

// otherSessionsNote is the work-selection view of the project's other live
// sessions, appended to list_backlog.
func (m *Manager) otherSessionsNote(absWS, exclude string, now time.Time) string {
	lines := m.liveSessionLines(m.primaryTreeFor(absWS), exclude, now)
	if lines == "" {
		return "Other sessions in this project: none has been active in the last hour (daemon state).\n"
	}
	return "Other live sessions in this project (daemon state; those idle for over an hour are omitted):\n" + lines
}

// scheduleWatchFromChat is the schedule_watch hook; the tool has already
// obtained the user's confirmation.
func (m *Manager) scheduleWatchFromChat(absWS string, spec orchestrator.WatchSpec) (string, error) {
	primary := m.primaryTreeFor(filepath.Clean(absWS))
	label := m.projectLabel(primary)
	info, err := m.createWatchAt(primary, label, WatchDefinition{
		Name: spec.Name, Brief: spec.Brief, Interval: Duration(spec.Interval), Model: spec.Model,
		Probe: spec.Probe, ProbeAlways: spec.ProbeAlways, Expires: spec.Expires, AllowedActions: spec.AllowedActions,
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Watch %q created in project %s; its first run starts now and then every %s. "+
		"Runs are separate sessions: do not also poll or sleep in this session.", info.Definition.Name, label, time.Duration(info.Definition.Interval)), nil
}

// watchSchedulerRunning is lock-free: session construction calls it while the
// scheduler may hold watchMu to launch a run.
func (m *Manager) watchSchedulerRunning() bool { return m.watchRunning.Load() }
