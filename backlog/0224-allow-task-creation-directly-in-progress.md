---
id: "0224"
title: Allow task creation directly in progress
status: done
priority: 2
created: "2026-07-28"
updated: "2026-07-28"
depends_on: []
spec_refs: []
---

## Description
Extend the create_task tool so agents can create an accepted backlog task with initial status `in_progress`, avoiding a separate update_task call when starting an active workstream.

Acceptance criteria:
- create_task accepts `in_progress` as an initial status.
- Existing `todo` default and `proposed` behavior remain unchanged.
- Tool descriptions/schema and durable documentation reflect the supported status.
- Tests cover direct in-progress creation and pass.

## Acceptance criteria

## Work log
