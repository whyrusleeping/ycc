---
id: "0631"
title: Preview task status and title on web task references
status: done
priority: 3
created: "2026-11-27"
updated: "2026-11-27"
depends_on: []
spec_refs: []
---

## Description

Hovering a task number in web conversation text or a task link shows the referenced project's task status and title without navigating. Keyboard focus exposes the same preview.

## Outcome

Web task links and conversation #mentions show a project-scoped preview on hover or keyboard focus. Verified with the web build and 522 tests, the embedded-bundle check, and a Chromium hover/dismiss smoke test.

Commit: Show task status and title on hover in the web client
