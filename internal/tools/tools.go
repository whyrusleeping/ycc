// Package tools provides the tool registry and the worker tools an agent uses to
// act on a workspace (spec §8). Tools are plain gollama.Tool values; "control"
// tools additionally signal the agent loop (e.g. to stop) via a *Control stashed
// in ToolResult.Structured.
package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/whyrusleeping/gollama"
)

// Control is an out-of-band signal a control tool returns to the agent loop via
// gollama.ToolResult.Structured. Stop ends the loop; Report is the final message;
// Mode, if set, requests the session transition to that mode after the loop ends;
// Prompt, if set, is the verbatim seed prompt for the new mode's loop (used by the
// pm → work hand-off to carry the target task + planning context instead of letting
// the work coordinator re-pick a task).
type Control struct {
	Stop   bool
	Report string
	Mode   string
	Prompt string
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

// BoolProp builds a {"type":"boolean","description":...} schema entry.
func BoolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

// StrArrProp builds a {"type":"array","items":{"type":"string"},...} schema
// entry for an optional list-of-strings argument.
func StrArrProp(desc string) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": desc,
		"items":       map[string]any{"type": "string"},
	}
}

// GetString pulls a required string argument; ok is false if missing/empty.
func GetString(params any, key string) (string, bool) { return getString(params, key) }

// GetStringSlice pulls a list-of-strings argument, ignoring non-string entries.
// Returns nil when the argument is absent.
func GetStringSlice(params any, key string) []string { return getStringSlice(params, key) }

// GetMapSlice pulls an array-of-objects argument, coercing each element that is
// a map[string]any. Returns nil when the argument is absent or not an array.
func GetMapSlice(params any, key string) []map[string]any { return getMapSlice(params, key) }

// GetBool pulls a boolean argument, returning def when absent or not a boolean.
func GetBool(params any, key string, def bool) bool { return getBool(params, key, def) }

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

// getStringSlice pulls a list-of-strings argument, dropping non-string and empty
// entries. Returns nil when absent or not an array.
func getStringSlice(params any, key string) []string {
	m, ok := params.(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := m[key].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, v := range raw {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// getMapSlice pulls an array-of-objects argument, coercing each element that is
// a map[string]any. Non-object elements are skipped. Returns nil when absent or
// not an array.
func getMapSlice(params any, key string) []map[string]any {
	m, ok := params.(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := m[key].([]any)
	if !ok {
		return nil
	}
	var out []map[string]any
	for _, v := range raw {
		if mm, ok := v.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out
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
	// ReadRoots are absolute, trusted read-only roots OUTSIDE the workspace that
	// the Read tool may also read from (e.g. the Go module cache and GOROOT).
	// They relax read access only: Write/Edit stay confined to Root via resolve.
	// Containment against these roots is symlink-aware (see resolveRead) so a
	// symlink inside an allowed root cannot be used to escape it.
	ReadRoots []string
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

// resolveRead cleans a user-supplied path for READ access. Like resolve it
// accepts in-workspace paths (absolute within Root, or relative to Root). In
// addition it accepts paths that fall within one of the trusted read-only roots
// in w.ReadRoots (e.g. the Go module cache), so the model can read dependency
// source that lives outside the workspace. Containment against ReadRoots is
// symlink-aware so an allowed root cannot be used to escape into arbitrary
// locations via a symlink. Write/Edit must keep using resolve — this relaxes
// read access only.
func (w *Workspace) resolveRead(p string) (string, error) {
	if p == "" {
		p = "."
	}
	var clean string
	if filepath.IsAbs(p) {
		clean = filepath.Clean(p)
	} else {
		clean = filepath.Clean(filepath.Join(w.Root, p))
	}
	// In-workspace fast path: same TEXTUAL ".."-rejection check resolve() uses,
	// to preserve the current in-workspace behavior exactly.
	if relToRoot, err := filepath.Rel(w.Root, clean); err == nil {
		if relToRoot != ".." && !strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
			return clean, nil
		}
	}
	// Otherwise the path must fall within one of the trusted read-only roots.
	for _, root := range w.ReadRoots {
		if root == "" {
			continue
		}
		if withinRoot(clean, root) {
			return clean, nil
		}
	}
	return "", fmt.Errorf("path %q is outside the workspace and not within a trusted read-only root", p)
}

// withinRoot reports whether clean is contained within root, resolving symlinks
// on both sides so the check cannot be fooled by a symlink inside root that
// points elsewhere. Non-existent paths are handled by evalExisting, which
// resolves the longest existing prefix.
func withinRoot(clean, root string) bool {
	cr := evalExisting(clean)
	rr := evalExisting(root)
	rel, err := filepath.Rel(rr, cr)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// evalExisting resolves symlinks in the longest existing prefix of p, then
// re-appends the (non-existent) trailing suffix. This makes containment checks
// robust when p does not yet exist: we resolve as far as the filesystem lets us
// and treat the remainder textually. If no prefix resolves, p is returned as-is.
func evalExisting(p string) string {
	p = filepath.Clean(p)
	cur := p
	var suffix string
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			if suffix == "" {
				return resolved
			}
			return filepath.Join(resolved, suffix)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// Reached the root without anything resolving.
			return p
		}
		suffix = filepath.Join(filepath.Base(cur), suffix)
		cur = parent
	}
}

// DefaultReadRoots returns the built-in trusted read-only roots: the Go module
// cache and GOROOT. Module-cache resolution mirrors the Go toolchain: $GOMODCACHE
// if set, else the first $GOPATH entry's pkg/mod, else $HOME/go/pkg/mod. Missing
// env vars are handled gracefully (entries that cannot be resolved are skipped).
// Each returned path is absolute and cleaned.
func DefaultReadRoots() []string {
	var roots []string
	if mc := goModCache(); mc != "" {
		roots = append(roots, mc)
	}
	if gr := runtime.GOROOT(); gr != "" {
		roots = append(roots, filepath.Clean(gr))
	}
	return roots
}

// goModCache resolves the Go module cache directory the way the toolchain does,
// returning "" only if even the $HOME fallback is unavailable.
func goModCache() string {
	if v := os.Getenv("GOMODCACHE"); v != "" {
		return filepath.Clean(v)
	}
	if gp := os.Getenv("GOPATH"); gp != "" {
		if parts := filepath.SplitList(gp); len(parts) > 0 && parts[0] != "" {
			return filepath.Clean(filepath.Join(parts[0], "pkg", "mod"))
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Clean(filepath.Join(home, "go", "pkg", "mod"))
	}
	return ""
}

// ReadRoots returns the default trusted read-only roots (DefaultReadRoots)
// followed by any caller-supplied extras, with each entry made absolute where
// possible, cleaned, empties dropped, and duplicates removed.
func ReadRoots(extra []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if strings.TrimSpace(p) == "" {
			return
		}
		if !filepath.IsAbs(p) {
			if abs, err := filepath.Abs(p); err == nil {
				p = abs
			}
		}
		p = filepath.Clean(p)
		if seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, p := range DefaultReadRoots() {
		add(p)
	}
	for _, p := range extra {
		add(p)
	}
	return out
}
