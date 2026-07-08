---
id: "0175"
title: 'E2E TUI harness: drive the real ycc binary under a PTY, screenshot rendered screens'
status: done
priority: 3
created: "2026-07-08"
updated: "2026-07-08"
depends_on: []
spec_refs:
    - Client UI (TUI)
---

## Description
Today's TUI testing is mock-based at three seams: in-process `model.View()` assertions + the `internal/tui/snapshot` rasterizer (§18.8), `scriptedTurner` behind `engine.Turner`, and httptest Connect servers. Nothing drives the *real application* — real binary, real Bubble Tea `Program` runtime in a real PTY, real one-shot daemon — or captures what the actual terminal screen looks like.

Build an end-to-end TUI harness:

1. **Scripted LLM stub at the HTTP boundary** — an OpenAI-compatible `/chat/completions` httptest server returning a pre-programmed sequence of responses (text and `tool_calls`). The real binary points at it via a generated `ycc.toml` (`backend = "openai"`, `base_url` = stub). The LLM stays mocked forever; everything else is real.
2. **PTY driver** — `TestMain` builds the `ycc` binary once; tests create a temp workspace (git init, spec.md, backlog/), spawn the binary under `creack/pty` with TERM/size set, and write real keystroke byte sequences.
3. **In-process VT emulator** — pipe PTY output into `github.com/charmbracelet/x/vt` (same charm cell model as ultraviolet). Tests wait for a screen predicate (regex over the text grid) instead of sleeping.
4. **Screenshots** — rasterize the emulator's cell grid to PNG by reusing/extending `internal/tui/snapshot` (a `RenderScreen`-style entry point beside `RenderANSI`). PNGs are **artifacts, not oracles**: written only when `YCC_TUI_SNAPSHOT_DIR` is set (existing convention), for human/agent multimodal inspection. Assertions run against the text grid only — no pixel-golden comparisons (spinners/timestamps make them brittle).

First scenario to prove the harness: launch `ycc` one-shot in the temp workspace → home menu renders → start a chat session → scripted model replies → reply visible on screen → screenshot. Follow-ons: ask_user option picker, settings overlay, resize.

## Acceptance criteria
- [ ] LLM stub server drives a scripted multi-turn conversation incl. tool calls through the real binary
- [ ] PTY harness spawns the built `ycc` binary, sends keystrokes, and syncs on screen-content predicates (no bare sleeps)
- [ ] Screen text assertions pass headless in CI (no external terminal emulator, ttyd, or ffmpeg)
- [ ] PNG screenshots of real rendered screens written when YCC_TUI_SNAPSHOT_DIR is set, reusing the snapshot rasterizer
- [ ] At least the happy-path scenario above covered; harness documented (docs/ or spec §18.8 extension) so new scenarios are easy to add

## Plan

Build an end-to-end TUI test harness that drives the REAL ycc binary under a PTY against a scripted OpenAI-compatible LLM stub, syncing on an in-process VT emulator's text grid, with optional PNG screenshots via the existing snapshot rasterizer.

Structure (new package, e.g. internal/e2e or test/e2e with package name e2e):

1. **LLM stub** (llmstub.go or similar): httptest server implementing POST /chat/completions. Scripted as an ordered list of responses, each = assistant text and/or tool_calls (id/name/arguments). Must honor BOTH modes: if request body has "stream":true, reply as SSE chunks (see gollama openai_stream_test.go for the exact chunk shapes: delta.content, delta.tool_calls, finish_reason, final usage chunk, `data: [DONE]`); else plain JSON chat.completion. Include a usage object. Record incoming request bodies for optional assertions. If the script is exhausted, return a final canned "done" text response rather than 500 (background daemon may probe).

2. **PTY harness** (harness.go):
   - TestMain: `go build -o <tmpdir>/ycc ./cmd/ycc` once; skip package under `testing.Short()`.
   - Per-test: temp workspace with `git init` (+ user.name/email config), spec.md, backlog/ dir, and a generated `ycc.toml` in the workspace root: `[models.stub] backend="openai" base_url=<stub URL> model="stub-model" key_env="YCC_E2E_KEY"` (workspace ycc.toml is found first by daemon.DiscoverConfig, so setup.NeedsSetup is false and no wizard appears). Check config.Load's required fields (internal/config/config_test.go has minimal examples) — e.g. whether a default/role mapping is needed.
   - Spawn the binary with creack/pty (add dep), cwd = workspace, env: HOME/XDG_CONFIG_HOME pointed at a temp dir (isolate user config/secrets/cache), TERM=xterm-256color, YCC_E2E_KEY=test-key, NO_COLOR unset. Set PTY size (e.g. 120x40) via pty.Setsize.
   - Feed PTY output continuously into a `github.com/charmbracelet/x/vt` Emulator (add dep, pseudo-version v0.0.0-20260705004817-2cc9a8fe1146; use the safe/locked emulator or guard with a mutex since a reader goroutine writes while tests read).
   - Sync primitive: `WaitFor(t, predicate func(screenText string) bool)` polling the emulator grid text (rows joined) with a deadline (~20s) and short poll interval — no bare sleeps. On timeout, fail with the current screen text (and write a PNG if snapshot dir set) for diagnosis.
   - Keystroke helper: write raw bytes/escape sequences to the PTY ("hello\r", arrows, ctrl+c = 0x03).

3. **Screenshots**: extend internal/tui/snapshot with a `RenderScreen`-style entry point that rasterizes a cell grid (both uv.ScreenBuffer and vt.Emulator expose `CellAt(x,y) *uv.Cell`; refactor RenderANSI's drawing loop to work over that small interface, keeping RenderANSI's signature/behavior identical). Alternative if uv-version drift bites: feed Emulator.Render()'s ANSI back through RenderANSI. Harness writes PNGs only when YCC_TUI_SNAPSHOT_DIR is set (existing convention), never asserts on pixels.

4. **First scenario** (e2e_test.go): launch `ycc` (no args → one-shot in-process daemon) in the temp workspace → WaitFor home menu text (mode list renders) → type an opening prompt + enter → session starts → script: response 1 contains a tool_call (e.g. read_file/bash on a file seeded in the workspace), response 2 is final text ("E2E-MARKER-…") → WaitFor the marker text visible on screen → screenshot → quit cleanly (q to menu once finished, then ctrl+c) and assert process exit. This exercises: real binary, real daemon, real engine loop + tool execution, real Bubble Tea render, scripted multi-turn incl. tool calls.

5. **Docs**: short doc (docs/e2e-tui.md or a section in docs/build-and-test.md) explaining the harness layers, how to run it, how to add a scenario, and the screenshot convention; plus a brief pointer in the spec's TUI-testing section (§18.8) if that's where testing seams are catalogued.

Verification: `go test ./...` green (e2e test passes headless, no external terminal emulator/ttyd/ffmpeg); run the e2e test with YCC_TUI_SNAPSHOT_DIR set and eyeball the PNG; `go vet ./...`.

Risks/notes: x/vt's ultraviolet dependency is older than the repo's — confirm it compiles under MVS; if the vt Emulator misbehaves on Bubble Tea v2 output (kitty keyboard queries etc.), the emulator side is read-only display so unanswered queries are fine, but keystrokes must be plain bytes the binary's input parser accepts. Timing: build once in TestMain to keep the suite fast.

### Starting points
- internal/tui/snapshot/snapshot.go — RenderANSI/WritePNG; drawing loop to factor over a CellAt-grid interface
- internal/tui/snapshot_test.go — YCC_TUI_SNAPSHOT_DIR convention
- cmd/ycc/main.go runTUI + resolveDaemon — one-shot in-process daemon; wizard gated by setup.NeedsSetup(workspace)
- internal/daemon/client.go DiscoverConfig — workspace/ycc.toml wins, then ~/.config/ycc/ycc.toml
- internal/config/config_test.go — minimal ycc.toml shapes ([models.X] backend/base_url/model/key_env)
- ~/go/pkg/mod/github.com/whyrusleeping/gollama@v0.0.0-20260706191715-8653ccb1bdc2/openai.go + openai_stream.go + openai_stream_test.go — exact JSON/SSE response formats the stub must emit (stream flag in request body)
- github.com/charmbracelet/x/vt@v0.0.0-20260705004817-2cc9a8fe1146 — Emulator: Write(pty output), CellAt, Render(), Resize; safe_emulator.go for locking
- internal/tui/help.go helpSections — home-menu/session keybindings (type prompt + enter starts selected mode; q returns to menu when finished)
- creack/pty — pty.Start + pty.Setsize; TERM=xterm-256color
- internal/tui/tui_test.go TestQuitGuardOneShotRunning — ctrl+c quit-guard semantics on one-shot daemons

## Work log
- 2026-07-08 plan: Build an end-to-end TUI test harness that drives the REAL ycc binary under a PTY against a scripted OpenAI-compatible LLM stub, syncing on an in-process VT emulator's text grid, with optional PNG scre
…[truncated]
- 2026-07-08 context hints: 10 recorded with plan
- 2026-07-08 context hints: internal/tui/snapshot/snapshot.go — RenderANSI/WritePNG; drawing loop to factor over a CellAt-grid interface; internal/tui/snapshot_test.go — YCC_TUI_SNAPSHOT_DIR convention; cmd/ycc/main.go runTU
…[truncated]
- 2026-07-08 implementer report: Built an end-to-end TUI test harness that drives the real `ycc` binary under a PTY against a scripted OpenAI-compatible LLM stub, syncing on an in-process VT emulator's text grid, with optional PNG sc
…[truncated]
- 2026-07-08 review tier: single-opus — reviewers: Claude
- 2026-07-08 review (Claude): accept — The change delivers a complete, working end-to-end TUI harness meeting every acceptance criterion. A scripted OpenAI-compatible httptest stub (both JSON and SSE modes, request recording, canned exhaus
…[truncated]
- 2026-07-08 decision: accept — commit: e2e TUI harness: drive the real ycc binary under a PTY with a scripted LLM stub (task 0175)  - internal/e2e: OpenAI-compatible /chat/completions stub (JSON + SSE), creack/pty driver   with isolated en
…[truncated]
