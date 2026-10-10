---
id: "0442"
title: 'Web quick capture: auto-focus the description textbox when opened via ⌥N'
status: todo
priority: 3
created: "2026-10-09"
updated: "2026-10-09"
depends_on: []
spec_refs: []
---

## Description
Pressing Option+N (Alt+N) opens the quick-capture dialog, but the "Describe the task" textarea doesn't get focus, so you can't start typing right away.

**Likely cause (not confirmed):** `CaptureDialog.tsx` puts `autoFocus` on the textarea. But `ui/Modal.tsx` only calls `dialog.showModal()` in a `useEffect`, which runs after React has already applied `autoFocus` while the children mount. `showModal()` then runs the dialog's own focusing steps and moves focus to the first focusable element. That's the × close button in `.modal-head`, or the Project `<select>` when there are several projects.

**Possible fix:** Focus the textarea after `showModal()` has run. Options: an `autofocus` attribute that the dialog respects, an `initialFocus` ref/prop on `Modal`, or a focus call in an effect/rAF after open. Choose whichever works for all modals, since NewTaskDialog and other dialogs probably have the same problem. On macOS, also check that the Option+N keystroke doesn't put a stray "˜" (dead-key) character into the textarea once it has focus.

**Acceptance criteria**
- Opening quick capture with ⌥N / Alt+N, the button, or the command palette focuses the description textarea, and typing goes straight into it.
- This holds both with a single project and with several (when the Project select is shown).
- The shortcut keystroke itself doesn't insert a character (such as "˜" on macOS) into the textarea.
- "Capture another" puts focus back in the textarea, and the clarifying-question input still gets focus as it does today.
- There's a test covering initial focus (unit/RTL, or Playwright if available).

## Acceptance criteria

## Work log
