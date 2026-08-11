package event

import (
	"strings"
	"testing"
)

// Render surfaces a task_focus event's task id so the focus is visible in the
// terse human-facing stream.
func TestRenderTaskFocus(t *testing.T) {
	out := Render(Event{Seq: 2, Actor: "coordinator", Type: TaskFocus, Data: map[string]any{"task": "0007"}})
	if !strings.Contains(out, "0007") {
		t.Fatalf("Render = %q, want it to show the focused task id", out)
	}
}
