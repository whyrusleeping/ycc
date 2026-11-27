---
id: "0545"
title: 'Quick replies: icons, label-to-message autofill, per-project editing on iOS, livelier swipe menu'
status: done
priority: 2
created: "2026-11-11"
updated: "2026-11-27"
depends_on: []
spec_refs:
    - spec.md
---

## Description

Owner feedback on 0526 (editable daemon-wide quick-reply list in Settings, in_review), given in session s_3bba4fcce9515f8e, event 2, 2026-11-11.

Status: in_review, awaiting on-device feel. Implemented in c04daca. 22f94bf lightened the swipe-menu backdrop to 85% at the owner's request (same session, event 405). A report that iOS resets chosen icons to the lightning bolt is tracked in 0549 (fix iOS quick-reply icon persistence, in_progress).

**Acceptance:**
- A quick reply has an optional icon from a fixed set. The daemon stores it in both lists, and iOS Settings offers an icon picker. The swipe menu shows the icon, and web keeps it when it saves.
- In the reply editor (iOS and web), typing a label fills in the message while the message is empty or still matches the label. After the user edits the message, the two stay independent.
- iOS Settings › Quick replies edits the daemon-wide list and every project's extras. Each reply has a "Project" picker: Global (the default) or a project. Saving writes each changed list.
- The iOS swipe menu feels like bubbles pulled out from the edge. Rows spring in from the trailing edge with a stagger and a slight fan sweep, and sweep back on close. Opening the menu is not slower than before.

## Outcome

c04daca and 22f94bf added persisted icons, scoped editing, label autofill and the revised swipe menu. Browser and simulator checks covered editing and saving; the owner confirmed editing, icons and swipe feel on device.

Commit: Close quick reply icons and swipe menu device review
