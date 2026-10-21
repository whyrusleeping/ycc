---
id: "0471"
title: Session fork and rewind from a user turn (conversation, optional file checkpoints)
status: proposed
priority: 2
created: "2026-10-21"
updated: "2026-10-21"
depends_on: []
spec_refs:
    - Event log
    - Session history and reopen
---

## Description
Branch or rewind a session from any earlier user turn. The append-only event log makes the conversation part cheap. File state is the hard part. The 2026-10 harness survey found this in Claude Code (`/rewind`), Gemini CLI, Copilot CLI, Cursor, Cline, Kilo, OpenCode, and Pi (session tree/fork).

## Sketch (to confirm before acceptance)
- **Fork (conversation):** a `ForkSession(session, at_seq)` RPC creates a new session. Its log replays the parent's events through a user-turn boundary, followed by a `session_forked{parent, seq}` marker. Replay then reconstructs history the same way reopen does. The parent is untouched. Clients get a "fork from here" action on user turns. Optionally, the fork can start in a fresh workstream so two approaches run in isolated worktrees.
- **Rewind** is a fork that replaces the current view in clients. It never truncates a log.
- **File checkpoints:** at each user turn, snapshot the session's attributed paths to a private ref (`refs/ycc/checkpoints/<session>/<seq>`, using a temp index plus write-tree; the user's index and HEAD are untouched). Restoring is opt-in and covers only paths this session claimed. It refuses paths that another scope or the user changed since, so a restore cannot clobber concurrent sessions in a shared worktree.

## Open questions
- Should file restore be in v1 or a follow-up? Fork alone is useful with git.
- How should forks of sessions with live jobs, pending questions, or task focus be handled? Probably drop the jobs and question, and keep the focus.

## Acceptance criteria

## Work log
