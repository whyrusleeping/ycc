---
id: "0261"
title: Inject git diff into reviewer context
status: done
priority: 3
created: "2026-08-06"
updated: "2026-08-08"
depends_on: []
spec_refs: []
---

## Description
Reviewers should start with a synthetic “git diff” tool call that shows whats been changed, they’re always going to need to call this anyway so lets save some tokens

## Acceptance criteria

## Plan

Goal: reviewers should not have to spend a turn running `git diff` — seed each reviewer loop with a synthetic Bash tool-call exchange whose result is the actual current diff, mirroring the existing implementer preload mechanism (internal/orchestrator/preload.go).

Design:
1. New helper (e.g. internal/orchestrator/reviewdiff.go, or extend preload.go) `buildReviewDiffHistory` that:
   - Obtains the change under review via `d.Repo.Diff()` (stages all + returns `git diff --cached` — the same stable snapshot semantics the implementer report already uses). If the diff is empty or Repo errors, return nothing (skip seeding; the reviewer prompt still tells them to inspect).
   - Builds a synthetic 3-message exchange: a short user nudge ("Orient yourself: the change under review is preloaded below"), an assistant message with a single Bash tool call (command like `git diff HEAD`, id e.g. `preload_diff`), and a tool result carrying the diff text.
   - Enforces a byte cap (reuse ~64 KiB like maxPreloadBytes) with a valid-UTF-8 truncation and a visible notice telling the reviewer to run `git diff HEAD` themselves for the rest (their sandboxed Bash can — it is a read-only command).
2. In spawn_reviewers (orchestrator.go ~line 745): compute the diff exchange ONCE per spawn (identical for all reviewers), then for each reviewer spec `loop.SetHistory(historyCopy)` before `loop.Seed(reviewerPrompt(...))`. Give each loop its own slice copy so loops don't share backing arrays.
3. When the diff was seeded, adjust the seeded prompt so it doesn't ask the reviewer to start by running git diff — e.g. reviewerPrompt gains a hasDiff flag and says "the current diff is already in your context above; inspect further with Read/Bash as needed" instead of "start with 'git diff'".
4. Emit synthetic transcript events mirroring emitSyntheticPreload, but per reviewer actor (`d.Emitter.With("reviewer:"+spec.label())`): model_turn (synthetic:true) + tool_call/tool_result for the Bash call. Do NOT emit user_input (ReplayHistory folds all user_input into coordinator history — known gotcha).
5. re_review (optional but cheap and clearly in spirit): before posting reReviewPrompt, append a fresh synthetic diff exchange to each handle's history (`h.loop.SetHistory(append(h.loop.History(), diffMsgs...))`) and soften the re-review prompt similarly when seeded. If this turns out awkward with the loop API, it's acceptable to keep re_review unchanged — the primary acceptance is the initial spawn.
6. Tests (internal/orchestrator): unit-test the builder (empty diff → no history; normal diff → 3 messages with correct roles/tool-call wiring; oversized diff → truncation notice within cap), plus a spawn_reviewers-level test asserting the reviewer loop's history starts with the synthetic diff exchange before the seed prompt (see revise_test.go ~line 337 for how reviewer specs/plans are stubbed, preload_test.go for the pattern).

Acceptance:
- Reviewers spawned via spawn_reviewers start with the diff already in context as a genuine-looking tool exchange; empty-diff and huge-diff cases handled; no user_input events emitted for synthetic history; existing tests pass (`go test ./internal/orchestrator/...` and a broader `go test ./...` sanity, ignoring known-flaky packages).

### Starting points
- internal/orchestrator/preload.go — buildPreloadHistory/emitSyntheticPreload: the pattern to mirror
- internal/orchestrator/orchestrator.go:745-755 — reviewer loop construction in spawn_reviewers; re_review at ~789
- internal/orchestrator/prompts.go:581 reviewerPrompt, :310 reReviewPrompt
- internal/git/git.go:54 Repo.Diff (git add -A + diff --cached)
- internal/tools/reviewer.go — reviewer toolset: Read + sandboxedBash + submit_review
- internal/engine/loop.go:448 SetHistory / :459 History
- tests: internal/orchestrator/preload_test.go, revise_test.go:337 (reviewers with Specs)

## Work log
- 2026-08-08 plan: Goal: reviewers should not have to spend a turn running `git diff` — seed each reviewer loop with a synthetic Bash tool-call exchange whose result is the actual current diff, mirroring the existing 
…[truncated]
- 2026-08-08 context hints: 7 recorded with plan
- 2026-08-08 context hints: internal/orchestrator/preload.go — buildPreloadHistory/emitSyntheticPreload/validUTF8Prefix: the pattern to mirror; internal/orchestrator/orchestrator.go:745-755 — reviewer loop construction; re_r
…[truncated]
- 2026-08-08 preload: 3 file(s), ~20 KiB seeded into implementer context
- 2026-08-08 implementer report: Implemented task 0261.  Changes: - Added `reviewdiff.go` to capture `Repo.Diff()` once per reviewer spawn and synthesize a user nudge + Bash `git diff HEAD` tool call + tool-result exchange. - Added a
…[truncated]
- 2026-08-08 review tier: single-opus — reviewers: sol
- 2026-08-08 review (sol): accept — The change correctly seeds each spawned reviewer with a three-message synthetic Bash diff exchange built from one stable `Repo.Diff()` snapshot, gives each loop its own history slice, updates prompts 
…[truncated]
