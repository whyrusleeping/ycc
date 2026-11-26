---
id: "0623"
title: Replace tool_output with per-session output files that Bash names in its result
status: todo
priority: 3
created: "2026-11-26"
updated: "2026-11-26"
depends_on: []
spec_refs:
    - docs/design/async-jobs.md
    - docs/design/daemon-restart.md
---

## Description
## Status
Accepted (owner decision, 2026-11-26, chat session discussing agent tool sets). Not started.

## Why
When a `Bash` result exceeds its 64 KiB / 2000-line budget, the model sees a head/tail preview and an `output_…` artifact id that it pages with the separate `tool_output` tool (`internal/tools/output.go`). Every agent that has `tool_output` also has `Read` (offset/limit windows), `Search` and `Bash`. The read-only sandbox binds `/` read-only, so sandboxed agents can read files too. A file path printed in the result serves the same purpose with tools the model already uses, and removes one tool definition (~685 B) from every coordinator and subagent request. Usage is low: the 0538 (tool consolidation) audit of 2026-11-10 found `tool_output` in 6–7% of coordinator sessions, plus 92 implementer, 22 reviewer and 21 generic-agent calls.

Supervised background jobs already persist their capture at `.ycc/sessions/<id>/jobs/<job>/artifact.bin` (jobshim, 4 MiB per job, 64 MiB retained per session) and rehydrate the in-memory store from it on reopen. Only foreground `Bash` captures are memory-only (`ArtifactStore`: 16 MiB, 64 records per agent workspace).

Related history: on 2026-11-10 the owner declined folding `tool_output` into a merged `jobs` tool (0538 item 2). This task removes it in favor of files instead, which is a different change.

## Scope
- Foreground `Bash` writes the full capture (keep the 4 MiB cap) to a per-session file under the session directory and names its absolute path in the truncated result, e.g. "full output: <path> (use Read with offset/limit, or grep/sed/tail)". Writing only over-budget captures is acceptable if every truncated result has a file.
- Background `Bash` jobs name the supervisor's existing capture file (or an equivalent) rather than an artifact id; drop the "not ready" artifact hints. If the file is written while the job streams, say that it can be tailed while running.
- Remove `tool_output` from `Inspect`, `ReadOnlyInspect`, `Reviewer` and `Editing`, and remove `ArtifactStore` along with its reopen rehydration (`reserveID` in `supervised_reopen.go`) where it is no longer needed. Keep `OutputMetadata` in events and point it at the path instead of an artifact id.
- Retention: bound per-session disk use (reuse or share jobshim's session retention policy) and clean up with the session's other state. A result that points at an evicted file says the file is gone; it never names a missing file without saying so.
- Agent workspaces without a session directory (e.g. modeleval, ycc-spike) either get a temp location or keep preview-only output; decide during implementation.
- Update `docs/design/async-jobs.md`, `docs/design/daemon-restart.md` and any spec or prompt text that names `tool_output`.

## Acceptance criteria
- No agent role is offered `tool_output`; prompts, spec and design docs do not name it as a current tool.
- A foreground and a background `Bash` command whose output exceeds the budget each print a path; a test reads the middle of that output with `Read` offset/limit and gets bytes matching the capture's sha256.
- A sandboxed read-only (reviewer or generic) agent's `Bash` can `cat` that path (test, or a manual check on a host with the sandbox, recorded here).
- Per-session output-file disk use is bounded and eviction is reported in later results (test).
- An existing session whose history contains `tool_output` calls still reopens and replays (test, following 0538's `TestReopenReplaysToolTheModeNoLongerOffers`).
- `go test ./internal/...` passes.


## Work log
