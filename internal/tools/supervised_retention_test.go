package tools

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/jobshim"
	"golang.org/x/sys/unix"
)

// delayedShimEnv selects a protocol test double for the supervisor that keeps
// shim.lock for a while after sending its exit record, modelling the window in
// which the real supervisor is still tearing down.
const delayedShimEnv = "YCC_TEST_DELAYED_SHIM"

// maybeDelayedShim runs the test double when TestMain is re-executed with it.
func maybeDelayedShim() {
	if os.Getenv(delayedShimEnv) != "1" || len(os.Args) != 3 || os.Args[1] != jobshim.HelperArg {
		return
	}
	if err := delayedShim(os.Args[2]); err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(2)
	}
	os.Exit(0)
}

func delayedShim(dir string) error {
	blob, err := os.ReadFile(filepath.Join(dir, "spec.json"))
	if err != nil {
		return err
	}
	var spec jobshim.Spec
	if err := json.Unmarshal(blob, &spec); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "shim.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return err
	}
	ln, err := net.Listen("unix", spec.Socket)
	if err != nil {
		return err
	}
	defer ln.Close()
	ready, gate := os.NewFile(3, "ready"), os.NewFile(4, "gate")
	defer gate.Close()
	hello := []byte(`{"type":"hello","version":1}` + "\n")
	if _, err := ready.Write(hello); err != nil {
		return err
	}
	ready.Close()
	conn, err := ln.Accept()
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.Write(hello); err != nil {
		return err
	}
	var word [3]byte
	if _, err := io.ReadFull(gate, word[:]); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "artifact.bin"), make([]byte, 1000), 0o600); err != nil {
		return err
	}
	rec := jobshim.ExitRecord{Version: 1, JobID: spec.JobID, State: jobshim.StateExited, Finished: time.Now(),
		Output: jobshim.OutputStats{Bytes: 1000, ArtifactBytes: 1000}}
	if blob, err = json.Marshal(rec); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "exit.json"), blob, 0o600); err != nil {
		return err
	}
	if err := json.NewEncoder(conn).Encode(map[string]any{"type": "exit", "version": 1, "exit": rec}); err != nil {
		return err
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(dir, "release")); err == nil {
			break
		}
	}
	return nil
}

// Retention reruns once the supervisor releases shim.lock, so a job harvested
// and notified while its supervisor was still exiting is not left above the
// session bound.
func TestSupervisedRetentionAfterSupervisorExit(t *testing.T) {
	t.Setenv(delayedShimEnv, "1")
	reg, jr, sup := supervisedWorkspace(t, &captureRec{})
	sup.SetRetainedBytes(512)
	dispatch(t, reg, "Bash", `{"command":"true","run_in_background":true}`)
	job, _ := jr.Get("job_1")
	waitJobExecution(t, job)
	dispatch(t, reg, "wait", `{"job_ids":["job_1"]}`)
	artifact := filepath.Join(sup.JobDir("job_1"), "artifact.bin")
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(artifact); err != nil {
		t.Fatalf("evidence evicted while its supervisor still held shim.lock: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sup.JobDir("job_1"), "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(artifact); os.IsNotExist(err) {
			return
		}
	}
	t.Fatal("finished, notified evidence stayed above the bound after the supervisor exited")
}
