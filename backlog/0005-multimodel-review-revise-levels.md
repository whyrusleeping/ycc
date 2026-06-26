---
id: "0005"
title: Multi-model review, revise loop, interaction levels (M3)
status: done
priority: 3
created: 2026-06-25
updated: 2026-06-25
depends_on: ["0004"]
spec_refs: ["The work orchestration", "Interaction levels", "Backends & model registry"]
---

## Description
Make review genuinely multi-perspective and closed-loop, and give the user control over
autonomy. Reviewers fan out concurrently across Claude/GPT/GLM/local; the coordinator
can send revision instructions back to the implementer (reusing its context) and trigger
a re-review (reusing reviewer contexts). Add the three interaction levels.

## Acceptance criteria
- [ ] reviewer fan-out across configured `roles.reviewers`, concurrent + barrier
- [ ] send_to_implementer (reuse implementer ctx) + re_review (reuse reviewer ctx)
- [ ] coordinator judge step: accept vs revise with recorded rationale
- [ ] interaction levels interactive | judgement | autonomous gate the ask_user tool
- [ ] autonomous mode accumulates assumptions/decisions into the final report

## Work log
- 2026-06-26 implemented:
  - `internal/config`: TOML config (models map + roles) + `Registry` building per-role
    gollama backends (anthropic/openai/ollama). `DefaultAnthropic` fallback. Tests pass.
  - `internal/orchestrator`: persistent subagents held in `Deps` across rounds; concurrent
    reviewer fan-out (`spawn_reviewers`, goroutines + WaitGroup barrier) across configured
    models; revise loop `send_to_implementer` + `re_review` reusing subagent contexts;
    `ask_user` tool; per-level coordinator prompt guidance; review aggregation (N/M accept).
  - `internal/session`: `interaction` implements `orchestrator.Asker` — autonomous mode
    auto-answers + records assumptions (appended to the final report); interactive/judgement
    block until answered. Manager rebuilt around `config.Registry`; reviewers built from
    `roles.reviewers`. SendInput answers a pending question if one is open.
  - proto+server: `AnswerQuestion` RPC. event types question_asked/answered.
  - `cmd/yccd`: `-config` flag (falls back to single Anthropic backend). `cmd/ycc`:
    `start --mode --level` flags.
  - Tests: revise-loop integration (scripted fakes: buggy impl → revise → context-reused
    fix → re-review accept → commit), interaction levels (autonomous/interactive/cancel),
    config load+registry. All pass under -race.
- 2026-06-26 verified LIVE (config: coordinator/implementer=opus, reviewers=[opus,haiku]):
  one autonomous `ycc start` drove the full loop; spawn_reviewers ran opus + haiku
  CONCURRENTLY (interleaved tool calls, two different models), barrier aggregated "2/2
  accept", coordinator committed 4567ef6 and marked the task done. Work log recorded both
  reviewers' verdicts + decision+sha.
- Notes: revise loop verified deterministically (unit test), not forced live (hard to make
  a model reliably ship a bug). Autonomous assumption-accumulation mechanism unit-tested;
  the live run needed no ask_user. Only Anthropic was exercised live (only key available),
  but openai/ollama backends are wired and config-tested.