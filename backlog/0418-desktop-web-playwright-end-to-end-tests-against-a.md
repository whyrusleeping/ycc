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
Optional follow-up. Automate most of plans/web-client-smoke.md with Playwright (headless Chromium) driving the built bundle served by a real `ycc daemon --web`, using a scripted/fake model backend like the daemon/CLI e2e harness in internal/e2e (`llmstub_test.go`; it replaced the TUI PTY suite when the TUI was retired in `6ce9e51`). It would cover token entry, streaming, paging, questions, and reconnect after a dropped connection. It runs as an opt-in script (needs Node + browser download), not part of `go test`.

## Work log
- 2026-11-23 groom: updated the harness reference (internal/e2e is now the daemon/CLI harness, not the TUI one) and removed an empty Acceptance criteria heading. 0410 (the desktop web client it builds on) is done. No Playwright suite exists in clients/web; ad-hoc Playwright screenshots live only in untracked scratch tooling. Still proposed.
