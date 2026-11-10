package orchestrator

import (
	"fmt"
	"path/filepath"
)

// SetPlanOnly switches a chat session's plan-only guardrail. Tools read it at
// call time, so a change applies from the next tool call.
func (d *Deps) SetPlanOnly(on bool) { d.planOnly.Store(on) }

// PlanOnly reports whether the plan-only guardrail is on.
func (d *Deps) PlanOnly() bool { return d.planOnly.Load() }

// planOnlyWriteGate refuses Write/Edit outside the planning docs while plan
// only is on. Bash is not gated: like pm mode, that part stays a prompt-level
// boundary.
func (d *Deps) planOnlyWriteGate(path string) error {
	if !d.PlanOnly() || d.Docs == nil || d.Docs.IsPlanningDestination(path) {
		return nil
	}
	rel := path
	if r, err := filepath.Rel(d.Workspace, path); err == nil {
		rel = filepath.ToSlash(r)
	}
	return fmt.Errorf("plan-only mode is on, so %s was not changed: only the design docs, backlog, plans and memory.md "+
		"may be edited. Record the change in a plan or backlog task, or ask the user to turn plan only off to implement it", rel)
}

// PlanOnlyNote is the user-role note that tells the coordinator about a
// plan-only change, delivered at the next turn boundary.
func PlanOnlyNote(on bool) string {
	if on {
		return "[plan only: on] The user turned on plan-only mode. Investigate, discuss and plan; do not implement. " +
			"You may edit the design docs, backlog, plans and memory.md. Write/Edit to any other path is refused, " +
			"spawned agents run read-only, and Bash must not be used to change code or project files. " +
			"The user turns it off when it is time to build."
	}
	return "[plan only: off] The user turned off plan-only mode. Your full chat tools are back, and you may now implement."
}
