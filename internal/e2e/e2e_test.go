package e2e

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// readScript is a chat turn that calls the real Read tool on the seeded file,
// then (after the tool result) replies with marker.
func readScript(marker string) []scriptedTurn {
	return []scriptedTurn{
		{
			Text: "Reading the file first.",
			ToolCalls: []scriptedToolCall{{
				ID:        "call_1",
				Name:      "Read",
				Arguments: `{"file_path":"hello.txt"}`,
			}},
		},
		{Text: "All set — " + marker + " confirmed.", Finish: "stop"},
	}
}

// TestE2EDaemonChatToolRoundTrip drives the persistent path end to end: a real
// `ycc daemon` process, `ycc --addr … start` streaming a chat session whose
// scripted model calls Read against the real workspace, and `ycc list` seeing
// the session afterwards.
func TestE2EDaemonChatToolRoundTrip(t *testing.T) {
	const marker = "E2E-MARKER-42"
	e := newEnv(t, readScript(marker))
	e.startDaemon()

	p := e.spawn(e.environ("YCC_TOKEN="+e.token), "--addr", e.addr, "start", "--mode", "chat", "read the hello file")
	id := p.sessionID()
	p.waitFor("Read(")
	p.waitFor("hello from the e2e workspace") // the real tool result
	p.waitFor(marker)

	// The tool call round-tripped through the real engine: the model was asked
	// for at least two turns (initial + post-tool-result).
	if n := e.stub.requestCount(); n < 2 {
		t.Fatalf("expected >= 2 LLM requests (initial + after tool result), got %d", n)
	}
	if out := e.cli("list"); !strings.Contains(out, id) || !strings.Contains(out, "chat") {
		t.Fatalf("ycc list does not show chat session %s:\n%s", id, out)
	}
}

// TestE2EDaemonAskUser covers a parked question: the scripted model calls
// ask_user, the CLI stream shows it, the answer arrives over the daemon API (as
// web and iOS send it), and the model's follow-up confirms the round trip.
func TestE2EDaemonAskUser(t *testing.T) {
	const marker = "E2E-PICKER-7"
	e := newEnv(t, []scriptedTurn{
		{ToolCalls: []scriptedToolCall{{
			ID:        "call_ask",
			Name:      "ask_user",
			Arguments: `{"question":"Which database should we use?","options":["postgres","sqlite"]}`,
		}}},
		{Text: "Great, going with your choice. " + marker, Finish: "stop"},
	})
	e.startDaemon()

	p := e.spawn(e.environ("YCC_TOKEN="+e.token), "--addr", e.addr, "start", "--mode", "chat", "help me pick a database")
	id := p.sessionID()
	p.waitFor("question_asked")
	e.answer(id, 1)
	p.waitFor(marker)

	// The chosen option reached the model as the ask_user tool result.
	e.stub.mu.Lock()
	last := e.stub.requests[len(e.stub.requests)-1]
	e.stub.mu.Unlock()
	msgs, _ := last["messages"].([]any)
	var result string
	for _, m := range msgs {
		if m, ok := m.(map[string]any); ok && m["role"] == "tool" && m["tool_call_id"] == "call_ask" {
			result = fmtJSON(m["content"])
		}
	}
	if !strings.Contains(result, "sqlite") || strings.Contains(result, "postgres") {
		t.Fatalf("ask_user result does not carry the chosen option: %q\nmessages: %s", result, fmtJSON(msgs))
	}
}

// TestE2EOneShotInProcess covers the no-daemon path: `ycc start` with no
// persistent daemon reachable runs one in-process, streams the session, and
// tears the daemon down on ctrl-c.
func TestE2EOneShotInProcess(t *testing.T) {
	const marker = "E2E-ONESHOT-3"
	e := newEnv(t, readScript(marker))

	p := e.spawn(e.environ(), "start", "--mode", "chat", "read the hello file")
	p.waitFor("one-shot in-process daemon")
	p.sessionID()
	p.waitFor(marker)

	if err := p.cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.exited:
	case <-time.After(10 * time.Second):
		t.Fatalf("ycc did not exit after ctrl-c\n%s", p.output())
	}
}

// TestE2ENoSubcommand checks plain `ycc` prints help pointing at the daemon and
// web client, and an unknown command fails.
func TestE2ENoSubcommand(t *testing.T) {
	e := newEnv(t, nil)
	run := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, yccBin, args...)
		cmd.Dir = e.workspace
		cmd.Env = e.environ()
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := run()
	if err != nil {
		t.Fatalf("ycc with no arguments failed: %v\n%s", err, out)
	}
	for _, want := range []string{"ycc daemon --web", "COMMANDS", "start"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help output lacks %q:\n%s", want, out)
		}
	}
	if out, err := run("bogus"); err == nil || !strings.Contains(out, `unknown command "bogus"`) {
		t.Fatalf("ycc bogus: err=%v\n%s", err, out)
	}
}

func fmtJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
