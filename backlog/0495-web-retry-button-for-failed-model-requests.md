---
id: "0495"
title: 'Web: Retry button for failed model requests'
status: done
priority: 3
created: "2026-10-31"
updated: "2026-10-31"
depends_on: []
spec_refs:
    - 18.7 Interrupt and steer
---

## Description
Add the failed-request Retry affordance available in iOS to the web session view.

Acceptance criteria:
- A failed model request that can be retried exposes a Retry button in the web UI.
- Retry resumes the pending turn without sending duplicate user input, and action failures remain visible.
- Retry is not offered for historical errors once the session has moved on, or when the failure requires a model change.

## Acceptance criteria

## Outcome

No implementation needed: the web UI already offers Retry in the upper-right session controls, using Resume without duplicate input. The user located it and withdrew the change request.
