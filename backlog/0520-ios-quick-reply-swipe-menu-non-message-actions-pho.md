---
id: "0520"
title: 'iOS quick-reply swipe menu: non-message actions (photo, new session, voice)'
status: done
priority: 4
created: "2026-11-10"
updated: "2026-11-22"
depends_on:
    - "0514"
spec_refs: []
---

## Description

Status: todo. The user accepted it on 2026-11-13 by changing its status on iOS (client analytics `task.status` to todo at 03:20:16Z, matching the file's change time). Which actions go in the menu has not been chosen.

This idea came from designing 0514 (iOS quick-reply swipe menu in the session view, done). The swipe menu could also hold actions that aren't messages, such as attaching a photo, starting a new session, or voice input. The menu would mix them with quick replies, like the reference app the owner shared (session s_0f66864a490b7ffc, event 2). Asked whether the menu should hold only replies or also actions, the owner answered "agree that would be good later, will think on it" (same session, event 29, 2026-11-10).

**Acceptance:** chosen actions appear in the swipe menu, set visually apart from message replies, and trigger the same flows as their existing buttons.

## Outcome

The iOS swipe menu now has two actions below the quick replies, set apart by a gap and a dashed accent outline. Add pictures opens the composer's Photos picker. New chat opens the New chat composer in the session's project. Both run at once and record composer.menu_action. The open menu renders and picks from rows captured at swipe start. Crowded columns shrink their rows to fit, so 24 replies plus both actions fit on screen. Voice was left out because the app has no voice button; the action set is an unattended assumption for the owner to confirm. Verified on the Mac: swift test 682/682, xcodebuild build and test, and simulator checks in light and dark of both flows and the full 26-row menu.

Commit: iOS: Add pictures and New chat actions in the quick-reply swipe menu (0520)
