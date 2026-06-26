package orchestrator

import (
	"context"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/tools"
)

// ModeInfo describes a session mode for the home menu (ListModes).
type ModeInfo struct {
	Name        string
	Title       string
	Description string
}

// Modes returns the selectable session modes (spec §9).
func Modes() []ModeInfo {
	return []ModeInfo{
		{"chat", "Chat", "Open-ended conversation and coding — no fixed workflow."},
		{"work", "Work on backlog", "Pick a backlog task, implement it, review it across models, and commit."},
		{"spec", "Author spec", "Collaboratively write and maintain spec.md."},
		{"backlog", "Build backlog", "Turn the spec into concrete backlog tasks."},
		{"feature", "New feature", "Understand the codebase, propose a plan, update spec + backlog, then optionally start work."},
		{"bug", "Bug report", "Investigate a bug, propose a fix plan, update the backlog, then optionally start work."},
	}
}

// BuildMode returns the tool registry and system prompt for a session mode. The
// "work" mode is the full coordinator (CoordinatorTools); the others are lighter
// authoring/intake coordinators that read the workspace and edit the docs.
func BuildMode(mode string, d *Deps, level string) (*tools.Registry, string) {
	ws := &tools.Workspace{Root: d.Workspace}
	reg := tools.New()
	switch mode {
	case "chat":
		reg.Add(tools.Editing(ws)...)
		reg.Add(readSpec(d), listBacklog(d), getTask(d), askUser(d))
		return reg, sys(chatModeSystem, level, d.Workspace)
	case "spec":
		reg.Add(tools.Inspect(ws)...)
		reg.Add(readSpec(d), updateSpec(d), askUser(d), tools.Finish())
		return reg, sys(specModeSystem, level, d.Workspace)
	case "backlog":
		reg.Add(tools.Inspect(ws)...)
		reg.Add(readSpec(d), listBacklog(d), getTask(d), createTask(d), updateTask(d), askUser(d), tools.Finish())
		return reg, sys(backlogModeSystem, level, d.Workspace)
	case "feature", "bug":
		reg.Add(tools.Inspect(ws)...)
		reg.Add(readSpec(d), listBacklog(d), getTask(d), proposePlan(d), createTask(d), updateSpec(d), switchToWork(), askUser(d), tools.Finish())
		base := featureModeSystem
		if mode == "bug" {
			base = bugModeSystem
		}
		return reg, sys(base, level, d.Workspace)
	default: // work
		return CoordinatorTools(d), CoordinatorSystem(level) + "\n\n" + workspaceNote(d.Workspace)
	}
}

const toolingHint = "Use the Read tool to view files, Edit/Write to change them, and Bash with " +
	"ripgrep (`rg 'pattern'`) to search. All tools run in the workspace root."

func workspaceNote(root string) string {
	return "Workspace root: " + root + " — Read/Write/Edit accept absolute paths within it (or paths relative to it)."
}

func sys(base, level, root string) string {
	return base + "\n\n" + toolingHint + "\n" + workspaceNote(root) + "\n\n" + levelGuidance(level)
}

func readSpec(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name:        "read_spec",
		Description: "Read the current spec.md document.",
		Params:      tools.Obj(map[string]any{}),
		Call: func(ctx context.Context, _ any) (*gollama.ToolResult, error) {
			body, err := d.Docs.ReadSpec()
			if err != nil {
				return tools.ErrResult("read_spec: %v", err), nil
			}
			if body == "" {
				return tools.OkResult("(spec.md is empty or does not exist yet)"), nil
			}
			return tools.OkResult(body), nil
		},
	}
}

func updateSpec(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name: "update_spec",
		Description: "Create or replace a named '## ' section of spec.md. Provide the section title and its full " +
			"new markdown content. Edits are section-scoped, so update one section at a time.",
		Params: tools.Obj(map[string]any{
			"section": tools.StrProp("the '## ' section title, e.g. 'Architecture'"),
			"content": tools.StrProp("the full markdown content for that section"),
		}, "section", "content"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			section, _ := tools.GetString(params, "section")
			content, _ := tools.GetString(params, "content")
			if err := d.Docs.UpdateSpecSection(section, content); err != nil {
				return tools.ErrResult("update_spec: %v", err), nil
			}
			d.Emitter.Emit(event.DocUpdated, map[string]any{"doc": "spec", "section": section})
			return tools.OkResult("updated spec section: " + section), nil
		},
	}
}

func createTask(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name:        "create_task",
		Description: "Create a new backlog task. Returns the assigned id. Regenerates the backlog index.",
		Params: tools.Obj(map[string]any{
			"title":       tools.StrProp("short task title"),
			"description": tools.StrProp("description and acceptance criteria (markdown)"),
			"priority":    map[string]any{"type": "integer", "description": "1 (highest) .. 5; default 3"},
			"depends_on":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "task ids this depends on"},
			"spec_refs":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "spec section titles this relates to"},
		}, "title"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			title, _ := tools.GetString(params, "title")
			desc, _ := tools.GetString(params, "description")
			body := ""
			if desc != "" {
				body = "## Description\n" + desc + "\n\n## Acceptance criteria\n\n## Work log\n"
			}
			t, err := d.Docs.Create(title, body, getInt(params, "priority", 3), getStrings(params, "depends_on"), getStrings(params, "spec_refs"))
			if err != nil {
				return tools.ErrResult("create_task: %v", err), nil
			}
			d.Docs.RenderIndex()
			d.Emitter.Emit(event.DocUpdated, map[string]any{"task": t.ID, "created": true})
			return tools.OkResult("created task " + t.ID + ": " + t.Title), nil
		},
	}
}

func switchToWork() *gollama.Tool {
	return &gollama.Tool{
		Name: "switch_to_work",
		Description: "Transition this session into work mode to begin implementing the backlog. Call only after the " +
			"plan is agreed and the backlog has been updated. This starts a fresh coordinator on the backlog.",
		Params: tools.Obj(map[string]any{}),
		Call: func(ctx context.Context, _ any) (*gollama.ToolResult, error) {
			return &gollama.ToolResult{
				Content:    "transitioning to work mode",
				Structured: &tools.Control{Stop: true, Mode: "work", Report: "Plan recorded and backlog updated; switching to work mode."},
			}, nil
		},
	}
}

func getInt(params any, key string, def int) int {
	if m, ok := params.(map[string]any); ok {
		if f, ok := m[key].(float64); ok {
			return int(f)
		}
	}
	return def
}

func getStrings(params any, key string) []string {
	m, ok := params.(map[string]any)
	if !ok {
		return nil
	}
	arr, ok := m[key].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
