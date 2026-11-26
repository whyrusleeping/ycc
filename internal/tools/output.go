package tools

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/jobshim"
	"golang.org/x/sys/unix"
)

const (
	// maxCapturedCommandBytes bounds one command's output file, as
	// jobshim.MaxArtifactBytes bounds a supervised job's.
	maxCapturedCommandBytes = jobshim.MaxArtifactBytes
	maxBashLines            = 2000
	maxBashContentBytes     = maxBashBytes - 1024 // reserve result metadata inside the 64 KiB envelope
	maxBashPreviewPayload   = maxBashContentBytes - 256
)

// OutputMetadata describes the captured output behind a bounded tool result.
// Content remains the exact text delivered to the model; this metadata is only
// used to make the durable event distinguish that projection from its capture.
type OutputMetadata struct {
	OutputFile    string `json:"output_file,omitempty"`
	CapturedBytes int64  `json:"captured_bytes"`
	CapturedLines int64  `json:"captured_lines"`
	SHA256        string `json:"sha256"`
	Truncated     bool   `json:"truncated"`
	CaptureLost   string `json:"capture_lost,omitempty"`
}

// OutputMetadataOf returns capture/projection metadata attached by Read/Bash.
func OutputMetadataOf(res *gollama.ToolResult) *OutputMetadata {
	if res == nil {
		return nil
	}
	m, _ := res.Structured.(*OutputMetadata)
	return m
}

// outputArtifact describes a command's retained output file: its path, the
// capture's size and digest, and any loss (a missing, evicted, or partial
// file is named together with what happened to it).
type outputArtifact struct {
	path   string
	bytes  int64
	lines  int64
	digest string
	lost   string
}

// maxSessionOutputBytes bounds the bytes of a session output directory's
// files (foreground Bash and direct background captures), counting each file
// still being written at the bytes reserved for it. It mirrors, and is kept
// apart from, jobshim's bound on supervised jobs' evidence. A variable so
// tests can lower it.
var maxSessionOutputBytes int64 = jobshim.DefaultSessionRetainedBytes

// maxSessionOutputFiles bounds how many files a session output directory
// holds, so captures of empty or tiny output cannot accumulate without bound.
const maxSessionOutputFiles = 256

// maxTrackedOutputFiles bounds the output files a workspace remembers naming,
// so a later result can report the ones evicted since.
const maxTrackedOutputFiles = 64

// outputFileName matches the files createOutputFile makes; retention never
// touches anything else in the directory.
var outputFileName = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}\.[0-9]{9}Z-[0-9a-f]{8}\.out$`)

var (
	// outputDirMu orders file creation, reservation, and eviction across every
	// workspace of the daemon.
	outputDirMu sync.Mutex
	// activeOutputFiles maps each file still being written to the bytes
	// reserved for it; retention never evicts one.
	activeOutputFiles = map[string]int64{}
)

var errNoOutputDir = errors.New("this agent has no session output directory")

// createOutputFile creates a new private file in the workspace's output
// directory and reserves reserve bytes for it, first evicting the oldest
// finished files until the directory's bounds hold. Its name sorts by
// creation time, which eviction follows. It fails, evicting nothing, when the
// reservations of files still being written leave no room.
func (w *Workspace) createOutputFile(reserve int64) (*os.File, error) {
	if w.OutputDir == "" {
		return nil, errNoOutputDir
	}
	dir := filepath.Clean(w.OutputDir)
	outputDirMu.Lock()
	defer outputDirMu.Unlock()
	d, err := openOutputDir(dir, true)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	if err := makeOutputRoomLocked(d, dir, reserve); err != nil {
		return nil, err
	}
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	name := time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(suffix) + ".out"
	fd, err := unix.Openat(int(d.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create output file: %w", err)
	}
	f := os.NewFile(uintptr(fd), filepath.Join(dir, name))
	activeOutputFiles[f.Name()] = reserve
	return f, nil
}

// openOutputDir opens dir as a directory handle pinned against substitution:
// starting from the workspace that holds its .ycc directory (or, for a dir
// outside one, from its parent), each component is opened relative to the
// previous one without following symlinks, so workspace content cannot
// redirect where captures are written or which files eviction removes. With
// create, the output directory itself is created owner-only if missing; its
// parent (the session directory, which holds the event log) must exist.
func openOutputDir(dir string, create bool) (*os.File, error) {
	base, rel := filepath.Dir(dir), []string{filepath.Base(dir)}
	for d := filepath.Dir(dir); d != filepath.Dir(d); d = filepath.Dir(d) {
		if filepath.Base(d) == ".ycc" {
			base = filepath.Dir(d)
			r, err := filepath.Rel(base, dir)
			if err != nil {
				return nil, err
			}
			rel = strings.Split(r, string(filepath.Separator))
			break
		}
	}
	fd, err := unix.Open(base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", base, err)
	}
	at := base
	for i, name := range rel {
		at = filepath.Join(at, name)
		const flags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
		next, err := unix.Openat(fd, name, flags, 0)
		if errors.Is(err, unix.ENOENT) && create && i == len(rel)-1 {
			if err = unix.Mkdirat(fd, name, 0o700); err == nil || errors.Is(err, unix.EEXIST) {
				next, err = unix.Openat(fd, name, flags, 0)
			}
		}
		unix.Close(fd)
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
			return nil, fmt.Errorf("output directory component %s is not a directory (symlinks are refused)", at)
		}
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", at, err)
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), dir), nil
}

// makeOutputRoomLocked evicts the oldest finished output files of d (the
// pinned handle of dir) until one more file reserving reserve bytes fits both
// bounds. A file that cannot fit beside the active reservations even with
// every finished file gone is refused before anything is evicted.
func makeOutputRoomLocked(d *os.File, dir string, reserve int64) error {
	var reserved int64
	active := 0
	for path, n := range activeOutputFiles {
		if filepath.Dir(path) == dir {
			reserved += n
			active++
		}
	}
	if reserved+reserve > maxSessionOutputBytes || active+1 > maxSessionOutputFiles {
		return fmt.Errorf("the session's output files are full with captures still being written")
	}
	names, err := d.Readdirnames(-1)
	if err != nil {
		return err
	}
	sort.Strings(names) // names sort by creation time
	type file struct {
		name string
		size int64
	}
	var finished []file
	var total int64
	for _, name := range names {
		if !outputFileName.MatchString(name) {
			continue
		}
		if _, ok := activeOutputFiles[filepath.Join(dir, name)]; ok {
			continue
		}
		var st unix.Stat_t
		if unix.Fstatat(int(d.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
			continue
		}
		finished = append(finished, file{name, st.Size})
		total += st.Size
	}
	for len(finished) > 0 && (total+reserved+reserve > maxSessionOutputBytes || len(finished)+active+1 > maxSessionOutputFiles) {
		if err := unix.Unlinkat(int(d.Fd()), finished[0].name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
			return fmt.Errorf("evict output file: %w", err)
		}
		total -= finished[0].size
		finished = finished[1:]
	}
	return nil
}

// finishOutputFile closes f, releasing its reservation; the file is then
// finished and evictable. It returns the close error.
func finishOutputFile(f *os.File) error {
	err := f.Close()
	outputDirMu.Lock()
	delete(activeOutputFiles, f.Name())
	outputDirMu.Unlock()
	return err
}

// removeOutputFile unlinks a file createOutputFile made, through the pinned
// output directory.
func removeOutputFile(path string) {
	outputDirMu.Lock()
	defer outputDirMu.Unlock()
	d, err := openOutputDir(filepath.Dir(path), false)
	if err != nil {
		return
	}
	defer d.Close()
	_ = unix.Unlinkat(int(d.Fd()), filepath.Base(path), 0)
}

// saveOutput writes data to a new output file and returns its path.
func (w *Workspace) saveOutput(data []byte) (string, error) {
	f, err := w.createOutputFile(int64(len(data)))
	if err != nil {
		return "", err
	}
	_, werr := f.Write(data)
	if cerr := finishOutputFile(f); werr == nil {
		werr = cerr
	}
	if werr != nil {
		removeOutputFile(f.Name())
		return "", werr
	}
	w.noteOutputFile(f.Name())
	return f.Name(), nil
}

// noteOutputFile remembers a file this workspace's results named, so a later
// result can report that it was evicted.
func (w *Workspace) noteOutputFile(path string) {
	w.outputMu.Lock()
	defer w.outputMu.Unlock()
	w.outputFiles = append(w.outputFiles, path)
	if n := len(w.outputFiles) - maxTrackedOutputFiles; n > 0 {
		w.outputFiles = append([]string(nil), w.outputFiles[n:]...)
	}
}

// outputEvictionNotice reports, once, the files named in earlier results that
// no longer exist: the session's retention bound evicted them.
func (w *Workspace) outputEvictionNotice() string {
	w.outputMu.Lock()
	defer w.outputMu.Unlock()
	var gone []string
	kept := w.outputFiles[:0]
	for _, path := range w.outputFiles {
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			gone = append(gone, path)
			continue
		}
		kept = append(kept, path)
	}
	w.outputFiles = kept
	if len(gone) == 0 {
		return ""
	}
	return "[output files named in earlier results were evicted by the session's output retention and are gone: " +
		strings.Join(gone, ", ") + "]"
}

// cappedFile writes the first maxCapturedCommandBytes of a stream to a file.
// Write never fails, so a full disk cannot break the command's pipe; the
// first error is kept instead.
type cappedFile struct {
	f   *os.File
	n   int64
	err error
}

func (c *cappedFile) Write(p []byte) (int, error) {
	if c.f != nil && c.err == nil && c.n < maxCapturedCommandBytes {
		k := int64(len(p))
		if room := maxCapturedCommandBytes - c.n; k > room {
			k = room
		}
		written, err := c.f.Write(p[:k])
		c.n += int64(written)
		c.err = err
	}
	return len(p), nil
}

func renderUTF8Range(data []byte, start, end, budget int) (string, int) {
	if budget < utf8.UTFMax {
		budget = utf8.UTFMax
	}
	var b strings.Builder
	at := start
	for at < end {
		r, size := utf8.DecodeRune(data[at:end])
		text := string(r)
		if r == utf8.RuneError && size == 1 {
			text = "�"
		}
		if b.Len()+len(text) > budget {
			break
		}
		b.WriteString(text)
		at += size
	}
	return b.String(), at
}

func lineCount(data []byte) int64 {
	if len(data) == 0 {
		return 0
	}
	n := int64(strings.Count(string(data), "\n"))
	if data[len(data)-1] != '\n' {
		n++
	}
	return n
}

func digestHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type commandCapture struct {
	// headTailOnly skips retaining the full capture, which a background job
	// streams to its output file instead.
	headTailOnly bool
	mu           sync.Mutex
	hash         hash.Hash
	full         []byte
	head         []byte
	tail         []byte
	bytes        int64
	newlines     int64
	last         byte
}

func newCommandCapture() *commandCapture { return &commandCapture{hash: sha256.New()} }

func (c *commandCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = c.hash.Write(p)
	c.bytes += int64(len(p))
	c.newlines += int64(strings.Count(string(p), "\n"))
	if len(p) > 0 {
		c.last = p[len(p)-1]
	}
	const headKeep = maxBashPreviewPayload * 2 / 3
	const tailKeep = maxBashPreviewPayload - headKeep
	if len(c.head) < headKeep {
		n := headKeep - len(c.head)
		if n > len(p) {
			n = len(p)
		}
		c.head = append(c.head, p[:n]...)
	}
	c.tail = append(c.tail, p...)
	if len(c.tail) > tailKeep {
		c.tail = append([]byte(nil), c.tail[len(c.tail)-tailKeep:]...)
	}
	if room := maxCapturedCommandBytes - len(c.full); room > 0 && !c.headTailOnly {
		n := len(p)
		if n > room {
			n = room
		}
		c.full = append(c.full, p[:n]...)
	}
	return len(p), nil
}

func (c *commandCapture) snapshot() (full, head, tail []byte, bytes, lines int64, digest, lost string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	bytes = c.bytes
	lines = c.newlines
	if c.bytes > 0 && c.last != '\n' {
		lines++
	}
	digest = hex.EncodeToString(c.hash.Sum(nil))
	full = append([]byte(nil), c.full...)
	head = append([]byte(nil), c.head...)
	tail = append([]byte(nil), c.tail...)
	if c.bytes > maxCapturedCommandBytes {
		lost = fmt.Sprintf("capture exceeded the %d-byte per-command limit; the file holds only its first %d bytes", maxCapturedCommandBytes, maxCapturedCommandBytes)
	}
	return
}

// commandResult projects a finished foreground capture into a bounded result.
// An over-budget capture is written to an output file the result names.
func commandResult(ws *Workspace, c *commandCapture) *gollama.ToolResult {
	full, head, tail, totalBytes, totalLines, digest, lost := c.snapshot()
	var preview string
	normalized := strings.ToValidUTF8(string(full), "�")
	truncated := totalBytes > maxBashContentBytes || totalLines > maxBashLines || len(normalized) > maxBashContentBytes
	if !truncated {
		preview = normalized
	} else {
		preview = commandPreview(head, tail, totalBytes, totalLines)
	}
	if strings.TrimSpace(preview) == "" {
		preview = "(no output)"
	}
	var file string
	if truncated {
		preview += fmt.Sprintf("\n[output projection: captured %d bytes/%d lines; budget %d bytes/%d lines; sha256 %s]",
			totalBytes, totalLines, maxBashBytes, maxBashLines, digest)
		path, err := ws.saveOutput(full)
		switch {
		case err != nil:
			lost = "full output not retained: " + err.Error()
			preview += "\n[" + lost + "; omitted output cannot be retrieved]"
		case lost != "":
			file = path
			preview += fmt.Sprintf("\nfirst %d bytes of output: %s (%s; use Read with offset/limit, or grep/sed/tail)", len(full), path, lost)
		default:
			file = path
			preview += "\nfull output: " + path + " (use Read with offset/limit, or grep/sed/tail)"
		}
	} else {
		preview += fmt.Sprintf("\n[output capture: %d bytes/%d lines; budget %d bytes/%d lines; sha256 %s]",
			totalBytes, totalLines, maxBashBytes, maxBashLines, digest)
	}
	res := okResult(preview)
	res.Structured = &OutputMetadata{OutputFile: file, CapturedBytes: totalBytes, CapturedLines: totalLines,
		SHA256: digest, Truncated: truncated, CaptureLost: lost}
	return res
}

func commandPreview(head, tail []byte, totalBytes, totalLines int64) string {
	// Select the line-bounded source windows before de-duplicating them. A short,
	// many-line capture can fit in both byte collectors while still requiring a
	// preview; dropping collector overlap first would discard its useful tail.
	head = firstLinesBytes(head, maxBashLines/2)
	tailCollectorBytes := len(tail)
	tail = lastLinesBytes(tail, maxBashLines/2)

	headBudget := maxBashPreviewPayload * 2 / 3
	tailBudget := maxBashPreviewPayload - headBudget
	h, headSourceBytes := normalizeUTF8Prefix(head, headBudget)
	// De-duplicate against the source bytes the bounded normalized head actually
	// rendered, not all bytes selected into the head collector. Replacement
	// expansion can leave an unrendered suffix that the tail still needs to show.
	headEnd := int64(headSourceBytes)
	tailStart := totalBytes - int64(tailCollectorBytes) + int64(tailCollectorBytes-len(tail))
	if overlap := headEnd - tailStart; overlap > 0 {
		if overlap >= int64(len(tail)) {
			tail = nil
		} else {
			tail = tail[overlap:]
		}
	}
	t, tailSourceBytes := normalizeUTF8Suffix(tail, tailBudget)
	shownSourceBytes := int64(headSourceBytes + tailSourceBytes)
	omittedBytes := totalBytes - shownSourceBytes
	if omittedBytes < 0 {
		omittedBytes = 0
	}
	shownLines := lineCount(head[:headSourceBytes]) + lineCount(tail[len(tail)-tailSourceBytes:])
	omittedLines := totalLines - shownLines
	if omittedLines < 0 {
		omittedLines = 0
	}
	return h + fmt.Sprintf("\n…[omitted %d source bytes/%d lines; showing command head and tail]…\n", omittedBytes, omittedLines) + t
}

func normalizeUTF8Prefix(data []byte, budget int) (string, int) {
	var b strings.Builder
	at := 0
	for at < len(data) {
		r, size := utf8.DecodeRune(data[at:])
		text := string(r)
		if r == utf8.RuneError && size == 1 {
			text = "�"
		}
		if b.Len()+len(text) > budget {
			break
		}
		b.WriteString(text)
		at += size
	}
	return b.String(), at
}

func normalizeUTF8Suffix(data []byte, budget int) (string, int) {
	type chunk struct {
		text   string
		source int
	}
	chunks := make([]chunk, 0, len(data))
	for at := 0; at < len(data); {
		r, size := utf8.DecodeRune(data[at:])
		text := string(r)
		if r == utf8.RuneError && size == 1 {
			text = "�"
		}
		chunks = append(chunks, chunk{text: text, source: size})
		at += size
	}
	start, rendered, source := len(chunks), 0, 0
	for start > 0 && rendered+len(chunks[start-1].text) <= budget {
		start--
		rendered += len(chunks[start].text)
		source += chunks[start].source
	}
	var b strings.Builder
	b.Grow(rendered)
	for _, chunk := range chunks[start:] {
		b.WriteString(chunk.text)
	}
	return b.String(), source
}

func firstLinesBytes(data []byte, n int) []byte {
	if n <= 0 {
		return nil
	}
	at := 0
	for i := 0; i < n; i++ {
		next := bytes.IndexByte(data[at:], '\n')
		if next < 0 {
			return data
		}
		at += next + 1
	}
	return data[:at]
}

func lastLinesBytes(data []byte, n int) []byte {
	if n <= 0 {
		return nil
	}
	end := len(data)
	if end > 0 && data[end-1] == '\n' {
		end--
	}
	at := end
	for i := 0; i < n; i++ {
		prev := bytes.LastIndexByte(data[:at], '\n')
		if prev < 0 {
			return data
		}
		at = prev
	}
	return data[at+1:]
}

func utf8PrefixLen(data []byte, n int) int {
	if n > len(data) {
		n = len(data)
	}
	for n > 0 && !utf8.Valid(data[:n]) {
		n--
	}
	return n
}
