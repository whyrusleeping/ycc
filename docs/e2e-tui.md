# End-to-end TUI harness (`internal/e2e`)

This harness drives the **real** `ycc` binary under a pseudo-terminal and asserts on what the
actual terminal screen renders. It is the highest-fidelity TUI test we have: the only mocked
seam is the LLM at the HTTP boundary — the binary, the one-shot in-process daemon, the engine
loop, tool execution, and the Bubble Tea runtime are all real.

It complements, rather than replaces, the in-process tests in `internal/tui` (which call
`model.Update`/`View` directly and rasterize frames with `internal/tui/snapshot`). Use the
in-process tests for fast, fine-grained view/logic assertions; use the e2e harness to prove
the whole stack wires together and real keystrokes produce the expected screens.

## Layers

```
scripted LLM stub  ──HTTP (OpenAI /chat/completions)──▶  ycc daemon (in-process, real)
        ▲                                                        │
        │ records requests                                       │ engine loop, tools, TUI
        │                                                        ▼
   scriptedTurn[]                                        ycc binary (real, under PTY)
                                                                 │ terminal output
                                                                 ▼
                        test  ◀── screen-text predicates ──  vt.Emulator  ◀── PTY bytes
                          │
                          └── screenshot() ──▶  snapshot.RenderScreen ──▶  PNG (optional)
```

1. **Scripted LLM stub** (`llmstub_test.go`). An `httptest` server implementing
   `POST /chat/completions`. It is programmed with an ordered list of `scriptedTurn`s (each is
   assistant text and/or `tool_calls`) and honors both request modes: `stream:true` requests
   get `chat.completion.chunk` SSE; others get a plain `chat.completion` JSON body. When the
   script is exhausted it returns a canned "done" turn (never an error), so a stray probe
   can't crash the run. Incoming request bodies are recorded for optional assertions
   (`stub.requestCount()`).

2. **PTY driver** (`harness_test.go`). `TestMain` runs `go build ./cmd/ycc` once. `launch`
   creates a temp workspace (git repo, `spec.md`, `backlog/`, a seeded file, and a workspace
   `ycc.toml` pointing at the stub), then spawns the binary with `creack/pty`. The environment
   is isolated (`HOME`/`XDG_CONFIG_HOME`/`XDG_CACHE_HOME` under a temp dir, `TERM=
   xterm-256color`, `YCC_E2E_KEY=test-key`, and crucially **no** `ANTHROPIC_API_KEY`). Because
   the workspace `ycc.toml` is discovered first, the first-run setup wizard never appears.

3. **VT emulator** (`github.com/charmbracelet/x/vt`). The child's PTY output is streamed into
   a `vt.SafeEmulator` (a locked emulator — a reader goroutine writes while the test reads).
   A second goroutine pipes the emulator's replies (its answers to the terminal queries the
   TUI emits — cursor position, device attributes, kitty-keyboard) back to the PTY. This is
   **required**: the emulator answers such queries by writing to its input pipe, and if nothing
   drains it that write blocks while holding the emulator lock, deadlocking every screen read.

4. **Synchronization**. Tests never sleep to wait for the UI. `waitForText`, `waitForRegex`,
   and `waitFor` poll the emulator's text grid (`screenText()`) until a predicate holds or a
   ~20s deadline elapses; on timeout they dump the current screen (and write a diagnostic PNG
   when the snapshot dir is set) and fail.

5. **Screenshots**. `harness.screenshot(name)` rasterizes the live emulator screen to a PNG via
   `snapshot.WriteScreenPNG` — but only when `YCC_TUI_SNAPSHOT_DIR` is set. Screenshots are
   **artifacts, not oracles**: assertions run against the text grid only (spinners and
   timestamps make pixel-goldens brittle). The PNGs exist for human / agent multimodal
   inspection.

## Running

```sh
# Full suite (builds the binary once; ~1s of scenario time):
go test ./internal/e2e/

# Skip it (e.g. a fast pre-commit loop) — the package no-ops under -short:
go test -short ./...

# Write PNG screenshots of every scenario for inspection:
YCC_TUI_SNAPSHOT_DIR=/tmp/e2eshots go test ./internal/e2e/ -run TestE2EChatHappyPath
```

The harness skips itself (rather than failing) when a PTY cannot be allocated, so it is safe
in constrained sandboxes. It needs no external terminal emulator, `ttyd`, or `ffmpeg`.

## Adding a scenario

A scenario is a normal `Test…` function:

```go
func TestE2EMyThing(t *testing.T) {
    h := launch(t, []scriptedTurn{
        {Text: "on it", ToolCalls: []scriptedToolCall{{ID: "c1", Name: "Read", Arguments: `{"file_path":"hello.txt"}`}}},
        {Text: "done — MARKER", Finish: "stop"},
    })

    h.waitForText("Pick a backlog task") // home menu rendered
    h.send("do the thing")               // type into the prompt
    h.waitForText("do the thing")
    h.send(keyEnter)                      // start the (chat) session
    h.waitForText("MARKER")               // scripted reply visible
    h.screenshot("my_thing")
}
```

Guidelines:

- **Match a tool the selected mode actually mounts.** The default home-menu selection is
  `chat`, which mounts `Read`/`Write`/`Edit`/`Bash`, plus `ask_user`, `remember`, and the
  backlog tools. Point a `Read`/`Bash` call at a file you seeded in `setupWorkspace`.
- **Sync on stable, rendered text.** Menu rows show the mode *name* and *description*
  (e.g. `Pick a backlog task`), not the title. Prefer a distinctive substring; use
  `waitForRegex` when whitespace varies.
- **Keystrokes are raw bytes.** Use the `key*` constants (`keyEnter`, `keyEsc`, arrows,
  `keyCtrlC`) or plain strings for typed text and digit picks.
- **No sleeps for synchronization** — always `waitFor*`. A sleep hides races and slows the
  suite.
- Keep scenarios short; the harness rebuilds nothing per test, so several small scenarios are
  cheap.
