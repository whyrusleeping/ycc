---
id: "0520"
title: 'iOS quick-reply swipe menu: non-message actions (photo, new session, voice)'
status: todo
priority: 4
created: "2026-11-10"
updated: "2026-11-13"
depends_on:
    - "0514"
spec_refs: []
---

## Description
Status: todo. The user accepted it on 2026-11-13 by changing its status on iOS (client analytics `task.status` to todo at 03:20:16Z, matching the file's change time). Which actions go in the menu has not been chosen.

This idea came from designing 0514 (iOS quick-reply swipe menu in the session view, done). The swipe menu could also hold actions that aren't messages, such as attaching a photo, starting a new session, or voice input. The menu would mix them with quick replies, like the reference app the owner shared (session s_0f66864a490b7ffc, event 2). Asked whether the menu should hold only replies or also actions, the owner answered "agree that would be good later, will think on it" (same session, event 29, 2026-11-10).

**Acceptance:** chosen actions appear in the swipe menu, set visually apart from message replies, and trigger the same flows as their existing buttons.

## Work log
- 2026-11-14 groom: recorded the 2026-11-13 acceptance (iOS status change, from client analytics) and dropped the stale "(if accepted)" and "wants to think on it" wording. Glossed 0514, cited the owner's idea and reply in session s_0f66864a490b7ffc, and removed the empty Acceptance criteria heading. Flagged the unchosen action set in the "Backlog groom: decisions needed" task.
