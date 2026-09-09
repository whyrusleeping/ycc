package modeleval

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/tools"
)

func atomicEditTool(root string) *gollama.Tool {
	return &gollama.Tool{
		Name:        "atomic_edit",
		Description: "Atomically apply multiple exact replacements to one file. Every old string must occur exactly once; validation finishes before a temporary file is renamed over the source, so failure applies no hunks.",
		Params: tools.Obj(map[string]any{
			"file_path": tools.StrProp("repository-relative file path"),
			"replacements": map[string]any{
				"type": "array", "minItems": 1,
				"description": "exact old/new replacements validated and committed together",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"old": tools.StrProp("exact text currently present once"),
						"new": tools.StrProp("replacement text"),
					},
					"required": []string{"old", "new"},
				},
			},
		}, "file_path", "replacements"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			if err := ctx.Err(); err != nil {
				return tools.ErrResult("atomic_edit: %v", err), nil
			}
			name, ok := tools.GetString(params, "file_path")
			if !ok {
				return tools.ErrResult("atomic_edit: missing file_path"), nil
			}
			path, err := confinedPath(root, name)
			if err != nil {
				return tools.ErrResult("atomic_edit: %v", err), nil
			}
			replacements := tools.GetMapSlice(params, "replacements")
			if len(replacements) == 0 {
				return tools.ErrResult("atomic_edit: replacements must not be empty"), nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return tools.ErrResult("atomic_edit: %v", err), nil
			}
			updated := string(data)
			for i, replacement := range replacements {
				old, oldOK := replacement["old"].(string)
				newText, newOK := replacement["new"].(string)
				if !oldOK || old == "" || !newOK {
					return tools.ErrResult("atomic_edit: replacement %d requires non-empty old and string new", i+1), nil
				}
				if count := strings.Count(updated, old); count != 1 {
					return tools.ErrResult("atomic_edit: replacement %d old text occurs %d times (want exactly 1); no changes applied", i+1, count), nil
				}
				updated = strings.Replace(updated, old, newText, 1)
			}
			info, err := os.Stat(path)
			if err != nil {
				return tools.ErrResult("atomic_edit: %v", err), nil
			}
			tmp, err := os.CreateTemp(filepath.Dir(path), ".model-eval-atomic-*")
			if err != nil {
				return tools.ErrResult("atomic_edit: %v", err), nil
			}
			tmpName := tmp.Name()
			defer os.Remove(tmpName)
			err = tmp.Chmod(info.Mode().Perm())
			if err == nil {
				_, err = tmp.WriteString(updated)
			}
			if err == nil {
				err = tmp.Sync()
			}
			if closeErr := tmp.Close(); err == nil {
				err = closeErr
			}
			if err == nil {
				err = os.Rename(tmpName, path)
			}
			if err != nil {
				return tools.ErrResult("atomic_edit: %v", err), nil
			}
			return tools.OkResult(fmt.Sprintf("applied %d replacements atomically to %s", len(replacements), name)), nil
		},
	}
}

func delegateImplementationTool(run func(context.Context, string) (string, error)) *gollama.Tool {
	return &gollama.Tool{
		Name:        "delegate_implementation",
		Description: "Delegate one complete scoped implementation task to an isolated implementer model. Waits for its report; the caller must inspect and verify the resulting repository facts.",
		Params:      tools.Obj(map[string]any{"task": tools.StrProp("complete scoped implementation task and required outcome")}, "task"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			task, ok := tools.GetString(params, "task")
			if !ok {
				return tools.ErrResult("delegate_implementation: missing task"), nil
			}
			report, err := run(ctx, task)
			if err != nil {
				return tools.ErrResult("delegate_implementation: %v", err), nil
			}
			return tools.OkResult("delegated implementer report: " + report), nil
		},
	}
}

func handoffTool() *gollama.Tool {
	return &gollama.Tool{
		Name:        "handoff",
		Description: "Record concise evidence for the evaluation harness's intentional interruption/rollover boundary. This tool does not modify files.",
		Params:      tools.Obj(map[string]any{"summary": tools.StrProp("facts needed to resume the task")}, "summary"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			summary, ok := tools.GetString(params, "summary")
			if !ok {
				return tools.ErrResult("handoff: missing summary"), nil
			}
			return tools.OkResult("handoff recorded: " + summary), nil
		},
	}
}

func investigateTool(root string) *gollama.Tool {
	return &gollama.Tool{
		Name:        "investigate",
		Description: "Delegate read-only investigation of the release-channel fixture. Returns source-attributed evidence; the caller remains responsible for implementation and verification.",
		Params:      tools.Obj(map[string]any{}),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			if err := ctx.Err(); err != nil {
				return tools.ErrResult("investigate: %v", err), nil
			}
			data, err := os.ReadFile(filepath.Join(root, "evidence.txt"))
			if err != nil {
				return tools.ErrResult("investigate: %v", err), nil
			}
			const prefix = "approved release channel: "
			line := strings.TrimSpace(string(data))
			if !strings.HasPrefix(line, prefix) {
				return tools.ErrResult("investigate: evidence.txt lacks an approved release channel"), nil
			}
			channel := strings.TrimSpace(strings.TrimPrefix(line, prefix))
			sum := sha256.Sum256(data)
			return tools.OkResult(fmt.Sprintf("EVIDENCE channel=%s source=evidence.txt sha256=%x", channel, sum)), nil
		},
	}
}

func confinedPath(root, name string) (string, error) {
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("file_path must be repository-relative")
	}
	clean := filepath.Clean(filepath.Join(root, filepath.FromSlash(name)))
	rel, err := filepath.Rel(root, clean)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("file_path escapes repository")
	}
	return clean, nil
}
