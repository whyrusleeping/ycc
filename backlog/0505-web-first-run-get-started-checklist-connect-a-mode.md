---
id: "0505"
title: 'Web first-run: Get started checklist (connect a model, add a project, onboard it); zero projects is not an error'
status: todo
priority: 2
created: "2026-11-09"
updated: "2026-11-27"
depends_on:
    - "0504"
spec_refs:
    - spec.md#19. Onboarding
---

## Description
The checklist shipped in 8bb9c6a6. Review found one correction: its API-key instructions guess OPENAI_API_KEY for every non-Anthropic model rather than showing the coordinator model's actual key name or the daemon's remedy. Completion also depends on 0504 (first-run daemon and CLI).

Acceptance:
- On a daemon with no sessions yet, the home shows a Get started checklist in place of the empty list: (1) connect a model — done when the coordinator model's health check passes; offers in-browser subscription connect for Claude and ChatGPT, API-key instructions, and a link to Settings → Models; (2) add a project — opens the existing Add project dialog; (3) onboard it — starts the Project manager Onboard preset in that project. Each step shows done/pending, and the list disappears once a session exists.
- Zero registered projects lists no sessions instead of an error.
- The token prompt says where to find the token.
- Established daemons (any session history) never see the checklist; broken models there keep using the health banner.

## Acceptance criteria

## Work log

- 2026-11-09: Implemented `features/onboarding/GetStarted.tsx` (home page while the daemon has no sessions; asks for an immediate health re-check when the coordinator check is still pending, and re-checks after a browser Claude login); zero projects lists nothing instead of erroring; token prompt explains where the token comes from; sidebar health banner and sidebar "Start a session" hidden on an empty daemon. Verified with Playwright against the no-config daemon (model step open) and a mock-model daemon (add project → Start onboarding → session opens → home returns to the session list), light and dark. Bundle rebuilt and committed in 8bb9c6a6. The later browser ChatGPT login (0507) superseded the CLI-only ChatGPT step.
- 2026-11-27 review: On a clean scratch daemon, headless Chromium displayed token-location guidance, both subscription buttons and the Add project dialog. Web typecheck, 522 tests and the embedded-bundle check passed. Returned to todo for the incorrect key hint; retest with a coordinator using a non-default key name.
