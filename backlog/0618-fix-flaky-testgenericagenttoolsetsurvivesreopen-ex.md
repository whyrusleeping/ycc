---
id: "0618"
title: Fix flaky TestGenericAgentToolsetSurvivesReopen/explicit_none
status: proposed
priority: 3
created: "2026-11-26"
updated: "2026-11-26"
depends_on: []
spec_refs: []
---

## Description
`internal/session` `TestGenericAgentToolsetSurvivesReopen/explicit_none` (subagent_models_test.go:90-92) fails intermittently with "reopened spawn_agent present=true, want false" and "reopened policy nil=true". A reviewer measured 15/30 failures on baseline ef64fa9 (before 0613) and 9/30 with 0613, so it predates 0613. The likely cause is that Reopen after Stop sometimes replays a log that does not yet contain session_started's subagent_models.

## Acceptance criteria
- Find the root cause (a race in the test, or one in Reopen/replay of subagent_models) and fix it.
- `go test -count=50 -run TestGenericAgentToolsetSurvivesReopen ./internal/session/` passes, with and without `-race`.

## Work log
