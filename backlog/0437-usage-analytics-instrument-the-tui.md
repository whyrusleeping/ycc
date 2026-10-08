---
id: "0437"
title: 'Usage analytics: instrument the TUI'
status: todo
priority: 5
created: "2026-10-08"
updated: "2026-10-08"
depends_on:
    - "0434"
spec_refs:
    - docs/design/usage-analytics.md#Kinds
---

## Description
Low priority (the user rarely uses the TUI). Record views (home menu, session, backlog browser, settings…) and key actions with `via=keyboard`, flushed through RecordUiEvents to the attached daemon, per docs/design/usage-analytics.md.

## Acceptance criteria

## Work log
