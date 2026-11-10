---
id: "0514"
title: 'iOS: quick-reply swipe menu in the session view'
status: todo
priority: 2
created: "2026-11-09"
updated: "2026-11-10"
depends_on:
    - "0513"
spec_refs:
    - docs/design/ios-client.md
---

## Description
iOS counterpart of 0513. It uses the same built-in and project-defined quick replies from the same daemon data. On iOS they appear in a press-drag-release menu, not as chips. The owner sends most check-in replies from the phone, where a one-handed gesture saves the most effort.

**Interaction:**
- **Open:** a drag that starts within about 20pt of the trailing edge of the session view opens the menu, with a light haptic. The transcript behind it blurs, and the replies fan out in an arc around the finger's vertical position. Each option shows the label plus a preview of the message text.
- **Pick:** the option nearest the finger highlights, with a selection haptic each time the highlight changes. Nothing is selected until the finger has actually moved onto an option, so the option under the initial touch point can't be picked by accident.
- **Release:** letting go on an option sends that message. Letting go back near the edge, or away from every option, cancels.
- **Tap fallback:** a quick flick with nothing selected leaves the menu open, so the user can tap an option or tap outside to dismiss. An accessibility action provides the same choices for VoiceOver.
- **Undo window:** after a pick, a "Sent: <label> · Undo" toast shows for about 2 seconds, and the message is sent only when the toast expires. Undo cancels the send.
- **Edge handle:** a faint handle on the trailing edge makes the gesture discoverable.
- **Availability:** the menu uses the same hide rules as 0513. It is unavailable while the session is running, while a pending ask_user question has its own sheet, and while the composer has text. The menu may later work during running sessions, after the owner has used it for a while.
- A send records the same `composer.quick_reply` analytics as web, with the chip kind and never the text.

**Acceptance:** an edge swipe, then drag and release, sends the chosen reply as a normal message once the undo window passes. Undo, cancel, and tap fallback work. The handle shows and the menu is unavailable under the hide rules. The set matches web for a project. Built and tested on the configured Mac simulator. Left in_review for on-device use.

## Acceptance criteria

## Work log
