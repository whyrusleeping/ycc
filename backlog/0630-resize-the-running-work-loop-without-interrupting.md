---
id: "0630"
title: Resize the running work loop without interrupting sessions
status: done
priority: 3
created: "2026-11-26"
updated: "2026-11-26"
depends_on: []
spec_refs: []
---

## Description

Allow the user to change the worker target while a loop is running. Raising it admits additional sessions under the existing scheduling rules. Lowering it leaves active sessions alone and withholds replacements until the working count falls below the new target. The change applies to this loop, not the project default.

## Outcome

Web and iOS clients can set a live loop’s worker target (1–16). Growth respects existing scheduling gates; shrinking drains excess sessions without cancelling them or changing the project default. Loop IDs guard against stale views. Verified with Go tests and race checks, web tests, Swift tests and an iOS simulator resize.

Commit: Resize a running work loop without interrupting sessions
