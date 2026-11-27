---
id: "0561"
title: 'iOS: publish a viewed file to the share server and manage its link'
status: done
priority: 2
created: "2026-11-18"
updated: "2026-11-27"
depends_on:
    - "0560"
spec_refs:
    - docs/design/publishing.md
---

## Description

This is the iOS half of the user's file-publishing request (2026-11-18, session s_b4282a8472af82af, event 2). It has its own task because iOS changes are verified separately: a Mac build plus on-device use. 0560 (share server, daemon RPCs, web) supplies the RPCs; the iOS code landed in the same commit, `adc40d0`.

## Acceptance criteria

- When the daemon reports a publish target, the iOS file viewer menu offers "Publish to <name>".
- After publishing, the link can be copied and shared through the system share sheet, and the viewer shows whether the current contents are already published.
- Published links for the file can be unpublished from the viewer.
- The change builds and its UI is checked in a simulator on the configured Mac.

## Outcome

adc40d0 shipped the file viewer publication menu. Mac tests and a simulator pass exercised publishing, sharing and revocation against a scratch share server. The owner confirmed file publishing on device.

Commit: Close the iOS file publishing device review
