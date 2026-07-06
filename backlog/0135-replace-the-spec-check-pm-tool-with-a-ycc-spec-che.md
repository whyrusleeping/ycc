---
id: "0135"
title: Replace the spec_check pm tool with a `ycc spec-check` CLI subcommand
status: done
priority: 3
created: "2026-07-04"
updated: "2026-07-05"
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

## Plan

Replace the pm-mode `spec_check` agent tool with a daemon-free `ycc spec-check` CLI subcommand.

1. CLI: add a `spec-check` command to the urfave/cli/v3 command tree in `cmd/ycc/main.go` (registered in the top-level `Commands` list). No daemon connection needed:
   - Resolve workspace from the existing global `--workspace` flag (default ".") — follow how other commands read it; make it absolute.
   - Build `docs.NewStore(workspace)`, call `DocFiles()`, read each file, convert to workspace-relative slash paths, build `[]specdoctor.DocFile`, run `specdoctor.Check(workspace, docFiles)` — i.e. port the body of `specCheck` in `internal/orchestrator/speccheck.go`.
   - Print `Report.Markdown()` to stdout. Exit 0 when no stale references; exit 1 (via `cli.Exit` or an error with exit code) when stale references exist (check the Report type in `internal/specdoctor` for how to detect staleness — e.g. a stale-count/entries field; add a small helper on Report if needed rather than string-matching the markdown). "No docs found" prints a note and exits 0 (matches current tool behavior).
2. Remove the tool: delete `internal/orchestrator/speccheck.go` and drop `specCheck(d)` from the pm registry in `BuildMode` (`internal/orchestrator/modes.go:93`).
3. Prompt: update `specDoctorPresetPrompt` in `internal/orchestrator/prompts.go` — Phase 1 becomes "run `ycc spec-check` via Bash" (mention `go run ./cmd/ycc spec-check` as fallback when the binary isn't on PATH in a dev workspace); keep two-phase flow and false-positive discipline unchanged; also update the Phase-3/output wording that says "(from spec_check)". Also check the general pm prompt for any spec_check mention.
4. Tests:
   - Update `internal/orchestrator/modes_test.go`: pm tool-list assertion (line ~115) drops `spec_check`; spec-doctor preset prompt assertion (~83) expects `spec-check`; replace/move the `specCheck` tool test (~208–235) — the pure logic is still covered by `internal/specdoctor` tests, but keep coverage of the CLI path: add a test for the new command logic (extract the command's core into a small testable func in cmd/ycc, e.g. runSpecCheck(workspace, stdout) (stale bool, err), and test it against a temp workspace with a doc referencing a removed package — mirroring the old test).
5. Docs: spec.md §6.4 (line ~329: "surfaced as the `spec_check` tool" → "surfaced as the `ycc spec-check` subcommand"), §9 pm tool list (line ~534: remove spec_check, note the CLI), docs/cli.md new command section (usage, exit codes, CI-gate note).
6. Verify: `go build ./... && go test ./...`; manually run `go run ./cmd/ycc spec-check` in the repo and confirm sensible output + exit code.

### Starting points
- internal/orchestrator/speccheck.go — current tool body to port (whole file, then delete)
- internal/orchestrator/modes.go:93 — pm registry line with specCheck(d)
- internal/orchestrator/prompts.go:334,352 — specDoctorPresetPrompt mentions of spec_check
- internal/orchestrator/modes_test.go:79-115,208-235 — prompt/tool-list assertions and old tool test
- cmd/ycc/main.go — urfave/cli/v3 tree; global --workspace flag at line ~554; command constructors return *cli.Command
- internal/docs.NewStore(ws).DocFiles(); internal/specdoctor.Check(ws, docFiles) -> Report with .Markdown()
- spec.md:329 (§6.4), spec.md:534 (§9 pm tools); docs/cli.md commands section

## Work log
- 2026-07-05 plan: Replace the pm-mode `spec_check` agent tool with a daemon-free `ycc spec-check` CLI subcommand.  1. CLI: add a `spec-check` command to the urfave/cli/v3 command tree in `cmd/ycc/main.go` (registered i
…[truncated]
- 2026-07-05 context hints: 7 recorded with plan
- 2026-07-05 context hints: internal/orchestrator/speccheck.go — current tool body to port (whole file, then delete); internal/orchestrator/modes.go:93 — pm registry line with specCheck(d); internal/orchestrator/prompts.go:3
…[truncated]
- 2026-07-05 implementer report: Replaced the pm-mode `spec_check` agent tool with a daemon-free `ycc spec-check` CLI subcommand.  Changes: - `cmd/ycc/speccheck.go` (new): adds the `spec-check` command (registered in the root Command
…[truncated]
- 2026-07-05 review tier: single-opus — reviewers: Claude
- 2026-07-05 review (Claude): accept — The change correctly replaces the pm-mode `spec_check` agent tool with a daemon-free `ycc spec-check` CLI subcommand. The new command (cmd/ycc/speccheck.go) resolves the workspace via the global --wor
…[truncated]
- 2026-07-05 decision: accept — commit: Replace the spec_check pm tool with a daemon-free `ycc spec-check` CLI subcommand (task 0135; also renumber backlog id collision 0133→0135)
- 2026-07-05 usage: 19,466 tok (in 122, out 19,344, cache_r 2,633,504, cache_w 112,698) · cost n/a (unpriced)
  implementer: 12,120 tok (in 76, out 12,044, cache_r 2,143,159, cache_w 70,266) · cost n/a (unpriced)
  coordinator: 5,296 tok (in 26, out 5,270, cache_r 337,528, cache_w 18,855) · cost n/a (unpriced)
  reviewer:Claude: 2,050 tok (in 20, out 2,030, cache_r 152,817, cache_w 23,577) · cost n/a (unpriced)
