---
id: "0144"
title: ycc export — session transcript to shareable markdown
status: done
priority: 4
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 18.6 Session history browser & reopen
---

## Description
Sessions are durable and browsable in the TUI, but can't leave the machine in readable form: no way to share "here's what the agent did and why" in a PR description, an issue comment, or a chat. The transcript renderer already folds tool plumbing and merges ask_user exchanges — reuse those semantics for a markdown export.

## Acceptance criteria
- [ ] `ycc export <session-id> [--out FILE]` writes a markdown transcript: turns, collapsed tool-call summaries, questions+answers folded per §18.3's one-block rule, review verdicts, commits (sha + message), final report, and a usage/cost footer.
- [ ] `--full` includes tool call payloads/results; default keeps the folded, human-readable view.
- [ ] Works for live and persisted sessions (same source as GetSessionTranscript).

## Plan

Goal: `ycc export <session-id> [--out FILE] [--full] [--project NAME]` renders a session's event log as shareable markdown, reusing the transcript-folding semantics the TUI uses (spec §18.3/§18.6).

1) New package `internal/export` (file `markdown.go` + `markdown_test.go`):
   - `type Options struct { SessionID string; Full bool; Usage []*v1.UsageRow; UsageTotal *v1.UsageRow }`
   - `func Markdown(events []*v1.Event, opts Options) string` — pure function over the same `[]*v1.Event` that GetSessionTranscript returns (parse each DataJson once into a map; skip Transient events defensively).
   - Folding semantics, ported from internal/tui/tui.go (mergedResultIdx / askQuestionIdx / resultCallIdx / answerIdxFor / isAskUserPlumbing / isFoldedAnswer / isEmptyModelTurn / isEchoedIdle / computeHiddenRow — all same-actor scans, id-matched pairing):
     * hide `user_input_delivered` markers (the queued echo already shows the text);
     * fold an adjacent same-actor (id-matching) tool_result into its tool_call: default renders ONE collapsed bullet per tool call: status glyph (✓ ok / ✗ error / ○ no result), backticked tool name, arg summary (file_path|path|pattern|command|query|url|task_id fallback to compact args, one-line, truncated ~100 chars), duration suffix, and for implementer/reviewer actors an actor tag on first-of-run;
     * ask_user one-block rule: `question_asked` is the canonical block for the exchange — hide the ask_user tool_call, its tool_result (unless error:true), and the paired `question_answered`; render Q (and for multi-question batches each numbered prompt) with the folded answer(s) beneath ("→ answer"), auto-answers as "→ auto-answered (autonomous mode)";
     * hide empty (tool-calls-only) model_turns and session_idle rows whose report merely echoes the preceding final model_turn (dropDuplicatePrefix logic).
   - Rendering (default mode):
     * header: `# Session <id>` plus a metadata line from session_started data (mode, workspace, interaction level, started ts) best-effort;
     * `user_input` → `**user:**` + text (note "(queued)" only if never delivered — has queued:true and no matching user_input_delivered);
     * `model_turn` with text → prose paragraph, prefixed `**<actor>:**` at the start of each actor run (firstOfRun over rendered rows);
     * `thinking` → omitted by default, included in --full as an italic/blockquote "reasoning" block;
     * `review_submitted` → bullet: `§ review (<model>): **ACCEPT/REVISE** — summary`;
     * `commit_made` → bullet: `● commit \`sha\` message`;
     * `plan_proposed` → "Plan" blockquote/section with the plan text (it's the "why");
     * `session_idle` (non-echoed) → its report text; also a trailing `## Final report` section from the LAST session_idle report if present;
     * `session_error` → `**error:**` + first lines of msg (full msg in --full); `subagent_spawned/finished`, `job_*`, `mode_changed`, `interrupted/resumed`, `budget_*`, `workstream_*` → terse italic one-liners; leave the very low-value config-change events (thinking_level_changed, role_config_changed, interaction_level_changed, review_tier_selected, task_focus, doc_updated, log) as terse one-liners too or omit-by-default where TUI shows nothing — keep it readable;
     * escape nothing inside fenced blocks; put tool payloads in ``` fences.
   - --full mode additionally expands each tool call: args as a fenced json block and the result payload in a fenced block (no folding away of payloads; ask_user folding and hidden bookkeeping rows stay).
   - Usage/cost footer: accumulate per-model event.Usage from model_turn data (input/output/cache_read/cache_write/total) into a `## Usage` markdown table; when opts.Usage rows are supplied (from GetUsage), prefer those rows (they carry cost + price_status) and render a cost column with the same partial-pricing asterisk semantics as `ycc cost`.

2) CLI: new `exportCommand()` in cmd/ycc/export.go, registered in newRootCommand:
   - args: `<session-id>`; flags `--out FILE`, `--full`, `--project NAME`;
   - dial daemon (a.dial(), same as other commands — this serves live and persisted sessions identically since GetSessionTranscript resolves both), call GetSessionTranscript{Project, SessionId};
   - best-effort call GetUsage{Project, GroupBy: ["session","model"]} and filter rows to the session id for the cost footer; ignore errors (footer falls back to event-derived token totals);
   - write markdown to --out file (0644) or stdout when --out is absent.

3) Tests (internal/export/markdown_test.go): synthetic event streams asserting — collapsed tool bullet + merged result; --full includes args/result payloads; ask_user 4-event exchange renders exactly one Q/A block (no repeated question/answer, plumbing hidden); multi-question batch folding; auto-answer line; empty model_turn hidden; echoed session_idle deduped + Final report section present; commit and review lines; usage footer totals (and cost column when usage rows supplied). Plus a light test for the CLI arg validation if cheap.

Verify: go build ./... && go test ./... ; eyeball `ycc export` output against a real session log if one exists in .ycc/sessions.

### Starting points
- internal/tui/tui.go:7437-7684 — fold helpers to port: mergedResultIdx, askQuestionIdx, resultCallIdx, answerIdxFor, isAskUserPlumbing, isFoldedAnswer, isEmptyModelTurn, isEchoedIdle, computeHiddenRow
- internal/tui/tui.go:9159-9330 — bodyFor/questionBody/renderBody + dropDuplicatePrefix/precedingTurnText (echoed-idle dedupe)
- internal/tui/tui.go:9996 argSummary; 10220 detailLine; 10341 eventUsage (DataJson usage parsing)
- cmd/ycc/main.go — command registration pattern (a.dial(), costCommand for GetUsage usage)
- internal/server/server.go:146 GetSessionTranscript; internal/session/session.go:1579 SessionTranscript (live + persisted)
- proto/ycc/v1/ycc.proto — Event{seq,ts,actor,type,data_json}, GetUsageRequest/UsageRow
- internal/tui/tui.go dataField/dataList/dataQuestions helpers (search `func dataField`) — reimplement locally in export pkg

## Work log
- 2026-07-06 plan: Goal: `ycc export <session-id> [--out FILE] [--full] [--project NAME]` renders a session's event log as shareable markdown, reusing the transcript-folding semantics the TUI uses (spec §18.3/§18.6). 
…[truncated]
- 2026-07-06 context hints: 7 recorded with plan
- 2026-07-06 context hints: internal/tui/tui.go:7437-7684 — fold helpers to port (mergedResultIdx, askQuestionIdx, resultCallIdx, answerIdxFor, isAskUserPlumbing, isFoldedAnswer, isEmptyModelTurn, isEchoedIdle, computeHiddenRo
…[truncated]
- 2026-07-06 implementer report: All clean.   ## Summary  I implemented **Task 0144: `ycc export`** — session transcript to shareable markdown.  ### What I changed  **New package `internal/export`:** - `markdown.go` — the public 
…[truncated]
- 2026-07-06 review tier: single-opus — reviewers: Claude
- 2026-07-06 review (Claude): accept — The change adds a new `internal/export` package (markdown.go + helpers.go + thorough table of tests) and a `ycc export <session-id> [--out] [--full] [--project]` CLI command registered in newRootComma
…[truncated]
- 2026-07-06 decision: accept — commit: cli: add `ycc export` — session transcript to shareable markdown with TUI-parity folding (task 0144)
- 2026-07-06 usage: 55,183 tok (in 186, out 54,997, cache_r 4,371,775, cache_w 307,862) · cost n/a (unpriced)
  implementer: 33,906 tok (in 82, out 33,824, cache_r 2,249,239, cache_w 87,078) · cost n/a (unpriced)
  coordinator: 13,081 tok (in 54, out 13,027, cache_r 1,303,615, cache_w 170,924) · cost n/a (unpriced)
  reviewer:Claude: 8,196 tok (in 50, out 8,146, cache_r 818,921, cache_w 49,860) · cost n/a (unpriced)
