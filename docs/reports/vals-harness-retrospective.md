# What vals development tells us about ycc

Investigation date: 2026-09-17. Investigation task: **0385**.

## Bottom line

Ycc has enabled substantial, difficult engineering on vals. The strongest evidence is not the volume of agent output: it is independently reviewed changes, concrete defects found in review, repeatable hardware experiments, and explicit rejection of attractive but unproven optimizations. The harness is especially useful when it preserves evidence and lets a coordinator bring different reviewers to a difficult problem.

The weak point is carrying that work efficiently and truthfully across boundaries: model calls, context windows, implementation/review rounds, background jobs, external experiments, and Git finalization. Agents repeatedly become workflow operators instead of engineers, while the human sometimes becomes the scheduler and anomaly detector. In particular, ycc is better at carefully executing a bounded engineering task than at autonomously managing a multi-day experiment against a coherent baseline. Some particularly painful examples are already fixed; several concrete current gaps remain.

My recommended direction is **less incidental orchestration, stronger evidence continuity**—not less review, weaker isolation, or an arbitrary cap on difficult investigations.

## 1. Evidence and limits

### Corpus

I inspected the local vals logs under `/home/why/code/vals/.ycc/sessions/`; historical records commonly identify the workspace as `valstore`. The directory was receiving new sessions during analysis. To make the census reproducible, all statistics below use events **strictly before 2026-09-17T00:00:00Z**, excluding empty logs. The retained event range is July 15–September 16.

| Measure | Fixed-cutoff observation |
|---|---:|
| Nonempty session logs | 210 |
| JSONL source bytes | 415,206,982 (~396 MiB) |
| Events / malformed JSON lines | 171,405 / 0 |
| Modes | 159 work, 50 chat, 1 pm |
| `model_turn` events | 49,179 |
| Synthetic preload turns / remaining turns | 354 / 48,825 |
| Tool calls / tool results | 51,186 / 51,184 |
| Tool results flagged `error` | 653 (1.28% of results) |
| Structured session-error events | 70, across 34 sessions |
| Review verdicts | 262 accept, 161 revise, 31 unknown |
| Structured commit events | 137, across 132 sessions |
| Sessions with at least one idle report | 206 |
| Tool-result text volume | 218,600,527 bytes |

All **137 distinct recorded commit SHAs resolve to commit objects in vals's local repository**. This corroborates delivery, not correctness or current branch membership. Chat sessions also commit through Bash; `commit_made` is not a complete delivery ledger.

Reproduce the census without a provider call:

```sh
python3 scripts/vals-session-census.py /home/why/code/vals/.ycc/sessions \
  --before 2026-09-17T00:00:00Z --summary
```

Omit `--summary` for per-log counts and hashes. Use complete/quiescent log snapshots; if a live append leaves an incomplete record, the cutoff census deliberately fails with a retry diagnostic rather than silently discarding it. Timestamp offsets are normalized for comparisons. The aggregate SHA-256 over sorted `session-id:filtered-content-sha256` records is:

```text
0d09077866733372b9cf75c4cd9067ba0e78dbcc7d8bda6e7d5927d97c5dd1d6
```

### Method

The investigation combined a full event census with three parallel read-only investigations: user interventions and long conversations; delegation/review/work-loop outcomes; and tool/provider reliability. I separately checked aggregates, Git objects, representative successful and difficult trajectories, and current source. The selected deep dives are deliberately stratified around failure modes and successful counterexamples, **not a statistically random outcome sample**.

References below are **session ID, event sequence**. Locate them in `<vals>/.ycc/sessions/<id>/events.jsonl`; sequence is the durable identifier, not an assumed physical line number. Raw logs remain local: this report does not embed entire transcripts, tool captures, credentials, or opaque provider state.

Important limits:

- A `session_error` can be a recoverable subagent error. An idle report can be a progress update or an honest incomplete outcome. Neither is a session success/failure label.
- Shell test failures often have `tool_result.error=false`; the 1.28% figure is a tool-protocol/error-flag rate, not command reliability.
- Session spans include human waits, overnight gaps, and reopenings. Work-session median span is 52.8 minutes, p90 3.72 hours; these are **not active compute times**.
- Review rounds and model choice are confounded with task difficulty, changing prompts, and changing harness versions. This is not a model leaderboard or causal A/B test.
- Recorded context sizes are estimates. Live retry/backoff events are not durably retained, so the logs cannot reconstruct all retry costs.
- This is a harness retrospective, not an independent audit establishing vals's production safety or validating every benchmark claim.

## 2. What worked—and should be preserved

### Independent review finds real bugs

Task 0292 is a strong example. An implementer prematurely committed a candidate. In the next session, reviewers found a placement-transition race that could escape mirroring, and fanout of rejected writes. Corrections added targeted regressions; both reviewers subsequently accepted.

Evidence: `s_0dda3cb359b3797e` #906 (September 11 handoff); `s_23ae3e3df5110f71` #250/#256 (findings), #1035/#1078 (acceptance).

Across the corpus, 62 sessions contain a `revise`; 52 of those also contain a commit event. Revision and recorded delivery frequently coexist; these counts alone do not establish that every finding was resolved before commit. The ordered examples above are stronger evidence of convergence. At the smaller end, task 0261 obtained one focused acceptance and committed a bounded estimator correction (`s_c26a24042a1601f4` #345/#384, September 8).

Task 0236 is another useful positive control: collision-resistant writer identities and shared HLC state were propagated through replication/storage/hints/anti-entropy; two reviewers accepted, then another revision caught wire-budget and handshake-readiness details. The final report preserved the protocol migration caveat and separated hardware rollout from software completion (`s_015f7a7a78891afa` #992/#1005, #1285/#1355, #1405/#1410, September 7).

**Preserve:** independent scrutiny, concrete regression tests, explicit deployment constraints, and the ability to revise after a superficially successful implementation.

### The system can retain negative findings instead of manufacturing success

Task 0208 needed seven implementation rounds and four numbered review rounds. That looks expensive until one reads the missing gates: per-owner progress, flush/merge versus actual compaction, bounded qpair ownership, and hardware restoration. The final evidence supported the eight-shard recommendation only after the missing compaction arm was added (`s_9784754c841e67fb` #829/#835, #1710/#1719, #2285/#2291, #2326/#2331, August 15).

Conversely, task 0186 kept its write-combiner feature default-off when hardware tails regressed despite substantial development effort (`s_8c5372d4370f4958` #3558, August 12).

The long September ingestion investigation also distinguishes source-event admission, acknowledged logical writes, and physical replication work, and eventually rejects an incomplete comparison after a crash (`s_3770be49aaf4e538` #866, #5260, #16598). These distinctions matter more than optimistic task closure.

**Preserve:** honest uncertainty and negative experiments. Missing commits or unchanged task status can coexist with real progress.

## 3. Current high-confidence opportunities

### A. The Codex adapter defeats the batching advice

After excluding synthetic preloads:

| Model alias | Tool-using turns | Exactly one tool call |
|---|---:|---:|
| astra | 5,519 | 5,519 (100%) |
| sol | 33,599 | 33,599 (100%) |
| claude | 6,947 | 5,774 (83.1%) |

This is not sufficient evidence of a model preference. **`internal/codex/codex.go:buildRequest` explicitly sets `ParallelToolCalls: false`.** The setting dates back to the adapter's introduction, while prompts encourage independent calls in one turn. The engine already handles multi-call turns and dispatches their tools sequentially (`internal/engine/loop.go`, the `for ci, call := range msg.ToolCalls` dispatch loop).

The immediate opportunity is enabling *generation of a batch*, not concurrently mutating the workspace. The corpus contains 39,118 astra/sol tool-using round trips with one call each; it does **not** establish how many were safely batchable or what the latency savings would be.

**Proposal 0386:** bounded backend-compatibility and behavioral A/B evaluation. Cover independent reads, dependent edits, stopping mid-batch, interruption/reopen, and provider replay ordering. Compare correctness before time/tokens. Do not merely strengthen the prompt or flip a production switch without testing.

### B. “READY” means different things to the agent and scheduler

One September 5 coordinator reported that all seven tasks labeled ready were actually blocked and correctly did nothing (`s_e2479ddba5f75449` #12).

The contradiction still exists: `internal/orchestrator/orchestrator.go`'s `list_backlog` annotates dependency-clear blocked tasks `[READY]` and includes them under “Ready to start,” while `internal/session/workloop.go:topReadyTask` excludes blocked tasks and accepts todo/in-progress work.

**Proposal 0387:** share eligibility semantics. “Dependencies satisfied” is useful information but must not imply “actionable now.” Keep blockers and explicit authorization gates intact. This is a small, high-confidence usability/correctness fix, not a reason to auto-unblock work.

Related preflight examples were legitimate refusals: task 0182 lacked a required fleet reservation (`s_16001646893b9a30` #65; `s_4e7264f4379bf626` #32, August 12), and task 0258 lacked an approved archive destination/deletion authorization (`s_6d9bb1b94f3bf338` #45, September 5). Existing proposal **0324** can make these prerequisites visible before expensive delegation.

### C. Review isolation works, but independent build execution often does not

Fifty review summaries mention cross-device errors; 62 mention read-only restrictions. These are overlapping text-match sets, not 112 independent failures. The qualitative pattern is clear: reviewers try Cargo, hit artifact-write restrictions, and either rely on the implementer's report or inspect prebuilt binaries.

Examples: `s_c26a24042a1601f4` #345 (September 8), `s_23ae3e3df5110f71` #1035 (September 11). A useful workaround is visible at `s_74bcfc320e3f8430` #841: the reviewer runs available test binaries despite being unable to build.

When an OS sandbox is available, current `internal/sandbox/sandbox.go` deliberately protects the workspace with Landlock or a read-only bind; the unsupported-platform fallback is prompt-only enforcement. Scratch writes are already possible; the deficiency is a reliable, discoverable build workflow with source identity—not a complete inability to run tests in isolation.

**Proposal 0388:** supply bounded scratch build/target directories, starting with Rust, and an isolated snapshot-copy fallback where necessary. Associate verification with the reviewed source snapshot. Report three distinct states: independently rebuilt/executed; inspected existing evidence; unavailable. Keep source, Git index, and unrelated work protected.

### D. Verification needs a concise evidence contract

Task 0186 repeatedly tested the wrong execution topology. A nominal 30-second hardware arm hung for over 58 minutes; another produced zero server writes because the detached executor was not driven. The coordinator eventually insisted on reproducing the real reactor/TCP arrangement before another rewrite (`s_8c5372d4370f4958` #2183, #2341, #2428/#2518). The session contains 1,060 model-turn events and 16 review events, but not all that effort is harness waste: later review found real lost-wake and timer-error defects.

Task 0197 illustrates a different ambiguity. A prototype regressed and production code was reverted; reviewers then debated whether an evidence-only negative result completed an implementation task. One review also caught a substantive inserts-versus-overwrites confound. The user ultimately authorized the negative-result disposition (`s_d8b8a396ebb85a0c` #2160, #2184, #2229/#2242, #2254/#2255, #2458/#2629). About 6h45m of this session's 10.32-hour span was waiting for that answer.

**Recommendation:** evolve existing **0324** preflight, **0320** analytics, and the useful ledger portion of **0322**, rather than creating another blanket process:

- Specify allowed outcomes before expensive experiments: ship, reject candidate with retained evidence, or leave adoption pending.
- Separate code correctness, verification completeness, experiment/adoption decision, and authorized task completion.
- Use a compact verification receipt: reviewed snapshot, command/test identity, test count/result, retained evidence reference, and which failure the test actually exercises.
- When the same failure recurs, suggest a reproducer-first pivot; do not call another full review solely to rediscover an already-known missing hardware gate.
- Never let a model quietly redefine acceptance criteria to escape a difficult task.

This is not a recommendation for an elaborate dossier on every small change.

## 4. Long-session behavior: continuity is more valuable than a larger transcript

The September 11–16 conversation `s_3770be49aaf4e538` contains 16,975 events, 5,210 model turns, 100 generic-agent invocations, and 24.3 MB of tool-result text. Its 132-hour span includes substantial idle gaps. The coordinator's last recorded pre-rollover model-turn estimate approaches 924k tokens; #15392 records a context-error recovery with an old selected-view estimate of 924,185.

This is an important workload, not an outlier to dismiss. The user authorized a sustained performance investigation, then repeatedly asked for check-ins. Fresh subagent handoffs kept specialist work manageable, but the parent still accumulated the whole investigation. A larger context limit alone does not solve continuity, relevance, status, or retained experiment identity.

Current ycc already has significant remedies: generic-agent fresh handoffs (**0359**), coordinator explicit/error-recovery rollover (**0357**, `internal/session/context_rollover.go`), provider-shaped context accounting (**0356**), and completion-driven idle-coordinator wakeup (**0379**). The log itself contains successful rollover and later `awaiting_jobs` progress reports. These should be credited, not proposed again as absent features.

**Remaining direction:** evaluate an opt-in proactive coordinator rollover policy at complete-turn boundaries, preserving verbatim user authority and explicit unresolved work, rather than waiting for an overflow. A compact current-work record should retain:

- authorized goal and stopping condition;
- active hypothesis and rejected alternatives;
- current source/build/experiment identities;
- accepted findings and unresolved criteria;
- live job/resource ownership and next action.

The full transcript remains the audit trail, not the mandatory working set. Any automatic rollover must fail closed on authority loss and prove restart/steering correctness; a hard context threshold alone is not a complete design. Custom model definitions also need a known context window: #15393 records `context_window=0`, so model-window-driven pressure handling cannot rely on a configured limit in that case.

There is a smaller, directly traceable current bug: #15392 reports **`new_context_tokens_est=0`** for an approximately 18.7 KB summary; the next successful coordinator turn (#15393) estimates 14,099 tokens. `ContextTokensEstimateForHistory` applies the previous history's measured-input anchor to a discontinuous replacement (`internal/engine/loop.go:538–566`). Its subtractive correction can go negative and clamp to zero. `SetHistory` resets the anchor afterward, explaining why the actual next request has a plausible size. **Proposal 0392** corrects prospective replacement accounting and tests the strictly-smaller check with comparable estimates; it does not add automatic policy or change context limits.

## 5. Where the human became the scheduler, anomaly detector, and source of truth

### Completed work did not always wake its parent

The clearest user-facing historical defect was not slow inference: finished work could sit until the user typed again. In `s_3770be49aaf4e538`, reviewer jobs finished at #3744/#3807 on September 11 but their completion notices were delivered at #3809/#3810 the next afternoon, immediately after the user's #3808 check-in. One report waited about 16h45m.

The full-corpus completion-to-automatic-notification sample has 167 pairs: median about nine seconds, p90 about 90 minutes, and 22 over 30 minutes. This is only the automatically notified subset; explicitly waited/claimed jobs are separate. It is not all avoidable compute time, but it establishes severe continuity failures when continued work was intended.

**0379 fixes this mechanism in current source.** Similar delays later in that long-lived session do not prove the fix failed: ordinary session-start events do not identify the actual running build. Verify deployment and keep this real trajectory as a regression case rather than reimplementing the feature.

### “The process is alive” is not a useful experiment health criterion

In `s_0dd031d407fe4524` (September 4), the user asked for hourly monitoring (#948). A watcher hit the then-relevant timeout validation (#954), then died immediately because a POSIX shell could not run its Bash arrays (#959). The user reported the failed job about 90 minutes later (#962). The model's response incorrectly claimed no monitoring time had been lost (#964).

More revealingly, the next report called **+70k source events** healthy (#967). The user asked whether that was slow (#976). Only then did the agent normalize the interval to about **13 events/s**, inspect dropped batches, and diagnose a rehome abort/retry livelock (#977/#982/#990). This does not prove the user was the only possible route to diagnosis; it shows a concrete case where reassurance preceded interpretation of the measurements.

The five-day September performance conversation shows the larger version of the same risk: careful local percentage gains preceded reconciliation of the much larger gap to historical performance (#16953/#16965/#16975). The workloads and hardware differ, so million-write microbenchmarks are not a valid direct baseline. Establishing a comparable baseline should be an early deliverable, not something the user must request after days of tuning.

**Proposal 0391:** bounded run supervision and health receipts. Track source/build, workload, acknowledgements/durability, elapsed time, expected baseline, drop/error counts, progress and monitoring failures. A completed local job is not the same thing as a healthy external experiment. Current background timeout semantics are already improved; do not infer that all present watchers must have a one-hour lifetime.

### Ask fewer repeated questions by making authority explicit—not by assuming permission

The 27 structured question/answer pairs account for about **32.5 aggregate hours awaiting answers**, with a median around nine minutes. This sums waits across sessions, not unique wall-clock time. Some were essential decisions; task 0197's negative-result closure is one example. Several repeatedly requested permission to destroy the test fleet's retained data.

A prior permissive answer is **not** standing authorization to wipe a later corpus. If the user wants recurring autonomy, offer an explicit resource-scoped, expiring/revocable test-data authorization and retain its provenance. Separate safe preparation from gated actions where feasible. Do not solve delayed answers by silently choosing destructive defaults or putting inferred permissions into advisory memory. Related scopes are **0256**, **0324**, and proposed **0391**.

### Memory can turn observations into invented policy

At `s_b44e2a8799400f69` #694 (August 29), the user explicitly says they never imposed a +/-13% benchmark-variance rule. The agent acknowledges the mistake (#696) and repairs memory to say that variance was measured once, not a user preference (#702).

**Proposal 0390:** distinguish user-stated guidance, measured observation, model inference, and proposed policy, with source event/date/scope and correction semantics. The failure is authority laundering through summarization, not simply a missing memory entry. Carrying more memory without provenance can make future sessions worse.

### Ordinary conversation must not become the secrets store

Credentials were supplied through an ordinary question answer and a session prompt (`s_d1a15d39d944c779` #63; `s_84d3c8c0940ee9e6` #2, August 22). A later answer asked the agent to recover the previously pasted credential from session history (`s_66adfde7ea26f5bb` #61). No values are reproduced here; current validity was not tested.

Owner-only logs are useful protection, but the plaintext still becomes part of retained history and potential exports/model replay. **Proposal 0389 (P1):** dedicated secret entry and named references, with raw values excluded from ordinary events and model-visible answers. Do not promise that regex redaction solves arbitrary tool-output leakage. Existing exposed credentials should be treated as candidates for rotation through their normal provider, and any historical-log cleanup must be an explicit user decision because rewriting the audit/replay source has consequences.

## 6. Git and finalization: preserve safety, reduce recovery archaeology

Several difficult sessions finished the engineering but struggled to reconcile task status, candidate commits, and the session's ownership baseline.

For task 0292, an implementer committed before independent review, moving HEAD and invalidating attribution (`s_0dda3cb359b3797e` #856/#874/#893). In the follow-up, updating the already-dirty task document itself invalidated attribution; accepted, committed code still did not imply successful administrative finalization (`s_23ae3e3df5110f71` #647/#658, #1101/#1114/#1118). Another reopened session lacked a persisted baseline (`s_9c0c11e8b18a23f3` #810/#827, September 10).

The guards were not pointless: they protected unrelated work and forced scrutiny of a genuinely defective candidate. Removing them would make the metric look better by removing safety.

Current remedies include explicit scoped Git changesets/persisted baselines (**0353**, done), active-task bookkeeping adoption (**0383**, done), and idempotent finalization work (**0355**, in_review). See `plans/explicit-git-changesets.md`.

**Next steps:** verify the remaining divergent-HEAD and interrupted-finalization paths with realistic fixtures; make delegated commit/task-finalization authority explicit; support snapshot-bound review/recovery of an already-created candidate. Display **code accepted**, **candidate committed**, and **task finalized** separately. Do not silently recapture ownership or turn “git add everything” into a recovery mechanism.

## 7. Historical failures versus today's harness

| Historical observation | Current interpretation |
|---|---|
| 41 of 70 structured session errors were `max_output_tokens` HTTP 400s, August 9–10 | Adapter now deliberately omits the unsupported parameter and has a wire-level test. Historical capability failure, not proof current Codex is unreliable. |
| 22 of 31 unknown review verdicts came from that parameter error; eight from rate limits | Unknown execution is not substantive rejection. Keep review execution status separate from verdict. |
| Repeated identical capability failure despite tier escalation (`s_2d497b782356c8d1` #176/#192) | Preflight and deterministic-failure suppression are better than blind retries; existing provider work **0363/0364** is relevant. |
| Retained implementer/reviewer context overflow | **0321** provides automatic subagent rollover; fresh handoffs and coordinator rollover also exist now. Verify recovery quality rather than treating this as untouched. |
| Work loop stopped because task metadata did not change after useful failed hardware evidence | **0344** fixed metadata-only progress stopping. Evidence-aware continuation is now explicit. |
| Parent stopped after a progress response while a child remained live | **0379** added completion-driven wakeup and `awaiting_jobs`; keep a long-running regression fixture. |
| Full-tree staging, missing reopen baseline, dirty task-log attribution | **0353/0383** address these; **0355** recovery still deserves validation. |
| Large full transcript transfer/rendering | **0382/0384** introduce indexed bounded views and faster history summaries. This log corpus cannot prove on-device responsiveness. |

The 70 error events occurred in 34 sessions, not 70 failed sessions. The dominant historical error was a narrow two-day incompatibility, so an undifferentiated “33% failure rate” would be false.

One current classification gap remains: all eight `unknown` errors in this cohort are Codex in-stream `cyber_policy` rejections, including September 15's `s_3770be49aaf4e538` #16656. `internal/engine/apierror.go` recognizes stream rate-limit/overload/server codes but not that policy code. Classify and display it as a non-retryable provider-policy refusal, retain the explanation and unfinished-work handoff, and preserve the refusal stop boundary. This belongs alongside typed-provider-error work **0364**. Do **not** implement blind retries, automatic cross-provider routing to evade a refusal, or prompt obfuscation; any continuation must resolve scope/authorization appropriately and respect applicable safeguards.

Task 0233 shows why unchanged status cannot mean no progress: two failed hardware gates yielded evidence, then a later session diagnosed serial durable mirror fanout and created an accepted prerequisite instead of blindly repeating (`s_38910dab7b63bd93` #575; `s_b92adfd52ef2590d` #546; `s_59fed849b01f78f3` #314, September 6–7). Preserve this behavior. **0366** explicitly rejects reinstating an arbitrary attempt-count exit without policy approval.

## 8. Measurement improvements

The event log made this retrospective possible, but answering basic questions still required custom parsing and trajectory reconstruction. Existing proposal **0320** is well supported.

Useful additions:

1. Record harness build/config/prompt/tool fingerprints in ordinary session starts, not only evaluation runs. Without them, before/after analysis is confounded.
2. Distinguish model requests from synthetic preload turns; review execution from substantive verdict; shell exit/timeout from tool-dispatch error; and progress idle from final outcome.
3. Give findings and experiments stable identities across rounds/sessions. Track whether a finding was fixed, disproved, accepted as a limitation, or still unresolved.
4. Link verification, reviewed changeset, candidate commit, task finalization and shell-based commits where possible, without pretending Git activity proves success.
5. Report actual current request size, cumulative token classes, actor/round/context mode, result volume, and active/wait time separately.
6. Keep retry UI hints transient, but attach sanitized attempt counts, retry categories and cumulative backoff to the eventual durable model turn. Successful retries currently disappear from the retrospective, preventing a retry-effectiveness calculation.

The raw provider `usage.total` is not a comparable cross-provider denominator. Here the recorded disjoint classes sum to approximately **5.505 billion token-visits**: 586.3M fresh input, 4,838.2M cache-read, 60.7M cache-write, and 20.1M output. Summing raw `total` yields 4.423B because Anthropic totals omit cache classes. These are repeated request accounting, **not unique text, context size, a bill, or a claim of wasted tokens**. Failed attempts may also lack usage. Current `internal/engine/loop.go` normalizes fresh input separately; any analytics should preserve those semantics.

Tool ergonomics matter, but should be prioritized by recovered effort rather than error count alone: 347/10,783 Edit results failed (3.2%), 97/12,853 Reads, 70/1,359 Searches, and 18/155 commits. Twelve commit errors were a 1,000-character outcome limit; many Edit errors were ambiguous matches or identical old/new strings. Safer patching (**0325**) and clear schema feedback are sensible, but an occasional exact-match refusal is often the guard doing its job. Do not replace safe failures with fuzzy, potentially wrong edits to improve a dashboard.

## 9. Recommended order and evaluation gates

### Immediate security hygiene

- **0389 — secret entry/reference workflow (P1).** Stop encouraging secrets in ordinary conversation. Separately review whether previously pasted credentials need rotation; this investigation did not rotate or validate them.

### Concrete, narrow harness gaps

1. **0386 — evaluate Codex multi-tool turns.** Potentially broad latency/context benefit; require live compatibility and safety evidence before changing defaults.
2. **0387 — unify readiness semantics.** Small deterministic fix with direct user-visible evidence.
3. **0392 — correct replacement-context estimates.** Small, source-traceable bug with live evidence.
4. **0388 — sandbox-compatible reviewer verification.** Improves both review credibility and efficiency without weakening isolation.

### Larger outcome/continuity improvements

5. **0391 — bounded experiment supervision and normalized health reports.** Stop depending on the user to discover dead watchers, stalled ingestion, or meaningless benchmark comparisons.
6. **0390 — memory provenance.** Keep an agent's measurement or inference from becoming invented user policy.
7. Validate existing **0355/0357/0379** together: accepted code, interrupted commit, context rollover, a live background child, user steering, and daemon reopen. Component unit tests alone do not establish the full recovery path.
8. Use **0324/0320/0366** to make experiment preconditions, evidence-aware continuation, resource envelopes, and outcomes explicit. Reuse the finding-ledger idea from **0322**, not its arbitrary circuit-breaker policy. Include typed non-retryable policy failures in **0364**.
9. Evaluate proactive parent working-set compaction and concise progress state on a long-session fixture. Treat policy and authority preservation as part of acceptance, not an afterthought.

The seven new tasks **0386–0392** are filed as **proposed**, not accepted implementation work. Existing tasks have not been silently promoted or their policies changed.

### Measure whether the changes actually help

Use repeated, matched scenarios built from these failure patterns, not raw historical model comparisons:

- independent reads with dependent edits and a mid-batch stop;
- a small Rust change whose reviewer rebuilds in the real sandbox;
- a legitimate blocked hardware gate versus actionable in-progress diagnosis;
- a negative experiment with a predetermined acceptance contract;
- an accepted candidate interrupted between commit and finalization;
- a long coordinator with background work crossing rollover/reopen and user correction.

Primary measures: correct outcome, no unauthorized mutation, preserved authority, source-bound verification, and successful recovery. Secondary measures: API round trips, token classes, time to useful result, repeated unchanged failures, and required human rescue. Avoid optimizing for fewer reviews, more commits, or fewer tool-error flags in isolation.

## Conclusion

Vals is evidence that ycc's basic architecture is useful: durable project state, inspectable event history, delegation, independent review, and hardware-backed verification can support serious work. The next gains should come from making those pieces cooperate with less manual bookkeeping. In particular, remove the adapter's unexplained single-call constraint if evaluation supports it, make readiness truthful, give reviewers a working isolated build path, and carry compact evidence across session/agent/Git boundaries.

No product behavior was changed during this investigation. Existing unrelated working-tree changes were left alone; vals source and session logs were inspected read-only.
