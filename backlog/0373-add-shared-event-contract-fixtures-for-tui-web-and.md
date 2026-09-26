---
id: "0373"
title: Add shared event-contract fixtures for TUI, web, and iOS projections
status: done
priority: 2
created: "2026-09-08"
updated: "2026-09-26"
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

## Outcome

testdata/event-contract/: 5 synthetic v1 scenarios (lifecycle, paused steer/delivery ordering, auto + another-client answers, accept/revise/unknown reviews with unknown event and duplicate replay, concurrent tails + reconnect reset) with partial semantic facts and a README on schema/extension. Consumed by TUI (applyLiveEvent), web (feedIngest) and YccKit EventContractTests (#filePath). Fixes: TUI/web no longer open gates for auto questions; web labels them as assumptions, tracks user_input_delivered, clears tails on reconnect, syncs phase/running/paused. server subscribe_restart_test covers restart replay + resubscribe from cursor. go build/vet/test tui/web/server and node pass; Swift ran only in Linux Docker scratch package (implementer-reported), not macOS. Standard review accepted.

Commit: clients: shared versioned event-contract fixtures consumed by TUI, web, and YccKit projections; restart/resubscribe transport test; fix auto-question gates and web delivery/phase tracking (0373)
