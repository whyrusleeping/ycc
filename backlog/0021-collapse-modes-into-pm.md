---
id: "0021"
title: Collapse spec/backlog/feature/bug into a single `pm` (project manager) mode
status: done
priority: 2
created: "2026-06-26"
updated: "2026-06-26"
depends_on:
    - "0006"
spec_refs:
    - Modes (the home menu)
    - The `work` orchestration (in detail)
---

## Description
The `spec`, `backlog`, `feature`, and `bug` modes are one capability set under four prompt
framings — they all read spec + explore code, edit the docs, and manage the backlog.
Collapse them into a single **`pm` (project manager)** mode for planning / intake / docs
work that does NO implementation. Keep `chat` (freeform, can edit code) and `work` (the
orchestrated pipeline). End state: three modes — `pm`, `chat`, `work` (spec §9).

Motivating frustration: `feature`/`bug` carry `switch_to_work`, which starts a FRESH
coordinator whose prompt (coordinatorSystem + modeTransitionPrompt) says to use the named
task "or the next ready task" — so after grooming the backlog it wandered off and churned an
unrelated task. The collapse removes that footgun and reshapes the hand-off.

Scope:
- New `pm` mode in `Modes()` / `BuildMode`. Tools: `Read`/`Write`/`Edit`/`Bash`,
  `list_backlog`/`get_task`/`create_task`/`update_task`, `propose_plan`, `ask_user`,
  `finish`. No `spawn_*`, no `commit`.
- Remove the `spec`, `backlog`, `feature`, `bug` modes. Preserve their framings as
  **opening-prompt presets** the home menu offers ("New feature" → explore then propose;
  "Bug report" → reproduce then localize; "Author spec"; "Build backlog") — each starts a
  `pm` session with a tailored first prompt. One mode, same affordances (point 4).
- Soft "no code" boundary: `pm` keeps Write/Edit (it maintains `spec.md`, a plain file) but
  is prompted not to edit code. Hard enforcement (path scoping / isolation) is deferred —
  same class as reviewer non-mutation (0008).
- Reshape the hand-off: `pm`'s `switch_to_work` must be DELIBERATE — (a) require explicit
  interactive user approval before transitioning, and (b) carry the planning context + the
  specific target task id into the `work` session so the coordinator implements THAT task
  rather than re-picking the next ready one. Update `modeTransitionPrompt` and the
  coordinator's task-selection so a carried task is used verbatim (no free re-pick).
- Keep `chat` for now (user still experimenting).

Open questions:
- How does the approval gate interact with interaction levels? Starting an implementation
  pipeline is high-impact / hard to reverse, so it may warrant a real confirmation even in
  `autonomous` (where `ask_user` normally auto-answers) — decide.
- `propose_plan` in `pm` only pays off if plans are durably retained/tracked — depends on
  the plan-library work (0020).

## Acceptance criteria
- [ ] a single `pm` mode replaces `spec`/`backlog`/`feature`/`bug`; `chat` and `work` remain
- [ ] `pm` does planning/docs/backlog only — no `spawn_implementer`/`commit`; "no code edits"
      is prompt-enforced (soft boundary)
- [ ] the home menu offers the old framings as presets that open `pm` with a tailored opening
      prompt — no separate modes for them
- [ ] `switch_to_work` is a deliberate hand-off only: requires explicit user approval AND
      carries the specific target task + planning context, so `work` implements that task
      rather than re-picking "the next ready task"
- [ ] `ListModes` / TUI home menu updated; in-session `mode_changed` still works
- [ ] spec §9 reflects the implemented design; `go test ./...` green

## Work log
- Collapsed `spec`/`backlog`/`feature`/`bug` into a single `pm` mode; `chat` and `work`
  remain (`Modes()`, `BuildMode` in internal/orchestrator/modes.go). `pm` tools:
  Read/Write/Edit/Bash, list_backlog/get_task/create_task/update_task, propose_plan,
  switch_to_work, ask_user, finish — no spawn_*/commit. Added `pmModeSystem` prompt with a
  soft "no code edits" boundary.
- Preserved the four framings as opening-prompt presets (`Presets()` + new `Preset` proto
  message and `ListModes` presets field); the TUI home menu lists modes + presets and seeds a
  preset's opening prompt when the user types nothing.
- Made `switch_to_work` deliberate: it takes `task_id`+`plan`, requires a `Confirm` approval
  (real human answer even in autonomous — declines if none), and carries a verbatim work
  hand-off prompt naming THAT task into the work session. Added `Control.Prompt` /
  `Result.NextPrompt` plumbing; the work transition prompt no longer says "or the next ready
  task".
- Added `Asker.Confirm` (orchestrator) + `interaction.Confirm` (session) with tests; updated
  modes_test/revise_test; updated spec §9 (already described target) and §11 (confirmation-gate
  exception). `go test ./...` green.
- 2026-06-26 implementer report: Collapsed `spec`/`backlog`/`feature`/`bug` into a single `pm` mode; `chat` and `work` remain. All acceptance criteria met; `go test ./...` green.  ## What changed  **Modes (internal/orchestrator/modes
…[truncated]
- 2026-06-26 plan: Collapse spec/backlog/feature/bug modes into a single `pm` mode; keep `chat` and `work`.  1. Modes(): return three modes — pm (planning/intake/docs, no implementation), chat, work. Remove spec/backl
…[truncated]
- 2026-06-26 review (claude): accept — The change cleanly collapses spec/backlog/feature/bug into a single `pm` mode while keeping `chat` and `work`, matching spec §9. `pm` has the prescribed toolset (Read/Write/Edit/Bash, backlog tools, 
…[truncated]
- 2026-06-26 revision: Added `switch_to_work` to the pm tool enumeration in spec.md §9 (line 324–326), so the listed tools — Read/Write/Edit/Bash, list_backlog/get_task/create_task/update_task, propose_plan, switch_to_
…[truncated]
- 2026-06-26 decision: accept — commit 0414bd1: Collapse spec/backlog/feature/bug modes into a single `pm` mode  - Modes() now returns three: pm (planning/intake/docs, no implementation),   chat, work. pm toolset: Read/Write/Edit/Bash, backlog tool
…[truncated]
