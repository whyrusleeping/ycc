package jobshim

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	MaybeHelper()
	os.Exit(m.Run())
}

func testSession(t *testing.T) *Session {
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
	if err := os.Chmod(rt, 0o700); err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(t.TempDir(), ".ycc", "sessions", "s_test")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	l := &Launcher{Executable: exe, RuntimeDir: rt}
	return l.ForSession(sessionDir)
}

func launch(t *testing.T, s *Session, id, command string, timeout time.Duration) *Handle {
	t.Helper()
	p, err := s.Prepare(Request{JobID: id, Command: command, Dir: t.TempDir(), Env: os.Environ(), Timeout: timeout})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	h, err := p.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return h
}

func waitOutcome(t *testing.T, h *Handle) Outcome {
	t.Helper()
	select {
	case <-h.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("supervised job did not finish")
	}
	return h.Outcome()
}

func TestSupervisedJobRunsAndRecordsEvidence(t *testing.T) {
	s := testSession(t)
	p, err := s.Prepare(Request{JobID: "job_1", Command: `printf 'hello %s\n' "$JOBSHIM_SECRET"; exit 3`,
		Dir: t.TempDir(), Env: append(os.Environ(), "JOBSHIM_SECRET=s3cr3t")})
	if err != nil {
		t.Fatal(err)
	}
	h, err := p.Start()
	if err != nil {
		t.Fatal(err)
	}
	out := waitOutcome(t, h)
	if out.Exit == nil || out.Exit.State != StateExited || out.Exit.ExitCode != 3 {
		t.Fatalf("outcome = %+v", out)
	}
	data, start := h.Reader().Retained()
	if start != 0 || string(data) != "hello s3cr3t\n" {
		t.Fatalf("retained = %q at %d", data, start)
	}
	art, stats, complete, err := ReadArtifact(h.Dir())
	if err != nil || !complete || string(art) != "hello s3cr3t\n" || stats.Lines != 1 {
		t.Fatalf("artifact = %q %+v complete=%t err=%v", art, stats, complete, err)
	}
	spec, _ := os.ReadFile(filepath.Join(h.Dir(), specFile))
	if bytes.Contains(spec, []byte("s3cr3t")) {
		t.Fatalf("spec.json persisted the environment: %s", spec)
	}
	for _, name := range []string{"", specFile, exitFile, artifactFile, "seg-0.log", lockFile} {
		fi, err := os.Stat(filepath.Join(h.Dir(), name))
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o600)
		if name == "" {
			want = 0o700
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%q mode = %v, want %v", name, fi.Mode().Perm(), want)
		}
	}
	if _, err := os.Lstat(s.SocketPath("job_1")); !os.IsNotExist(err) {
		t.Errorf("control socket left behind: %v", err)
	}
}

func TestSupervisedJobWaitsForStartGate(t *testing.T) {
	s := testSession(t)
	work := t.TempDir()
	marker := filepath.Join(work, "ran")
	p, err := s.Prepare(Request{JobID: "job_1", Command: "touch " + marker, Dir: work, Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("child ran before the start gate opened")
	}
	if !lockHeld(s.JobDir("job_1")) {
		t.Fatal("ready supervisor does not hold shim.lock")
	}
	if out := waitOutcome(t, mustStart(t, p)); out.Exit == nil || out.Exit.ExitCode != 0 {
		t.Fatalf("outcome = %+v", out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("child did not run after the start gate opened")
	}
}

func mustStart(t *testing.T, p *Pending) *Handle {
	t.Helper()
	h, err := p.Start()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestAbortNeverStartsChild(t *testing.T) {
	s := testSession(t)
	work := t.TempDir()
	marker := filepath.Join(work, "ran")
	p, err := s.Prepare(Request{JobID: "job_1", Command: "touch " + marker, Dir: work, Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	p.Abort()
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("aborted job ran its command")
	}
	if _, err := os.Stat(s.JobDir("job_1")); !os.IsNotExist(err) {
		t.Fatalf("aborted job dir remains: %v", err)
	}
}

func TestSupervisedJobTimeout(t *testing.T) {
	s := testSession(t)
	out := waitOutcome(t, launch(t, s, "job_1", "sleep 30", 300*time.Millisecond))
	if out.Exit == nil || out.Exit.State != StateTimedOut {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestKillOverControlSocketKillsProcessGroup(t *testing.T) {
	s := testSession(t)
	work := t.TempDir()
	pidFile := filepath.Join(work, "pid")
	h := launch(t, s, "job_1", "sleep 30 & echo $! > "+pidFile+"; wait", 0)
	var grandchild int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(pidFile)
		if n, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			grandchild = n
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if grandchild == 0 {
		t.Fatal("job did not start its pipeline")
	}
	if err := h.Kill(); err != nil {
		t.Fatal(err)
	}
	out := waitOutcome(t, h)
	if out.Exit == nil || out.Exit.State != StateKilled {
		t.Fatalf("outcome = %+v", out)
	}
	deadline = time.Now().Add(5 * time.Second)
	for syscall.Kill(grandchild, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if syscall.Kill(grandchild, 0) == nil {
		t.Fatalf("pipeline process %d survived the kill", grandchild)
	}
}

func TestDeadSupervisorIsExecutionUnknown(t *testing.T) {
	s := testSession(t)
	work := t.TempDir()
	pidFile := filepath.Join(work, "pid")
	p, err := s.Prepare(Request{JobID: "job_1", Command: "echo $$ > " + pidFile + "; exec sleep 30", Dir: work, Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	supervisor := p.cmd.Process
	h := mustStart(t, p)
	var child int
	for i := 0; i < 500 && child == 0; i++ {
		data, _ := os.ReadFile(pidFile)
		child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		time.Sleep(10 * time.Millisecond)
	}
	if child == 0 {
		t.Fatal("child did not start")
	}
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
	_ = supervisor.Kill() // simulate a crashed supervisor (test only)
	out := waitOutcome(t, h)
	if out.Exit != nil || !strings.Contains(out.Unknown, "without an exit record") {
		t.Fatalf("outcome = %+v", out)
	}
	if syscall.Kill(child, 0) != nil {
		t.Fatal("test setup: the child should outlive its supervisor")
	}
	if err := h.Kill(); err == nil {
		t.Fatal("Kill after supervision was lost claimed success")
	}
	if got := s.Probe("job_1"); got.Unknown == "" {
		t.Fatalf("Probe = %+v, want unknown", got)
	}
	if why := SessionBusy(s.Dir()); why == "" {
		t.Fatal("session with an unknown job is not protected from GC")
	}
	if err := h.MarkUnknown(out.Unknown); err != nil {
		t.Fatal(err)
	}
	if why := SessionBusy(s.Dir()); !strings.Contains(why, "unknown") {
		t.Fatalf("SessionBusy = %q", why)
	}
}

// A control socket that vanishes while the supervisor still holds its lock is
// lost supervision, both for a probe and for the attached daemon; only the
// supervisor's later exit record confirms the stop.
func TestVanishedSocketWhileLockHeld(t *testing.T) {
	s := testSession(t)
	p, err := s.Prepare(Request{JobID: "job_1", Command: "sleep 30", Dir: t.TempDir(), Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	supervisor := p.cmd.Process
	h := mustStart(t, p)
	if got := s.Probe("job_1"); !got.Running {
		t.Fatalf("Probe = %+v, want running", got)
	}
	if err := os.Remove(s.SocketPath("job_1")); err != nil {
		t.Fatal(err)
	}
	if got := s.Probe("job_1"); got.Running || !strings.Contains(got.Unknown, "vanished") {
		t.Fatalf("Probe = %+v, want unknown (vanished socket)", got)
	}
	out := waitOutcome(t, h)
	if out.Exit != nil || !strings.Contains(out.Unknown, "vanished") {
		t.Fatalf("attached outcome = %+v, want unknown (vanished socket)", out)
	}
	if why := SessionBusy(s.Dir()); why == "" {
		t.Fatal("running job does not protect its session")
	}
	if err := h.Kill(); err == nil {
		t.Fatal("Kill claimed success without a control channel")
	}
	// Test-only stand-in for an operator: SIGTERM makes the supervisor kill
	// its child and still write its exit record.
	_ = supervisor.Signal(syscall.SIGTERM)
	rec := h.Confirm()
	if rec == nil || rec.State != StateKilled {
		t.Fatalf("Confirm = %+v, want the supervisor's killed record", rec)
	}
}

// A failed segment write closes that segment; the next one starts at the true
// stream offset, so readers see the lost bytes as a gap rather than shifted
// data.
func TestSegmentWriteFailureLeavesGap(t *testing.T) {
	dir := t.TempDir()
	w, err := newOutputWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("abc"))
	good := w.cur
	broken, err := os.Open(segmentPath(dir, 0)) // read-only: writes fail
	if err != nil {
		t.Fatal(err)
	}
	w.cur = broken
	w.Write([]byte("def"))
	good.Close()
	w.Write([]byte("ghi"))
	stats := w.finish()
	if stats.Loss == "" || stats.Bytes != 9 || stats.ArtifactBytes != 9 {
		t.Fatalf("stats = %+v", stats)
	}
	data, start := NewReader(dir).Retained()
	if string(data) != "ghi" || start != 6 {
		t.Fatalf("retained %q at %d, want \"ghi\" at 6", data, start)
	}
}

func TestSessionBusyUntilHarvested(t *testing.T) {
	s := testSession(t)
	if why := SessionBusy(s.Dir()); why != "" {
		t.Fatalf("empty session busy: %q", why)
	}
	h := launch(t, s, "job_1", "true", 0)
	waitOutcome(t, h)
	<-h.procExited
	if why := SessionBusy(s.Dir()); !strings.Contains(why, "not been harvested") {
		t.Fatalf("SessionBusy = %q", why)
	}
	if err := h.MarkHarvested(); err != nil {
		t.Fatal(err)
	}
	if why := SessionBusy(s.Dir()); why != "" {
		t.Fatalf("harvested session still busy: %q", why)
	}
}

// expectedSeq is the output of `seq 1 n`.
func expectedSeq(n int) []byte {
	var b bytes.Buffer
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%d\n", i)
	}
	return b.Bytes()
}

func TestSegmentRotationAndConcurrentReads(t *testing.T) {
	s := testSession(t)
	const n = 300000 // about 2 MiB
	want := expectedSeq(n)
	h := launch(t, s, "job_1", fmt.Sprintf("seq 1 %d", n), 0)
	r := h.Reader()
	reads := 0
	for {
		data, start := r.Retained()
		if start+int64(len(data)) > int64(len(want)) || !bytes.Equal(data, want[start:start+int64(len(data))]) {
			t.Fatalf("read %d: retained [%d,%d) does not match the stream", reads, start, start+int64(len(data)))
		}
		reads++
		select {
		case <-h.Done():
		default:
			continue
		}
		break
	}
	out := h.Outcome()
	if out.Exit == nil || out.Exit.Output.Bytes != int64(len(want)) {
		t.Fatalf("outcome = %+v", out)
	}
	data, start := r.Retained()
	if start+int64(len(data)) != int64(len(want)) || len(data) < SegmentSize || len(data) > MaxSegments*SegmentSize {
		t.Fatalf("final retained [%d,+%d) of %d", start, len(data), len(want))
	}
	offsets, _ := listSegments(h.Dir())
	if len(offsets) > MaxSegments {
		t.Fatalf("%d segments retained", len(offsets))
	}
	art, stats, complete, err := ReadArtifact(h.Dir())
	if err != nil || !complete || !bytes.Equal(art, want) || stats.Lines != n {
		t.Fatalf("artifact: %d bytes, complete=%t, stats=%+v, err=%v", len(art), complete, stats, err)
	}
}

func TestArtifactTruncationIsRecorded(t *testing.T) {
	s := testSession(t)
	h := launch(t, s, "job_1", "head -c 5000000 /dev/zero", 0)
	out := waitOutcome(t, h)
	if out.Exit == nil || !out.Exit.Output.Truncated || out.Exit.Output.Bytes != 5000000 {
		t.Fatalf("outcome = %+v", out)
	}
	art, _, complete, err := ReadArtifact(h.Dir())
	if err != nil || complete || len(art) != MaxArtifactBytes {
		t.Fatalf("artifact %d bytes complete=%t err=%v", len(art), complete, err)
	}
}

func TestRetentionEvictsOldestFinishedFirst(t *testing.T) {
	s := testSession(t)
	s.l.RetainedBytes = 2500 // each job keeps 2000 bytes: its tail segment and head capture
	var handles []*Handle
	for i := 1; i <= 3; i++ {
		h := launch(t, s, fmt.Sprintf("job_%d", i), "head -c 1000 /dev/zero", 0)
		waitOutcome(t, h)
		<-h.procExited
		if err := h.MarkHarvested(); err != nil {
			t.Fatal(err)
		}
		handles = append(handles, h)
		time.Sleep(10 * time.Millisecond)
	}
	running := launch(t, s, "job_4", "head -c 1000 /dev/zero; sleep 30", 0)
	defer func() { _ = running.Kill(); waitOutcome(t, running) }()
	time.Sleep(200 * time.Millisecond)
	s.EnforceRetention(func(id string) bool { return id != "job_2" })
	// Only finished, notified evidence counts: job_1 and job_3 (4000 bytes),
	// not job_2 (not evictable) or the running job_4. Evicting the oldest,
	// job_1, brings it within the bound.
	for i, wantEvicted := range []bool{true, false, false} {
		evicted := exists(filepath.Join(handles[i].Dir(), evictedFile))
		if evicted != wantEvicted {
			t.Errorf("job_%d evicted=%t, want %t", i+1, evicted, wantEvicted)
		}
	}
	data, start := handles[0].Reader().Retained()
	if len(data) != 0 || start != 1000 {
		t.Fatalf("evicted job read %d bytes at %d; want none at the stream end", len(data), start)
	}
	if _, _, _, err := ReadArtifact(handles[0].Dir()); err == nil {
		t.Fatal("evicted artifact is still readable")
	}
	// A second pass never resurrects evicted evidence, and finishes an
	// eviction whose deletion was interrupted.
	if err := os.WriteFile(filepath.Join(handles[0].Dir(), artifactFile), []byte("left over"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.EnforceRetention(nil)
	if data, _ := handles[0].Reader().Retained(); len(data) != 0 {
		t.Fatal("evicted evidence came back")
	}
	if exists(filepath.Join(handles[0].Dir(), artifactFile)) {
		t.Fatal("interrupted eviction was not finished")
	}
}

func TestDialRefusesProtocolMismatch(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ycc-rt-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "c.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		data, _ := json.Marshal(controlMsg{Type: "hello", Version: ProtocolVersion + 1})
		conn.Write(append(data, '\n'))
		time.Sleep(time.Second)
		conn.Close()
	}()
	if _, err := dialControl(path); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("dialControl = %v, want a version mismatch", err)
	}
}

func TestPrepareRefusesRuntimeDirInsideWorkspace(t *testing.T) {
	s := testSession(t)
	inside := filepath.Join(s.workspace(), "rt")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	s.l.RuntimeDir = inside
	if _, err := s.Prepare(Request{JobID: "job_1", Command: "true", Dir: s.workspace()}); err == nil || !strings.Contains(err.Error(), "inside the workspace") {
		t.Fatalf("Prepare = %v, want refusal of a runtime dir inside the workspace", err)
	}
}

func TestPrepareRefusesSymlinkedAncestor(t *testing.T) {
	exe, _ := os.Executable()
	rt, err := os.MkdirTemp("/tmp", "ycc-rt-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(rt)
	ws := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.MkdirAll(filepath.Join(elsewhere, "sessions", "s_x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(ws, ".ycc")); err != nil {
		t.Fatal(err)
	}
	s := (&Launcher{Executable: exe, RuntimeDir: rt}).ForSession(filepath.Join(ws, ".ycc", "sessions", "s_x"))
	if _, err := s.Prepare(Request{JobID: "job_1", Command: "true", Dir: ws}); err == nil {
		t.Fatal("Prepare wrote evidence through a symlinked .ycc")
	}
}

func TestPrepareRefusesSymlinkedJobsDir(t *testing.T) {
	s := testSession(t)
	if err := os.Symlink(t.TempDir(), JobsDir(s.Dir())); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prepare(Request{JobID: "job_1", Command: "true", Dir: t.TempDir()}); err == nil {
		t.Fatal("Prepare followed a symlinked jobs dir")
	}
	if _, err := s.Prepare(Request{JobID: "../x", Command: "true", Dir: t.TempDir()}); err == nil {
		t.Fatal("Prepare accepted a path-like job id")
	}
}

func TestRuntimeDirRefusesForeignOrSymlink(t *testing.T) {
	base := t.TempDir()
	link := filepath.Join(base, "rt")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDir(link); err == nil {
		t.Fatal("ensurePrivateDir accepted a symlink")
	}
	loose := filepath.Join(base, "loose")
	if err := os.Mkdir(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDir(loose); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(loose); fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %v", fi.Mode().Perm())
	}
}

// A segment that cannot be created at a rotation boundary loses that span but
// never stalls the writer.
func TestSegmentCreateFailureKeepsDraining(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "capture")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	w, err := newOutputWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(bytes.Repeat([]byte{'a'}, SegmentSize))
	parked := filepath.Join(parent, "parked")
	if err := os.Rename(dir, parked); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { w.Write([]byte("lost")); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("writer stalled when segment creation failed")
	}
	if err := os.Rename(parked, dir); err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("kept"))
	stats := w.finish()
	if stats.Loss == "" || stats.Bytes != SegmentSize+8 {
		t.Fatalf("stats = %+v", stats)
	}
	data, start := NewReader(dir).Retained()
	if string(data) != "kept" || start != SegmentSize+4 {
		t.Fatalf("retained %q at %d", data, start)
	}
}
