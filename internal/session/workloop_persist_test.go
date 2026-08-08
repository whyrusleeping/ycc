package session

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/docs"
)

func workLoopPersistTestRegistry() *config.Registry {
	return config.NewRegistry(&config.Config{
		Models: map[string]config.Model{"c": {Backend: "ollama", Model: "m"}},
		Roles:  config.Roles{Coordinator: "c", Implementer: "c", Reviewers: []string{"c"}},
	})
}

func TestFinishedWorkLoopRestoresDigest(t *testing.T) {
	ws := t.TempDir()
	reg := workLoopPersistTestRegistry()
	m := NewManager(reg, ws)
	store := docs.NewStore(ws)
	completedTask, err := store.Create("completed by loop", "", 1, nil, nil)
	if err != nil {
		t.Fatalf("Create completed task: %v", err)
	}
	blockedTask, err := store.Create("blocked by loop", "", 2, nil, nil)
	if err != nil {
		t.Fatalf("Create blocked task: %v", err)
	}

	m.newRunSession = func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		return func(context.Context) (loopSessRec, bool, error) {
			tasks, err := store.List()
			if err != nil {
				return loopSessRec{}, false, err
			}
			id := topReadyTask(tasks)
			switch id {
			case completedTask.ID:
				_, err = store.Update(id, func(task *docs.Task) { task.Status = docs.StatusDone })
			case blockedTask.ID:
				_, err = store.Update(id, func(task *docs.Task) {
					task.Status = docs.StatusBlocked
					task.Body = "## Work log\n- blocked: waiting for a credential\n"
				})
			default:
				return loopSessRec{}, false, nil
			}
			if err != nil {
				return loopSessRec{}, false, err
			}
			return loopSessRec{
				id: "sess_" + id, focus: id, tokens: 125, cost: 0.25,
				priceStatus: "priced",
				commits:     []loopCommit{{task: id, sha: "abc" + id}},
				verdicts:    []string{"approve"},
			}, false, nil
		}
	}

	if _, err := m.StartWorkLoop(""); err != nil {
		t.Fatalf("StartWorkLoop: %v", err)
	}
	want := waitLoopFinished(t, m, "")
	if len(want.Completed) != 1 || len(want.Blocked) != 1 {
		t.Fatalf("source digest missing expected rows: completed=%d blocked=%d", len(want.Completed), len(want.Blocked))
	}

	// A fresh manager simulates a daemon restart. It has no in-memory loop map,
	// so GetWorkLoop must restore the complete finished snapshot from the workspace.
	restarted := NewManager(reg, ws)
	got, err := restarted.GetWorkLoop("")
	if err != nil {
		t.Fatalf("GetWorkLoop after restart: %v", err)
	}
	if got == nil {
		t.Fatal("GetWorkLoop after restart returned nil")
	}
	if got.LoopID != want.LoopID || got.State != "finished" || got.Outcome != want.Outcome {
		t.Fatalf("restored identity/state = %q %q %q, want %q %q %q",
			got.LoopID, got.State, got.Outcome, want.LoopID, want.State, want.Outcome)
	}
	if !got.StartedAt.Equal(want.StartedAt) {
		t.Fatalf("restored started_at = %v, want %v", got.StartedAt, want.StartedAt)
	}
	if got.TotalTokens != want.TotalTokens || got.TotalCost != want.TotalCost || got.CostStatus != want.CostStatus {
		t.Fatalf("restored totals = (%d, %v, %q), want (%d, %v, %q)",
			got.TotalTokens, got.TotalCost, got.CostStatus, want.TotalTokens, want.TotalCost, want.CostStatus)
	}
	if !reflect.DeepEqual(got.Sessions, want.Sessions) {
		t.Fatalf("restored sessions = %#v, want %#v", got.Sessions, want.Sessions)
	}
	if !reflect.DeepEqual(got.Completed, want.Completed) || !reflect.DeepEqual(got.Blocked, want.Blocked) ||
		!reflect.DeepEqual(got.InReview, want.InReview) || !reflect.DeepEqual(got.Created, want.Created) {
		t.Fatalf("restored digest differs:\n got=%#v %#v %#v %#v\nwant=%#v %#v %#v %#v",
			got.Completed, got.Blocked, got.InReview, got.Created,
			want.Completed, want.Blocked, want.InReview, want.Created)
	}
}

func TestRunningWorkLoopRestoresInterruptedAndCanRestart(t *testing.T) {
	ws := t.TempDir()
	reg := workLoopPersistTestRegistry()

	// Simulate the last durable snapshot written by a daemon that died while its
	// current unattended session was still running.
	old := &workLoop{
		loopID:           "loop_before_restart",
		project:          filepath.Base(ws),
		workspace:        ws,
		state:            "running",
		currentSessionID: "sess_in_flight",
		startedAt:        time.Unix(1_700_000_000, 123).UTC(),
		sessions: []loopSessRec{{
			id: "sess_done", focus: "0001", tokens: 40, cost: 0.04, priceStatus: "priced",
		}},
		cumTokens:  40,
		cumCost:    0.04,
		costStatus: "priced",
	}
	old.persist()

	restarted := NewManager(reg, ws)
	got, err := restarted.GetWorkLoop("")
	if err != nil {
		t.Fatalf("GetWorkLoop after restart: %v", err)
	}
	if got == nil {
		t.Fatal("interrupted loop vanished after restart")
	}
	if got.State != "finished" || got.Outcome != "loop interrupted: daemon restarted" {
		t.Fatalf("restored state/outcome = %q/%q", got.State, got.Outcome)
	}
	if got.CurrentSessionID != "" {
		t.Fatalf("restored current session = %q, want empty", got.CurrentSessionID)
	}
	if len(got.Sessions) != 1 || got.TotalTokens != 40 || got.TotalCost != 0.04 {
		t.Fatalf("restored interrupted accumulator = sessions=%#v tokens=%d cost=%v",
			got.Sessions, got.TotalTokens, got.TotalCost)
	}

	persisted, ok := readPersistedWorkLoop(ws)
	if !ok {
		t.Fatal("interrupted loop correction was not persisted")
	}
	if persisted.State != "finished" || persisted.Outcome != "loop interrupted: daemon restarted" || persisted.CurrentSessionID != "" {
		t.Fatalf("corrected file has state/outcome/session = %q/%q/%q",
			persisted.State, persisted.Outcome, persisted.CurrentSessionID)
	}

	// The restored record is historical, not live, so it must not trip the
	// already-running guard when the user explicitly starts a fresh loop.
	store := docs.NewStore(ws)
	task, err := store.Create("work after restart", "", 1, nil, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	restarted.newRunSession = func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		return func(context.Context) (loopSessRec, bool, error) {
			_, err := store.Update(task.ID, func(task *docs.Task) { task.Status = docs.StatusDone })
			return loopSessRec{id: "sess_after_restart", focus: task.ID, priceStatus: "unpriced"}, false, err
		}
	}
	started, err := restarted.StartWorkLoop("")
	if err != nil {
		t.Fatalf("StartWorkLoop after interrupted restore: %v", err)
	}
	if started.LoopID == old.loopID {
		t.Fatalf("new loop reused interrupted loop id %q", started.LoopID)
	}
	final := waitLoopFinished(t, restarted, "")
	if final.Outcome != "loop complete: no ready tasks remain" {
		t.Fatalf("new loop outcome = %q", final.Outcome)
	}
}

func TestWaitingWorkLoopRestoresInterrupted(t *testing.T) {
	ws := t.TempDir()
	resumeAt := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Second)
	old := &workLoop{
		loopID: "loop_waiting", project: filepath.Base(ws), workspace: ws,
		state: "waiting", startedAt: time.Now().Add(-time.Hour).UTC(),
		resumeAt: resumeAt, waitKind: "rate_limit",
	}
	old.persist()

	restarted := NewManager(workLoopPersistTestRegistry(), ws)
	got, err := restarted.GetWorkLoop("")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.State != "finished" || got.Outcome != "loop interrupted: daemon restarted" {
		t.Fatalf("restored waiting loop = %+v", got)
	}
	if !got.ResumeAt.IsZero() || got.WaitKind != "" {
		t.Fatalf("finished interrupted loop retained wait metadata: %+v", got)
	}
}

func TestGetWorkLoopIgnoresAbsentAndCorruptPersistence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		corrupt bool
	}{
		{name: "absent"},
		{name: "corrupt", corrupt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := t.TempDir()
			if tc.corrupt {
				path := workLoopPersistPath(ws)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("MkdirAll: %v", err)
				}
				if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
			}
			m := NewManager(workLoopPersistTestRegistry(), ws)
			got, err := m.GetWorkLoop("")
			if err != nil {
				t.Fatalf("GetWorkLoop: %v", err)
			}
			if got != nil {
				t.Fatalf("GetWorkLoop = %#v, want nil", got)
			}
		})
	}
}
