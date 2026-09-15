package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
)

const finalizationRecordVersion = 1

const (
	finalizationPrepared        = "prepared"
	finalizationTaskCompleted   = "task_completed"
	finalizationCommitInstalled = "commit_installed"
	finalizationCommitted       = "committed"
	finalizationDecisionEmitted = "decision_emitted"
	finalizationEventsEmitted   = "events_emitted"
)

type finalizationRecord struct {
	Version        int                 `json:"version"`
	TaskID         string              `json:"task_id"`
	Message        string              `json:"message"`
	Outcome        string              `json:"outcome"`
	FinalizationID string              `json:"finalization_id"`
	BaselineID     string              `json:"baseline_id"`
	Phase          string              `json:"phase"`
	Completion     *docs.Completion    `json:"completion"`
	Commit         *git.CommitRecovery `json:"commit,omitempty"`
	SHA            string              `json:"sha,omitempty"`
}

type finalizationJournal interface {
	Load() (*finalizationRecord, error)
	Save(*finalizationRecord) error
}

type fileFinalizationJournal struct{ path string }

func (j fileFinalizationJournal) Load() (*finalizationRecord, error) {
	return loadFinalization(j.path)
}

func (j fileFinalizationJournal) Save(record *finalizationRecord) error {
	return saveFinalization(j.path, record)
}

func finalizeTask(ctx context.Context, d *Deps, taskID, message, outcome string) (string, error) {
	if err := finalizationCanProceed(ctx, d); err != nil {
		return "", err
	}
	task, err := d.Docs.Get(taskID)
	if err != nil {
		return "", err
	}
	// Docs accepts numeric aliases (for example "1" for "0001"). Recovery
	// state and emitted identities must always use the durable canonical ID.
	taskID = task.ID
	path, err := d.Repo.FinalizationPath(taskID)
	if err != nil {
		return "", err
	}
	return finalizeTaskWithJournal(ctx, d, taskID, message, outcome, fileFinalizationJournal{path: path})
}

func finalizeTaskWithJournal(ctx context.Context, d *Deps, taskID, message, outcome string, journal finalizationJournal) (string, error) {
	task, err := d.Docs.Get(taskID)
	if err != nil {
		return "", err
	}
	taskID = task.ID
	record, err := journal.Load()
	if err != nil {
		return "", err
	}
	if record == nil {
		preflight, err := d.changeset(taskID)
		if err != nil {
			return "", fmt.Errorf("unsafe changeset: %w", err)
		}
		// Adoption must not turn a previously completed, unowned task into an
		// accepted implementation. Keep the legacy done/no-op guard strict.
		legacyEmpty := false
		if task.Status == docs.StatusDone {
			strict, strictErr := d.Repo.Changes(d.Baseline)
			legacyEmpty = strictErr == nil && strings.TrimSpace(strict.Diff) == ""
		}
		if task.Status == docs.StatusDone && (legacyEmpty || strings.TrimSpace(preflight.Diff) == "") {
			committed, err := d.Repo.WorktreeFileMatchesHEAD(task.Path)
			if err != nil {
				return "", fmt.Errorf("verify completed task in HEAD: %w", err)
			}
			if !committed {
				return "", fmt.Errorf("task %s is done only in preexisting/unowned worktree state, not in HEAD; commit it explicitly or retry from the original baseline that owns its document", taskID)
			}
			head, err := d.Repo.RevParse("HEAD")
			if err != nil {
				return "", err
			}
			return shortCommit(head), nil
		}
		completion, err := d.Docs.PrepareCompletion(taskID, outcome, message)
		if err != nil {
			return "", err
		}
		record = newFinalizationRecord(taskID, message, outcome, preflight.BaselineID, preflight.ID, completion)
		if err := journal.Save(record); err != nil {
			return "", fmt.Errorf("persist finalization intent: %w", err)
		}
	} else {
		if err := validateFinalization(record); err != nil {
			return "", fmt.Errorf("task %s has an invalid finalization journal: %w", taskID, err)
		}
		if record.TaskID != taskID {
			return "", fmt.Errorf("finalization journal belongs to task %s, not %s", record.TaskID, taskID)
		}
		if record.Phase == finalizationPrepared {
			if err := renewUncommittedFinalization(d, journal, record, message, outcome); err != nil {
				return "", err
			}
		}
		if record.Message != message || record.Outcome != outcome {
			return "", fmt.Errorf("task %s has a different pending or completed finalization; retry with its original message and outcome", taskID)
		}
	}

	if record.Phase == finalizationPrepared || record.Phase == finalizationTaskCompleted {
		if _, err := d.Docs.ApplyCompletion(record.Completion, true); err != nil {
			return "", fmt.Errorf("apply recoverable task completion: %w", err)
		}
		if record.Phase == finalizationPrepared {
			record.Phase = finalizationTaskCompleted
			if err := journal.Save(record); err != nil {
				record.Phase = finalizationPrepared
				if _, restoreErr := d.Docs.ApplyCompletion(record.Completion, false); restoreErr != nil {
					return "", fmt.Errorf("record completed task state: %v; restoring the uncommitted task also failed: %v", err, restoreErr)
				}
				return "", fmt.Errorf("record completed task state: %w (task was restored)", err)
			}
		}
	}

	if record.Phase == finalizationTaskCompleted {
		if err := finalizationCanProceed(ctx, d); err != nil {
			return "", restoreAfterUncommittedFailure(d.Docs, journal, record, err)
		}
		sha, state, classified, commitErr := finishGitCommit(d, record, journal)
		if commitErr != nil {
			if classified && state == git.CommitInstalled {
				record.SHA = sha
				record.Phase = finalizationCommitInstalled
				if saveErr := journal.Save(record); saveErr != nil {
					return "", fmt.Errorf("commit %s was installed but index publication failed: %v; recording pending index recovery also failed: %v", sha, commitErr, saveErr)
				}
				return "", fmt.Errorf("commit %s was installed; selected index paths remain pending: %w", sha, commitErr)
			}
			if !classified {
				return "", fmt.Errorf("commit outcome is uncertain; task completion and recovery identity were retained for inspection: %w", commitErr)
			}
			return "", restoreAfterUncommittedFailure(d.Docs, journal, record, commitErr)
		}
		record.SHA = sha
		record.Phase = finalizationCommitted
		if err := journal.Save(record); err != nil {
			return "", fmt.Errorf("commit %s succeeded; finalization journal remains recoverable but could not record the committed phase: %w", sha, err)
		}
	}

	if record.Phase == finalizationCommitInstalled {
		sha, err := d.Repo.RecoverCommit(record.Commit, record.Message)
		if err != nil {
			return "", fmt.Errorf("commit %s is installed; selected index paths remain pending: %w", record.SHA, err)
		}
		record.SHA = sha
		record.Phase = finalizationCommitted
		if err := journal.Save(record); err != nil {
			return "", fmt.Errorf("commit %s and selected index paths succeeded; finalization event publication remains pending: %w", sha, err)
		}
	}

	if err := publishFinalizationEvents(ctx, d, journal, record); err != nil {
		return "", fmt.Errorf("commit %s succeeded; durable finalization event publication is pending: %w", record.SHA, err)
	}
	return record.SHA, nil
}

func newFinalizationRecord(taskID, message, outcome, baselineID, finalizationID string, completion *docs.Completion) *finalizationRecord {
	return &finalizationRecord{
		Version: finalizationRecordVersion, TaskID: taskID,
		Message: message, Outcome: outcome, BaselineID: baselineID, FinalizationID: finalizationID,
		Phase: finalizationPrepared, Completion: completion,
	}
}

// renewUncommittedFinalization lets a rejected pre-commit attempt incorporate
// source fixes and additional task evidence. Once git has durably created an
// exact commit, its task transition and scoped identity remain strict.
func renewUncommittedFinalization(d *Deps, journal finalizationJournal, record *finalizationRecord, message, outcome string) error {
	current, err := d.Docs.Get(record.TaskID)
	if err != nil {
		return err
	}
	if record.Commit != nil {
		state, _, err := d.Repo.CommitStatus(record.Commit, record.Message)
		if err != nil {
			return fmt.Errorf("inspect pending commit before renewing finalization: %w", err)
		}
		if state != git.CommitUncreated {
			return nil
		}
	}
	preflight, err := d.changeset(record.TaskID)
	if err != nil {
		return fmt.Errorf("unsafe changeset while renewing finalization: %w", err)
	}
	if preflight.BaselineID != record.BaselineID {
		return fmt.Errorf("task %s finalization belongs to original baseline %s, not current baseline %s; reopen the original session baseline rather than narrowing accepted work", record.TaskID, record.BaselineID, preflight.BaselineID)
	}
	if sameFinalizationTask(current, record.Completion.After) {
		return nil // interrupted after applying completion; resume the saved transition
	}
	completion, err := d.Docs.PrepareCompletion(record.TaskID, outcome, message)
	if err != nil {
		return err
	}
	fresh := newFinalizationRecord(record.TaskID, message, outcome, record.BaselineID, preflight.ID, completion)
	*record = *fresh
	if err := journal.Save(record); err != nil {
		return fmt.Errorf("renew rejected finalization: %w", err)
	}
	return nil
}

func restoreAfterUncommittedFailure(store *docs.Store, journal finalizationJournal, record *finalizationRecord, cause error) error {
	if _, restoreErr := store.ApplyCompletion(record.Completion, false); restoreErr != nil {
		return fmt.Errorf("%v; restoring the resumable task also failed: %v", cause, restoreErr)
	}
	record.Phase = finalizationPrepared
	if saveErr := journal.Save(record); saveErr != nil {
		return fmt.Errorf("%v; task was restored but recovery journal update failed: %v", cause, saveErr)
	}
	return cause
}

func finishGitCommit(d *Deps, record *finalizationRecord, journal finalizationJournal) (string, git.CommitState, bool, error) {
	if record.Commit != nil {
		state, sha, err := d.Repo.CommitStatus(record.Commit, record.Message)
		if err != nil {
			return sha, state, false, err
		}
		if state == git.CommitDiverged {
			identity := "for changeset " + shortCommit(record.Commit.ChangesetID)
			if sha != "" {
				identity = sha
			}
			return sha, state, true, fmt.Errorf("reviewed commit %s was not installed because HEAD moved from %s; task was not accepted and unrelated HEAD will not be overwritten", identity, shortCommit(record.Commit.BaseCommit))
		}
		if state == git.CommitCreated || state == git.CommitInstalled {
			recovered, recoverErr := d.Repo.RecoverCommit(record.Commit, record.Message)
			if recoverErr == nil {
				return recoveredOr(sha, recovered), git.CommitInstalled, true, nil
			}
			latest, latestSHA, statusErr := d.Repo.CommitStatus(record.Commit, record.Message)
			if statusErr != nil {
				return recoveredOr(sha, latestSHA), latest, false, fmt.Errorf("recover commit %s: %v; inspect commit state: %v", sha, recoverErr, statusErr)
			}
			return recoveredOr(sha, latestSHA), latest, true, recoverErr
		}
	}
	changes, err := d.changeset(record.TaskID)
	if err != nil {
		return "", git.CommitUncreated, true, fmt.Errorf("unsafe changeset after task finalization: %w", err)
	}
	if changes.BaselineID != record.BaselineID {
		return "", git.CommitUncreated, true, fmt.Errorf("task %s finalization belongs to original baseline %s, not current baseline %s; reopen the original session baseline rather than narrowing accepted work", record.TaskID, record.BaselineID, changes.BaselineID)
	}
	if record.Commit == nil {
		record.Commit = changes.Recovery()
		if err := journal.Save(record); err != nil {
			return "", git.CommitUncreated, true, fmt.Errorf("persist reviewed commit identity: %w", err)
		}
	} else if record.Commit.ChangesetID != changes.ID {
		return "", git.CommitCreated, true, fmt.Errorf("finalization changeset %s is stale (current snapshot is %s); inspect/review current scoped changes before retrying", record.Commit.ChangesetID, changes.ID)
	}
	sha, err := d.Repo.Commit(changes, record.Message)
	if err == nil {
		return sha, git.CommitInstalled, true, nil
	}
	state, knownSHA, statusErr := d.Repo.CommitStatus(record.Commit, record.Message)
	if statusErr != nil {
		return knownSHA, state, false, fmt.Errorf("commit changeset %s: %v; inspect commit state: %v", changes.ID, err, statusErr)
	}
	return knownSHA, state, true, fmt.Errorf("commit changeset %s: %w", changes.ID, err)
}

func recoveredOr(fallback, recovered string) string {
	if recovered != "" {
		return recovered
	}
	return fallback
}

func sameFinalizationTask(a, b *docs.Task) bool {
	if a == nil || b == nil {
		return a == b
	}
	ac, bc := *a, *b
	if len(ac.DependsOn) == 0 {
		ac.DependsOn = nil
	}
	if len(bc.DependsOn) == 0 {
		bc.DependsOn = nil
	}
	if len(ac.SpecRefs) == 0 {
		ac.SpecRefs = nil
	}
	if len(bc.SpecRefs) == 0 {
		bc.SpecRefs = nil
	}
	return reflect.DeepEqual(&ac, &bc)
}

func publishFinalizationEvents(ctx context.Context, d *Deps, journal finalizationJournal, record *finalizationRecord) error {
	if record.Phase == finalizationEventsEmitted {
		return nil
	}
	if err := finalizationCanProceed(ctx, d); err != nil {
		return err
	}
	dataID := record.FinalizationID
	if record.Commit != nil {
		dataID = record.Commit.ChangesetID
	}
	if record.Phase == finalizationCommitted {
		if err := emitDurable(ctx, d.Emitter, event.DecisionMade, map[string]any{
			"task": record.TaskID, "decision": "accept", "finalization_id": dataID,
		}); err != nil {
			return err
		}
		record.Phase = finalizationDecisionEmitted
		if err := journal.Save(record); err != nil {
			return fmt.Errorf("decision was emitted but its recovery checkpoint failed: %w", err)
		}
	}
	if record.Phase == finalizationDecisionEmitted {
		if err := emitDurable(ctx, d.Emitter, event.CommitMade, map[string]any{
			"task": record.TaskID, "sha": record.SHA, "message": record.Message, "finalization_id": dataID,
		}); err != nil {
			return err
		}
		record.Phase = finalizationEventsEmitted
		if err := journal.Save(record); err != nil {
			return fmt.Errorf("commit event was emitted but its recovery checkpoint failed: %w", err)
		}
	}
	return nil
}

func validateFinalization(record *finalizationRecord) error {
	if record.Version != finalizationRecordVersion || record.Completion == nil || record.Completion.Before == nil || record.Completion.After == nil || record.FinalizationID == "" || record.BaselineID == "" {
		return fmt.Errorf("missing required recovery data")
	}
	if record.Completion.Before.ID != record.TaskID || record.Completion.After.ID != record.TaskID || record.Completion.After.Status != docs.StatusDone {
		return fmt.Errorf("completion transition does not match task")
	}
	switch record.Phase {
	case finalizationPrepared, finalizationTaskCompleted:
		return nil
	case finalizationCommitInstalled, finalizationCommitted, finalizationDecisionEmitted, finalizationEventsEmitted:
		if record.Commit == nil || record.SHA == "" {
			return fmt.Errorf("phase %s has no committed identity", record.Phase)
		}
		return nil
	default:
		return fmt.Errorf("unknown phase %q", record.Phase)
	}
}

func finalizationCanProceed(ctx context.Context, d *Deps) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d == nil || d.Docs == nil || d.Repo == nil || d.Emitter == nil {
		return fmt.Errorf("finalization dependencies are unavailable")
	}
	if err := d.Emitter.Err(); err != nil {
		return fmt.Errorf("event log is unavailable: %w", err)
	}
	return nil
}

func emitDurable(ctx context.Context, emitter *event.Emitter, typ event.Type, data map[string]any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := emitter.Err(); err != nil {
		return fmt.Errorf("event log is unavailable: %w", err)
	}
	ev := emitter.Emit(typ, data)
	if err := emitter.Err(); err != nil {
		return fmt.Errorf("record %s: %w", typ, err)
	}
	if ev.Seq == 0 {
		return fmt.Errorf("record %s: event was not durably recorded", typ)
	}
	return nil
}

func loadFinalization(path string) (*finalizationRecord, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read finalization journal: %w", err)
	}
	var record finalizationRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("decode finalization journal: %w", err)
	}
	return &record, nil
}

func saveFinalization(path string, record *finalizationRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".finalization-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func shortCommit(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
