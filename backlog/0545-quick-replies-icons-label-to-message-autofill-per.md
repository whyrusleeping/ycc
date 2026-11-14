---
id: "0545"
title: 'Quick replies: icons, label-to-message autofill, per-project editing on iOS, livelier swipe menu'
status: in_review
priority: 2
created: "2026-11-11"
updated: "2026-11-11"
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

## Work log
- 2026-11-11: Proto `QuickReply.icon` is a free-form snake_case name of up to 32 characters, stored in both lists. The defaults are check/forward/upload. YccKit adds `QuickReplyIcons` (24 icons; unknown names fall back to the default's icon, else bolt) and `QuickReplyScope`. `QuickRepliesEditorModel` now edits Global plus every project's extras and saves each changed list. `labelFillingMessage`/`message` handle the autofill on iOS, and web does the same with a `follows` flag. Web keeps icons when it saves. Swipe menu rows are driven by per-row state (spring with a stagger, sweeping out from a hinge past the edge at the finger) and tuck back in 0.24 s before the menu drops. Tests pass: Go server/session, web (449), YccKit filter, and xcodebuild test. Checked on simulator ycc-review-A against ios-review :8841: add with autofill, bug icon and the wheeler project; moving a reply to ycc-demo keeps its editor open; Save wrote all three lists (checked with GetQuickReplies); recorded open and close frames. Left in_review for on-device feel.
- 2026-11-14 groom: removed the duplicate Work log and empty Acceptance criteria headings. Glossed 0526, cited the owner feedback session, and added a current-state line with commits c04daca and 22f94bf and the 0549 icon-persistence report.
