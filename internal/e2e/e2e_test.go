package e2e

import (
	"regexp"
	"testing"
)

// TestE2EChatHappyPath is the first scenario proving the harness end-to-end. It
// drives the REAL ycc binary under a PTY:
//
//  1. launch ycc one-shot in a temp workspace → the home menu renders;
//  2. type an opening prompt and press enter → a chat session starts;
//  3. the scripted model replies with a tool call (Read hello.txt), the engine
//     executes it against the real workspace, then the model replies with a
//     unique final marker;
//  4. the marker becomes visible on the rendered screen;
//  5. a PNG screenshot of the real screen is written (when YCC_TUI_SNAPSHOT_DIR
//     is set) and the process quits cleanly.
//
// Assertions run only against the emulator's text grid — no pixel comparison.
func TestE2EChatHappyPath(t *testing.T) {
	const marker = "E2E-MARKER-42"

	h := launch(t, []scriptedTurn{
		// Turn 1: acknowledge, then call the real Read tool on the seeded file.
		{
			Text: "Reading the file first.",
			ToolCalls: []scriptedToolCall{{
				ID:        "call_1",
				Name:      "Read",
				Arguments: `{"file_path":"hello.txt"}`,
			}},
		},
		// Turn 2 (after the tool result): final answer carrying the marker.
		{Text: "All set — " + marker + " confirmed.", Finish: "stop"},
	})

	// The home menu renders the mode list; sync on a stable description line.
	h.waitForText("Pick a backlog task")
	h.screenshot("01_home_menu")

	// Type an opening prompt (chat is the default-selected mode) and start it.
	h.send("read the hello file")
	h.waitForText("read the hello file")
	h.send(keyEnter)

	// The scripted model's final marker appears in the transcript.
	h.waitForText(marker)
	h.screenshot("02_chat_reply")

	// The tool call round-tripped through the real engine: the model was asked
	// for at least two turns (initial + post-tool-result).
	if n := h.stub.requestCount(); n < 2 {
		t.Fatalf("expected >= 2 LLM requests (initial + after tool result), got %d", n)
	}
}

// TestE2EResize verifies the harness can resize the terminal and the real TUI
// re-renders at the new width. It reuses the home-menu screen (no session) so it
// stays fast, and asserts the menu still renders after a resize.
func TestE2EResize(t *testing.T) {
	h := launch(t, nil)
	h.waitForText("Pick a backlog task")

	// Grow the PTY and tell the emulator; the running TUI redraws via SIGWINCH.
	h.resize(150, 48)
	// The title bar and the mode list survive the reflow.
	h.waitForRegex(regexp.MustCompile(`ycc\s+—\s+home`))
	h.waitForText("Plan and intake")
	h.screenshot("03_resized_menu")
}

// TestE2ESettingsOverlay verifies the modal settings overlay (esc from the home
// menu) renders over the real TUI.
func TestE2ESettingsOverlay(t *testing.T) {
	h := launch(t, nil)
	h.waitForText("Pick a backlog task")

	h.send(keyEsc)
	h.waitForText("interaction level")
	h.waitForText("model backends")
	h.screenshot("04_settings_overlay")
}

// TestE2EAskUserPicker drives the ask_user option picker end-to-end: the
// scripted model asks a question with suggested options, the real TUI renders
// the picker, the harness picks an option by number, and the model's follow-up
// (carrying a marker) confirms the answer round-tripped back through the engine.
func TestE2EAskUserPicker(t *testing.T) {
	const marker = "E2E-PICKER-7"

	h := launch(t, []scriptedTurn{
		// Turn 1: ask the user to choose a database, offering two options.
		{ToolCalls: []scriptedToolCall{{
			ID:        "call_ask",
			Name:      "ask_user",
			Arguments: `{"question":"Which database should we use?","options":["postgres","sqlite"]}`,
		}}},
		// Turn 2 (after the answer): confirm with a marker.
		{Text: "Great, going with your choice. " + marker, Finish: "stop"},
	})

	h.waitForText("Pick a backlog task")

	// Start a chat session that will immediately ask a question.
	h.send("help me pick a database")
	h.waitForText("help me pick a database")
	h.send(keyEnter)

	// The picker renders the offered options.
	h.waitForText("postgres")
	h.waitForText("sqlite")
	h.screenshot("05_ask_user_picker")

	// Pick option 1 by number, then the model confirms.
	h.send("1")
	h.waitForText(marker)
	h.screenshot("06_ask_user_answered")
}
