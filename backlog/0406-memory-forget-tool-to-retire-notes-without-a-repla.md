---
id: "0406"
title: 'Memory: forget tool to retire notes without a replacement'
status: done
priority: 2
created: "2026-09-30"
updated: "2026-10-10"
depends_on: []
spec_refs:
    - docs/design/project-memory.md#Write and grooming policy
---

## Description
The only memory mutation is `remember`, so removing an obsolete note requires writing a replacement. Add a coordinator `forget` tool: {ids, reason} appends an audit retraction record that supersedes the ids and is never rendered into prompts. Always permitted (it only shrinks active memory). Unknown ids error; already-inactive ids are reported.

## Acceptance criteria

## Work log
