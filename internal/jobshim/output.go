package jobshim

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// outputWriter is the supervisor's sink for the child's combined output. It
// writes rotating tail segments and the bounded head capture, and counts and
// hashes the full stream. Write never fails, so a full disk cannot break the
// child's pipe: lost evidence is recorded in the exit record instead.
type outputWriter struct {
	mu       sync.Mutex
	dir      string
	cur      *os.File
	curOff   int64   // absolute offset of the current segment's first byte
	curSize  int64   // bytes actually written to the current segment
	pos      int64   // absolute stream offset of the next byte
	segs     []int64 // retained segment offsets, oldest first
	artifact *os.File
	artBytes int64
	hash     hash.Hash
	total    int64
	newlines int64
	last     byte
	loss     string
}

func newOutputWriter(dir string) (*outputWriter, error) {
	w := &outputWriter{dir: dir, hash: sha256.New()}
	seg, err := createPrivate(segmentPath(dir, 0))
	if err != nil {
		return nil, err
	}
	art, err := createPrivate(filepath.Join(dir, artifactFile))
	if err != nil {
		seg.Close()
		return nil, err
	}
	w.cur, w.segs, w.artifact = seg, []int64{0}, art
	return w, nil
}

func createPrivate(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

func segmentPath(dir string, offset int64) string {
	return filepath.Join(dir, fmt.Sprintf("%s%d%s", segmentPrefix, offset, segmentSuffix))
}

func (w *outputWriter) noteLoss(err error) {
	if w.loss == "" && err != nil {
		w.loss = err.Error()
	}
}

func (w *outputWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, _ = w.hash.Write(p)
	w.total += int64(len(p))
	w.newlines += int64(bytes.Count(p, []byte{'\n'}))
	if len(p) > 0 {
		w.last = p[len(p)-1]
	}
	if w.artifact != nil && w.artBytes < MaxArtifactBytes {
		n := int64(len(p))
		if room := MaxArtifactBytes - w.artBytes; n > room {
			n = room
		}
		k, err := w.artifact.Write(p[:n])
		w.artBytes += int64(k) // the capture holds exactly the bytes that landed
		if err != nil {
			w.noteLoss(fmt.Errorf("artifact write: %w", err))
			_ = w.artifact.Close()
			w.artifact = nil
		}
	}
	for rest := p; len(rest) > 0; {
		if w.cur == nil || w.curSize >= SegmentSize {
			w.rotate(w.pos)
		}
		n := int64(len(rest))
		if room := SegmentSize - w.curSize; n > room {
			n = room
		}
		if w.cur == nil {
			// No segment could be opened: these bytes are lost, and the next
			// segment starts at their true end offset.
			w.pos += n
			rest = rest[n:]
			continue
		}
		k, err := w.cur.Write(rest[:n])
		w.curSize += int64(k)
		w.pos += n
		rest = rest[n:]
		if err != nil || int64(k) != n {
			if err == nil {
				err = fmt.Errorf("short write (%d of %d bytes)", k, n)
			}
			w.noteLoss(fmt.Errorf("segment write: %w", err))
			// Close the damaged segment; the next one starts at the true
			// absolute offset, so readers see the lost bytes as a gap.
			_ = w.cur.Close()
			w.cur = nil
		}
	}
	return len(p), nil
}

// rotate starts a segment at absolute offset next before unlinking the oldest,
// so a reader always finds contiguous coverage of the retained tail.
func (w *outputWriter) rotate(next int64) {
	if w.cur != nil {
		_ = w.cur.Close()
		w.cur = nil
	}
	f, err := createPrivate(segmentPath(w.dir, next))
	if err != nil {
		// Without a segment the next span is lost (Write still consumes it,
		// so the child's output keeps draining) and creation is retried at
		// the following span's true offset.
		w.noteLoss(fmt.Errorf("segment rotate: %w", err))
		w.curOff, w.curSize = next, 0
		return
	}
	w.cur, w.curOff, w.curSize = f, next, 0
	w.segs = append(w.segs, next)
	for len(w.segs) > MaxSegments {
		_ = os.Remove(segmentPath(w.dir, w.segs[0]))
		w.segs = w.segs[1:]
	}
}

// finish syncs and closes the output files and returns the stream's stats.
func (w *outputWriter) finish() OutputStats {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, f := range []*os.File{w.cur, w.artifact} {
		if f != nil {
			if err := f.Sync(); err != nil {
				w.noteLoss(fmt.Errorf("sync output: %w", err))
			}
			_ = f.Close()
		}
	}
	w.cur, w.artifact = nil, nil
	lines := w.newlines
	if w.total > 0 && w.last != '\n' {
		lines++
	}
	return OutputStats{Bytes: w.total, Lines: lines, SHA256: hex.EncodeToString(w.hash.Sum(nil)),
		ArtifactBytes: w.artBytes, Truncated: w.total > MaxArtifactBytes, Loss: w.loss}
}

// Reader reads a job directory's retained tail segments. It is safe to use
// while the supervisor is still writing and rotating them.
type Reader struct{ dir string }

// NewReader returns a reader over jobDir's retained output.
func NewReader(jobDir string) *Reader { return &Reader{dir: jobDir} }

// Retained returns the retained output tail and the absolute offset of its
// first byte. Evicted evidence returns no data positioned at the end of the
// stream, so a read reports the whole stream as an eviction gap rather than
// resurrecting it.
func (r *Reader) Retained() ([]byte, int64) {
	for attempt := 0; attempt < 3; attempt++ {
		data, start, err := r.read()
		if err == nil {
			return data, start
		}
		if !errors.Is(err, fs.ErrNotExist) {
			break
		}
		// Rotation unlinked the oldest segment between listing and reading.
	}
	return nil, r.evictedEnd()
}

func (r *Reader) evictedEnd() int64 {
	if rec, err := ReadExit(r.dir); err == nil {
		return rec.Output.Bytes
	}
	return 0
}

func (r *Reader) read() ([]byte, int64, error) {
	if exists(filepath.Join(r.dir, evictedFile)) {
		return nil, r.evictedEnd(), nil
	}
	offsets, err := listSegments(r.dir)
	if err != nil {
		return nil, 0, err
	}
	if len(offsets) == 0 {
		return nil, 0, nil
	}
	var buf []byte
	start := offsets[0]
	for i, off := range offsets {
		data, err := os.ReadFile(segmentPath(r.dir, off))
		if err != nil {
			return nil, 0, err
		}
		if i > 0 && start+int64(len(buf)) != off {
			// Not contiguous with what precedes it: keep the newer run.
			buf, start = nil, off
		}
		buf = append(buf, data...)
	}
	return buf, start, nil
}

func listSegments(dir string) ([]int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var offsets []int64
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, segmentPrefix) || !strings.HasSuffix(name, segmentSuffix) {
			continue
		}
		off, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(name, segmentPrefix), segmentSuffix), 10, 64)
		if err != nil || off < 0 {
			continue
		}
		offsets = append(offsets, off)
	}
	sort.Slice(offsets, func(i, j int) bool { return offsets[i] < offsets[j] })
	return offsets, nil
}

// ReadArtifact returns the head capture. complete is false when the stream
// exceeded the capture bound or a write error lost part of it, so a truncated
// capture is never presented as the whole output.
func ReadArtifact(jobDir string) (data []byte, stats OutputStats, complete bool, err error) {
	rec, err := ReadExit(jobDir)
	if err != nil {
		return nil, OutputStats{}, false, err
	}
	if exists(filepath.Join(jobDir, evictedFile)) {
		return nil, rec.Output, false, fs.ErrNotExist
	}
	data, err = os.ReadFile(filepath.Join(jobDir, artifactFile))
	if err != nil {
		return nil, rec.Output, false, err
	}
	complete = !rec.Output.Truncated && rec.Output.Loss == "" && int64(len(data)) == rec.Output.Bytes
	return data, rec.Output, complete, nil
}
