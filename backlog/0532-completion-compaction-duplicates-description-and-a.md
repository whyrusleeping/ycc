---
id: "0532"
title: 'Completion compaction duplicates Description and Acceptance criteria headings when a description uses ## subheadings'
status: proposed
priority: 3
created: "2026-11-10"
updated: "2026-11-10"
depends_on: []
spec_refs:
    - 6.2 Backlog
---

## Description
## Why
Found while working on 0530 (backlog-groom preset), 2026-11-10. Done tasks whose description used `##` subheadings come out of completion compaction with a duplicated `## Description` heading and a second copy of their acceptance criteria. Example: ycc `backlog/0528-*.md` (doc-style backlog rules, committed ab7371c) has `## Description` twice in a row, and its criteria appear both inside the description and again under a second `## Acceptance criteria`.

Cause (agent's reading of the code, not yet reproduced in a test): `docs.TaskBody` puts `## Description` in front of a description that starts with another heading such as `## Why`. That section is empty, so `collectSectionContent(body, "description")` in `internal/docs/docs.go` returns "". `compactCompletedBody` then falls back to `stripOperationalSections(body)`, which keeps every heading, the original `## Description` and `## Acceptance criteria` included. It then writes its own `## Description` and criteria on top. The backlog-groom preset (0530) cleans up the existing copies, but compaction keeps producing new ones.

## Acceptance criteria
- Compacting a task whose description starts with `## Why` / `## Scope` subsections gives one `## Description` heading, keeps the subsections, and has a single criteria section.
- A focused test in `internal/docs` covers that body shape.
- Existing compaction tests still pass.

## Work log
