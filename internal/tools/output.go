package tools

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/whyrusleeping/gollama"
)

const (
	maxCapturedCommandBytes = 4 * 1024 * 1024
	maxArtifactStoreBytes   = 16 * 1024 * 1024
	maxArtifactRangeBytes   = 128 * 1024
	maxArtifactRecords      = 64
	maxBashLines            = 2000
	maxBashContentBytes     = maxBashBytes - 1024 // reserve result metadata inside the 64 KiB envelope
	maxBashPreviewPayload   = maxBashContentBytes - 256
)

// OutputMetadata describes the captured output behind a bounded tool result.
// Content remains the exact text delivered to the model; this metadata is only
// used to make the durable event distinguish that projection from its capture.
type OutputMetadata struct {
	ArtifactID    string `json:"artifact_id,omitempty"`
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

type outputArtifact struct {
	id      string
	data    []byte
	bytes   int64
	lines   int64
	digest  string
	lost    string
	evicted bool
	ready   bool
}

// ArtifactStore is a session/tool-scope command-output store. IDs are random
// capabilities and stores are not shared between agent workspaces. Both each
// capture and total retained bytes are bounded; old captures become explicit
// tombstones rather than silently resolving to unrelated output.
type ArtifactStore struct {
	mu          sync.Mutex
	items       map[string]*outputArtifact
	order       []string // retained data, oldest first
	recordOrder []string // all live records/tombstones, oldest first
	retained    int
}

func NewArtifactStore() *ArtifactStore {
	return &ArtifactStore{items: make(map[string]*outputArtifact)}
}

func artifactID() string {
	idBytes := make([]byte, 12)
	if _, err := rand.Read(idBytes); err != nil {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%p", &idBytes)))
		copy(idBytes, sum[:])
	}
	return "output_" + hex.EncodeToString(idBytes)
}

// reserve allocates a stable capability before an asynchronous command starts,
// so killed commands still have a discoverable retrieval path while cmd.Wait
// drains and finalizes their capture.
func (s *ArtifactStore) reserve() string {
	id := artifactID()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[id] = &outputArtifact{id: id}
	s.recordOrder = append(s.recordOrder, id)
	s.trimRecordsLocked()
	return id
}

func (s *ArtifactStore) put(data []byte, totalBytes, totalLines int64, digest, lost string) outputArtifact {
	id := s.reserve()
	return s.complete(id, data, totalBytes, totalLines, digest, lost)
}

func (s *ArtifactStore) complete(id string, data []byte, totalBytes, totalLines int64, digest, lost string) outputArtifact {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.items[id]
	if !ok {
		// The reservation aged out while the command was running. Keep the stable
		// capability as an explicit tombstone rather than silently retaining output
		// beyond the record policy.
		a = &outputArtifact{id: id, lost: "artifact reference expired before capture completed"}
		s.items[id] = a
		s.recordOrder = append(s.recordOrder, id)
	} else if len(a.data) > 0 {
		s.retained -= len(a.data)
		s.removeRetainedIDLocked(id)
	}
	a.bytes, a.lines, a.digest, a.ready = totalBytes, totalLines, digest, true
	if a.lost == "" {
		a.lost = lost
	}
	if a.lost == "" {
		for s.retained+len(data) > maxArtifactStoreBytes && len(s.order) > 0 {
			oldID := s.order[0]
			s.order = s.order[1:]
			old, ok := s.items[oldID]
			if !ok || len(old.data) == 0 {
				continue
			}
			s.retained -= len(old.data)
			old.data = nil
			old.evicted = true
			old.lost = "evicted by the bounded artifact retention limit"
		}
		a.data = append([]byte(nil), data...)
		if len(a.data) > 0 {
			s.order = append(s.order, id)
			s.retained += len(a.data)
		}
	}
	s.trimRecordsLocked()
	return cloneArtifact(a)
}

func (s *ArtifactStore) trimRecordsLocked() {
	for len(s.recordOrder) > maxArtifactRecords {
		id := s.recordOrder[0]
		s.recordOrder = s.recordOrder[1:]
		old, ok := s.items[id]
		if !ok {
			continue
		}
		s.retained -= len(old.data)
		s.removeRetainedIDLocked(id)
		delete(s.items, id)
	}
}

func (s *ArtifactStore) removeRetainedIDLocked(id string) {
	for i, candidate := range s.order {
		if candidate == id {
			copy(s.order[i:], s.order[i+1:])
			s.order = s.order[:len(s.order)-1]
			return
		}
	}
}

func cloneArtifact(a *outputArtifact) outputArtifact {
	copy := *a
	copy.data = append([]byte(nil), a.data...)
	return copy
}

func (s *ArtifactStore) get(id string) (outputArtifact, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.items[id]
	if !ok {
		return outputArtifact{}, false
	}
	return cloneArtifact(a), true
}

func toolOutput(ws *Workspace) *gollama.Tool {
	return &gollama.Tool{
		Name: "tool_output",
		Description: "Retrieve a byte range from a retained foreground/background Bash output artifact without rerunning the command. " +
			"offset is zero-based and limit is at most 128 KiB. Artifact access is scoped to this agent session; captures are at most 4 MiB " +
			"and the store retains at most 16 MiB, so storage-limit loss or eviction is reported explicitly.",
		Params: obj(map[string]any{
			"artifact_id": strProp("the output artifact id advertised by Bash, e.g. output_ab12..."),
			"offset":      map[string]any{"type": "integer", "minimum": 0, "description": "zero-based source byte offset (default 0)"},
			"limit":       map[string]any{"type": "integer", "minimum": 1, "maximum": maxArtifactRangeBytes, "description": "maximum source bytes to retrieve (default and maximum 128 KiB)"},
		}, "artifact_id"),
		Call: func(_ context.Context, params any) (*gollama.ToolResult, error) {
			id, ok := getString(params, "artifact_id")
			if !ok {
				return errResult("tool_output: missing 'artifact_id'"), nil
			}
			a, ok := ws.artifactStore().get(id)
			if !ok {
				return errResult("tool_output: artifact %q is not available in this agent session (it may have expired)", id), nil
			}
			if !a.ready {
				return errResult("tool_output: artifact %s capture is not ready; the background process is still exiting. Retry this range after the job process has exited; the command does not need to be rerun", id), nil
			}
			if a.lost != "" {
				return errResult("tool_output: artifact %s is unavailable: %s; capture was %d bytes/%d lines, sha256 %s", id, a.lost, a.bytes, a.lines, a.digest), nil
			}
			offset := getInt(params, "offset", 0)
			if offset < 0 {
				offset = 0
			}
			limit := getInt(params, "limit", maxArtifactRangeBytes)
			if limit < 1 || limit > maxArtifactRangeBytes {
				limit = maxArtifactRangeBytes
			}
			if offset >= len(a.data) {
				return okResult(fmt.Sprintf("artifact %s: offset %d is at/past end (%d bytes)", id, offset, len(a.data))), nil
			}
			start, sourceEnd := utf8Window(a.data, offset, limit)
			renderBudget := limit
			if renderBudget > 1024 {
				renderBudget -= 512
			}
			if sourceEnd == start && start < len(a.data) {
				_, size := utf8.DecodeRune(a.data[start:])
				sourceEnd = start + size
			}
			body, end := renderUTF8Range(a.data, start, sourceEnd, renderBudget)
			more := end < len(a.data)
			note := fmt.Sprintf("\n[artifact %s: bytes %d-%d of %d; more: %t; sha256 %s]", id, start, end, len(a.data), more, a.digest)
			if more {
				note += fmt.Sprintf("\nNext: tool_output({artifact_id:%q, offset:%d, limit:%d})", id, end, limit)
			}
			return okResult(body + note), nil
		},
	}
}

func utf8Window(data []byte, offset, limit int) (int, int) {
	start := offset
	for start < len(data) && start > 0 && !utf8.RuneStart(data[start]) {
		start++
	}
	end := start + limit
	if end > len(data) {
		end = len(data)
	}
	for end > start && end < len(data) && !utf8.RuneStart(data[end]) {
		end--
	}
	return start, end
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
	mu       sync.Mutex
	hash     hash.Hash
	full     []byte
	head     []byte
	tail     []byte
	bytes    int64
	newlines int64
	last     byte
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
	if c.full != nil || c.bytes == int64(len(p)) {
		if c.bytes <= maxCapturedCommandBytes {
			c.full = append(c.full, p...)
		} else {
			c.full = nil
		}
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
		lost = fmt.Sprintf("capture exceeded the %d-byte per-artifact storage limit", maxCapturedCommandBytes)
	}
	return
}

func commandResult(store *ArtifactStore, c *commandCapture) (*gollama.ToolResult, outputArtifact) {
	return commandResultForArtifact(store, c, "")
}

func commandResultForArtifact(store *ArtifactStore, c *commandCapture, artifactID string) (*gollama.ToolResult, outputArtifact) {
	full, head, tail, totalBytes, totalLines, digest, lost := c.snapshot()
	var a outputArtifact
	if artifactID == "" {
		a = store.put(full, totalBytes, totalLines, digest, lost)
	} else {
		a = store.complete(artifactID, full, totalBytes, totalLines, digest, lost)
	}
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
	if truncated {
		preview += fmt.Sprintf("\n[output projection: captured %d bytes/%d lines; budget %d bytes/%d lines; sha256 %s; artifact %s]",
			totalBytes, totalLines, maxBashBytes, maxBashLines, digest, a.id)
		if lost != "" || a.lost != "" {
			preview += "\n[artifact storage loss: " + a.lost + "; omitted output cannot be retrieved]"
		} else {
			preview += fmt.Sprintf("\nRetrieve without rerunning: tool_output({artifact_id:%q, offset:0, limit:%d})", a.id, maxArtifactRangeBytes)
		}
	} else {
		preview += fmt.Sprintf("\n[output capture: %d bytes/%d lines; budget %d bytes/%d lines; sha256 %s; artifact %s]",
			totalBytes, totalLines, maxBashBytes, maxBashLines, digest, a.id)
	}
	res := okResult(preview)
	res.Structured = &OutputMetadata{ArtifactID: a.id, CapturedBytes: totalBytes, CapturedLines: totalLines,
		SHA256: digest, Truncated: truncated, CaptureLost: a.lost}
	return res, a
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
