---
id: "0310"
title: Adopt lean engineering standards and remove prompt-wording tests
status: done
priority: 2
created: "2026-08-10"
updated: "2026-08-10"
depends_on: []
spec_refs:
    - Vision & philosophy
    - docs/design/doc-style.md#Shared register
---

## Description
Establish concise project standards for proportional testing, documentation, planning, and review. Wire those standards into the agent prompts, revise the spec's absolute process language, remove tests that assert prompt prose, and apply a small behavior-neutral comment cleanup in internal/orchestrator as an exemplar.

## Acceptance criteria
- A short root engineering/contribution standard defines when tests, docs, plans, and reviewer agents are warranted and explicitly rejects coverage-driven and prose-testing work.
- Implementer, coordinator, and reviewer prompts enforce proportional test/document/review behavior and tell reviewers to name a concrete failure before requesting a test.
- The spec no longer states that every code change requires a persisted plan and external review.
- Tests that assert prompt keywords or wording are removed without removing behavioral mode/tool-boundary coverage.
- Historical task/spec breadcrumbs in internal/orchestrator comments are reduced while preserving comments that explain non-obvious behavior.
- Relevant Go tests and the full Go test suite pass.

## Work log
