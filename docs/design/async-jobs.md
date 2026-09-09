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
