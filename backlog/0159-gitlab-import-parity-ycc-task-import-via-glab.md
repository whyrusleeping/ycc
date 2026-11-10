---
id: "0159"
title: 'GitLab import parity: ycc task import via glab'
status: done
priority: 5
created: "2026-07-06"
updated: "2026-11-10"
depends_on:
    - "0155"
    - "0156"
spec_refs:
    - docs/design/forge-integration.md#5. Flow 1 — issues → backlog (`ycc task import`)
---

## Description
From docs/design/forge-integration.md §5/§11 (design spike 0146).

GitLab parity for issue import: add a `glab issue view --output json` adapter behind the same `ycc task import` command, selected by forge/host inference from internal/forge. Same field mapping, origin dedupe, and failure modes as the GitHub path.

## Acceptance criteria
- [ ] `ycc task import <gitlab-issue-url>` works (gitlab.com and self-hosted hosts glab is authed for).
- [ ] Shared import logic; only the fetch/parse adapter differs.

## Work log
- 2026-11-10: Closed without implementation. The owner closed the forge workflow tasks (see 0156); there is no `ycc task import` to reach parity with.
