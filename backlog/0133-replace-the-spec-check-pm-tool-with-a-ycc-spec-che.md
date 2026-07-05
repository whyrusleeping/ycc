---
id: "0133"
title: Replace the spec_check pm tool with a `ycc spec-check` CLI subcommand
status: todo
priority: 3
created: "2026-07-04"
updated: "2026-07-04"
depends_on: []
spec_refs:
    - 6.4 Spec doctor — drift & coverage checking
    - 9 Session modes
---

## Description
`spec_check` is an always-registered pm tool that exists to serve exactly one preset (spec-doctor), and it is fully deterministic — it doesn't need to be an agent tool at all. Move it to a CLI subcommand and have the agent run it via Bash.

Rationale:
- Drops a tool from pm's registry (part of the broader tool-count trim; the plans tools were already folded into prompt guidance).
- Makes the deterministic drift check directly usable by humans and CI (pre-commit / CI gate), which a tool-only surface prevents.
- No capability loss: the spec-doctor preset prompt just says "run `ycc spec-check` via Bash first" instead of "call spec_check first".

Sketch:
- Add a `spec-check` command to the urfave/cli/v3 tree in `cmd/ycc/main.go`. It runs locally against a workspace (default: current directory or `--workspace`), needs no daemon: resolve the docs set with `internal/docs` (spec entry point + configured `doc_globs`), run `internal/specdoctor.Check`, print `Report.Markdown()` to stdout.
- Exit code: 0 when no stale references, non-zero (e.g. 1) when stale references are found, so it works as a CI gate. A "no docs found" situation should not be a failure (report and exit 0), matching the tool's current behavior.
- Remove `specCheck` from `internal/orchestrator/speccheck.go` and its registration in `BuildMode("pm", ...)` in `modes.go`; the pure logic stays in `internal/specdoctor`.
- Update the spec-doctor preset prompt (`specDoctorPresetPrompt` in `internal/orchestrator/prompts.go`) to instruct Phase 1 as "run `ycc spec-check` with Bash"; keep the two-phase flow and false-positive discipline unchanged. Update `modes_test.go` prompt assertions accordingly (the tests check for "spec_check" mentions).
- Update spec §6.4 (surfaced as the `spec_check` tool → surfaced as the `ycc spec-check` subcommand) and the pm tool list in §9; add the command to docs/cli.md.

Open question for implementation: the agent's Bash runs `ycc` from PATH — ensure the docs/prompt phrasing tolerates a dev workspace where the binary may need `go run ./cmd/ycc spec-check` as a fallback.

## Acceptance criteria
- `ycc spec-check` exists, runs daemon-free against the workspace docs set, prints the markdown stale-reference report, exits non-zero iff stale references are found.
- pm mode no longer registers a `spec_check` tool; the spec-doctor preset drives Phase 1 through the CLI via Bash.
- spec.md §6.4 / §9 and docs/cli.md updated; all tests pass.

## Work log
