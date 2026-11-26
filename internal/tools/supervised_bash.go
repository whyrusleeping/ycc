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
	started := map[string]any{"id": job.ID(), "kind": job.Kind(), "label": cmdStr, "mutates": job.Mutates(), "supervised": true}

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
	handle, err := pending.Start()
	if err == nil {
		job.SetOutputSource(handle.Reader())
	}
	// kill_job's request goes straight to the supervisor so a failure to reach
	// it is reported; session-end cancellation is relayed in superviseBash.
	job.SetStopper(handle.Kill)
	job.SetNotifiedHook(func() { go enforceSupervisedRetention(ws) })
	go superviseBash(ws, owner, job, handle, before, artifactID, timeout)
	return job, artifactID, nil
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
func superviseBash(ws *Workspace, owner string, job *jobs.Job, h *jobshim.Handle, before *shellSnapshot, artifactID string, timeout time.Duration) {
	go func() {
		select {
		case <-job.Context().Done():
			// kill_job or session end. A failure means supervision is lost,
			// which the outcome below reports.
			_ = h.Kill()
		case <-h.Done():
		}
	}()
	out := h.Outcome()
	if out.Exit != nil {
		harvestSupervisedBash(ws, owner, job, h, out.Exit, before, artifactID, timeout, true)
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
		ws.Emitter.EmitAs(owner, event.JobFinished, map[string]any{
			"id": rep.ID, "kind": rep.Kind, "label": rep.Label,
			"status": string(rep.Status), "tail": rep.Result, "execution_unknown": true,
		})
	} else {
		// Already killed: the kill was requested but never confirmed.
		note = "[" + warning + "]"
	}
	job.MarkExecutionUnknown(note)
	if rec := h.Confirm(); rec != nil {
		harvestSupervisedBash(ws, owner, job, h, rec, before, artifactID, timeout, false)
	}
}

// harvestSupervisedBash turns an exit record into the job's final report and
// artifact, attributes the shell's writes, and releases execution.
func harvestSupervisedBash(ws *Workspace, owner string, job *jobs.Job, h *jobshim.Handle, rec *jobshim.ExitRecord, before *shellSnapshot, artifactID string, timeout time.Duration, report bool) {
	notice := ws.claimShellChanges(before)
	status, exitInfo := supervisedExit(rec, timeout)
	artifact := completeSupervisedArtifact(ws.artifactStore(), artifactID, h.Dir())
	if report {
		result := exitInfo
		if tail := job.Tail(20); strings.TrimSpace(tail) != "" {
			result += "\n" + tail
		}
		if artifact.lost != "" {
			result += fmt.Sprintf("\n[output artifact %s unavailable: %s; captured %d bytes/%d lines, sha256 %s]",
				artifact.id, artifact.lost, artifact.bytes, artifact.lines, artifact.digest)
		} else {
			result += fmt.Sprintf("\n[full output artifact %s: %d bytes/%d lines, sha256 %s; retrieve ranges with tool_output]",
				artifact.id, artifact.bytes, artifact.lines, artifact.digest)
		}
		if notice != "" {
			result += "\n" + notice
		}
		if job.Finish(status, result) {
			emitJobFinished(ws.Emitter, owner, job)
		}
	}
	_ = h.MarkHarvested()
	job.ExecutionComplete()
	// A job notified before it finished executing becomes evictable only now,
	// and only once its supervisor has released shim.lock.
	enforceSupervisedRetention(ws)
	go func() {
		<-h.Exited()
		enforceSupervisedRetention(ws)
	}()
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
	data, stats, complete, err := jobshim.ReadArtifact(jobDir)
	lost := ""
	switch {
	case err != nil:
		lost = "capture unavailable: " + err.Error()
	case stats.Truncated:
		lost = fmt.Sprintf("capture exceeded the %d-byte per-artifact storage limit", maxCapturedCommandBytes)
	case stats.Loss != "":
		lost = "capture incomplete: " + stats.Loss
	case !complete:
		lost = "capture incomplete"
	}
	if stats.SHA256 == "" {
		stats.SHA256 = digestHex(data)
	}
	return store.complete(artifactID, data, stats.Bytes, stats.Lines, stats.SHA256, lost)
}
