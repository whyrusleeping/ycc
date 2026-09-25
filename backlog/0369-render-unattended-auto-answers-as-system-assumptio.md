---
id: "0369"
title: Render unattended auto-answers as system assumptions in iOS
status: done
priority: 2
created: "2026-09-08"
updated: "2026-09-25"
depends_on: []
spec_refs:
    - §11 Questions, unattended work, and confirmation
    - §18.3 Structured questions
---

## Description

The daemon stamps auto:true on unattended question/answer events, but iOS ignores it: an automatic question can appear as a pending human gate and its canned answer can be attributed to the user. Evidence: internal/session/interaction.go auto-answer emission; clients/ios/YccKit/Sources/YccKit/SessionProjection.swift applyQuestionAsked/foldAnswer. TUI/web already distinguish automatic handling.

## Acceptance criteria

- Auto-answered questions never open an interactive answer sheet or imply a required user action.
- Transcript presentation clearly labels unattended/system assumption and preserves the original question and automatic response as evidence.
- Real human answers remain distinguishable, including batched questions and answers from another client.
- Tests cover live auto question+answer arrival, transcript catch-up, duplicate replay, mixed human/automatic exchanges, and legacy events without the flag.
- Preserve question coalescing and authoritative durable-answer behavior.

## Outcome

iOS SessionProjection folds auto:true question_asked into a new non-interactive `.assumption(questions:response:)` row (full batch prompts/options + automatic response), never sets pendingQuestion; auto answers fold into it (and convert an unflagged ask). Human/legacy questions unchanged. Daemon sessionview index keeps `auto` in summaries and no longer exposes auto questions as pending state (schema v3 rebuild). SwiftUI AssumptionRowView renders it as "Unattended — system assumption". Verified go build/vet/test sessionview, 59 SessionProjectionTests via Docker Swift 6.2 scratch package; SwiftUI view not compiled (needs Xcode). Standard review accepted.

Commit: ios+sessionview: render unattended auto-answers as system assumptions, never pending gates (0369)
