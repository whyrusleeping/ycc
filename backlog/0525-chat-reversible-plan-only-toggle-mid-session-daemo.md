---
id: "0525"
title: 'Chat: reversible "plan only" toggle mid-session (daemon + web)'
status: done
priority: 3
created: "2026-11-10"
updated: "2026-11-10"
depends_on: []
spec_refs:
    - spec.md#9. Modes
    - docs/design/web-client.md
---

## Description

**Context (2026-11-11 mode review):** PM's no-code guardrail goes unused partly because it's chosen at session start, while intent changes partway through (think → decide → build), and leaving PM costs a session break. If a guardrail is wanted, it belongs inside the conversation and should be reversible (cf. Claude Code's plan mode). The owner accepted this as a TODO. Check actual use after it ships before investing more.

**Scope**
- Session-level `plan_only` state settable while a chat session is live (RPC). It takes effect at the next turn boundary, is recorded as an event so replay and reopen reproduce it, and is shown in session state.
- While on, the coordinator is told it is planning only: investigate, discuss, and edit docs/backlog/plans/memory. Edit/Write outside configured design docs, backlog, plans and memory.md are refused with a clear message. Bash stays available (honestly a soft boundary there, as in PM); spawned agents are forced read-only.
- Turning it off restores full chat tools with context intact. A short synthetic note tells the model it may now implement.
- Web: a toggle in the session composer/header, a visible "Plan only" indicator, and an option to start a new chat with it on.

**Acceptance**
- The spec describes the toggle and its boundary.
- Tests cover enforcement (edits to code paths refused, doc/backlog edits allowed, spawned agents read-only), toggling mid-session, and replay/reopen.
- Web screenshot evidence (light/dark). Bundle rebuilt.
- iOS parity is a follow-up, only if the web toggle gets used.

## Outcome

Chat sessions have a reversible plan-only guardrail: SetPlanOnly RPC and StartSession.plan_only (chat only). plan_only_changed is recorded at once (SessionViewState.plan_only, transcript row); Write/Edit outside spec/doc_globs/backlog/plans/memory.md are refused (symlink-resolved), spawned agents forced read-only, Bash soft. The model is told at the next turn boundary via a plan_only_delivered note that replay, reopen and rollover reproduce. Web: composer toggle, header marker, palette action, New session option; bundle rebuilt. Spec §9, remote-api.md, web-client.md updated. Go + web tests; light/dark screenshots in .ycc/tmp/planonly-shots/shots. iOS parity is a follow-up.

Commit: Chat plan-only toggle: SetPlanOnly / StartSession.plan_only, Write/Edit gate to planning docs (symlink-aware), read-only subagents, replayable notes, web toggle + marker + New session option (0525)
