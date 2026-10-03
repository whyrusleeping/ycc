---
id: "0412"
title: 'Desktop web: rich transcript — markdown, syntax highlighting, tool previews, diffs, search'
status: todo
priority: 3
created: "2026-10-02"
updated: "2026-10-02"
depends_on:
    - "0410"
spec_refs:
    - docs/design/web-client.md#Rendering safety
    - Client interaction model
---

## Description
Third phase of docs/design/web-client.md. Reference iOS MarkdownBlocks/MarkdownTable, SyntaxHighlighter, ToolPreview, DiffFormatter/DiffView, and the TUI transcript search.

- Markdown for model turns: GFM tables and lists, raw HTML disabled, links restricted to safe schemes. Fenced code gets syntax highlighting over escaped tokens, plus copy buttons.
- Tool rows: concise previews per tool (bash command + exit, edit path + hunk summary, read path, etc.), expanding to full detail in place or in the inspector.
- Commit references open GetCommitDiff in the inspector, with a highlighted unified diff and a truncation notice. A "working changes" panel uses GetWorkingChanges, including snapshot-linked review verdicts.
- File paths in the transcript link to the file viewer once the files phase lands. Until then they render as copyable text.
- Transcript search (Ctrl/Cmd-F within the session) with next/prev jump. It works across paged history by loading earlier pages as needed.

## Acceptance criteria
- [ ] Markdown containing `<script>`/`<img onerror>` and javascript: links renders inertly (covered by unit tests).
- [ ] Diffs and code blocks are highlighted, and copy buttons copy the exact source text.
- [ ] Search finds a match in an earlier, not-yet-loaded page and scrolls to it.

## Work log
