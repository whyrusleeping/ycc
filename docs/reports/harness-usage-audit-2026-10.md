# Harness usage and context audit — October 2026

## Scope and evidence

Read-only inspection and streaming analysis of reachable session logs on the development host, followed by focused harness cleanup. This is not a complete end-to-end live-model evaluation. No new provider runs were performed for the audit.

The corpus contains **987 unique sessions**, June 28–October 9, 2026: ycc 429, valstore 291, oldgrowth 108, lamia 95, sage 25, ychat-dev 20, ipfs-nu 8, algoz 3, wheeler 2, ychat 2, plus four web-e2e fixtures. Discovery covered the project registry, `/home/why/code`, and reachable temporary/review copies. Realpath/inode dedup collapsed symlinks; session-id dedup removed 631 copied logs, preferring the registered original. Benchmark JSONL that was not a ycc session was excluded. Live logs were still changing during the scan.

Counts below exclude synthetic tool calls. The analysis used event types, actor/model metadata, tool argument fields, and result/status markers, not transcript excerpts. They describe one operator's workloads, not all harness users. Rare-feature counts must also be interpreted against when features became available.

## Job tools

386 sessions used at least one job tool.

| Operation | Calls | Coordinator | Implementer | Other actors |
| --- | ---: | ---: | ---: | ---: |
| wait | 1,633 | 910 | 723 | 0 |
| background Bash | 1,587 | 719 | 868 | 0 |
| job_output | 263 | 84 | 179 | 0 |
| kill_job | 80 | 36 | 44 | 0 |
| list_jobs | 17 | 14 | 3 | 0 |
| job_result | 14 | 4 | 10 | 0 |
| tool_output (artifact reads, separate from jobs) | 116 | 18 | 91 | 7 |

Background Bash was 1,587 of 63,148 Bash calls. Implementer spawning used background mode zero times in 573 calls; reviewer spawning used it 129 times in 568 calls. Generic spawning always backgrounds execution.

### Observed behavior

- `wait` passed one id in 1,457 calls, multiple ids in 164, and omitted ids in 12. Explicit `for` appeared 126 times: 51 any, 75 all. Preserve multi-job waits and the optional mode rather than declaring them unused.
- 321 waits returned still-running results (19.7%); 297 were repeat waits on the same running job. Explicit timeout values were common. Changing runtime caps is a separate policy decision, not part of this cleanup.
- For the post-September-10 cohort with delivery bookkeeping, all 813 running-job waits were by the owner. Of 273 waits on terminal jobs, 272 preceded completion delivery/claim. Thus `wait` is an acknowledgement/synchronization primitive, not merely report retrieval.
- `job_output`: 153 default calls, 87 tail requests, and 23 cursor/limit requests. 186 targeted running jobs; 77 targeted terminal jobs, 66 after a wait. 53 were repeated peeks on the same running job. Keep bounded, repeatable output retrieval; do not use peeks as scheduling polls.
- `job_result`: 13 terminal reads and one running-job error. Five reads followed a wait; four targeted already-claimed jobs. A separate final-report tool is unnecessary, but its non-consuming semantics should not simply be replaced by a claiming wait.
- `list_jobs` was rare, but ten of its 17 calls requested all owners. Retain inexpensive discovery and its explicit scope instead of deleting the most-used listing option.
- Reviewers/read-only generic agents already lack the background-job tool suite. There is no unused suite to remove from those roles.

### Implemented decision

Four job controls remain: `list_jobs` discovers; `job_output` peeks; `wait` waits and acknowledges completion; `kill_job` cancels. `job_result` is removed. With only a job id, `job_output` returns running output/activity or a terminal final report. Explicit cursor/limit or tail requests retain captured-output behavior. Peeks never claim completion; reports remain repeatable after automatic delivery or waiting. Byte-range artifact retrieval remains a separate `tool_output` capability.

No background lifetime, notification, replay, cancellation, or timeout policy was redesigned.

## Compaction and fresh contexts

- **Two coordinator `context_view_changed` events**, both valstore automatic `context_error_recovery` (September 14 and 21). **Zero explicit rollovers.** Coordinator rollover became available around mid-September; this is not a full three-month exposure window.
- Seven structured context-length errors, all in August: two coordinator and five implementer. This predates the rollover feature; successful later recovery need not emit the same terminal error.
- Subagent `rollover_reason`: 233 `coordinator_fresh` events (147 generic, 60 implementer, 26 reviewer), two `automatic_pressure` events, zero subagent `context_error_recovery` events.
- Fresh requests: 149/240 `send_to_agent`, 99/438 `send_to_implementer`, and 43/317 `re_review`. These are requested-mode counts, distinct from lifecycle rollover counts.

Coordinator-chosen fresh context is not necessarily a human choice; the analysis did not inspect user prose. Historical context/window ratios are advisory because configured model windows and older estimates can be inaccurate.

**Conclusion:** ordinary work does not rely on coordinator compaction. Keep existing reactive recovery and manual escape hatches, but do not invest in semantic/proactive compaction here. Favor smaller tasks, fresh per-task sessions, and bounded agent handoffs. No compaction runtime changes were made.

## Named-task startup

40 sessions had the old synthetic `list_backlog` + `get_task` preload; none called `list_backlog` themselves afterward. Among the other 947 sessions, 684 called it (746 calls, 645 within the first five coordinator turns). These are different task-selection cohorts, not a controlled comparison.

Named-task startup now preloads only the real `get_task` exchange, including a compact current status/dependency eligibility line. Independent review identified that dependency readiness would otherwise be lost when removing the full list; missing and unfinished dependencies remain explicit. The prompt advises listing the backlog only for selection or missing information. The existing synthetic-call flag and emitted tool names distinguish the new cohort, so future logs can show whether agents start manually listing after task-only preload. The historical cohort cannot answer that question, and no post-change live A/B was claimed.

## Tool descriptions and schemas

Production tools were inspected across direct/delegated work, chat, pm, implementer, reviewer, integration, and capture. No clearly unused schema fields were found. Useful distinct options (search scopes, safe-write policy/revision, batched questions, output ranges, review provenance) remain.

The main cuts remove duplicated workflow prose and repeated enum/range explanations from file/search tools, questions, memory, task creation, delegation/revision, review submission, and job controls. Write's full-source hash distinction, credential-value prohibition, scope/authority rules, review receipts, and concurrent-writer guidance remain.

Point-in-time scratch measurements of the working tree, before this follow-on versus after the cuts:

| Tool set | Approximate serialized bytes, before → after |
| --- | ---: |
| Direct work | 23,207 → 18,095 |
| Delegated work | 26,612 → 20,345 |
| Chat | 21,232 → 15,901 |
| PM | 20,893 → 16,051 |
| Implementer | 13,858 → 10,980 |
| Reviewer (no sandbox in measurement environment) | 7,120 → 5,626 |
| Integration | 13,260 → 10,321 |

These are intermediate working-tree snapshots, not an immutable production/provider A/B. A later eligibility-description adjustment changes several rows slightly. Registry measurements exclude dynamic model catalogs and use the fallback review-tier description; production configuration adds variable context. Serialized API definitions are not actual provider tokenization or billed input. The defensible conclusion is roughly one-fifth less tool-definition context, separately from the earlier role-prompt reductions.

## Lazy roles

Session assembly snapshots subagent metadata without constructing clients or resolving credentials. Factories construct backends only when invoked and surface construction failures through a non-nil erroring backend. Independent review with no available models fails explicitly rather than silently becoming author self-review; explicit self-review remains available. The coordinator's own backend still must be usable to start execution.
