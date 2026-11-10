---
id: "0538"
title: 'Consolidate coordinator tools: fewer, merged tools per mode based on actual usage'
status: done
priority: 3
created: "2026-11-10"
updated: "2026-11-10"
depends_on: []
spec_refs:
    - spec.md#9. Modes
---

## Description
## Why
On 2026-11-10 the owner raised a concern about tool proliferation after 0524 added `start_work` to chat. The tool definitions sent on every coordinator request were measured with `BuildMode` and every optional hook set:

| mode | tools | tool definition size |
|---|---|---|
| chat | 24 | ~20.4 KB |
| work | 25 | ~20.7 KB |
| pm | 22 | ~16.5 KB |

Each tool adds tokens to every request and one more option the model weighs on every turn. Tools have been added one at a time (recently `schedule_watch` and `start_work`) with no review of the whole set. The owner accepted a separate cleanup task instead of reverting 0524.

## Candidates (unverified; usage data decides)
- `start_work` (chat) and `switch_to_work` (pm) both hand a task to work. Use one hand-off tool for both modes.
- `remember` and `forget` → one memory tool.
- `list_jobs`, `job_output`, `wait`, `kill_job` (plus `tool_output`) → fewer job tools.
- `list_backlog` and `get_task`.
- Hooks that add a tool plus a paragraph of system-prompt guidance (`schedule_watch`, `start_work`). Check whether the guidance can be shorter or the tool offered only when relevant.

## Approach
1. Measure first. Mine `~/code/*/.ycc/sessions/*/events.jsonl` for `tool_call` counts per tool and mode. Deduplicate symlinked workspaces with realpath, and exclude wheeler's 2026-10-12 runaway work loop. Report rarely or never used tools and tools that are usually called together.
2. Propose the merges and removals to the owner, with the numbers, before changing anything.
3. Implement the accepted set:
   - Keep the behavior the merged tools covered, or drop it on purpose.
   - Update the prompts that name old tools, spec §7/§9, and any client rendering keyed on tool names.
   - Replay of old event logs must keep working: old tool names in history still render and reopen.

## Acceptance criteria
- A usage report covering every tool per mode (calls, sessions using it) is recorded in this task's work log.
- The owner has approved the consolidation list before implementation.
- After the change, each mode's tool count and definition size are recorded beside the baseline above, and both are lower.
- Existing sessions that used removed or renamed tools still reopen and replay (test).
- `go test ./internal/...` passes. Prompts and spec reference no removed tool names.

## Work log
### 2026-11-10 — usage report (step 1)
Source: coordinator `tool_call` events in every `~/code/*/.ycc/sessions/*/events.jsonl` (workspaces deduped by realpath), with mode from `session_started` and switched by `mode_changed`. Wheeler's 2026-10-12 runaway loop (1,821 sessions) is excluded. Script and raw output: `.ycc/tmp/tool-usage/mine.py`, `report.txt`; per-tool definition sizes: `baseline-defs.txt`. "since" = share of that mode's sessions started on or after the tool's first recorded call, which is fairer for newer tools.

**work** — 928 sessions, 59,629 calls (direct and delegate together):

| tool | calls | sessions | % since | def bytes |
|---|---|---|---|---|
| Bash | 31,160 | 927 | 100% | 1075 |
| Read | 8,804 | 872 | 94% | 869 |
| Edit | 6,214 | 549 | 59% | 617 |
| get_task | 2,106 | 928 | 100% | 266 |
| update_task | 1,449 | 862 | 93% | 472 |
| commit | 1,092 | 783 | 84% | 880 |
| Search | 1,046 | 240 | 45% | 1546 |
| list_backlog | 922 | 910 | 98% | 593 |
| finish | 916 | 896 | 97% | 570 |
| spawn_reviewers | 908 | 875 | 94% | 1384 |
| Write | 882 | 291 | 32% | 969 |
| wait | 871 | 383 | 46% | 886 |
| propose_plan | 649 | 559 | 60% | 505 |
| spawn_implementer (delegate only) | 578 | 528 | 57% | 1434 |
| re_review | 536 | 360 | 39% | 760 |
| create_task | 468 | 325 | 35% | 1138 |
| send_to_implementer (delegate only) | 443 | 233 | 25% | 814 |
| remember | 339 | 291 | 35% | 1179 |
| tool_output | 48 | 36 | 7% | 685 |
| ask_user | 44 | 30 | 3% | 1172 |
| job_output | 40 | 26 | 3% | 881 |
| web_search | 35 | 20 | 2% | 520 |
| kill_job | 33 | 22 | 3% | 464 |
| fetch_page | 28 | 13 | 1% | 413 |
| list_jobs | 15 | 15 | 3% | 430 |
| forget | 2 | 2 | 0% | 586 |
| schedule_watch | 0 | 0 | — | 1776 |

**chat** — 554 sessions, 49,328 calls:

| tool | calls | sessions | % since | def bytes |
|---|---|---|---|---|
| Bash | 22,876 | 546 | 99% | 1075 |
| Edit | 9,466 | 442 | 80% | 617 |
| Read | 8,393 | 511 | 92% | 869 |
| Search | 1,135 | 194 | 63% | 1546 |
| Write | 1,087 | 254 | 46% | 969 |
| wait | 1,072 | 182 | 34% | 886 |
| update_task | 1,032 | 278 | 50% | 472 |
| create_task | 923 | 292 | 53% | 1138 |
| get_task | 716 | 207 | 37% | 266 |
| spawn_agent | 533 | 131 | 36% | 803 |
| web_search | 408 | 77 | 14% | 520 |
| send_to_agent | 405 | 69 | 19% | 899 |
| remember | 397 | 224 | 43% | 1179 |
| list_backlog | 326 | 246 | 44% | 593 |
| fetch_page | 246 | 61 | 11% | 413 |
| ask_user | 109 | 79 | 14% | 1172 |
| job_output | 72 | 41 | 8% | 881 |
| kill_job | 68 | 28 | 6% | 464 |
| list_jobs | 26 | 23 | 7% | 430 |
| tool_output | 23 | 18 | 6% | 685 |
| forget | 11 | 7 | 3% | 586 |
| commit (chat, added today by 0463) | 0 | 0 | — | 794 |
| schedule_watch | 0 | 0 | — | 1776 |
| start_work (added today by 0524) | 0 | 0 | — | 1240 |

**pm** — 41 sessions, 1,050 calls:

| tool | calls | sessions | % since | def bytes |
|---|---|---|---|---|
| Bash | 391 | 40 | 98% | 1075 |
| remember | 155 | 25 | 69% | 1179 |
| Edit | 99 | 16 | 39% | 617 |
| Read | 85 | 24 | 59% | 869 |
| get_task | 68 | 16 | 39% | 266 |
| forget | 54 | 17 | 71% | 586 |
| list_backlog | 36 | 27 | 66% | 593 |
| finish | 31 | 29 | 71% | 570 |
| update_task | 28 | 9 | 22% | 472 |
| create_task | 22 | 16 | 39% | 1138 |
| ask_user | 22 | 11 | 27% | 1172 |
| Write | 14 | 10 | 24% | 969 |
| propose_plan | 13 | 12 | 29% | 505 |
| Search | 9 | 3 | 12% | 1546 |
| fetch_page | 8 | 3 | 7% | 413 |
| switch_to_work | 7 | 7 | 19% | 685 |
| web_search | 6 | 3 | 7% | 520 |
| tool_output, list_jobs, job_output, wait, kill_job | 0 | 0 | — | 3346 together |

**watch** and **integrate** modes: no recorded sessions. Retired names still present in history: `job_result` (chat, 4 calls), `bash` (work, 1), `spec_check` (pm, 2).

**Used together:** almost all pairs are file and shell tools (Bash+Read, Read+Search, Bash+Edit). The only pairings among the candidates are `list_backlog`→`get_task` (792 consecutive calls in work; both are in nearly every work session) and `forget`+`remember` in pm (25 same-turn pairs, from memory grooming). Job tools are rarely called together: `wait` accounts for most job-tool calls, and list/output/kill are each used in 2–8% of sessions.

**Subagents** share the editing set (`tools.Editing`), so merging job tools affects them too. Implementer: wait 740, job_output 183, tool_output 92, kill_job 45, list_jobs 4; general agents: tool_output 21, web 44; reviewers: tool_output 22.

**Baseline (all hooks set):** chat 24 tools / 20,273 B; work direct 25 / 20,636 B; work delegate 27 / 22,884 B; pm 22 / 16,521 B. The task's baseline above matches.

### 2026-11-10 — proposed consolidation (step 2, awaiting owner approval)
1. **Job tools:** merge `list_jobs`, `job_output` and `kill_job` into one `jobs` tool (`action: list|output|kill`). Keep `wait`, which accounts for most job-tool calls. Saves 2 tools and about 1 KB in every mode and every subagent.
2. **`tool_output`:** fold it into the same `jobs` tool as an output read by artifact id. Saves 1 more tool and about 0.5 KB. Optional: it is a different id space from job ids.
3. **`forget`:** offer it only in pm. It was used in 0% of work and 3% of chat sessions, and `remember` with `supersedes` covers corrections. Memory grooming (pm, 71%) keeps it. Saves 1 tool and 586 B in chat and work; the shared memory prompt header becomes mode-aware.
4. **`schedule_watch`:** offer it in chat only, not work. It is the largest definition (1,776 B), has never been called, and no watches exist in any workspace. Also shorten its description and guidance. Saves 1 tool and about 2.3 KB in work.
5. **`web_search` + `fetch_page`:** merge into one `web` tool (`query` or `url`). Used in 1–2% of work and 11–14% of chat sessions. Saves 1 tool and about 0.4 KB in every mode.
6. **`start_work` / `switch_to_work`:** no per-mode saving, because each mode has only one of them. Unifying them means pm would start a separate work session instead of converting itself. That changes behavior, so this is left for the owner to decide. Note: another session is reverting 0524 (`start_work`) in this worktree right now (`git revert d6a1b3f` in progress at 09:58Z), which may make this item moot.
7. **Keep:** `list_backlog` and `get_task`. Both are in nearly every work session, and a merge saves only about 270 B.
8. **Trim descriptions** of the largest frequently used definitions (Search 1,546 B, spawn_reviewers 1,384 B, remember 1,179 B, ask_user 1,172 B, create_task 1,138 B) without changing behavior.

Projected result with items 1, 3, 4 and 5: chat 24→20, work 25→20, pm 22→19 tools, with sizes down about 2–5 KB per mode. Adding item 2 removes one more tool per mode. Under the acceptance criteria, implementation waits for the owner's approval of this list.

**Update 10:00Z:** commit 23ca1a5 reverted 0524, so chat no longer has `start_work`. The current chat baseline with all hooks set is 23 tools / 19,033 B (20,273 − 1,240), item 6 is moot, and the item 1+3+5 projection for chat becomes 23→19.

**Blocked (2026-11-10, unattended session):** the owner must approve the list before implementation; the question was asked but no one was available to answer. To unblock, record which of items 1–5, 7 and 8 are approved, then set the task to todo. The baseline measurement can be re-run with a throwaway `BuildMode` test that sets `StartWork` and `ScheduleWatch` and sums `json.Marshal(reg.APIDefs())` per tool.

### 2026-11-10 — owner decision (step 2 resolved)
Approved: **item 4** (`schedule_watch` in chat only, not work, with a shorter description and guidance) and **a moderate part of item 8** (tighten the largest frequently used definitions, such as Search, spawn_reviewers, remember, ask_user and create_task, without going too far or changing behavior). Not approved: items 1, 2, 3 and 5; item 6 is moot; item 7 keeps the status quo. Acceptance is adjusted to this scope: work loses one tool (`schedule_watch`), so chat and pm keep their tool counts and must only get smaller definitions. Record each mode's count and size beside the baseline, and keep the replay criterion for old `schedule_watch` calls in work-mode history.

### 2026-11-10 — implemented
Work no longer offers `schedule_watch` or its guidance (chat keeps both, shortened). The descriptions of Search, spawn_reviewers, ask_user, create_task and remember were tightened without changing schemas or constraints. `TestReopenReplaysToolTheModeNoLongerOffers` (internal/session) reopens a work session whose history contains a `schedule_watch` call, and `TestScheduleWatchOfferedWithGuidance` checks that only chat offers it. Spec §9 and docs/design/watches.md are updated. `go test ./internal/...` passes.

Measured with `BuildMode` and every hook set. The chat "before" is 19,242 B rather than 19,033 B because definitions changed after the baseline was recorded.

| mode | tools | definition bytes | system prompt bytes |
|---|---|---|---|
| chat | 23 → 23 | 19,242 → 18,082 (−6.0%) | 4,697 → 4,581 |
| work direct | 25 → 24 | 20,636 → 17,708 (−14.2%) | 6,435 → 5,767 |
| work delegate | 27 → 26 | 22,884 → 19,956 (−12.8%) | 6,621 → 5,953 |
| pm | 22 → 22 | 16,521 → 15,685 (−5.1%) | 2,840 → 2,840 |

Per tool: Search 1,546 → 1,396; spawn_reviewers 1,384 → 1,068; remember 1,179 → 908; ask_user 1,172 → 936; create_task 1,138 → 959; schedule_watch 1,776 → 1,452.
