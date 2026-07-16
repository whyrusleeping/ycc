---
id: "0216"
title: Always render final session reports expanded in TUI and iOS
status: done
priority: 2
created: "2026-07-15"
updated: "2026-07-16"
depends_on: []
spec_refs:
    - Client UI (TUI)
    - docs/design/ios-client.md#Phase 1 — observe, answer, control (parity with the web client)
---

## Description
Treat the `session_idle.report` as the canonical human-facing finish message. In the TUI it must always be expanded regardless of the auto-expand preference or manual collapse state and rendered as Markdown; when it repeats the immediately preceding final coordinator `model_turn`, suppress that duplicate turn rather than suppressing the finish report. In iOS, project the report into a dedicated finish row and render it as a prominent Markdown summary card, coalescing an identical preceding model message.

Acceptance criteria:
- TUI finish report is always visible and expanded with Markdown rendering when auto-expand logs is off.
- A user cannot collapse the final report; ordinary rows remain toggleable.
- Echoed final model-turn/report content appears exactly once, under the finish presentation.
- A differing or additive finish report remains visible in full.
- iOS has a dedicated final-report projection kind and polished Markdown rendering.
- Projection/TUI regression tests cover echo coalescing, Markdown content, and forced expansion.

## Acceptance criteria

## Work log
