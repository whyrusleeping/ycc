---
id: "0503"
title: 'iOS: render markdown images of workspace files inline in transcripts'
status: in_review
priority: 3
created: "2026-11-09"
updated: "2026-11-09"
depends_on: []
spec_refs:
    - spec.md#18. Client interaction model
---

## Description
Agents are now prompted (internal/orchestrator/prompts.go `imageGuidance`) to embed pictures as markdown images naming workspace files, e.g. `![login screen](.ycc/tmp/login.png)`. The web client already renders these (clients/web/src/features/markdown/MarkdownImage.tsx, via ReadFile). iOS `MarkdownText` (clients/ios/App/MarkdownText.swift, YccKit `MarkdownBlocks`) does not: inline-only `AttributedString(markdown:)` drops/shows only alt text.

Spec: spec.md §18 (agent markdown images render inline through ReadFile; remote image URLs never fetched).

Acceptance criteria:
- A line consisting of a markdown image (`![alt](path)`) whose destination resolves to a workspace file via `FileReference.fromLink` with the installed `FileLinkContext` renders as an inline image (fit to width, bounded height) fetched with ReadFile from the session's file root (worktree-aware), in transcripts, final reports, and other `.fileLinks` surfaces.
- Tapping it opens the existing file viewer (zoomable image) sheet.
- Loading, unreadable, non-image, or too-large files fall back to the alt text (tappable link when resolvable); remote `http(s)` images are never fetched and show as a link with the alt text.
- YccKit unit test for block parsing of image lines; built and tested on the configured Mac (xcodebuild + `swift test`).

## Acceptance criteria

## Work log
