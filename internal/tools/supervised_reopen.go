package tools

import (
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/jobshim"
)

// SupervisedReopen is a reopening session's reconciliation of its supervised
// background jobs (docs/design/daemon-restart.md §3). ReconcileSupervisedJobs
// decides each job's restored state without side effects beyond connecting to
// live supervisors; Activate then records and follows what it found. Its
// methods are nil-safe: a session without a job supervisor reconciles nothing.
type SupervisedReopen struct {
	sup  *jobshim.Session
	jobs []*reopenedJob
	// marker holds survivors' outcomes back until session_reopened, which
	// lists them as running, is recorded (or the reopen is torn down).
	marker     chan struct{}
	markerOnce sync.Once
}

// reopenedJob is one supervised job's reconciliation.
type reopenedJob struct {
	id, owner, artifact string
	owned               bool // owned by the surviving coordinator
	survivor            bool // still running and re-attached as a live job
	timeout             time.Duration
	handle              *jobshim.Handle // nil when the exit record was already on disk
	follow              bool            // re-attach a runner (execution not yet confirmed)
	kill                bool            // ask the supervisor to kill its child once followed
	durable             bool            // the job's terminal evidence is (or became) durable
	finish              map[string]any  // job_finished to record before session_reopened
	claim               map[string]any  // job_claimed for an outcome no owner will be told of
	harvest             bool            // mark the exit record harvested once durable
	unknown             string          // record that execution is unknown, with this reason
	hydrate             bool            // re-hydrate the head capture into the owner's store
	art                 outputArtifact  // the capture's description (data is read at Activate)
}

// supervisionLostWarning states that supervision ended while the child may
// still be running.
func supervisionLostWarning(reason string) string {
	return fmt.Sprintf("supervision of this job was lost (%s); its command may still be running and could not be confirmed stopped", reason)
}

// OwnerGoneDelivery marks a recovered job_finished (its "delivery" field) or a
// job_claimed (its "reason") whose report is delivered to nobody: the
// subagent that owned the job did not survive the restart, and a later run of
// the same actor must not inherit the report. Replay restores such a job as
// already notified.
const OwnerGoneDelivery = "owner_gone"

// ownerGoneClaim is the durable claim repairing a subagent's terminal report
// recorded without the owner_gone delivery (a crash between writes).
func ownerGoneClaim(e *jobs.Restored) map[string]any {
	return map[string]any{"id": e.ID, "kind": e.Kind, "label": e.Label, "status": string(e.Status),
		"result": e.Result, "reason": OwnerGoneDelivery}
}

// ReconcileSupervisedJobs reconciles entries' supervised jobs in place, in
// job id order, across five axes: owner and handoff (the coordinator, or a
// subagent whose shell does not survive it), terminal evidence (the entry's
// recorded status), execution state (an exit record, a live supervisor, or
// unknown), retained disk evidence, and delivery state (Notified). Outcomes:
//   - an exit record without terminal evidence is a downtime finish, reported
//     from the record, the on-disk tail, and the head capture;
//   - a live coordinator job without terminal evidence survives as a live
//     tracked job (Live);
//   - a live subagent job is killed: its report becomes killed, nobody is
//     notified, and its execution stays tracked until the exit is confirmed;
//   - no exit record and no live supervisor is a loss whose execution is
//     unknown, never reported complete, while a late exit record can still
//     confirm it;
//   - a job with terminal evidence keeps its report and claim; its execution
//     is confirmed by an exit record, followed while the supervisor lives (a
//     kill still unwinding is asked again), or restored as unknown;
//   - a job an operator cleared as stopped (jobshim.Cleared) without an exit
//     record is a loss whose execution is not followed further.
//
// coordinator names the actor whose jobs survive a reopen. adopted holds the
// handles a passive supervisor was already following (PassiveJobs.Take): they
// are used instead of connecting again, and detached when the reconciliation
// does not follow them. Entries the reconciliation does not handle (direct
// children, jobs without a job directory) are left for FinalizeRestoredJobs'
// legacy loss.
func ReconcileSupervisedJobs(sup *jobshim.Session, entries []jobs.Restored, coordinator string, now time.Time, adopted map[string]*jobshim.Handle) *SupervisedReopen {
	if sup == nil {
		detachAll(adopted)
		return nil
	}
	adopted = maps.Clone(adopted)
	defer detachAll(adopted) // the handles no job below took
	var order []int
	for i := range entries {
		if entries[i].Supervised && entries[i].Kind == "bash" {
			order = append(order, i)
		}
	}
	// Ids are allocated before launches are recorded, so log order can differ.
	sort.SliceStable(order, func(a, b int) bool { return jobNumber(entries[order[a]].ID) < jobNumber(entries[order[b]].ID) })
	r := &SupervisedReopen{sup: sup, marker: make(chan struct{})}
	for _, i := range order {
		e := &entries[i]
		spec, err := sup.ReadSpec(e.ID)
		if err != nil {
			continue
		}
		terminal := e.Status != jobs.Running
		j := &reopenedJob{id: e.ID, owner: e.Owner, artifact: e.Artifact, owned: e.Owner == coordinator,
			timeout: time.Duration(spec.TimeoutMS) * time.Millisecond, durable: terminal}
		r.jobs = append(r.jobs, j)
		if terminal && !e.Notified && !j.owned {
			// A subagent's report no live actor will receive (a crash between
			// writes left it unclaimed): suppress it durably.
			e.Notified = true
			j.claim = ownerGoneClaim(e)
		}
		dir := sup.JobDir(e.ID)
		// Disk evidence of the exit wins over a supervisor still answering
		// while it finishes: the job did end during the downtime.
		if rec, err := jobshim.ReadExit(dir); err == nil {
			r.exited(j, e, rec, terminal, dir)
			continue
		}
		if jobshim.Cleared(dir) {
			r.cleared(j, e, terminal, now)
			continue
		}
		if h := adopted[e.ID]; h != nil {
			j.handle = h
			delete(adopted, e.ID)
		} else {
			j.handle = sup.Attach(e.ID)
		}
		select {
		case <-j.handle.Done():
			if out := j.handle.Outcome(); out.Exit != nil {
				r.exited(j, e, out.Exit, terminal, dir)
			} else {
				r.lost(j, e, out.Unknown, terminal, dir, now)
			}
			continue
		default:
		}
		// A live supervisor.
		j.follow = true
		switch {
		case !terminal && j.owned:
			e.Live, j.survivor = true, true
		case !terminal:
			// A subagent's shell does not outlive its subagent, which did not
			// survive the restart.
			e.Status, e.Notified, e.Finished, e.ExecutionPending = jobs.Killed, true, now, true
			e.Result = "killed: its subagent did not survive the daemon restart"
			if tail := recoveredTail(dir); strings.TrimSpace(tail) != "" {
				e.Result += "\n" + tail
			}
			j.kill = true
			j.finish = reopenFinish(e, false, true)
		default:
			// Terminal but still executing: a kill still unwinding is asked
			// again, and a subagent's shell is stopped.
			e.ExecutionPending = true
			j.kill = e.Status == jobs.Killed || !j.owned
		}
	}
	return r
}

// exited reconciles a job whose supervisor left an exit record.
func (r *SupervisedReopen) exited(j *reopenedJob, e *jobs.Restored, rec *jobshim.ExitRecord, terminal bool, dir string) {
	j.harvest = !r.sup.Harvested(e.ID)
	j.art = describeSupervisedArtifact(e.Artifact, dir)
	j.hydrate = j.owned && e.Artifact != ""
	if terminal {
		return
	}
	status, info := supervisedExit(rec, j.timeout)
	e.Status, e.Finished, e.Notified = status, rec.Finished, !j.owned
	e.Result = supervisedReport(info, recoveredTail(dir), j.art, recoveredAttributionNote)
	j.finish = reopenFinish(e, false, !j.owned)
}

// lost reconciles a job whose supervision is gone without an exit record. It
// stays followed, so a late exit record (a supervisor still holding its lock)
// confirms the stop; meanwhile its shell counts as running in its worktree.
func (r *SupervisedReopen) lost(j *reopenedJob, e *jobs.Restored, reason string, terminal bool, dir string, now time.Time) {
	j.unknown, j.follow = reason, true
	warning := supervisionLostWarning(reason)
	e.ExecutionUnknown = true
	if terminal {
		if !e.Notified && !strings.Contains(e.Result, "supervision of this job was lost") {
			// Undelivered: the one notification carries the warning. A report
			// already claimed or notified is never notified again.
			e.Result += "\n[" + warning + "]"
		}
		return
	}
	e.Status, e.Finished, e.Notified = jobs.Lost, now, !j.owned
	e.Result = "lost: " + warning
	if tail := recoveredTail(dir); strings.TrimSpace(tail) != "" {
		e.Result += "\n" + tail
	}
	if e.Artifact != "" {
		e.Result += fmt.Sprintf("\n[output artifact %s: not available until an exit record confirms the command stopped]", e.Artifact)
	}
	j.finish = reopenFinish(e, true, !j.owned)
}

// cleared reconciles a job an operator cleared as stopped without an exit
// record: an unreported one is lost, and nothing follows its execution.
func (r *SupervisedReopen) cleared(j *reopenedJob, e *jobs.Restored, terminal bool, now time.Time) {
	if terminal {
		return
	}
	e.Status, e.Finished, e.Notified = jobs.Lost, now, !j.owned
	e.Result = "lost: supervision of this job was lost, and an operator cleared it as stopped"
	j.finish = reopenFinish(e, false, !j.owned)
}

// detachAll stops following handles, leaving their supervisors running.
func detachAll(handles map[string]*jobshim.Handle) {
	for _, h := range handles {
		h.Detach()
	}
}

func jobNumber(id string) int {
	var n int
	_, _ = fmt.Sscanf(id, "job_%d", &n)
	return n
}

// recoveredTail is the last lines of a job directory's retained output.
func recoveredTail(dir string) string {
	data, _ := jobshim.NewReader(dir).Retained()
	return jobs.TailText(data, 20)
}

// reopenFinish is the job_finished a reopen records for a reconciled outcome.
// ownerGone marks, in the same record, that nobody is notified of it.
func reopenFinish(e *jobs.Restored, unknown, ownerGone bool) map[string]any {
	data := map[string]any{"id": e.ID, "kind": e.Kind, "label": e.Label, "status": string(e.Status),
		"tail": e.Result, "recovered": true}
	if unknown {
		data["execution_unknown"] = true
	}
	if ownerGone {
		data["delivery"] = OwnerGoneDelivery
	}
	return data
}

// Reconciled names every job the reconciliation handled, which replay must
// not report lost on restart (engine.ReplayHistoryReconciled).
func (r *SupervisedReopen) Reconciled() map[string]bool {
	if r == nil {
		return nil
	}
	out := make(map[string]bool, len(r.jobs))
	for _, j := range r.jobs {
		out[j.id] = true
	}
	return out
}

// Surviving lists the coordinator's jobs still running after reconciliation,
// in id order (session_reopened surviving_jobs).
func (r *SupervisedReopen) Surviving() []string {
	if r == nil {
		return nil
	}
	var ids []string
	for _, j := range r.jobs {
		if j.survivor {
			ids = append(ids, j.id)
		}
	}
	return ids
}

// ReleaseMarker lets survivors record their outcomes. The session calls it
// once session_reopened (listing them in surviving_jobs) is recorded, and
// before any teardown joins them, so a survivor that finishes meanwhile is
// recorded after the marker that lists it.
func (r *SupervisedReopen) ReleaseMarker() {
	if r == nil {
		return
	}
	r.markerOnce.Do(func() { close(r.marker) })
}

// Abandon stops following the supervisors connected during reconciliation,
// leaving them running, when the reopen does not go ahead.
func (r *SupervisedReopen) Abandon() {
	if r == nil {
		return
	}
	r.ReleaseMarker()
	for _, j := range r.jobs {
		if j.handle != nil {
			j.handle.Detach()
		}
	}
}

// subagentJoinWait bounds how long Activate waits for a cancelled subagent
// shell to exit: the supervisor's SIGKILL plus its WaitDelay, and some slack.
const subagentJoinWait = jobshim.WaitDelay + 5*time.Second

// Activate records and follows the reconciliation in ws, the coordinator's
// workspace over the restored registry. It runs before the reopened session
// is published or launched:
//   - it records the downtime finishes and losses (ahead of the run's
//     session_reopened), with a durable claim for the outcomes of subagent
//     jobs nobody will be told of;
//   - it marks exit records harvested once their terminal evidence is durable,
//     and records unknown execution on disk;
//   - it re-hydrates the coordinator's captures into its artifact store one at
//     a time, under the store's bounds, and points every job at its on-disk
//     output with retention re-applied after delivery;
//   - it re-attaches a runner to each job whose execution is not yet
//     confirmed. That shell is registered as running in its worktree for
//     overlap, but its writes are never claimed automatically.
//
// A cancelled subagent shell's exit is awaited (bounded), so the coordinator
// does not run while it is still being stopped.
func (r *SupervisedReopen) Activate(ws *Workspace) {
	if r == nil {
		return
	}
	for _, j := range r.jobs {
		if j.finish != nil {
			j.durable = emitDurable(ws.Emitter, j.owner, event.JobFinished, j.finish)
			if job, ok := ws.Jobs.Get(j.id); ok && j.durable {
				job.MarkRecorded()
			}
		}
		if j.claim != nil {
			emitDurable(ws.Emitter, j.owner, event.JobClaimed, j.claim)
		}
	}
	var joins []*jobs.Job
	for _, j := range r.jobs {
		job, ok := ws.Jobs.Get(j.id)
		if !ok {
			if j.handle != nil {
				j.handle.Detach()
			}
			continue
		}
		dir := r.sup.JobDir(j.id)
		job.SetOutputSource(jobshim.NewReader(dir))
		job.SetNotifiedHook(func() { go enforceSupervisedRetention(ws) })
		if j.harvest && j.durable {
			_ = r.sup.MarkHarvested(j.id)
		}
		if j.unknown != "" {
			_ = r.sup.MarkUnknown(j.id, j.unknown)
		}
		// The coordinator's captures return to its store; a subagent's die
		// with it, never exposed to another actor.
		owner := ws
		if !j.owned {
			owner = &Workspace{Root: ws.Root, Jobs: ws.Jobs, JobSupervisor: ws.JobSupervisor, Emitter: ws.Emitter,
				Ownership: ws.Ownership, MutationToken: ws.MutationToken, Artifacts: NewArtifactStore()}
		}
		if j.hydrate {
			store := owner.artifactStore()
			store.reserveID(j.artifact)
			completeSupervisedArtifact(store, j.artifact, dir)
		}
		if !j.follow {
			continue
		}
		if j.owned && j.artifact != "" {
			owner.artifactStore().reserveID(j.artifact)
		}
		if j.survivor {
			job.SetTerminationHint(fmt.Sprintf("[output artifact %s: capture finalizes after the process exits; retrieve ranges with tool_output; a not-ready response is temporary]", j.artifact))
		}
		var gate <-chan struct{}
		if j.survivor {
			gate = r.marker
		}
		attachSupervisedBash(owner, j.owner, job, j.handle, owner.recoveredShell(), j.artifact, j.timeout, true, gate)
		if j.kill {
			_ = j.handle.Kill()
			if !j.owned {
				joins = append(joins, job)
			}
		}
	}
	for _, job := range joins {
		settled := make(chan struct{})
		go func() { job.WaitExecutionSettled(); close(settled) }()
		select {
		case <-settled:
		case <-time.After(subagentJoinWait):
			// Not confirmed stopped: fail closed. Its runner keeps following
			// it, and a later exit record still confirms the stop.
			job.MarkExecutionUnknown("")
		}
	}
	enforceSupervisedRetention(ws)
}
