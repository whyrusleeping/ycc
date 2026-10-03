---
id: "0418"
title: 'Desktop web: Playwright end-to-end tests against a real daemon with a scripted model'
status: proposed
priority: 4
created: "2026-10-02"
updated: "2026-10-02"
depends_on:
    - "0410"
spec_refs:
    - docs/design/web-client.md#Verification
---

## Description
Optional follow-up. Automate most of plans/web-client-smoke.md with Playwright (headless Chromium) driving the built bundle served by a real `ycc daemon --web`, using a scripted/fake model backend like the TUI e2e harness (internal/e2e). It would cover token entry, streaming, paging, questions, and reconnect after a dropped connection. It runs as an opt-in script (needs Node + browser download), not part of `go test`.

## Acceptance criteria

## Work log
