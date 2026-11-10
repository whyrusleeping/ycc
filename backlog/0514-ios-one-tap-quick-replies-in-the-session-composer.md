---
id: "0514"
title: 'iOS: one-tap quick replies in the session composer'
status: todo
priority: 2
created: "2026-11-09"
updated: "2026-11-09"
depends_on:
    - "0513"
spec_refs:
    - docs/design/ios-client.md
---

## Description
iOS counterpart of 0513: the same built-in and project-defined quick-reply chips, from the same daemon data, shown above the session composer on an idle session. They follow the same hide rules (running, pending question sheet, text entered) and record the same `composer.quick_reply` analytics. The owner sends most check-in replies from the phone, so this is where typing costs the most.

**Acceptance:** chips send as normal messages, hide correctly, and match the web set for a project. Built and tested on the configured Mac simulator. Left in_review for on-device use.

## Acceptance criteria

## Work log
