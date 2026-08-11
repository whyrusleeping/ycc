# Design: asynchronous jobs

> Status: accepted and implemented.

## Context

Shell commands and subagents sometimes have useful work to overlap. Making each kind of
background activity a separate mechanism would give the coordinator several incompatible ways to
wait, inspect, cancel, and receive completion. It would also make replay ordering difficult.

## Decision

A session owns one job registry for background shell commands and child agents. Jobs share an id
namespace, lifecycle, progress read, bounded wait, cancellation, and final report contract.
Foreground execution remains the right choice when the result gates the next step; background
execution exists to overlap meaningful independent work or leave a watcher running.

Final reports have one consumed flag and are delivered exactly once by either:

- a `wait` covering the completed job; or
- an injected user-role notification at an engine checkpoint.

`job_output` is a non-consuming progress view. This avoids the deadlock-prone design where a
blocking retrieval path competes with a separate completion queue. A completion found between
results from one model tool-call batch is deferred until the complete batch has entered history,
preserving provider requirements that tool results immediately follow their calls.

Job start, finish, and injected completion are durable events. Jobs themselves do not survive a
daemon restart; replay turns an unmatched start into a lost-on-restart completion so the resumed
conversation remains valid.

## Mutation safety

Background execution does not weaken the per-worktree single-writer invariant. Read-only agents
may fan out. A mutating background agent is refused while another mutating job is live in the same
tree. Parallel mutation uses workstreams, where each agent owns a separate linked worktree.

## Rejected alternatives

- Concurrently dispatching every tool call from one model turn is unsafe for mutation and is not
  needed for explicit background work.
- Separate shell and agent job APIs duplicate lifecycle and delivery behavior.
- Polling completion wastes turns and can race notification delivery; progress reads are for
  diagnostics, not scheduling.
