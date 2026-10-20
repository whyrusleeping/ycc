# Wheeler task granularity: 2026-10-17

## Scope and conclusion

Read-only investigation of Wheeler's backlog, git history, and session logs for
2026-10-16 22:33 through 2026-10-17 22:33 UTC, plus ycc's current prompts and work-loop
implementation. Freeze the quantitative sample at the first 49 completed sessions of
`loop_bc5e02f2`; its 50th session was still running. All 50 sessions started in this
window were direct-work Astra coordinators. This is not a comparison with another
coordinator model, a correctness audit of Wheeler, or a measured prompt experiment.

There is substantial recursive decomposition, but not predominantly trivial code
changes. The concern is **task/session granularity rather than lack of implementation
progress**: one narrow prerequisite repeatedly becomes a new task, independent review,
commit, and fresh session, while the coherent integration outcome remains unfinished.
Start with explicit task-sizing guidance, not a task-count cap or weaker safety gates.

## Observations

Source: `/home/why/code/wheeler/.ycc/workloop.json`, each referenced session's
`events.jsonl`, `backlog/0047*`, `backlog/0068*`, and git history through the cutoff.

| Measure | Observed |
| --- | --- |
| Completed loop sessions | 49, each with one commit and one initial reviewer spawn |
| Total recorded session duration | 16 h 24 min; median 18.8 min |
| Tasks created by those sessions | 50: 0048–0097 |
| Clustering work, 0069–0097 | 29 completed tasks/sessions over 9 h 42 min |
| Clustering reviews | 17 standard, 12 comprehensive initial spawns; 12 re-review calls; 62 individual review submissions |
| Clustering bookkeeping calls | 29 task creates, 49 propose_plan, 109 get_task, 29 list_backlog |
| Budgets/context rollover | Captured session/loop caps all unbounded; no budget or context-view-change events in the completed sample |

Task 0098 had already been created by the running session. Thus 0068 had spawned
30 child/prerequisite tasks, with 29 completed, yet was still in progress. Parentage is
established from task descriptions/session reports, not a structured parent field.

The initial split was justified and explicitly requested: 0047 says "to be split before
work starts" and covers the interner, L1, L2, live ingestion, derived stages, full rebuild,
and cutover. Its first six children are sensible subsystem/delivery boundaries.

The later clustering sequence is more questionable as task packaging:

- 0073: bounded canonical seeding; 0089: make that seeding cooperative.
- 0080: finite receipt/seed/seal barrier; 0092: make that barrier cooperative;
  0093: integrate it with pending handoff/live priority.
- 0084: first successor handoff; 0094: successor sealing; 0095: completed replay
  memory release; 0098: first completed-prefix input retirement, still in progress
  at the cutoff. Repeated rotation remains unfinished.
- 0096: simultaneous states/split capacity; 0097: merge/ancestry capacity. These
  are related measurements that could plausibly share a capacity-validation task.

Those are candidates for coherent outcome-based grouping, not proof that all should
have been one commit. Some requirements were discovered during implementation, and
isolated durability changes can deserve their own review.

Three parent-closure sessions (0055, 0056, 0052) consumed 24 min 22 s and three independent
reviews after child completion. Their commits changed only backlog/design documents;
reports include rerunning integration suites and reconciling evidence. This is observable
closure overhead, not a claim that all 24 minutes could safely be eliminated.

The work is substantial. Across all 51 commits in the window (49 loop commits plus two
earlier backlog/design commits), median added+deleted lines were 642. A coarse path-based
classification finds about 11,403 implementation lines added, 14,737 test lines, 5,547
Markdown documentation lines, 1,458 probe lines, and 1,372 bookkeeping lines. These are
change-volume indicators, not quality or productivity scores.

There are concrete discoveries worth preserving: 0091 improved measured retained replay
from roughly 83 to 1,818 historical items/s; 0086 found an actual librdkafka handoff race;
0097 measured a non-preemptible merge pause of about 674 s CPU / 42.6 s GPU. A blanket
"stop hardening and ship" instruction would be inappropriate.

## Session evidence

Selected references (paths relative to Wheeler `.ycc/sessions`):

- `s_61891bdc76990526/events.jsonl`, event 57: selects 0068, then says it will split
  allocation limits and "complete that slice in this run"; events 62/67 create separate
  parent/child plans. This starts the repeated clustering pattern.
- `s_2010d6f23e9a6c96/events.jsonl`, event 46: splits cooperative seeding, expressly
  excluding live-priority integration. Event 222 reports the complete 240-test real-service
  suite taking 667.5 s. Whole session: 833 s. This shows the fixed verification cost a
  small primitive can carry; it does not imply those tests were unnecessary.
- `s_f6786d9dbba770a7/events.jsonl`, events 47/211: finishes cooperative finite-barrier
  units while recording pending handoff as the next integration step.
- `s_ff859ea9ad437697/events.jsonl`, events 23/61: initially announces rotation/retirement
  work, then chooses the separately excluded merge-capacity measurement. Useful evidence,
  but another session leaves the same connected outcome open.

Splits are not forced by current context exhaustion. Among clustering sessions that
create a task, the median measured coordinator input at the first creation is about
59k tokens, maximum 87k, against a recorded 400k window. No hard-budget/rollover event
appears. Keeping context manageable is still a legitimate benefit of decomposition.

Do not treat aggregate work-loop token counts as unique new context or dollar spend:
usage contains repeated/cached input, and these models are unpriced in the loop snapshot.

## Harness explanation

### Task, commit, and session boundaries are coupled

`internal/orchestrator/prompts.go:65,72` says "ONE accepted backlog task";
`:91–93` says to commit once criteria are met, then finish, with commit owning the done
transition. `internal/orchestrator/finalization.go:135–147` actually applies completion
as part of finalization. A partial checkpoint of an unfinished parent is not the normal
commit-tool workflow.

Consequently, when Astra wants a small reviewed checkpoint, creating a task around that
checkpoint gives it a natural completion/exit path. This is a plausible structural
incentive, not evidence of deliberate metric gaming.

### Splitting has permission but no sizing criterion

`prompts.go:99–100`: "Split necessary accepted scope into todo tasks; adjacent speculative
work stays proposed." This should primarily distinguish accepted work from new scope,
but reads as an unconditional splitting instruction. Neither it nor `create_task`
(`internal/orchestrator/modes.go:185–211`) distinguishes an implementation step from an
independently useful deliverable. PM guidance also lacks such a distinction.

The "smallest change" guidance limits unnecessary scope; it does not tell the agent how
to choose a complete task boundary. A coherent outcome may require several small edits.
Wheeler has no CONTRIBUTING.md supplying an additional sizing rule. The ycc memory about
preferring smaller agents/tasks is not Wheeler memory and should not be blamed here.

### Local scope review does not evaluate global convergence

Reviewers correctly review the assigned child's criteria (`prompts.go:116–130`). A child
can pass while the parent's useful integration remains absent. Fresh sessions receive the
preceding report and reread the parent, so this is not simply lost context: the reports
repeatedly acknowledge the missing outcome. What is absent is an explicit decision about
whether another narrow child is the best route to finishing it.

### The scheduler tolerates expansion but does not cause it

`internal/session/workloop.go:501–503` treats either a commit or any backlog change as
progress, resetting stall backoff at `:536–539`. It cannot distinguish convergence from
an expanding prerequisite chain. However, all 49 sampled sessions committed; changing
creation-only progress detection would not address this incident. Nor does it establish
that Astra was responding to a hidden progress metric.

An independent read-only harness review identified the same ambiguous split instruction
and missing sizing guidance, but no explicit current instruction to manufacture tiny tasks.
Current prompt source was last changed October 10. Exact historical system prompts were
not available in the inspected session records, so current-source causality remains an
inference; no controlled before/after evaluation was performed.

## Recommended changes, in order

1. **Clarify task granularity in the shared work/PM guidance.** Default to a coherent,
   independently verifiable outcome. Implementation steps belong in the current task's
   plan. Split for a meaningful deliverable, distinct dependency/release boundary, risk
   isolation, or evidenced context/resource need—not merely to give each step its own
   review/commit. Preserve original parent acceptance and accepted/proposed semantics.
   The tool description can briefly reinforce this rather than duplicating a long policy.
2. **Before recursively splitting again, compare against the parent outcome.** State what
   this slice enables and why it needs its own task rather than being part of the next
   connected milestone. Repeatedly incomplete integration should prompt a concise
   reassessment of the approach, not automatic refusal, arbitrary task limits, or another
   mandatory reviewer. For Wheeler, the useful unit is a recoverable rolling catchup path,
   not each helper API in isolation.
3. **Reuse valid completion evidence.** Parent closure should inspect the acceptance map
   and existing evidence, with fresh integration testing only for an unverified seam,
   changed code/environment, or another concrete regression risk. Do not mechanically
   repeat child reviews/suites; equally, do not auto-close a parent from child statuses.

First evaluate a small prompt change on subsequent comparable work: fewer step-only
children and closure-only sessions, more completed connected outcomes, and no increase
in escaped defects or lost acceptance criteria. Raw task completion count is not the target.

If the behavior persists, consider separating a reviewed checkpoint commit from task
completion. That would remove a deeper incentive to invent child tasks for intermediate
commits, but changes finalization/recovery semantics and is not a prompt-only tweak.
Likewise structured parent lineage and milestone-level digest warnings could improve
visibility, but are larger follow-ups, not prerequisites for trying clearer guidance.

Do not start with fewer safety tests, disabling review, hard minimum task sizes, a maximum
number of children, automatic parent closure, or forcing all work into one giant session.
No harness behavior or Wheeler state was changed by this investigation.
