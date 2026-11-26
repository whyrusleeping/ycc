---
id: "0624"
title: Keep the iOS keyboard dismissed while rereading a session
status: done
priority: 3
created: "2026-11-26"
updated: "2026-11-26"
depends_on: []
spec_refs: []
---

## Description

After typing a draft in an existing session and scrolling back through the transcript, the keyboard should dismiss and stay dismissed. The draft remains intact and tapping the composer resumes editing.

## Outcome

Separated editing-state notifications from one-shot focus requests. Reproduced the keyboard bounce on the baseline and verified on an iPhone simulator that scrolling back keeps the keyboard hidden, preserves the draft, and allows tap-to-resume.

Commit: iOS: keep the keyboard dismissed when scrolling a session
