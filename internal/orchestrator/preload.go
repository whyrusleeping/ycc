package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/tools"
)

const (
	maxPreloadFiles = 16
	maxPreloadBytes = 64 * 1024
	maxPreloadLines = 2000

	preloadNudge     = "Orient yourself: read these coordinator-preselected files before starting."
	preloadTruncated = "…[preload truncated: total preload budget exceeded; Read the rest yourself]"
	preloadSkipped   = "(preload skipped: total preload budget exhausted; Read this file yourself if needed)"
)

type preloadFile struct {
	Path   string
	Offset *int
	Limit  *int
}

type preloadBuild struct {
	History  []gollama.Message
	Files    int
	Bytes    int
	Omitted  int
	Results  []*gollama.ToolResult
	Calls    []gollama.ToolCall
	Duration []int64
}

// parsePreloadFiles converts the coordinator-facing {path,offset?,limit?}
// objects into Read arguments. Read's normal default is already 2,000 lines;
// explicitly larger limits are clamped to the same per-preload ceiling.
func parsePreloadFiles(params any) []preloadFile {
	raw := tools.GetMapSlice(params, "preload_files")
	out := make([]preloadFile, 0, len(raw))
	for _, item := range raw {
		path, _ := tools.GetString(item, "path")
		p := preloadFile{Path: path}
		if _, ok := item["offset"]; ok {
			v := tools.GetInt(item, "offset", 1)
			p.Offset = &v
		}
		if _, ok := item["limit"]; ok {
			v := tools.GetInt(item, "limit", maxPreloadLines)
			if v > maxPreloadLines {
				v = maxPreloadLines
			}
			p.Limit = &v
		}
		out = append(out, p)
	}
	return out
}

type preloadReadArgs struct {
	FilePath string `json:"file_path"`
	Offset   *int   `json:"offset,omitempty"`
	Limit    *int   `json:"limit,omitempty"`
}

// buildPreloadHistory executes genuine Read calls and turns their bounded,
// text-only results into the synthetic exchange installed before the seed.
// Read failures are deliberately ordinary tool results, never Go errors.
func buildPreloadHistory(ctx context.Context, reg *tools.Registry, files []preloadFile) preloadBuild {
	var out preloadBuild
	if len(files) == 0 {
		return out
	}
	if len(files) > maxPreloadFiles {
		out.Omitted = len(files) - maxPreloadFiles
		files = files[:maxPreloadFiles]
	}
	out.Files = len(files)
	out.Calls = make([]gollama.ToolCall, 0, len(files))
	for i, file := range files {
		argBytes, _ := json.Marshal(preloadReadArgs{FilePath: file.Path, Offset: file.Offset, Limit: file.Limit})
		out.Calls = append(out.Calls, gollama.ToolCall{
			ID:   fmt.Sprintf("preload_%d", i+1),
			Type: "function",
			Function: gollama.ToolCallFunction{
				Name:      "Read",
				Arguments: string(argBytes),
			},
		})
	}

	nudge := preloadNudge
	if out.Omitted > 0 {
		nudge += fmt.Sprintf(" (%d more preload file(s) omitted by the %d-file cap; Read them yourself if needed.)", out.Omitted, maxPreloadFiles)
	}
	out.History = append(out.History,
		gollama.Message{Role: "user", Content: nudge},
		gollama.Message{Role: "assistant", Content: "", ToolCalls: out.Calls},
	)

	// Reserve enough room for a truncation notice and one skipped notice per
	// tuple. This keeps every final tool-result string, including visible bounds
	// markers, inside the advertised aggregate byte cap.
	payloadBudget := maxPreloadBytes - len(preloadTruncated) - len(files)*len(preloadSkipped)
	if payloadBudget < 0 {
		payloadBudget = 0
	}
	payloadUsed := 0
	exhausted := false
	for _, call := range out.Calls {
		var res *gollama.ToolResult
		var ms int64
		if exhausted {
			res = tools.OkResult(preloadSkipped)
		} else {
			start := time.Now()
			res = reg.Dispatch(ctx, call)
			ms = time.Since(start).Milliseconds()
			if len(res.Images) > 0 || len(res.Documents) > 0 {
				kind := "media"
				if len(res.Documents) > 0 {
					kind = "document"
				} else if len(res.Images) > 0 {
					kind = "image"
				}
				base := strings.TrimSpace(res.Content)
				if base != "" {
					base += " "
				}
				res.Content = base + fmt.Sprintf("(preloaded %s attachment omitted to keep seed history text-only; Read this file yourself when visual contents are needed)", kind)
				res.Images = nil
				res.Documents = nil
			}
			remaining := payloadBudget - payloadUsed
			if len(res.Content) > remaining {
				if remaining < 0 {
					remaining = 0
				}
				res.Content = validUTF8Prefix(res.Content, remaining) + preloadTruncated
				exhausted = true
			} else {
				payloadUsed += len(res.Content)
			}
		}
		out.Results = append(out.Results, res)
		out.Duration = append(out.Duration, ms)
		out.Bytes += len(res.Content)
		out.History = append(out.History, gollama.Message{Role: "tool", ToolCallID: call.ID, Content: res.Content})
	}
	return out
}

func validUTF8Prefix(s string, n int) string {
	if n >= len(s) {
		return s
	}
	if n <= 0 {
		return ""
	}
	for n > 0 && (s[n]&0xc0) == 0x80 {
		n--
	}
	return s[:n]
}

// emitSyntheticPreload records exactly the final text installed in history.
// There is intentionally no user_input event: ReplayHistory treats those as
// coordinator conversation even when another actor emitted them.
func emitSyntheticPreload(em *event.Emitter, impl AgentSpec, built preloadBuild) {
	if built.Files == 0 || em == nil {
		return
	}
	em = em.With("implementer")
	em.Emit(event.ModelTurn, map[string]any{
		"text": "", "tool_calls": built.Files, "model_name": impl.Name,
		"backend": impl.Backend, "model_id": impl.Model, "synthetic": true,
	})
	for i, call := range built.Calls {
		em.Emit(event.ToolCall, map[string]any{
			"name": "Read", "args": call.Function.Arguments, "id": call.ID, "synthetic": true,
		})
		res := built.Results[i]
		em.Emit(event.ToolResult, map[string]any{
			"name": "Read", "result": res.Content, "error": res.IsError,
			"id": call.ID, "duration_ms": built.Duration[i], "synthetic": true,
		})
	}
}
