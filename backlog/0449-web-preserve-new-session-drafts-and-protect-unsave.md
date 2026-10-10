---
id: "0449"
title: 'Web: preserve new-session drafts and protect unsaved model/tier edits'
status: done
priority: 2
created: "2026-10-10"
updated: "2026-10-10"
depends_on: []
spec_refs:
    - docs/design/web-client.md#Session input and pictures
---

## Description
Accepted UI audit improvement 1. Keep new-session drafts (text, project, mode, model, preset and staged pictures) for the life of the tab when navigating away and back. Applying a preset must not silently destroy a nonempty edited prompt. Model/review-tier editors must preserve drafts or confirm discard on close, including Escape, and must not disappear during a save. Preserve existing in-flight web work. Verify observed draft-loss cases; avoid persisting sensitive drafts beyond the tab unnecessarily.

## Outcome
Implemented tab-local draft/picture ownership across navigation, guarded preset replacement, and model/tier discard/save-close guards. Native Chromium verified retained text and picture previews, cancelled preset replacement and nested Escape behavior. Focused regressions and full web build/test pass. Drafts deliberately do not survive reload/tab closure. Commit: `web: polish personal inbox and desktop UX` (includes pre-existing changes at the user's request).
