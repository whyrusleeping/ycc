---
id: "0234"
title: Default Anthropic base URL during model discovery
status: done
priority: 2
created: "2026-08-04"
updated: "2026-08-04"
depends_on: []
spec_refs:
    - §13 Model backends
    - §18.2 Settings
---

## Description
Fix model discovery for a newly added Anthropic OAuth backend when the base URL field is blank.

## Acceptance criteria
- Anthropic model discovery treats a blank base URL as `https://api.anthropic.com`.
- Backends without a canonical default still reject a blank base URL.
- Tests cover the defaulting behavior.

## Work log
