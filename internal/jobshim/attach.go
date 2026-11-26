package jobshim

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Attach returns a handle on a supervised job that an earlier daemon started
// (docs/design/daemon-restart.md §3). It follows the job like a handle from
// Start: the exit record arrives over the control socket, and loss of
// supervision fails closed as an unknown outcome. This daemon is not the
// supervisor's parent, so the supervisor's exit is observed through the
// release of its shim.lock instead of being reaped. When the control socket
// cannot be reached the outcome is decided at once: the exit record if the
// supervisor left one, otherwise unknown.
func (s *Session) Attach(jobID string) *Handle {
	dir := s.JobDir(jobID)
	stop := make(chan struct{})
	h := &Handle{s: s, jobID: jobID, dir: dir, socket: s.SocketPath(jobID),
		procExited: lockReleased(dir, stop), stopWatch: stop, done: make(chan struct{})}
	ctl, err := dialControl(h.socket)
	if err != nil {
		if rec, err := ReadExit(dir); err == nil {
			h.outcome = Outcome{Exit: rec}
		} else if lockHeld(dir) {
			h.outcome = Outcome{Unknown: "the job supervisor's control socket vanished while it still holds " + lockFile}
		} else {
			h.outcome = Outcome{Unknown: "the job supervisor exited without an exit record"}
		}
		close(h.done)
		return h
	}
	h.ctl = ctl
	go h.watch()
	return h
}

// lockPoll is how often an attached handle checks whether the supervisor
// still holds its shim.lock.
const lockPoll = 250 * time.Millisecond

// lockReleased returns a channel closed once no supervisor holds dir's
// shim.lock, which the kernel releases however the supervisor exits. Closing
// stop ends the watch (Detach) without closing the channel.
func lockReleased(dir string, stop <-chan struct{}) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		t := time.NewTicker(lockPoll)
		defer t.Stop()
		for lockHeld(dir) {
			select {
			case <-stop:
				return
			case <-t.C:
			}
		}
		close(ch)
	}()
	return ch
}

// DescribeArtifact reports the head capture's stats without reading it.
// complete is false when the capture is truncated, lost bytes, or is missing.
func DescribeArtifact(jobDir string) (stats OutputStats, complete bool, err error) {
	rec, err := ReadExit(jobDir)
	if err != nil {
		return OutputStats{}, false, err
	}
	if Evicted(jobDir) {
		return rec.Output, false, fs.ErrNotExist
	}
	fi, err := os.Stat(filepath.Join(jobDir, artifactFile))
	if err != nil {
		return rec.Output, false, err
	}
	complete = !rec.Output.Truncated && rec.Output.Loss == "" && fi.Size() == rec.Output.Bytes
	return rec.Output, complete, nil
}

// ReadSpec returns jobID's spec.json.
func (s *Session) ReadSpec(jobID string) (*Spec, error) {
	var spec Spec
	if err := readJSON(filepath.Join(s.JobDir(jobID), specFile), &spec); err != nil {
		return nil, err
	}
	if spec.Version != ProtocolVersion || spec.JobID != jobID {
		return nil, errors.New("job spec does not match")
	}
	return &spec, nil
}

// Harvested reports whether an earlier daemon already consumed jobID's exit
// record.
func (s *Session) Harvested(jobID string) bool {
	return exists(filepath.Join(s.JobDir(jobID), harvestedFile))
}

// MarkHarvested records that jobID's exit record is consumed (Handle.MarkHarvested).
func (s *Session) MarkHarvested(jobID string) error {
	dir := s.JobDir(jobID)
	_ = os.Remove(filepath.Join(dir, unknownFile))
	return writeMarker(dir, harvestedFile, time.Now().UTC().Format(time.RFC3339Nano)+"\n")
}

// MarkUnknown records jobID's fail-closed execution-unknown state (Handle.MarkUnknown).
func (s *Session) MarkUnknown(jobID, reason string) error {
	return writeMarker(s.JobDir(jobID), unknownFile, reason+"\n")
}

// Evicted reports whether jobDir's retained evidence was evicted by the
// session's retention bound.
func Evicted(jobDir string) bool { return exists(filepath.Join(jobDir, evictedFile)) }
