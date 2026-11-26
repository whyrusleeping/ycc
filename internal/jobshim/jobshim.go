// Package jobshim supervises background shell jobs out of process, so a job's
// command and its retained evidence can outlive the daemon that started it
// (docs/design/daemon-restart.md §2).
//
// The daemon re-executes the ycc binary with HelperArg; that supervisor process
// runs in its own session, holds an advisory lock on shim.lock for its whole
// life, serves a control socket in a daemon-owned runtime directory, and starts
// `sh -c <command>` in its own process group only after the daemon has made the
// job_started event durable. The child's combined output goes to rotating tail
// segments and a bounded head capture under <session-dir>/jobs/<job-id>/, and
// the supervisor writes an atomic exit.json when the child is done.
//
// The daemon never signals a process id read from the workspace: it asks the
// supervisor to kill its child over the control socket. A supervisor that dies
// without an exit record, or whose control socket vanishes while its lock is
// still held, leaves the job's execution unknown, never reported as complete.
//
// cmd/ycc must call MaybeHelper at the very top of main, and test binaries that
// launch supervised jobs must call it from TestMain.
package jobshim

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// HelperArg is the hidden first argument that marks a re-exec of the ycc binary
// as a job supervisor.
const HelperArg = "__ycc-job-supervisor"

// ProtocolVersion is the supervisor protocol version checked at handshake. A
// replaced binary that speaks a different version is refused rather than
// trusted to behave identically.
const ProtocolVersion = 1

const (
	// SegmentSize is the size of one rotating tail segment.
	SegmentSize = 256 * 1024
	// MaxSegments is how many tail segments are kept (256-512 KiB retained).
	MaxSegments = 2
	// MaxArtifactBytes bounds the head capture in artifact.bin.
	MaxArtifactBytes = 4 * 1024 * 1024
	// WaitDelay bounds how long the supervisor waits for the child's output
	// pipe to close after the child exits or is killed.
	WaitDelay = 10 * time.Second
	// DefaultSessionRetainedBytes bounds the retained output of one session's
	// finished, notified jobs.
	DefaultSessionRetainedBytes = 64 * 1024 * 1024
)

// File names inside a job directory.
const (
	specFile       = "spec.json"
	lockFile       = "shim.lock"
	exitFile       = "exit.json"
	artifactFile   = "artifact.bin"
	supervisorLog  = "supervisor.log"
	harvestedFile  = "harvested"
	unknownFile    = "execution_unknown"
	evictedFile    = "evicted"
	clearedFile    = "cleared" // an operator confirmed the command stopped
	segmentPrefix  = "seg-"
	segmentSuffix  = ".log"
	jobsDirName    = "jobs"
	maxSocketPath  = 100 // below the 104-byte macOS / 108-byte Linux sun_path limit
	readyTimeout   = 15 * time.Second
	controlTimeout = 5 * time.Second
)

// Spec is the versioned job description the daemon writes to spec.json. It
// never contains environment variables: the scrubbed environment reaches the
// supervisor through exec and is passed on to the child.
type Spec struct {
	Version   int    `json:"version"`
	JobID     string `json:"job_id"`
	Command   string `json:"command"`
	Dir       string `json:"dir"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`
	Socket    string `json:"socket"`
}

// Exit states recorded in ExitRecord.State.
const (
	StateExited     = "exited"      // the child ran to completion (any exit code)
	StateTimedOut   = "timed_out"   // the supervisor killed the child at timeout_s
	StateKilled     = "killed"      // the supervisor killed the child on request
	StateStartError = "start_error" // the child could not be started
	StateNotStarted = "not_started" // the start gate closed before "go"
)

// OutputStats describes the job's full output stream and its head capture.
type OutputStats struct {
	Bytes         int64  `json:"bytes"`
	Lines         int64  `json:"lines"`
	SHA256        string `json:"sha256"`
	ArtifactBytes int64  `json:"artifact_bytes"`
	Truncated     bool   `json:"truncated"`
	Loss          string `json:"loss,omitempty"` // a write error lost part of the retained evidence
}

// ExitRecord is the supervisor's atomic completion record (exit.json), also sent
// over the control socket.
type ExitRecord struct {
	Version  int         `json:"version"`
	JobID    string      `json:"job_id"`
	State    string      `json:"state"`
	ExitCode int         `json:"exit_code"`
	Error    string      `json:"error,omitempty"`
	Started  time.Time   `json:"started,omitzero"`
	Finished time.Time   `json:"finished"`
	Output   OutputStats `json:"output"`
}

// MaybeHelper runs the job supervisor and exits when this process was
// re-executed with HelperArg; otherwise it returns immediately.
func MaybeHelper() {
	if len(os.Args) >= 3 && os.Args[1] == HelperArg {
		os.Exit(supervisorMain(os.Args[2]))
	}
}

// JobsDir is the directory holding a session's supervised job directories.
func JobsDir(sessionDir string) string { return filepath.Join(sessionDir, jobsDirName) }

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// ReadExit returns the job directory's exit record, or os.ErrNotExist. A
// record naming another job than the directory's is refused: it confirms
// nothing about this job.
func ReadExit(jobDir string) (*ExitRecord, error) {
	var rec ExitRecord
	if err := readJSON(filepath.Join(jobDir, exitFile), &rec); err != nil {
		return nil, err
	}
	if rec.Version != ProtocolVersion {
		return nil, fmt.Errorf("exit record version %d, want %d", rec.Version, ProtocolVersion)
	}
	if rec.JobID != filepath.Base(jobDir) {
		return nil, fmt.Errorf("exit record names job %q, not %q", rec.JobID, filepath.Base(jobDir))
	}
	return &rec, nil
}

// writeFileAtomic writes data to path through a synced temporary file and a
// rename, then syncs the directory, so readers see the old file or the whole
// new one.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(name)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
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
	if err := os.Rename(name, path); err != nil {
		return err
	}
	ok = true
	syncDir(dir)
	return nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

func writeMarker(jobDir, name, content string) error {
	return writeFileAtomic(filepath.Join(jobDir, name), []byte(content))
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
