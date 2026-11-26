package tools

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/whyrusleeping/ycc/internal/credenv"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/jobshim"
)

// startSupervisedBash is startBackgroundBash through an out-of-process job
// supervisor (internal/jobshim, docs/design/daemon-restart.md §2). The command
// never starts before its job_started event is durable: the supervisor waits
// at its start gate until then, and is told to exit without starting the
// command when the event cannot be recorded (the returned error). Output is
// read from the supervisor's on-disk tail segments and head capture.
func startSupervisedBash(ws *Workspace, cmdStr string, timeout time.Duration) (*jobs.Job, string, error) {
	owner := ws.Emitter.Actor()
	job, ok := ws.Jobs.TryStartTracked("bash", cmdStr, owner)
	if !ok {
		return nil, "", nil
	}
	sup := ws.JobSupervisor
	artifactID := ws.artifactStore().reserve()
	job.SetTerminationHint(fmt.Sprintf("[output artifact %s: capture finalizes after the process exits; retrieve ranges with tool_output; a not-ready response is temporary]", artifactID))
	// The artifact id lets a reopen re-hydrate the capture into the owner's
	// store (docs/design/daemon-restart.md §3).
	started := map[string]any{"id": job.ID(), "kind": job.Kind(), "label": cmdStr, "mutates": job.Mutates(),
		"supervised": true, "artifact": artifactID}

	before := ws.snapshotShell(cmdStr, true)
	pending, err := sup.Prepare(jobshim.Request{JobID: job.ID(), Command: cmdStr, Dir: ws.Root,
		Env: append(credenv.Scrub(os.Environ()), ws.Env...), Timeout: timeout})
	if err != nil {
		ws.endShell(before)
		ws.Emitter.EmitAs(owner, event.JobStarted, started)
		ws.artifactStore().complete(artifactID, nil, 0, 0, digestHex(nil), "the command was not started")
		if job.Finish(jobs.Failed, "exit: failed to start: "+err.Error()) {
			emitJobFinished(ws.Emitter, owner, job)
		}
		job.ExecutionComplete()
		return job, artifactID, nil
	}
	// Only a stamped event from a healthy recorder is durable: a closed or
	// failed event.Log returns an unstamped one.
	if ev := ws.Emitter.EmitAs(owner, event.JobStarted, started); ev.Seq == 0 || ws.Emitter.Err() != nil {
		pending.Abort()
		ws.endShell(before)
		ws.artifactStore().complete(artifactID, nil, 0, 0, digestHex(nil), "the command was not started")
		job.Finish(jobs.Failed, "not started: job_started could not be recorded durably")
		job.ExecutionComplete()
		reason := "the session event log is closed"
		if err := ws.Emitter.Err(); err != nil {
			reason = err.Error()
		}
		return nil, "", fmt.Errorf("job_started could not be recorded durably (%s); the command was not started", reason)
	}
	if job.Context().Err() != nil {
		// Session teardown reached the job while it was launching: never start
		// the command (the teardown has already finalized its report).
		pending.Abort()
		ws.endShell(before)
		ws.artifactStore().complete(artifactID, nil, 0, 0, digestHex(nil), "the command was not started")
		job.Finish(jobs.Killed, "killed: the session ended before the command started")
		job.ExecutionComplete()
		return job, artifactID, nil
	}
	handle, err := pending.Start()
	if err == nil {
		job.SetOutputSource(handle.Reader())
	}
	// kill_job's request goes straight to the supervisor so a failure to reach
	// it is reported; session-end cancellation is relayed in superviseBash.
	attachSupervisedBash(ws, owner, job, handle, before, artifactID, timeout, false, nil)
	return job, artifactID, nil
}

// attachSupervisedBash wires a supervised job to its handle and follows it.
// recovered marks a job re-attached after a daemon restart; a non-nil gate
// holds the job's outcome back until it closes (see SupervisedReopen.ReleaseMarker).
func attachSupervisedBash(ws *Workspace, owner string, job *jobs.Job, h *jobshim.Handle, before *shellSnapshot, artifactID string, timeout time.Duration, recovered bool, gate <-chan struct{}) {
	job.SetStopper(h.Kill)
	// A graceful restart leaves the command running for the next daemon.
	job.SetDetacher(h.Detach)
	job.SetNotifiedHook(func() { go enforceSupervisedRetention(ws) })
	go superviseBash(ws, owner, job, h, before, artifactID, timeout, recovered, gate)
}

// enforceSupervisedRetention bounds the session's finished, notified evidence.
func enforceSupervisedRetention(ws *Workspace) {
	ws.JobSupervisor.EnforceRetention(func(id string) bool {
		other, ok := ws.Jobs.Get(id)
		return ok && other.Status() != jobs.Running && other.Notified() && !other.ExecutionRunning()
	})
}

// superviseBash relays kill requests to the supervisor and records the job's
// outcome. A lost supervision fails closed: the job is reported lost with its
// execution unknown, its shell stays registered as running, and only a later
// exit record confirms that execution stopped.
func superviseBash(ws *Workspace, owner string, job *jobs.Job, h *jobshim.Handle, before *shellSnapshot, artifactID string, timeout time.Duration, recovered bool, gate <-chan struct{}) {
	go func() {
		select {
		case <-job.Context().Done():
			// A graceful restart detaches the job or asks its stopper itself
			// (jobs.Registry.SuspendForRestart).
			if jobs.RestartSuspended(job.Context()) {
				return
			}
			// kill_job or session end. A failure means supervision is lost,
			// which the outcome below reports.
			_ = h.Kill()
		case <-h.Done():
		}
	}()
	out := h.Outcome()
	if out.Detached {
		// Left running across a graceful restart: the next daemon's reopen
		// re-attaches it and records its outcome.
		ws.endShell(before)
		return
	}
	if gate != nil {
		<-gate
	}
	if out.Exit != nil {
		harvestSupervisedBash(ws, owner, job, h, out.Exit, before, artifactID, timeout, true, recovered)
		return
	}
	_ = h.MarkUnknown(out.Unknown)
	warning := fmt.Sprintf("supervision of this job was lost (%s); its command may still be running and could not be confirmed stopped", out.Unknown)
	result := "lost: " + warning
	if tail := job.Tail(20); strings.TrimSpace(tail) != "" {
		result += "\n" + tail
	}
	result += fmt.Sprintf("\n[output artifact %s: not available until an exit record confirms the command stopped]", artifactID)
	note := ""
	if job.Finish(jobs.Lost, result) {
		rep := job.Report()
		if emitDurable(ws.Emitter, owner, event.JobFinished, map[string]any{
			"id": rep.ID, "kind": rep.Kind, "label": rep.Label,
			"status": string(rep.Status), "tail": rep.Result, "execution_unknown": true,
		}) {
			job.MarkRecorded()
		}
	} else if !job.ExecutionUnknown() {
		// Already killed: the kill was requested but never confirmed. (A
		// reopen that found supervision already lost said so in the report.)
		note = "[" + warning + "]"
	}
	job.MarkExecutionUnknown(note)
	if rec := h.Confirm(); rec != nil {
		harvestSupervisedBash(ws, owner, job, h, rec, before, artifactID, timeout, false, recovered)
	}
}

// emitDurable records an event and reports whether it is durable: only a
// stamped event from a healthy recorder is.
func emitDurable(em *event.Emitter, actor string, t event.Type, data map[string]any) bool {
	ev := em.EmitAs(actor, t, data)
	return ev.Seq != 0 && em.Err() == nil
}

// recoveredAttributionNote is the recovered report's note that the shell's
// writes were not attributed: its pre-restart snapshot did not survive, and a
// post-restart one would claim writes made by others meanwhile.
const recoveredAttributionNote = "[automatic write attribution was skipped: this command ran across a daemon restart, so its writes are left unclaimed]"

// harvestSupervisedBash turns an exit record into the job's final report and
// artifact, attributes the shell's writes, and releases execution. The exit
// record is marked consumed (harvested, so GC may later remove it) only once
// the job's terminal report is durable in the log (Job.Recorded): the
// job_finished this call records, an earlier one, or the restored log's. A
// report finalized only in memory (a teardown kill) leaves the record for the
// next reopen. Execution is released either way.
func harvestSupervisedBash(ws *Workspace, owner string, job *jobs.Job, h *jobshim.Handle, rec *jobshim.ExitRecord, before *shellSnapshot, artifactID string, timeout time.Duration, report, recovered bool) {
	notice := recoveredAttributionNote
	if recovered {
		ws.endShell(before)
	} else {
		notice = ws.claimShellChanges(before)
	}
	status, exitInfo := supervisedExit(rec, timeout)
	artifact := completeSupervisedArtifact(ws.artifactStore(), artifactID, h.Dir())
	if report {
		if job.Finish(status, supervisedReport(exitInfo, job.Tail(20), artifact, notice)) {
			emitJobFinished(ws.Emitter, owner, job)
		}
	}
	if job.Recorded() {
		_ = h.MarkHarvested()
	}
	job.ExecutionComplete()
	// A job notified before it finished executing becomes evictable only now,
	// and only once its supervisor has released shim.lock.
	enforceSupervisedRetention(ws)
	go func() {
		<-h.Exited()
		enforceSupervisedRetention(ws)
	}()
}

// supervisedReport is a finished supervised job's report: exit summary,
// output tail, artifact reference, and attribution notice.
func supervisedReport(exitInfo, tail string, artifact outputArtifact, notice string) string {
	result := exitInfo
	if strings.TrimSpace(tail) != "" {
		result += "\n" + tail
	}
	switch {
	case artifact.id == "":
		// A job recorded before job_started named its artifact.
	case artifact.lost != "":
		result += fmt.Sprintf("\n[output artifact %s unavailable: %s; captured %d bytes/%d lines, sha256 %s]",
			artifact.id, artifact.lost, artifact.bytes, artifact.lines, artifact.digest)
	default:
		result += fmt.Sprintf("\n[full output artifact %s: %d bytes/%d lines, sha256 %s; retrieve ranges with tool_output]",
			artifact.id, artifact.bytes, artifact.lines, artifact.digest)
	}
	if notice != "" {
		result += "\n" + notice
	}
	return result
}

func supervisedExit(rec *jobshim.ExitRecord, timeout time.Duration) (jobs.Status, string) {
	switch rec.State {
	case jobshim.StateExited:
		if rec.ExitCode == 0 && rec.Error == "" {
			return jobs.Done, "exit 0"
		}
		if rec.Error != "" {
			return jobs.Failed, "exit error: " + rec.Error
		}
		return jobs.Failed, fmt.Sprintf("exit %d", rec.ExitCode)
	case jobshim.StateTimedOut:
		return jobs.Failed, fmt.Sprintf("command timed out after %s", timeout)
	case jobshim.StateKilled:
		return jobs.Failed, "killed: the job supervisor terminated the command"
	default:
		reason := rec.Error
		if reason == "" {
			reason = "the job supervisor did not start the command"
		}
		return jobs.Failed, "exit: failed to start: " + reason
	}
}

// completeSupervisedArtifact moves the supervisor's head capture into the
// actor's artifact store. A capture that was truncated or lost bytes is
// recorded as unavailable, never presented as the complete output.
func completeSupervisedArtifact(store *ArtifactStore, artifactID, jobDir string) outputArtifact {
	data, a := readSupervisedArtifact(artifactID, jobDir)
	if artifactID == "" {
		return a // no capability was recorded for this job
	}
	return store.complete(artifactID, data, a.bytes, a.lines, a.digest, a.lost)
}

// readSupervisedArtifact reads the supervisor's head capture and describes it
// as artifact id, without storing it.
func readSupervisedArtifact(artifactID, jobDir string) ([]byte, outputArtifact) {
	data, stats, complete, err := jobshim.ReadArtifact(jobDir)
	if stats.SHA256 == "" {
		stats.SHA256 = digestHex(data)
	}
	return data, supervisedArtifact(artifactID, jobDir, stats, complete, err)
}

// describeSupervisedArtifact is readSupervisedArtifact without reading the
// capture, for a report that must not hold every capture in memory.
func describeSupervisedArtifact(artifactID, jobDir string) outputArtifact {
	stats, complete, err := jobshim.DescribeArtifact(jobDir)
	return supervisedArtifact(artifactID, jobDir, stats, complete, err)
}

func supervisedArtifact(artifactID, jobDir string, stats jobshim.OutputStats, complete bool, err error) outputArtifact {
	lost := ""
	switch {
	case err != nil && jobshim.Evicted(jobDir):
		lost = "evicted by the session's retained job evidence bound"
	case err != nil:
		lost = "capture unavailable: " + err.Error()
	case stats.Truncated:
		lost = fmt.Sprintf("capture exceeded the %d-byte per-artifact storage limit", maxCapturedCommandBytes)
	case stats.Loss != "":
		lost = "capture incomplete: " + stats.Loss
	case !complete:
		lost = "capture incomplete"
	}
	return outputArtifact{id: artifactID, bytes: stats.Bytes, lines: stats.Lines, digest: stats.SHA256, lost: lost}
}
