// Package tools provides the tool registry and the worker tools an agent uses to
// act on a workspace (spec §8). Tools are plain gollama.Tool values; "control"
// tools additionally signal the agent loop (e.g. to stop) via a *Control stashed
// in ToolResult.Structured.
package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/whyrusleeping/gollama"
)

// Control is an out-of-band signal a control tool returns to the agent loop via
// gollama.ToolResult.Structured. Stop ends the loop; Report is the final message;
// Mode, if set, requests the session transition to that mode after the loop ends.
type Control struct {
	Stop   bool
	Report string
	Mode   string
}

// ControlOf returns the *Control carried by a tool result, or nil.
func ControlOf(res *gollama.ToolResult) *Control {
	if res == nil {
		return nil
	}
	if c, ok := res.Structured.(*Control); ok {
		return c
	}
	return nil
}

// Registry holds the tools available to an agent and dispatches calls.
type Registry struct {
	tools  []*gollama.Tool
	byName map[string]*gollama.Tool
}

// New returns an empty Registry.
func New() *Registry {
	return &Registry{byName: map[string]*gollama.Tool{}}
}

// Add registers one or more tools. A later tool with the same name replaces an
// earlier one in both the lookup map and the ordered slice (so APIDefs and
// Dispatch stay consistent).
func (r *Registry) Add(ts ...*gollama.Tool) {
	for _, t := range ts {
		if _, exists := r.byName[t.Name]; exists {
			for i, ex := range r.tools {
				if ex.Name == t.Name {
					r.tools[i] = t
					break
				}
			}
		} else {
			r.tools = append(r.tools, t)
		}
		r.byName[t.Name] = t
	}
}

// APIDefs returns the tool definitions to pass to a model request.
func (r *Registry) APIDefs() []gollama.ToolParam {
	defs := make([]gollama.ToolParam, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, t.ApiDef())
	}
	return defs
}

// Dispatch executes a tool call by name. A missing tool returns an error result
// (not a Go error) so the model can see and recover from it.
func (r *Registry) Dispatch(ctx context.Context, call gollama.ToolCall) *gollama.ToolResult {
	t, ok := r.byName[call.Function.Name]
	if !ok {
		return errResult("no such tool %q", call.Function.Name)
	}
	res, err := gollama.HandleToolCall(ctx, []*gollama.Tool{t}, call)
	if err != nil {
		return errResult("tool %q failed: %v", call.Function.Name, err)
	}
	return res
}

// --- exported helpers for other packages building tools (e.g. orchestrator) ---

// Obj builds a JSON-schema object params spec.
func Obj(props map[string]any, required ...string) gollama.ToolFunctionParams {
	return obj(props, required...)
}

// StrProp builds a {"type":"string","description":...} schema entry.
func StrProp(desc string) map[string]any { return strProp(desc) }

// GetString pulls a required string argument; ok is false if missing/empty.
func GetString(params any, key string) (string, bool) { return getString(params, key) }

// ErrResult builds an error tool result (visible to the model).
func ErrResult(format string, args ...any) *gollama.ToolResult { return errResult(format, args...) }

// OkResult builds a successful tool result.
func OkResult(content string) *gollama.ToolResult { return okResult(content) }

// --- helpers shared by tool implementations ---

// obj builds a JSON-schema "object" params spec for a tool.
func obj(props map[string]any, required ...string) gollama.ToolFunctionParams {
	if required == nil {
		required = []string{}
	}
	return gollama.ToolFunctionParams{Type: "object", Properties: props, Required: required}
}

// strProp is a shorthand for a {"type":"string","description":...} schema entry.
func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// getString pulls a required string argument; ok is false if missing/empty.
func getString(params any, key string) (string, bool) {
	m, ok := params.(map[string]any)
	if !ok {
		return "", false
	}
	s, ok := m[key].(string)
	return s, ok && s != ""
}

// getInt pulls an integer argument (JSON numbers arrive as float64), or def.
func getInt(params any, key string, def int) int {
	if m, ok := params.(map[string]any); ok {
		switch v := m[key].(type) {
		case float64:
			return int(v)
		case int:
			return v
		}
	}
	return def
}

// getBool pulls a boolean argument, or def.
func getBool(params any, key string, def bool) bool {
	if m, ok := params.(map[string]any); ok {
		if b, ok := m[key].(bool); ok {
			return b
		}
	}
	return def
}

func errResult(format string, args ...any) *gollama.ToolResult {
	return &gollama.ToolResult{Content: fmt.Sprintf(format, args...), IsError: true}
}

func okResult(content string) *gollama.ToolResult {
	return &gollama.ToolResult{Content: content}
}

// Workspace confines tool file operations to a root directory.
type Workspace struct {
	Root string
	// OnWrite, if set, is invoked with the resolved absolute path after a
	// successful Write or Edit. Callers use it to surface document updates
	// (e.g. an edit to spec.md) as events; it must not block.
	OnWrite func(path string)
}

// resolve cleans a user-supplied path and confines it to the workspace. Absolute
// paths (the Claude-Code convention) are accepted when they fall within the
// workspace root; relative paths are joined to the root.
//
// Confinement is best-effort and TEXTUAL: it rejects "../" escapes but does NOT
// resolve symlinks, so a symlink already inside the workspace that points outside
// it would not be caught here. Hard isolation (incl. symlinks) is the job of the
// sandboxing work (task 0008); agents also have unrestricted Bash regardless.
func (w *Workspace) resolve(p string) (string, error) {
	if p == "" {
		p = "."
	}
	var clean string
	if filepath.IsAbs(p) {
		clean = filepath.Clean(p)
	} else {
		clean = filepath.Clean(filepath.Join(w.Root, p))
	}
	relToRoot, err := filepath.Rel(w.Root, clean)
	if err != nil {
		return "", fmt.Errorf("invalid path %q", p)
	}
	if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the workspace", p)
	}
	return clean, nil
}
