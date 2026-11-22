---
id: "0471"
title: Session fork and rewind from a user turn (conversation, optional file checkpoints)
status: done
priority: 2
created: "2026-10-21"
updated: "2026-11-22"
depends_on: []
spec_refs:
    - Event log
    - Session history and reopen
---

## Description

Status: done. The user accepted it on 2026-11-13 by changing its status on iOS (client analytics `task.status` to todo at 03:20:31Z, matching the file's change time). The user did not answer the open questions; on 2026-11-22 the unattended work-loop session that picked the task decided them (agent inference, reversible) and wrote the acceptance criteria below: v1 is the conversation fork with the daemon RPC and the web action. The iOS action is 0588 (todo) and file checkpoints are 0589 (proposed).

Branch or rewind a session from any earlier user turn. The append-only event log makes the conversation part cheap. File state is the hard part. The 2026-10 harness survey (the coding-harness feature research in session s_9fdcf317e49ae2b7, where the user called session fork and rewind "very useful", event 204, 2026-10-21) found this in Claude Code (`/rewind`), Gemini CLI, Copilot CLI, Cursor, Cline, Kilo, OpenCode, and Pi (session tree/fork).

## Acceptance criteria

- [x] `ForkSession(project, session_id, at_seq)` creates a new session whose log holds the parent's events before the user turn at `at_seq` (for a queued steer, before the point where the model received it), followed by a `session_forked { parent, at_seq }` marker, and gives it its own git baseline. It returns the new id and the turn's text. The parent log is not modified. A seq that is not a user turn is rejected as an invalid argument; an unknown session is not found.
- [x] The fork reopens through the ordinary persisted-session path: the first `SendInput` replays the copied history, and the model's first request ends with that input where the cut turn was. Picture attachments of the copied turns are copied with it. Copied model turns and commits are not counted as the fork's usage or commits.
- [x] History summaries report `forked_from`, and the transcript shows the `session_forked` marker as a row that links to the parent.
- [x] Web: user turns offer "Fork from here", which forks, opens the new session, and puts the turn's text in its composer.
- [x] Spec describes fork semantics.
- [x] Go tests cover the cut (plain turns and steers, including mid-batch delivery), the marker, the rejected fork points, the first model request after a steer fork, running jobs, attachment copy, accounting, baseline, and reopen replay; web typecheck and tests pass.

## Outcome

ForkSession(project, session, at_seq) copies the parent's log before a user turn into a new persisted session ending in a session_forked marker, with copied attachments and its own git baseline; a steer is cut where the model received it (after its open tool batch, stopping at run boundaries, excluding later deferred messages). A reopened fork waits for its first input, which takes the cut turn's place; open questions are dropped, unreported jobs settled by claims; usage, budget flags and commit attribution restart at the marker; summaries report forked_from. Web: "Fork from here" on user turns opens the fork with the turn's text in the composer; linked marker row; sidebar fork tag. Spec updated. Follow-ups: 0588 iOS (todo), 0589 file checkpoints (proposed). Review: sol accepted after 7 rounds.

Commit: Session fork and rewind from a user turn: ForkSession RPC, session_forked marker, web "Fork from here" (0471)
