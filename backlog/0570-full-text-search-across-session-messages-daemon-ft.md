---
id: "0570"
title: Full-text search across session messages (daemon FTS index + search RPC + client UI)
status: todo
priority: 3
created: "2026-11-20"
updated: "2026-11-21"
depends_on:
    - "0571"
    - "0572"
    - "0573"
spec_refs:
    - spec.md#18.6 Session history and reopen
    - docs/design/web-client.md
---

## Description
**Status:** accepted; split on 2026-11-21 into 0571 (daemon index, RPC and spec), 0572 (web UI) and 0573 (iOS UI, built on the Mac). This parent closes after all three, checked against the criteria below. Scope decided by the user on 2026-11-20 in the session that created this task.

## Problem

Search exists only inside a single transcript: TUI `/` (0116, 0119) and web Ctrl/Cmd-F (0412). The web inbox search (0451) is limited to loaded history summaries, and the daemon has no search RPC. Finding "the session where we discussed X" means paging through history or grepping `.ycc/sessions/*/events.jsonl` by hand.

## Evidence (2026-11 measurement on this workspace)

- This project has 617 sessions with 431 MB of logs. The 13 local projects hold 2.3 GB in total.
- Conversational text is small. Assistant `model_turn` text is about 3 MB, `user_input` about 0.14 MB, and question/answer about 0.08 MB.
- Tool I/O dominates: `tool_result` is 245 MB and `tool_call` 48 MB.
- `internal/sessionview` already maintains a rebuildable per-project SQLite projection (`.ycc/session-view.sqlite`, schema-versioned and mode 0600). It is caught up incrementally from the JSONL logs.
- The bundled `modernc.org/sqlite` v1.58.0 is compiled with `SQLITE_ENABLE_FTS5`.

## Scope decisions (user, 2026-11-20)

- **Indexed content:** user input, assistant text, and asked questions with their answers. Not indexed: tool calls, tool results, thinking, plan/review/commit summaries.
- **Clients:** daemon, web and iOS. The TUI is out of scope.
- **Search span:** the current project only. Search does not cover all projects at once.

## Proposed design (authoring agent's inference)

1. **Index.** Add an FTS5 table in the per-project session-view database, populated in the same catch-up pass as rows, so it shares the JSONL-is-authority and rebuild-on-schema-bump semantics.
   - Each entry records session id, row id, seq, actor and timestamp.
   - Sessions that have never been opened are backfilled in the background. Searching must not require opening each session first.
2. **RPC.** Add `SearchSessions(project, query, cursor, limit)` for one project. It returns hits ranked by bm25 (secondary order: recency) with a highlighted snippet (FTS5 `snippet()`), session id, session title and row id. While backfill is incomplete, the response reports that.
3. **Jump to hit.** Opening a hit lands on the matching row. `GetSessionViewPage` currently pages backward by cursor only, so it needs an anchor-row option (or a cursor derived from a row id).
4. **Web.** Search in the session list and command palette, showing hits with snippets. Selecting a hit opens the session at the matching row.
5. **iOS.** A search field on the project's session list, showing hits with snippets. Selecting a hit opens the session at the matching row. The work is built and checked on the configured Mac.

## Acceptance criteria

- [ ] A query matching text from a session that has not been opened since daemon start returns that session after backfill.
- [ ] Hits include a highlighted snippet and open the transcript scrolled to the matching row, including rows far back in long sessions, on both web and iOS.
- [ ] The index stays current as live sessions append events, and rebuilds cleanly after a schema bump or a deleted database.
- [ ] Tool calls, tool results and thinking are not indexed.
- [ ] Spec §18.6 and the web client design doc describe the search contract.

## Work log
