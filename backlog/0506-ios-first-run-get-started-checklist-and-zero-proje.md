---
id: "0506"
title: 'iOS first-run: Get started checklist and zero-project empty state'
status: todo
priority: 3
created: "2026-11-09"
updated: "2026-11-09"
depends_on:
    - "0504"
    - "0505"
spec_refs:
    - spec.md#19. Onboarding
---

## Description
Bring the web first-run work (0505) to iOS: after connecting to a new daemon, the home shows the same Get started checklist (connect a model via Settings → Provider accounts / command hints, add a project, onboard it) in place of the empty Recent list; zero registered projects shows an empty state rather than an error. Build and verify on the configured Mac simulator.

Also bring the web follow-ups to iOS: a "Connect ChatGPT" provider account using the BeginOpenAILogin/CompleteOpenAILogin/CancelOpenAILogin RPCs from 0507 (Swift bindings already regenerated; the user pastes the localhost:1455 redirect URL), and the 0508 Settings reorganization and plain-language labels (main agent / reviewers / implementer, Code review, model status from GetHealth) where they apply to GlobalSettingsView.

## Acceptance criteria

## Work log
