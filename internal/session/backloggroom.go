package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
)

// Automatic backlog grooming. Backlog drift builds up in bursts of agent work,
// so the daemon schedules an unattended backlog-groom pm session once enough
// tasks have changed since the last finished groom, rather than on a clock:
// a project nobody touches is never groomed. Like memory grooming it is bounded
// to one run per project, a persisted cooldown, and the preset model binding
// (else the default coordinator).
const (
	backlogGroomPreset = "backlog-groom"
	// backlogGroomCooldown is the minimum time between automatic backlog grooms
	// of one project, whatever their outcome.
	backlogGroomCooldown = 24 * time.Hour
	// backlogGroomChurn is how many tasks must be created or changed since the
	// last finished groom before another is worthwhile. It sits below the batch
	// size so one run reviews all the churn that triggered it.
	backlogGroomChurn = 10
	// backlogGroomBatch caps the changed tasks handed to one run (the preset's
	// own batch bound is about 15 tasks).
	backlogGroomBatch = 15
	// backlogGroomStateFile persists scheduling state per primary tree.
	backlogGroomStateFile = "backlog-groom.json"
	// backlogGroomReportMax bounds the stored final report.
	backlogGroomReportMax = 4096
)

// BacklogGroomState is the persisted record of a project's automatic backlog
// grooming: the latest run and the churn baseline.
type BacklogGroomState struct {
	LastSession  string    `json:"last_session,omitempty"`
	LastStarted  time.Time `json:"last_started,omitempty"`
	LastFinished time.Time `json:"last_finished,omitempty"`
	Outcome      string    `json:"outcome,omitempty"` // finished | error | stopped | timed out | interrupted
	// Changed is the churn (tasks created or changed, excluding in_progress
	// tasks) that triggered the latest run; Batch lists the changed task ids it
	// reviewed first, and Deferred counts changed tasks left for a later run.
	Changed  int      `json:"changed,omitempty"`
	Batch    []string `json:"batch,omitempty"`
	Deferred int      `json:"deferred,omitempty"`
	// Report is the latest finished run's final report, clipped.
	Report string `json:"report,omitempty"`
	// BaselineAt is when Baseline was last recorded: at the first check (so an
	// existing backlog is not all counted as churn) and after each finished
	// groom. Baseline maps each backlog file name to a content fingerprint;
	// tasks a run deferred keep their older fingerprint so they still count.
	BaselineAt time.Time         `json:"baseline_at,omitempty"`
	Baseline   map[string]string `json:"baseline,omitempty"`
}

func backlogGroomStatePath(primary string) string {
	return filepath.Join(primary, ".ycc", backlogGroomStateFile)
}

func loadBacklogGroomState(primary string) BacklogGroomState {
	var st BacklogGroomState
	if data, err := os.ReadFile(backlogGroomStatePath(primary)); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	return st
}

func saveBacklogGroomState(primary string, st BacklogGroomState) {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	path := backlogGroomStatePath(primary)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// BacklogGroomInfo reports a project's automatic backlog-groom status.
type BacklogGroomInfo struct {
	Enabled bool
	// RunningSession is the live automatic groom session id, or "".
	RunningSession string
	Last           BacklogGroomState
}

// BacklogGroomInfo returns automatic backlog-groom status for a registered
// project. Last omits the churn baseline.
func (m *Manager) BacklogGroomInfo(project string) (BacklogGroomInfo, error) {
	absWS, err := m.resolveProjectWorkspace(project)
	if err != nil {
		return BacklogGroomInfo{}, err
	}
	primary := m.primaryTreeFor(filepath.Clean(absWS))
	m.groomMu.Lock()
	running := m.backlogGrooms[primary]
	m.groomMu.Unlock()
	if running == groomStarting {
		running = ""
	}
	last := loadBacklogGroomState(primary)
	last.Baseline = nil
	return BacklogGroomInfo{Enabled: m.backlogAutoGroom(primary), RunningSession: running, Last: last}, nil
}

func (m *Manager) backlogAutoGroom(primary string) bool {
	return m.reg.BacklogAutoGroom() && docs.NewStore(primary).BacklogAutoGroom()
}

// backlogFile is one backlog task file's identity and content fingerprint.
type backlogFile struct {
	name   string // file name, the baseline key
	id     string
	status docs.Status
	hash   string
	// grooms counts "groom:" markers; a run that adds one has reviewed the task.
	grooms int
}

// snapshotBacklog fingerprints every task file in primary's backlog, sorted by
// task id. It never repairs duplicate ids: scheduling is read-only.
func snapshotBacklog(primary string) ([]backlogFile, error) {
	store := docs.NewStore(primary)
	store.SetRepairLease(func() (func(), error) { return nil, errors.New("read-only backlog groom check") })
	tasks, err := store.ListMetadata()
	if err != nil {
		return nil, err
	}
	out := make([]backlogFile, 0, len(tasks))
	for _, t := range tasks {
		data, err := os.ReadFile(t.Path)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(data)
		out = append(out, backlogFile{
			name: filepath.Base(t.Path), id: t.ID, status: t.Status, hash: hex.EncodeToString(sum[:12]),
			grooms: strings.Count(strings.ToLower(string(data)), "groom:"),
		})
	}
	return out, nil
}

func fingerprints(files []backlogFile) map[string]string {
	out := make(map[string]string, len(files))
	for _, f := range files {
		out[f.name] = f.hash
	}
	return out
}

// backlogChurn lists the tasks created or changed since baseline that a groom
// may review, oldest id first. in_progress tasks, which a work session owns,
// neither count nor get reviewed until they leave that status.
func backlogChurn(baseline map[string]string, files []backlogFile) []backlogFile {
	var churn []backlogFile
	for _, f := range files {
		if old, ok := baseline[f.name]; ok && old == f.hash {
			continue
		}
		if f.status != docs.StatusInProgress {
			churn = append(churn, f)
		}
	}
	sort.SliceStable(churn, func(i, j int) bool { return churn[i].id < churn[j].id })
	return churn
}

// maybeGroomBacklog runs at session start: when the project's backlog has
// churned enough since its last finished groom and no groom ran within the
// cooldown, it starts an unattended backlog-groom session in the primary tree.
// Sessions in linked worktrees check their project's primary backlog, which is
// what their work merges into.
func (m *Manager) maybeGroomBacklog(absWS string) {
	primary := m.primaryTreeFor(filepath.Clean(absWS))
	if !m.backlogAutoGroom(primary) {
		return
	}
	due := func(st BacklogGroomState) bool {
		if m.groomStop {
			return false
		}
		if _, running := m.backlogGrooms[primary]; running {
			return false
		}
		return st.LastStarted.IsZero() || time.Since(st.LastStarted) >= backlogGroomCooldown
	}
	m.groomMu.Lock()
	ok := due(loadBacklogGroomState(primary))
	m.groomMu.Unlock()
	if !ok {
		return
	}
	// Fingerprint outside the lock; the reservation below re-checks.
	files, err := snapshotBacklog(primary)
	if err != nil {
		return
	}

	m.groomMu.Lock()
	st := loadBacklogGroomState(primary)
	if !due(st) {
		m.groomMu.Unlock()
		return
	}
	if st.BaselineAt.IsZero() {
		// First check: an existing backlog is not churn. Older tasks are reached
		// later through each run's oldest-ungroomed fill.
		st.BaselineAt = time.Now()
		st.Baseline = fingerprints(files)
		saveBacklogGroomState(primary, st)
		m.groomMu.Unlock()
		return
	}
	churn := backlogChurn(st.Baseline, files)
	if len(churn) < backlogGroomChurn {
		m.groomMu.Unlock()
		return
	}
	m.backlogGrooms[primary] = groomStarting
	m.groomWG.Add(1)
	m.groomMu.Unlock()

	batch := churn
	if len(batch) > backlogGroomBatch {
		batch = batch[:backlogGroomBatch]
	}
	ids := make([]string, len(batch))
	for i, f := range batch {
		ids[i] = f.id
	}
	// The run acknowledges only tasks it reviews: those that gain a "groom:"
	// entry relative to this launch snapshot.
	startGrooms := make(map[string]int, len(files))
	for _, f := range files {
		startGrooms[f.name] = f.grooms
	}
	prompt := orchestrator.BacklogAutoGroomPrompt(len(churn), ids, len(churn)-len(batch))

	start := m.startBacklogGroom
	if start == nil {
		start = m.startBacklogGroomSession
	}
	s, err := start(primary, prompt)

	m.groomMu.Lock()
	if err != nil {
		delete(m.backlogGrooms, primary)
		m.groomMu.Unlock()
		m.groomWG.Done()
		return
	}
	m.backlogGrooms[primary] = s.ID
	st = loadBacklogGroomState(primary)
	st.LastSession, st.LastStarted, st.LastFinished, st.Outcome = s.ID, time.Now(), time.Time{}, ""
	st.Changed, st.Batch, st.Deferred, st.Report = len(churn), ids, len(churn)-len(batch), ""
	saveBacklogGroomState(primary, st)
	m.groomMu.Unlock()
	go m.watchBacklogGroom(primary, s, startGrooms)
}

// startBacklogGroomSession starts the real unattended groom in the primary
// tree. The preset field routes model selection through
// roles.presets["backlog-groom"], falling back to the default coordinator. The
// origin is the generic automation origin so clients keep it out of inboxes.
func (m *Manager) startBacklogGroomSession(primary, prompt string) (*Session, error) {
	return m.start(Config{
		Workspace:  primary,
		Mode:       "pm",
		Unattended: true,
		Origin:     OriginAutomation,
		Preset:     backlogGroomPreset,
		Prompt:     prompt,
	}, false)
}

// watchBacklogGroom waits for an automatic backlog groom to end, records its
// outcome and report, and — only when it finished — advances the churn baseline
// past the tasks it reviewed. It is joined by ReclaimAll.
func (m *Manager) watchBacklogGroom(primary string, s *Session, startGrooms map[string]int) {
	defer m.groomWG.Done()
	outcome := m.awaitGroom(s)
	report := clipReport(event.Reduce(s.Log().Snapshot()).LastReport, backlogGroomReportMax)
	files, snapErr := snapshotBacklog(primary)

	m.groomMu.Lock()
	defer m.groomMu.Unlock()
	st := loadBacklogGroomState(primary)
	if st.LastSession == s.ID {
		st.LastFinished = time.Now()
		st.Outcome = outcome
		st.Report = report
		if outcome == "finished" && snapErr == nil {
			st.Baseline = advanceBaseline(st.Baseline, files, startGrooms)
			st.BaselineAt = st.LastFinished
		}
		saveBacklogGroomState(primary, st)
	}
	if m.backlogGrooms[primary] == s.ID {
		delete(m.backlogGrooms, primary)
	}
}

// advanceBaseline is the baseline after a finished groom. A task the run
// reviewed — one that gained a "groom:" entry since launch — is acknowledged at
// its current fingerprint. Every other task keeps its old fingerprint (or none),
// so deferred, skipped, in_progress, and concurrently edited tasks still count
// as churn. Deleted task files drop out.
func advanceBaseline(old map[string]string, files []backlogFile, startGrooms map[string]int) map[string]string {
	baseline := make(map[string]string, len(files))
	for _, f := range files {
		if f.grooms > startGrooms[f.name] {
			baseline[f.name] = f.hash
		} else if prev, ok := old[f.name]; ok {
			baseline[f.name] = prev
		}
	}
	return baseline
}

// clipReport trims s to at most n bytes without splitting a UTF-8 sequence.
func clipReport(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}
