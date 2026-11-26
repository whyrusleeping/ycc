package tools

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/jobshim"
)

func supervisedWorkspace(t *testing.T, rec event.Recorder) (*Registry, *jobs.Registry, *jobshim.Session) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	rt, err := os.MkdirTemp("/tmp", "ycc-rt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(rt) })
	_ = os.Chmod(rt, 0o700)
	root := t.TempDir()
	sessionDir := filepath.Join(root, ".ycc", "sessions", "s_test")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sup := (&jobshim.Launcher{Executable: exe, RuntimeDir: rt}).ForSession(sessionDir)
	jr := jobs.NewRegistry()
	t.Cleanup(func() { _ = jr.KillAll() })
	ws := &Workspace{Root: root, Jobs: jr, JobSupervisor: sup, Emitter: event.NewEmitter(rec, "coordinator")}
	reg := New()
	reg.Add(Editing(ws)...)
	return reg, jr, sup
}

func waitJobExecution(t *testing.T, job *jobs.Job) {
	t.Helper()
	done := make(chan struct{})
	go func() { job.WaitExecution(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("%s execution was never joined", job.ID())
	}
}

// A supervised background command reports its exit and output like a direct
// one, with its output read back from the supervisor's files.
func TestSupervisedBackgroundBash(t *testing.T) {
	rec := &captureRec{}
	reg, jr, sup := supervisedWorkspace(t, rec)
	res := dispatch(t, reg, "Bash", `{"command":"printf 'one\ntwo\n'; exit 2","run_in_background":true}`)
	if res.IsError || !strings.Contains(res.Content, "job_1") {
		t.Fatalf("Bash bg = %q", res.Content)
	}
	started := rec.find(event.JobStarted)
	if started == nil || started.Data["supervised"] != true {
		t.Fatalf("job_started = %+v, want supervised", started)
	}
	res = dispatch(t, reg, "wait", `{"job_ids":["job_1"],"timeout_s":30}`)
	if !strings.Contains(res.Content, "[job job_1 failed]") || !strings.Contains(res.Content, "exit 2") ||
		!strings.Contains(res.Content, "two") || !strings.Contains(res.Content, "[full output: ") {
		t.Fatalf("wait = %q", res.Content)
	}
	job, _ := jr.Get("job_1")
	waitJobExecution(t, job)
	if fin := rec.find(event.JobFinished); fin == nil || fin.Data["status"] != "failed" {
		t.Fatalf("job_finished = %+v", fin)
	}
	out := dispatch(t, reg, "job_output", `{"job_id":"job_1","cursor":4}`)
	if !strings.HasPrefix(out.Content, "job job_1 [failed]\ntwo\n") || !strings.Contains(out.Content, "output bytes 4-8") {
		t.Fatalf("job_output = %q", out.Content)
	}
	path := between(res.Content, "[full output: ", ": ")
	if path != jobshim.ArtifactPath(sup.JobDir("job_1")) {
		t.Fatalf("report names output file %q", path)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "one\ntwo\n" {
		t.Fatalf("output file = %q, %v", data, err)
	}
	if why := jobshim.SessionBusy(sup.Dir()); why != "" {
		t.Fatalf("harvested job still protects its session: %q", why)
	}
}

// A supervised job's over-budget output is the job directory's output file,
// named when the job starts and in its report, and readable with Read.
func TestSupervisedBackgroundBashOutputFile(t *testing.T) {
	rec := &captureRec{}
	reg, _, sup := supervisedWorkspace(t, rec)
	started := dispatch(t, reg, "Bash", `{"command":"seq 1 30000 | sed 's/^/row /'","run_in_background":true}`)
	path := between(started.Content, "Its output is written to ", " while it runs")
	if path != jobshim.ArtifactPath(sup.JobDir("job_1")) {
		t.Fatalf("start result names %q: %q", path, started.Content)
	}
	report := dispatch(t, reg, "wait", `{"job_ids":["job_1"],"timeout_s":30}`)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("[full output: %s: %d bytes/30000 lines, sha256 %s]", path, len(data), digestHex(data)); !strings.Contains(report.Content, want) {
		t.Fatalf("report = %q, want %q", report.Content, want)
	}
	window := dispatch(t, reg, "Read", fmt.Sprintf(`{"file_path":%q,"offset":15000,"limit":2}`, path))
	if window.IsError || !strings.Contains(window.Content, "row 15000") || !strings.Contains(window.Content, "row 15001") || strings.Contains(window.Content, "row 15002") {
		t.Fatalf("Read window = %q", window.Content)
	}
}

func between(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	s = s[i+len(start):]
	if j := strings.Index(s, end); j >= 0 {
		return s[:j]
	}
	return s
}

// kill_job reaches the supervisor only over its control socket; execution is
// joined once the supervisor's exit record lands.
func TestSupervisedKillJob(t *testing.T) {
	rec := &captureRec{}
	reg, jr, sup := supervisedWorkspace(t, rec)
	dispatch(t, reg, "Bash", `{"command":"sleep 30","run_in_background":true}`)
	res := dispatch(t, reg, "kill_job", `{"job_id":"job_1"}`)
	if res.IsError || !strings.Contains(res.Content, "killed job_1") {
		t.Fatalf("kill_job = %q", res.Content)
	}
	job, _ := jr.Get("job_1")
	waitJobExecution(t, job)
	exit, err := jobshim.ReadExit(sup.JobDir("job_1"))
	if err != nil || exit.State != jobshim.StateKilled {
		t.Fatalf("exit record = %+v, %v", exit, err)
	}
}

func TestSupervisedTimeout(t *testing.T) {
	reg, _, _ := supervisedWorkspace(t, &captureRec{})
	dispatch(t, reg, "Bash", `{"command":"sleep 30","run_in_background":true,"timeout_s":1}`)
	res := dispatch(t, reg, "wait", `{"job_ids":["job_1"],"timeout_s":30}`)
	if !strings.Contains(res.Content, "command timed out after 1s") {
		t.Fatalf("wait = %q", res.Content)
	}
}

// failingRecorder records events but reports a terminal persistence failure,
// as event.Log does once an fsync fails.
type failingRecorder struct{ captureRec }

func (f *failingRecorder) Err() error { return errors.New("sync event log: disk full") }

// When job_started cannot be made durable the supervisor is told to exit and
// the command never runs.
func TestSupervisedBashAbortsWhenStartIsNotDurable(t *testing.T) {
	rec := &failingRecorder{}
	reg, _, sup := supervisedWorkspace(t, rec)
	marker := filepath.Join(t.TempDir(), "ran")
	res := dispatch(t, reg, "Bash", `{"command":"touch `+marker+`","run_in_background":true}`)
	if !res.IsError || !strings.Contains(res.Content, "could not be recorded durably") {
		t.Fatalf("Bash bg = %q, want a durability refusal", res.Content)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the command ran although job_started was not durable")
	}
	if _, err := os.Stat(sup.JobDir("job_1")); !os.IsNotExist(err) {
		t.Fatalf("aborted job dir remains: %v", err)
	}
}

// supervisorPID finds the supervisor serving jobDir (test-only; the daemon
// itself never signals a process id).
func supervisorPID(t *testing.T, jobDir string) int {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Skip("no /proc on this host")
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cmdline, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		args := bytes.Split(cmdline, []byte{0})
		if len(args) >= 3 && string(args[1]) == jobshim.HelperArg && string(args[2]) == jobDir {
			return pid
		}
	}
	t.Fatalf("no supervisor for %s", jobDir)
	return 0
}

// A supervisor that dies without an exit record fails closed: the job is lost
// with its execution unknown, kill_job and KillAll say so instead of claiming a
// clean join, and the owner still gets one deliverable report.
func TestSupervisedLostSupervisionIsExecutionUnknown(t *testing.T) {
	rec := &captureRec{}
	reg, jr, sup := supervisedWorkspace(t, rec)
	pidFile := filepath.Join(t.TempDir(), "pid")
	dispatch(t, reg, "Bash", `{"command":"echo $$ > `+pidFile+`; exec sleep 30","run_in_background":true}`)
	var child int
	for i := 0; i < 500 && child == 0; i++ {
		data, _ := os.ReadFile(pidFile)
		child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		time.Sleep(10 * time.Millisecond)
	}
	if child == 0 {
		t.Fatal("command did not start")
	}
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
	if err := syscall.Kill(supervisorPID(t, sup.JobDir("job_1")), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	job, _ := jr.Get("job_1")
	select {
	case <-job.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("lost supervision was never reported")
	}
	deadline := time.Now().Add(10 * time.Second)
	for !job.ExecutionUnknown() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if job.Status() != jobs.Lost || !job.ExecutionUnknown() || !job.ExecutionRunning() {
		t.Fatalf("status=%s unknown=%t running=%t", job.Status(), job.ExecutionUnknown(), job.ExecutionRunning())
	}
	fin := rec.find(event.JobFinished)
	if fin == nil || fin.Data["status"] != "lost" || fin.Data["execution_unknown"] != true {
		t.Fatalf("job_finished = %+v", fin)
	}
	reports := jr.DrainFinished("coordinator")
	if len(reports) != 1 || !strings.Contains(reports[0].Result, "may still be running") {
		t.Fatalf("delivered reports = %+v", reports)
	}
	res := dispatch(t, reg, "kill_job", `{"job_id":"job_1"}`)
	if !res.IsError || !strings.Contains(res.Content, "could not be confirmed stopped") {
		t.Fatalf("kill_job = %q", res.Content)
	}
	var unjoined *jobs.UnjoinedError
	if err := jr.KillAllCause(nil); !errors.As(err, &unjoined) || len(unjoined.IDs) != 1 || unjoined.IDs[0] != "job_1" {
		t.Fatalf("KillAllCause = %v, want job_1 unjoined", err)
	}
	if why := jobshim.SessionBusy(sup.Dir()); !strings.Contains(why, "unknown") {
		t.Fatalf("SessionBusy = %q", why)
	}
}

// A closed event log returns an unstamped event without an error: that is not
// a durable job_started either, so the command must not run.
func TestSupervisedBashAbortsOnClosedLog(t *testing.T) {
	log, err := event.OpenLog(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	reg, _, _ := supervisedWorkspace(t, log)
	marker := filepath.Join(t.TempDir(), "ran")
	res := dispatch(t, reg, "Bash", `{"command":"touch `+marker+`","run_in_background":true}`)
	if !res.IsError || !strings.Contains(res.Content, "could not be recorded durably") {
		t.Fatalf("Bash bg = %q, want a durability refusal", res.Content)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the command ran without a durable job_started")
	}
}

// The attached daemon treats a vanished control socket as lost supervision.
func TestSupervisedAttachedSocketVanishes(t *testing.T) {
	reg, jr, sup := supervisedWorkspace(t, &captureRec{})
	dispatch(t, reg, "Bash", `{"command":"sleep 30","run_in_background":true}`)
	pid := supervisorPID(t, sup.JobDir("job_1"))
	// Test-only cleanup: SIGTERM makes the supervisor kill its child.
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGTERM) })
	if err := os.Remove(sup.SocketPath("job_1")); err != nil {
		t.Fatal(err)
	}
	job, _ := jr.Get("job_1")
	select {
	case <-job.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("vanished control socket was never noticed")
	}
	deadline := time.Now().Add(5 * time.Second)
	for !job.ExecutionUnknown() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if job.Status() != jobs.Lost || !job.ExecutionUnknown() {
		t.Fatalf("status=%s unknown=%t", job.Status(), job.ExecutionUnknown())
	}
	// The supervisor's later exit record confirms the stop.
	_ = syscall.Kill(pid, syscall.SIGTERM)
	waitJobExecution(t, job)
	if job.ExecutionUnknown() {
		t.Fatal("a confirmed exit did not clear execution_unknown")
	}
}

// A kill whose effect is never confirmed says so in the report delivered to
// the owner, even though the kill already finalized the job.
func TestSupervisedKillThenLostSupervisionWarns(t *testing.T) {
	reg, jr, sup := supervisedWorkspace(t, &captureRec{})
	pidFile := filepath.Join(t.TempDir(), "pid")
	dispatch(t, reg, "Bash", `{"command":"echo $$ > `+pidFile+`; exec sleep 30","run_in_background":true}`)
	var child int
	for i := 0; i < 500 && child == 0; i++ {
		data, _ := os.ReadFile(pidFile)
		child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		time.Sleep(10 * time.Millisecond)
	}
	if child == 0 {
		t.Fatal("command did not start")
	}
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
	if err := syscall.Kill(supervisorPID(t, sup.JobDir("job_1")), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	dispatch(t, reg, "kill_job", `{"job_id":"job_1"}`)
	job, _ := jr.Get("job_1")
	deadline := time.Now().Add(10 * time.Second)
	for !job.ExecutionUnknown() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	reports := jr.DrainFinished("coordinator")
	if len(reports) != 1 || !strings.Contains(reports[0].Result, "may still be running") {
		t.Fatalf("delivered = %+v, want the supervision-loss warning", reports)
	}
}

// Evidence of the last job becomes evictable once it is both harvested and
// notified, whichever happens last.
func TestSupervisedRetentionAfterLastClaim(t *testing.T) {
	reg, jr, sup := supervisedWorkspace(t, &captureRec{})
	sup.SetRetainedBytes(512)
	dispatch(t, reg, "Bash", `{"command":"head -c 1000 /dev/zero","run_in_background":true}`)
	job, _ := jr.Get("job_1")
	waitJobExecution(t, job)
	if _, err := os.Stat(filepath.Join(sup.JobDir("job_1"), "artifact.bin")); err != nil {
		t.Fatalf("unnotified evidence was evicted: %v", err)
	}
	dispatch(t, reg, "wait", `{"job_ids":["job_1"]}`)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(sup.JobDir("job_1"), "artifact.bin")); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("notified evidence above the session bound was never evicted")
}
