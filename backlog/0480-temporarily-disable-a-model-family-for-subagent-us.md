---
id: "0480"
title: Temporarily disable models by shared billing or quota group
status: proposed
priority: 3
created: "2026-10-24"
updated: "2026-10-24"
depends_on: []
spec_refs:
    - 13. Models, credentials, and review tiers
    - 7.3 Subagents and asynchronous jobs
---

## Description
Allow the user to turn off a group of models sharing a billing system when API credits or subscription quota run out, then easily turn them back on without deleting model configurations. The user clarified that the grouping must follow the shared billing/quota pool, not the model family: subscription-backed Claude models and separately billed Claude API models may belong to different groups.

## Acceptance criteria
- One control disables or re-enables models belonging to a shared billing/quota group.
- Disabling one group leaves models billed through other groups available, including models in the same family.
- Agents cannot invoke disabled-group models as subagents; configuration and credentials remain intact for re-enabling.

## Design considerations (proposed, not yet approved)
- Explicit named group membership avoids assuming a provider, model-family prefix, or API-key name uniquely identifies a billing pool; multiple keys can share one account balance.
- Treat the group switch as an additional availability gate so re-enabling a group preserves individually disabled models.
- Prefer a general availability switch over a subagent-only exception: exhausted billing applies to coordinator inference too. Confirm scope before implementation.
- Enforce the live gate at inference boundaries, including retained agents and reviewers/implementers, without cancelling an in-flight request.
- Manual switching is the requested scope; automatic quota detection, fallback, and scheduled re-enabling are separate ideas.

## Work log
Existing per-model enabled/disabled settings do not provide shared billing groups and do not tear down already-constructed clients. Proposal revised to reflect the user's billing-pool clarification; implementation has not started.
