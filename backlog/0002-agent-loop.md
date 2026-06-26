---
id: "0002"
title: Core agent loop with worker tools (M0 spike)
status: done
priority: 1
created: 2026-06-25
updated: 2026-06-25
depends_on: ["0001"]
spec_refs: ["Agent engine", "Tools"]
---

## Description
The atom of the system: a `Loop` that runs turns against a gollama client, dispatches
tool calls, feeds results back, and terminates when the model yields with no tool call
or calls a control tool (`finish`). Includes the first worker tools so a single agent can
do a real coding task end-to-end. Events to stdout for now.

## Acceptance criteria
- [ ] `Loop{client, model, system, tools, history, events}` with a `run()` driver
- [ ] worker tools: read_file, write_file, edit_file, list_dir, grep, glob, bash
- [ ] control-tool concept (a tool may end the loop / change state)
- [ ] demo: point the loop at a scratch repo and have it complete a small task
- [ ] every turn / tool call / result emits a structured event

## Work log
- 2026-06-25 implemented:
  - `internal/event`: Event/Type model, `Emitter` (seq+ts stamping), `StdoutSink` (M0).
  - `internal/tools`: `Registry` + `Control` (control-tool signal via ToolResult.Structured);
    worker tools read_file/write_file/edit_file/list_dir/grep/glob/bash + `finish` (control);
    `Workspace` path confinement.
  - `internal/engine`: `Loop` over a `Turner` interface (satisfied by `*gollama.Client`);
    run turn → dispatch tool calls → feed results → repeat; stop on control tool, on a
    no-tool-call yield, or at MaxTurns.
  - `cmd/ycc-spike`: wires a gollama client + worker tools + loop; backend-flaggable.
  - Tests: `loop_test.go` (scripted fake backend: stop-on-finish, yield, result-feedback,
    max-turns) and `worker_test.go` (write/read/edit, non-unique edit, path confinement,
    grep/glob, bash cwd, finish control, unknown tool). `go build ./...` + `go test ./...` pass.
- 2026-06-25 NOTE / still in_review: no live end-to-end run yet — the build env has no
  ANTHROPIC/OPENAI/GLM keys and no local Ollama. Acceptance criterion "point the loop at a
  scratch repo and complete a small task" needs a real-backend run:
  `ANTHROPIC_API_KEY=… go run ./cmd/ycc-spike -dir ./scratch "add hello.txt saying hi"`.
- 2026-06-25 DONE: live end-to-end run against claude-opus-4-8 succeeded (fibonacci task:
  wrote main.go → go mod init → go run → verified output → finish, 3 turns; and a
  greet.txt task, 2 turns). Found+fixed a gollama gap: the native Anthropic path didn't
  send the required `anthropic-version` header — now defaulted in
  `gollama/anthropic.go` (`ChatCompletionAnthropic`) when the caller hasn't set one.
