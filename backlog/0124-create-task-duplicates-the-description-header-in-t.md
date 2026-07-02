---
id: "0124"
title: create_task duplicates the "## Description" header in task bodies
status: todo
priority: 4
created: "2026-07-02"
updated: "2026-07-02"
depends_on: []
spec_refs: []
---

## Description
The create_task tool (internal/orchestrator/modes.go, createTask) wraps the supplied description in a `"## Description\n" + desc` body. When the caller's description already begins with its own `## Description` header (agents often supply fully-structured markdown), the resulting task file contains the header twice (see e.g. backlog/0120, 0123). Same risk applies to a duplicated `## Acceptance criteria` section.

## Acceptance criteria
- [ ] createTask (and the capture path, if it shares the assembly) does not emit a duplicate `## Description` / `## Acceptance criteria` header when the supplied description already contains one
- [ ] unit test covering both a plain-text description and a pre-structured markdown description
- [ ] existing task files are unaffected (no migration required; optionally tidy the known duplicates)

## Acceptance criteria

## Work log
