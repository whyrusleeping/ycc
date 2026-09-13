---
id: "0345"
title: Make iOS composer Return insert a newline instead of sending
status: in_review
priority: 3
created: "2026-09-08"
updated: "2026-09-08"
depends_on: []
spec_refs:
    - docs/design/ios-client.md
---

## Description
User reports accidental sends when pressing Return. Existing-session ComposerTextField passes onSubmit: send while NewSessionView does not, causing inconsistent behavior.

Remove return-key submission from the shared composer; keep explicit send controls. Return must insert a newline in both new and existing session drafts, including hardware keyboards, without losing image paste, focus, or autocorrect handling. Update iOS design and native-input smoke checklist. Verify on device/Mac (no Swift toolchain available here).

## Acceptance criteria

## Work log
