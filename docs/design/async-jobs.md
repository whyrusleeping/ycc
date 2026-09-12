# Design: asynchronous jobs

> Status: accepted and implemented.

## Context

Shell commands and subagents sometimes have useful work to overlap. Making each kind of
background activity a separate mechanism would give the coordinator several incompatible ways to
wait, inspect, cancel, and receive completion. It would also make replay ordering difficult.

## Decision

A session owns one job registry for background shell commands and child agents. Jobs share a stable,
monotonic id namespace, lifecycle, discovery, progress read, bounded wait, cancellation, and retained
final-result contract. Foreground execution remains the right choice when the result gates the next
step; background execution exists to overlap meaningful independent work or leave a watcher running.

`list_jobs` exposes jobs in deterministic start order with owner, state, elapsed time, and mutation
metadata. Its default view is scoped to the calling actor; a session-wide view is explicit. Agent jobs
also expose bounded last-activity, current-tool, turn-count, and token-usage summaries. These summaries
contain no prompts, tool arguments/results, model text, or private reasoning.

Completion notification and evidence access are separate:

- an automatic final notification is claimed exactly once at an engine checkpoint;
- an explicit `wait` synchronizes with completion, returns the retained report, and suppresses a later
  automatic notification;
- `job_result` retrieves that retained final report repeatably, whether notification happened via a
  checkpoint or `wait`;
- `job_output` is a non-consuming, repeatable progress/evidence view and changes neither notification
  nor result state.

A completion found between results from one model tool-call batch is deferred until the complete batch
has entered history, preserving provider requirements that tool results immediately follow their calls.

Automatic delivery is also gated on execution, not just on the report. A tracked process or subagent
publishes its terminal report while it still holds its mutation/lifetime leases, so its completion
signal and its claimable notification both become available at the separate boundary where execution
actually stops. A wake therefore never starts a coordinator turn against work that is still releasing
ownership, in either order of the two boundaries; an explicit `wait` keeps its existing contract and
synchronizes with the terminal report itself.

Checkpoint delivery only reaches a coordinator that is still running. A coordinator that already sent
its final response while jobs remain live is therefore woken by the completion itself: the registry
publishes a coalescing terminal-transition signal that the session's single run owner selects on while
idle. Because the wake, user input, steer corrections, and context rollover are all consumed by that
one owner, the woken turn cannot run in parallel with another model invocation, and the signal is
buffered, so a job finishing exactly while the parent transitions to idle is still observed. An idle
user message accepted just before the wake is absorbed into the same continuation ahead of the
notification it durably precedes, so live and replayed history agree. Waking
claims whatever is deliverable at that moment through the same exactly-once path as a checkpoint —
suppressed by an earlier explicit wait, recorded as durable `job_notified` synthetic context rather
than user intent, and coalesced so several completions enter history before one turn starts. States
that represent an explicit human boundary — pause, stop, refusal, session error, a blocked report, a
reopened session awaiting its first input, and an in-flight rollover — are never resumed this way;
their reports fall back to checkpoint delivery. Prompt and tool guidance correspondingly tells a
coordinator with no independent work left to report progress and wait rather than end the turn.

Terminal-state observers use the same eligibility rather than lifecycle status alone. Status and the
wake decision are recorded together, and a claimed-but-not-yet-running continuation stays visible, so
the unattended work loop and the idle reaper cannot mistake a coordinator that is waiting for
delegated work — or resuming on it — for a finished session and kill its jobs. Eligibility is false
for anything nothing will wake, so a blocked or errored session is still reclaimed promptly.
Execution that is terminal and already notified does not extend eligibility while its process is still
exiting: the single-writer guard and the shutdown execution join already cover that case.

A subagent turn is also an explicit child-job lifecycle boundary. By default, every running job owned by
that subagent is cancelled and its process/agent execution is joined before the subagent releases mutation
ownership; completed, previously unnotified reports are included in the subagent result. A watcher may
continue only when the subagent's finish control explicitly hands over its job id and purpose. That transfer
records the parent owner and delivery contract: completion is offered exactly once at a parent checkpoint
unless an explicit wait claims it first, while `job_result` and output evidence remain repeatable. Handoffs
are durable and discoverable through `list_jobs`. A mutating handoff keeps its lifetime lease, prevents a
safe final changeset from being attributed, and blocks subsequent mutating work until the process actually
stops. Error, blocked, cancellation, hard-stop, and session-shutdown paths apply the same cleanup-and-join
rule; terminal report state alone is not proof that execution has stopped. Session shutdown closes job
registration before snapshotting runners: a concurrent tracked start is either registered in time to be
cancelled and joined, or is declined before launch and releases any mutation lease/agent-running guard.

Bash output is retained as a bounded tail addressed by absolute byte cursors. Reads can request a
forward cursor range or retained line tail, identify the retained absolute interval and any eviction
gap, and return a truthful next cursor. Completed Bash reports continue to advertise the fuller output
artifact when one was captured; basic retained job output does not depend on artifact availability.

Job start, finish, explicit-wait notification claims, and injected completion are durable events. Claims
and injected notifications carry the terminal metadata and raw result as well as the stable id, so a
checkpoint or wait that races ahead of the worker's finish-event write still preserves repeatable evidence.
On session reopen the registry restores stable ids, owners, lifecycle metadata, final reports, and
notification state from those events. Jobs themselves do not survive a daemon restart: an unmatched
start is restored terminal as `lost`, replay reports the restart loss to the conversation exactly once, and
newly started jobs continue after the greatest restored id.

## Mutation safety

Background execution does not weaken the per-worktree single-writer invariant. Read-only agents may
fan out. A mutating background agent is refused while another mutating job is live in the same tree.
Parallel mutation uses workstreams, where each agent owns a separate linked worktree.

## Rejected alternatives

- Concurrently dispatching every tool call from one model turn is unsafe for mutation and is not
  needed for explicit background work.
- Separate shell and agent job APIs duplicate lifecycle and delivery behavior.
- Polling completion wastes turns and can race notification delivery; progress reads are for
  diagnostics, not scheduling.
- Treating notification delivery as destruction of the final report makes completed work impossible
  to inspect after a checkpoint or resume.
