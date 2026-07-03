package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

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

// Modes returns the selectable session modes (spec §9). There are three: chat
// (freeform, can edit code — the default first option), work (the orchestrated
// implementation pipeline), and pm (the catch-all planning / intake / docs mode,
// no implementation). The home menu additionally offers the onboard opening-prompt
// preset that drops into pm (see Presets).
func Modes() []ModeInfo {
	return []ModeInfo{
		{"chat", "Chat", "Open-ended conversation and coding — no fixed workflow."},
		{"work", "Work on backlog", "Pick a backlog task, implement it, review it across models, and commit."},
		{"pm", "Project manager", "Plan and intake — spec authoring, backlog grooming, new features, and bug reports. No implementation."},
	}
}

// Preset is a home-menu entry that opens a pm session with a tailored opening
// prompt (spec §9). Today there are two presets: onboard, the distinct first-run
// flow, and spec-doctor, the on-demand spec/code drift & coverage check; the
// former spec/backlog/feature/bug framings are just ordinary pm work.
type Preset struct {
	Name        string // menu key (distinct from the mode)
	Title       string
	Description string
	Mode        string // always "pm" today
	Prompt      string // verbatim opening prompt seeded into the pm session
}

// Presets returns the opening-prompt presets the home menu offers under pm. The
// former spec/feature/bug/backlog framings have been dropped as separate presets
// (they are all ordinary pm work); onboard (the first-run flow) and spec-doctor
// (on-demand drift & coverage checking) remain.
func Presets() []Preset {
	return []Preset{
		{"onboard", "Onboard this project", "Orient from existing project docs (spec entry point, any docs/ tree) and backlog, then establish or refresh them — greenfield (full spec) or brownfield (adopt existing docs, scoped to your work).", "pm", onboardPresetPrompt},
		{"spec-doctor", "Spec doctor (drift & coverage)", "Check the spec against the code: run the deterministic reference check, then compare spec sections to the code to surface drift and coverage gaps — with proposed backlog tasks and suggested spec edits for your approval.", "pm", specDoctorPresetPrompt},
	}
}

// BuildMode returns the tool registry and system prompt for a session mode. The
// "work" mode is the full coordinator (CoordinatorTools); "pm" is the planning /
// intake / docs coordinator (no implementation); "chat" is the freeform assistant.
// The spec docs and code are plain files: pm/chat read them with Read and edit
// them with Edit/Write — there is no dedicated spec tool. An OnWrite hook surfaces
// an edit anywhere in the docs set (the spec entry point plus any configured
// doc_globs — spec §6.1) as a doc_updated event.
func BuildMode(mode string, d *Deps, level string) (*tools.Registry, string) {
	ws := &tools.Workspace{
		Root:      d.Workspace,
		ReadRoots: tools.ReadRoots(d.ReadRoots),
		OnWrite: func(path string) {
			if d.Docs.IsDoc(path) {
				data := map[string]any{"doc": "spec"}
				if rel, err := filepath.Rel(d.Workspace, path); err == nil {
					data["path"] = filepath.ToSlash(rel)
				}
				d.Emitter.Emit(event.DocUpdated, data)
			}
		},
	}
	reg := tools.New()
	switch mode {
	case "chat":
		reg.Add(tools.Editing(ws)...)
		reg.Add(listBacklog(d), getTask(d), createTask(d), updateTask(d), askUser(d))
		return reg, sys(chatModeSystem, level, d.Workspace)
	case "pm":
		// pm maintains the project's design docs (plain files) so it keeps
		// Write/Edit, but it does no implementation: no spawn_* / commit, and the
		// prompt enforces a soft "no code edits" boundary (hard enforcement is
		// future work).
		reg.Add(tools.Editing(ws)...)
		reg.Add(listBacklog(d), getTask(d), createTask(d), updateTask(d), proposePlan(d), listPlans(d), runPlan(d), savePlan(d), specCheck(d), switchToWork(d), askUser(d), tools.Finish())
		return reg, sys(pmModeSystem, level, d.Workspace)
	default: // work
		return CoordinatorTools(d, ws), sys(coordinatorSystem, level, d.Workspace)
	}
}

// The shared tooling guidance, split so read-only roles (reviewers) get the
// read/search rules without the editing sentence they have no tools for.
const (
	inspectHint = "Use the Read tool to view files (prefer it over `cat`/`sed`), and search with Bash + " +
		"ripgrep (`rg 'pattern'`, `rg --files -g '*.go'`) rather than grep. Every Bash command runs in a " +
		"fresh shell already rooted at the workspace and the working directory does not carry between " +
		"calls, so run commands directly instead of prefixing a redundant `cd` into the workspace root — " +
		"write `rg 'pattern'`, not `cd <workspace> && rg 'pattern'`. (Chaining real steps with `&&`, e.g. " +
		"`go build ./... && go test ./...`, is fine; only the leading `cd` into the root is redundant.)"
	editHint = "Change files with the Edit tool (exact string replacement) or Write (create/overwrite " +
		"whole file) rather than via shell redirection."
)

func workspaceNote(root string) string {
	return "Workspace root: " + root + " — Read/Write/Edit accept absolute paths within it (or paths " +
		"relative to it), and every Bash command also starts in this directory, so commands need no `cd` here."
}

// sys assembles the full system prompt every agent uses: the role's base prompt,
// the shared tooling guidance, the workspace note, and — when level is non-empty —
// the interaction-level policy. One assembly path keeps the shared rules
// byte-identical across roles instead of hand-copied paraphrases that drift.
// Subagents (implementer/reviewers) pass level="" because they have no ask_user
// gate; the interaction level is the coordinator's concern.
func sys(base, level, root string) string {
	return assemble(base, level, root, true)
}

// inspectSys assembles the system prompt for read-only roles (reviewers): the
// same shared guidance minus the editing sentence.
func inspectSys(base, root string) string {
	return assemble(base, "", root, false)
}

func assemble(base, level, root string, editing bool) string {
	hint := inspectHint
	if editing {
		hint += " " + editHint
	}
	s := base + "\n\n" + hint + "\n" + workspaceNote(root)
	if level != "" {
		s += "\n\n" + levelGuidance(level)
	}
	return s
}

func createTask(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name:        "create_task",
		Description: "Create a new backlog task. Returns the assigned id.",
		Params: tools.Obj(map[string]any{
			"title":       tools.StrProp("short task title"),
			"description": tools.StrProp("description and acceptance criteria (markdown)"),
			"priority":    map[string]any{"type": "integer", "description": "1 (highest) .. 5; default 3"},
			"depends_on":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "task ids this depends on"},
			"spec_refs":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "spec references this relates to: a bare section title refers to the spec entry point; `path#Section` references a section of another doc in the docs set"},
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
			d.Emitter.Emit(event.DocUpdated, map[string]any{"task": t.ID, "created": true})
			return tools.OkResult("created task " + t.ID + ": " + t.Title), nil
		},
	}
}

// switchToWork is pm's DELIBERATE hand-off to the work pipeline (spec §9). It is
// never automatic: it requires explicit interactive user approval, and it carries
// the specific target task id + planning context into the work session so the
// coordinator implements THAT task rather than re-picking "the next ready task".
//
// Starting an implementation pipeline is high-impact and hard to reverse, so the
// approval gate is a REAL confirmation even in autonomous mode (where ask_user
// normally auto-answers) — if no human is available, the hand-off is declined and
// pm stays put rather than silently launching work.
func switchToWork(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name: "switch_to_work",
		Description: "Hand this session off to the work pipeline to IMPLEMENT one specific task. Call only after " +
			"the plan is agreed and recorded (propose_plan) and the task exists in the backlog. Requires explicit " +
			"user approval; you must pass the exact target task_id and a plan summary, which are carried into the " +
			"work session so it implements THAT task (it will not re-pick a different one). If the user declines, " +
			"stay in pm mode.",
		Params: tools.Obj(map[string]any{
			"task_id": tools.StrProp("the exact backlog task id to implement, e.g. 0021"),
			"plan":    tools.StrProp("a concise summary of the agreed plan / planning context to carry into the work session"),
		}, "task_id", "plan"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			id, _ := tools.GetString(params, "task_id")
			plan, _ := tools.GetString(params, "plan")
			if strings.TrimSpace(id) == "" {
				return tools.ErrResult("switch_to_work: a target task_id is required"), nil
			}
			// Deliberate hand-off: get explicit approval. Confirm forces a real
			// human answer even in autonomous mode (it does not auto-answer).
			ok, err := d.Asker.Confirm(ctx, fmt.Sprintf(
				"Start the implementation pipeline now for task %s? This launches the work coordinator to implement it.", id))
			if err != nil {
				return tools.ErrResult("switch_to_work: %v", err), nil
			}
			if !ok {
				return tools.OkResult("User declined to start work; staying in pm mode."), nil
			}
			// The hand-off carries the explicit target task; record focus now so the
			// work session is durably linked to it for cost attribution (spec §20.2).
			// The work coordinator dedupes when it later accepts the same task.
			d.emitFocus(id)
			return &gollama.ToolResult{
				Content:    "transitioning to work mode for task " + id,
				Structured: &tools.Control{Stop: true, Mode: "work", Report: "Plan agreed for task " + id + "; switching to work mode.", Prompt: workHandoffPrompt(id, plan)},
			}, nil
		},
	}
}

// workHandoffPrompt seeds the work coordinator with the carried task + plan so it
// implements THAT task verbatim instead of re-picking the next ready one.
func workHandoffPrompt(taskID, plan string) string {
	p := "You are now in work mode, handed off from planning (pm). Implement task " + taskID +
		" specifically — do NOT pick a different or \"next ready\" task. Read it with get_task, set it " +
		"in_progress, and drive it to a reviewed, committed state following the usual work flow."
	if strings.TrimSpace(plan) != "" {
		p += "\n\nPlanning context carried from pm (refine as needed):\n" + plan
	}
	return p
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
