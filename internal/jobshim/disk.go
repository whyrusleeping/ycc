package jobshim

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// lockHeld reports whether a live supervisor holds jobDir's shim.lock.
func lockHeld(jobDir string) bool {
	f, err := os.Open(filepath.Join(jobDir, lockFile))
	if err != nil {
		return false
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_SH|unix.LOCK_NB); err != nil {
		return errors.Is(err, unix.EWOULDBLOCK)
	}
	_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return false
}

// ProbeResult is the observed execution state of a job directory.
type ProbeResult struct {
	Running bool        // a live supervisor answers on its control socket
	Exit    *ExitRecord // the supervisor's exit record
	Unknown string      // why execution is unknown (fail closed)
}

// Probe determines a job's execution state from its directory and its
// daemon-derived control socket, without trusting any process id.
func (s *Session) Probe(jobID string) ProbeResult {
	dir := s.JobDir(jobID)
	if rec, err := ReadExit(dir); err == nil {
		return ProbeResult{Exit: rec}
	}
	if lockHeld(dir) {
		ctl, err := dialControl(s.SocketPath(jobID))
		if err != nil {
			return ProbeResult{Unknown: "the job supervisor's control socket vanished while it still holds " + lockFile}
		}
		ctl.close()
		return ProbeResult{Running: true}
	}
	return ProbeResult{Unknown: "the job supervisor exited without an exit record"}
}

// jobBusy reports why a job directory must not be deleted, or "".
func jobBusy(dir string) string {
	if lockHeld(dir) {
		return "a job supervisor is still running"
	}
	if exists(filepath.Join(dir, unknownFile)) {
		return "a job's execution is unknown"
	}
	if exists(filepath.Join(dir, harvestedFile)) {
		return ""
	}
	if exists(filepath.Join(dir, exitFile)) {
		return "a job's exit record has not been harvested"
	}
	if exists(filepath.Join(dir, specFile)) {
		return "a job supervisor exited without an exit record"
	}
	return ""
}

// SessionBusy reports why sessionDir must not be deleted: a job in it holds a
// live shim.lock, an unharvested exit record, or an uncleared or implied
// execution-unknown state. It returns "" when the directory may go.
func SessionBusy(sessionDir string) string {
	entries, err := os.ReadDir(JobsDir(sessionDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ""
		}
		return fmt.Sprintf("cannot inspect supervised jobs: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if why := jobBusy(filepath.Join(JobsDir(sessionDir), e.Name())); why != "" {
			return e.Name() + ": " + why
		}
	}
	return ""
}

func evidenceSize(dir string) int64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var n int64
	for _, e := range entries {
		name := e.Name()
		if name != artifactFile && !(strings.HasPrefix(name, segmentPrefix) && strings.HasSuffix(name, segmentSuffix)) {
			continue
		}
		if fi, err := e.Info(); err == nil {
			n += fi.Size()
		}
	}
	return n
}

// EnforceRetention bounds the evidence of the session's finished, notified
// jobs: those whose exit record was harvested and for which evictable (the
// daemon's "finished, notified, and no longer executing") is true. Only that
// set counts toward the bound, so running or undelivered jobs never cost
// another job its history. While the set exceeds the bound the oldest
// finished job is evicted first. Evicted jobs keep a marker, so a later read
// reports the eviction instead of resurrecting data; a deletion interrupted
// earlier is finished on the next pass.
func (s *Session) EnforceRetention(evictable func(jobID string) bool) {
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	limit := s.retainedLocked()
	entries, err := os.ReadDir(JobsDir(s.dir))
	if err != nil {
		return
	}
	type candidate struct {
		id       string
		dir      string
		size     int64
		finished time.Time
	}
	var total int64
	var candidates []candidate
	for _, e := range entries {
		if !e.IsDir() || !jobIDPattern.MatchString(e.Name()) {
			continue
		}
		dir := filepath.Join(JobsDir(s.dir), e.Name())
		if exists(filepath.Join(dir, evictedFile)) {
			removeEvidence(dir)
			continue
		}
		if !exists(filepath.Join(dir, harvestedFile)) || exists(filepath.Join(dir, unknownFile)) || lockHeld(dir) {
			continue
		}
		rec, err := ReadExit(dir)
		if err != nil || (evictable != nil && !evictable(e.Name())) {
			continue
		}
		size := evidenceSize(dir)
		total += size
		candidates = append(candidates, candidate{id: e.Name(), dir: dir, size: size, finished: rec.Finished})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].finished.Equal(candidates[j].finished) {
			return candidates[i].finished.Before(candidates[j].finished)
		}
		return jobNumber(candidates[i].id) < jobNumber(candidates[j].id)
	})
	for _, c := range candidates {
		if total <= limit {
			return
		}
		// The marker lands first, so a concurrent reader reports eviction
		// rather than a partially deleted tail.
		if err := writeMarker(c.dir, evictedFile, time.Now().UTC().Format(time.RFC3339Nano)+"\n"); err != nil {
			continue
		}
		removeEvidence(c.dir)
		total -= c.size
	}
}

// removeEvidence deletes an evicted job's tail segments and head capture.
func removeEvidence(dir string) {
	offsets, _ := listSegments(dir)
	for _, off := range offsets {
		_ = os.Remove(segmentPath(dir, off))
	}
	_ = os.Remove(filepath.Join(dir, artifactFile))
}

func jobNumber(id string) int {
	var n int
	_, _ = fmt.Sscanf(id, "job_%d", &n)
	return n
}
