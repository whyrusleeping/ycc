# Design: watches

> Status: accepted. Daemon, tools, and RPC are implemented (spec §9.2); client surfaces are pending.

## Context

Users ask a session to check a live system on a schedule, report, and raise a question when
something needs attention. Without daemon support the model builds this from a background
`sleep` and a long `wait` inside one turn that never ends. A wheeler session ran this way for
13 checks over two days (`s_7aac4d11912b6830`, from 2026-10-31) and showed these failures:

- `ask_user` blocks the turn, and the next sleep starts only after the answer. Three questions
  produced gaps of 1.5, 1.5 and 12 hours with no checks, during the incidents being reported.
- Context grew about 10k tokens per check (41k to 194k by check 13) because every check
  replays all earlier ones.
- The schedule exists only as a live job in a live turn. A daemon restart or session stop
  ends it silently.
- Reports are mid-turn assistant text with no status, so "all good" and "look at this" read
  the same, and the session always appears to be running.
- The model rewrites nearly identical probe commands each cycle.

## Decision

A **watch** is a daemon-owned recurring check for one project. Each run is a fresh, short
session; continuity comes from a bounded notes record passed from one run to the next.

### Definition

A watch has a name, project, brief (what to check and what counts as wrong), interval,
model (default: the configured coordinator), optional probe command, optional expiry, and an
optional list of pre-authorized actions. Definitions and run state persist under the project's
`.ycc/watches/` and survive daemon restart; only a persistent daemon schedules runs. Watches can be paused, resumed, run now, edited
and deleted.

### Schedule

Due times are anchored to the schedule, not to run completion, so the cadence does not drift.
Runs of one watch never overlap; if a run is still active when the next is due, the due run is
recorded as skipped. After daemon downtime, missed runs collapse into a single catch-up run
whose context states the gap.

With a probe command, the daemon runs it first and starts a model run only when it exits
non-zero (or always, if the watch says so). A passing probe records an `ok` report without a
model call.

### Run context and report

A run session (origin `watch`, unattended) starts with:

- the brief and the watch's notes;
- the previous report's status and summary;
- pending questions from earlier runs, with their age;
- an activity snapshot of the project (below);
- probe output, when there is a probe.

The run ends by calling a `report` tool with a status (`ok`, `warn` or `alert`), a short
summary, actions taken, and the replacement notes. Notes are bounded (a few KB) and hold
baselines, open threads, and the user's standing decisions. When the user answers a watch
question, the daemon appends the question and answer to the notes itself, so a decision such
as "1–2 minute lag spikes are acceptable" carries forward even if the model omits it.

Notification: `ok` is silent; `warn` updates the watch's status badge without a push; `alert`
pushes. A run that ends without calling `report` records a failed run, which counts as `warn`.

### Questions

A watch question does not stop the schedule. The asking run parks waiting for the answer
while later runs proceed, see the pending question, and do not ask it again. When the answer
arrives, the parked run resumes, acts on it, and files a follow-up report. This differs from
ordinary unattended sessions (spec §11), whose questions are auto-answered.

### Actions and other agents

Watches may take low-risk actions without asking. Low-risk means reversible, and not
deleting data, editing or committing code, or restarting or stopping services. Actions in the
watch's pre-authorized list are also allowed. Every action is listed in the report with how to
undo it, and a run that acted reports at least `warn`.

To avoid interfering with other agents, the activity snapshot lists the project's live
sessions (mode, task, phase, last activity), their live background jobs with command labels,
their workstreams, and `in_progress` backlog tasks. A watch does not act on anything
attributable to a live session or an `in_progress` task, including the processes, data and
services that task operates; it asks instead. If ownership is unclear, the thing is treated as
owned. In the wheeler session, pausing the history load belonged to task 0175, so under this
rule it would still have been a question, as it was.

### Creation and surfaces

Watches are created through RPC and through a `schedule_watch` tool, so a chat request like
"check every three hours and ask me if something is wrong" becomes a watch rather than a sleep
loop. Because a watch can act unattended, creation shows the definition and requires the
user's confirmation. Clients list each project's watches with latest status and next due time,
show the report timeline, and keep watch run sessions out of the main recent-session list.

Agents also propose a watch unprompted when a turn ends while external work they started keeps
running. In the 2026-11-09 usage analysis, 24 owner messages were pure check-ins ("how's it
going?"); 18 followed a turn that ended with a statement while a soak, replay, or backfill ran
on. A background job does not need a watch, because its completion already wakes the session.

## Rejected alternatives

- **One long-lived session with a durable wakeup tool.** It fixes restart and question gaps
  but keeps the unbounded context growth and the always-running presentation.
- **Fresh runs without notes.** Each run would lose baselines and the user's standing
  decisions, and would re-raise questions the user already answered.

## Relationship to other work

Health monitoring (spec §14.2) covers the daemon's own dependencies; watches are the
user-defined counterpart and can share its scheduling and banner surfaces. Task 0391 (bounded
supervision of long-running experiments) describes the same need for experiment check-ins.
Automatic memory-groom sessions are the precedent for daemon-started sessions.
