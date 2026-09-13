---
id: "0373"
title: Add shared event-contract fixtures for TUI, web, and iOS projections
status: blocked
priority: 2
created: "2026-09-08"
updated: "2026-09-10"
depends_on: []
spec_refs:
    - §5.2 Event contract
    - §18 Client interaction model
---

## Description
Three client projections independently interpret event payloads and already disagree on queued input, automatic answers, review verdicts, and paused state. Create shared factual fixtures rather than forcing identical layouts. Existing SessionProjectionTests has fixture-loading support; TUI/web have their own reducers/render paths.

## Acceptance criteria
- A shared versioned fixture set describes canonical events plus expected semantic facts: lifecycle phase, durable cursor, pending question, input delivery/provenance, review outcome, and per-actor transient tails.
- Go TUI, Swift YccKit, and web tests consume the same fixtures and assert facts while allowing client-specific presentation.
- Cover queued steer while paused, resumed/delivered ordering, unattended auto-answer, another-client answers, accept/revise/unknown review, duplicate replay, reconnect tail reset, concurrent actors, and unknown legacy events.
- Include drop-and-resubscribe and subscribe-after-restart scenarios in suitable transport tests, not only pure projection tests.
- Fixtures contain no private transcripts or credentials; document how to extend them for future event changes.
- Record Swift execution requirements honestly; tests runnable on the user's Mac are not claimed executed on the Go-only host.

## Work log

- Preflight: blocked from completing the required cross-client assertions in this workspace. Confirmed iOS `applyQuestionAsked`/`foldAnswer` ignore automatic provenance and always create a human pending gate; required semantic fixture coverage therefore needs the still-blocked 0369 correction, not tests that bless that defect. Projection and existing client tests contain unrelated baseline-dirty work, which the changeset guard forbids modifying. Resume in a clean isolated task session (coordinate 0369/0370/0372 contract work), or resolve existing owners' changes before a new baseline; no fixtures or implementation were added.
