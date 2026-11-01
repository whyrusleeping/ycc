---
id: "0496"
title: 'Web: inline Retry beside the current session error'
status: done
priority: 3
created: "2026-10-31"
updated: "2026-10-31"
depends_on: []
spec_refs:
    - 18.7 Interrupt and steer
---

## Description
Keep the existing upper-right Retry button and also provide a small Retry affordance beside the current error message in the conversation.

## Acceptance criteria
- The current retryable model failure has a small Retry action near its transcript error message.
- The action resumes the pending turn without adding user input, shows pending feedback, and reports action failures normally.
- Historical errors, subagent errors, and failures marked non-retryable do not offer this action.

## Outcome

Added a small inline Retry link beside the latest retryable coordinator error, retaining the upper-right button. It uses the existing Resume action, disables while pending, and shows “Retrying…”. Session error metadata keeps the action off historical/subagent errors and prevents repeated-notice grouping from hiding it. Rebuilt the embedded web bundle.

Verification: `scripts/web-build.sh` passed (typecheck, production build, 424 tests including eight focused inline-Retry cases); `go test ./internal/web` and `git diff --check` passed. Focused review found no issues.
