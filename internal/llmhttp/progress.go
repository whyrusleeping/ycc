package llmhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"sync/atomic"
)

// Progress records whether this request received generated SSE output (as
// opposed to heartbeat or lifecycle frames). It is safe to read after a turn.
type Progress struct{ generated atomic.Bool }

func (p *Progress) Generated() bool { return p.generated.Load() }

// MarkGenerated reports generated output from an adapter that cannot use NewClient.
// It is harmless if the context has no progress tracker.
func MarkGenerated(ctx context.Context) {
	if p, ok := ctx.Value(progressKey{}).(*Progress); ok {
		p.generated.Store(true)
	}
}

type progressKey struct{}

// WithProgress attaches an attempt-local tracker for clients using NewClient.
func WithProgress(ctx context.Context) (context.Context, *Progress) {
	p := &Progress{}
	return context.WithValue(ctx, progressKey{}, p), p
}

// sseLines inspects complete SSE data lines, including lines split across HTTP
// Reads. Only the first 4 KiB of a line is buffered; oversized frames are
// ignored for classification, never retained unboundedly.
type sseLines struct {
	line      []byte
	oversized bool
}

const maxProgressLine = 4 << 10

func (s *sseLines) feed(p []byte, progress *Progress) {
	if progress.Generated() {
		return
	}
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		var part []byte
		if end < 0 {
			part, p = p, nil
		} else {
			part, p = p[:end], p[end+1:]
		}
		if len(s.line)+len(part) > maxProgressLine {
			s.oversized = true
		} else if !s.oversized {
			s.line = append(s.line, part...)
		}
		if end >= 0 {
			if !s.oversized && generatedFrame(s.line) {
				progress.generated.Store(true)
				return
			}
			s.line, s.oversized = s.line[:0], false
		}
	}
}

// This is intentionally a bounded heuristic, not a provider parser. It only
// labels well-formed, recognized generated-output frames; lifecycle and ping
// frames do not count toward the costly partial-output retry budget.
func generatedFrame(line []byte) bool {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return false
	}
	payload := bytes.TrimSpace(line[len("data:"):])
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return false
	}
	var frame struct {
		Type         string `json:"type"`
		ContentBlock struct {
			Type string `json:"type"`
		} `json:"content_block"`
		Choices []struct {
			Delta struct {
				Content          string          `json:"content"`
				Reasoning        string          `json:"reasoning"`
				ReasoningContent string          `json:"reasoning_content"`
				ToolCalls        json.RawMessage `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal(payload, &frame) != nil {
		return false
	}
	switch frame.Type {
	case "content_block_delta", "response.output_text.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta", "response.function_call_arguments.delta", "response.output_item.done":
		return true
	case "content_block_start":
		return frame.ContentBlock.Type == "tool_use"
	}
	for _, choice := range frame.Choices {
		d := choice.Delta
		if d.Content != "" || d.Reasoning != "" || d.ReasoningContent != "" || (len(d.ToolCalls) > 0 && !bytes.Equal(bytes.TrimSpace(d.ToolCalls), []byte("null")) && !bytes.Equal(bytes.TrimSpace(d.ToolCalls), []byte("[]"))) {
			return true
		}
	}
	return false
}
