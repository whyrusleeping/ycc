---
id: "0298"
title: Markdown table rendering on ios agent responses needs work
status: in_review
priority: 3
created: "2026-08-08"
updated: "2026-08-08"
depends_on: []
spec_refs: []
---

## Description

## Acceptance criteria

## Plan

Add GFM table support to the iOS markdown renderer.

Problem: clients/ios/App/MarkdownText.swift has no table handling — pipe tables in agent responses fall through to the inline-markdown paragraph path and render as raw `| a | b |` text.

1) YccKit parser (headless-testable, per the project's "logic in YccKit, thin SwiftUI shell" rule):
   - New file clients/ios/YccKit/Sources/YccKit/MarkdownTable.swift with a public `MarkdownTable` model (header cells, body rows, per-column alignment: leading/center/trailing) and a parser entry point that, given an array of lines and a start index, returns `(table, consumedLineCount)` or nil.
   - GFM rules, pragmatic subset: first line is the header row (contains at least one unescaped `|`), second line must be a valid delimiter row (cells matching `:?-+:?` with optional outer pipes), subsequent lines that contain a pipe are body rows; stop at first blank/non-row line. Cell splitting honors `\|` escapes; leading/trailing outer pipes optional; rows are padded/truncated to the header's column count (GFM behavior). Delimiter alignment colons map to column alignment.
2) YccKit tests: new clients/ios/YccKit/Tests/YccKitTests/MarkdownTableTests.swift — basic table, alignment colons, escaped pipes, ragged rows (pad/truncate), no-delimiter → nil (stays a paragraph), optional outer pipes, table terminated by blank line/prose, inline markdown left intact inside cells (parser returns raw cell strings).
3) MarkdownText.swift: restructure the block scanner from for-in over lines to an index-based loop so it can look ahead: at each prose position, try the YccKit table parser; on success flush prose, append a new `.table(MarkdownTable)` block, and skip the consumed lines. Add a `TableBlock` view: horizontally scrollable `Grid` in the same card styling as CodeBlock (secondary 0.1 background, 0.15 stroke, cornerRadius 8), semibold header row + divider, cells rendered via the existing inline `rendered(_:)` helper with per-column alignment, `.font(.subheadline)`-ish sizing consistent with the rest, text selection enabled.
4) No behavior change for non-table content; code fences still win (table detection only runs outside fenced blocks).

Verification: no Swift toolchain in this environment — careful review + user runs `swift test` in clients/ios/YccKit and builds on device. Task ends `in_review` (project convention for iOS work awaiting on-device use); left uncommitted alongside the other in_review iOS work.

### Starting points
- clients/ios/App/MarkdownText.swift — the whole renderer; blocks scanner is `private var blocks: [Block]`
- clients/ios/YccKit/Sources/YccKit/DiffFormatter.swift — example of a pure YccKit logic type with tests
- clients/ios/YccKit/Tests/YccKitTests/ — test style to mirror (XCTest)
- iOS 17 deployment target: SwiftUI Grid/GridRow available
- MarkdownText already imports YccKit? No — add `import YccKit`; App target links YccKit (project.yml)

## Work log
- 2026-08-08 plan: Add GFM table support to the iOS markdown renderer.  Problem: clients/ios/App/MarkdownText.swift has no table handling — pipe tables in agent responses fall through to the inline-markdown paragraph 
…[truncated]
- 2026-08-08 context hints: 5 recorded with plan
- 2026-08-08 context hints: clients/ios/App/MarkdownText.swift is preloaded — the whole current renderer; clients/ios/YccKit/Sources/YccKit/DiffFormatter.swift + its tests — example of a pure-logic YccKit type and test style
…[truncated]
- 2026-08-08 preload: 3 file(s), ~17 KiB seeded into implementer context
- 2026-08-08 implementer report: Implemented task 0298 in the three allowed files: - Added `MarkdownTable` to YccKit: a Foundation-only, public/Sendable/Equatable model and look-ahead parser supporting GFM-style delimiters, leading/c
…[truncated]
- 2026-08-08 review tier: single-opus — reviewers: sol
- 2026-08-08 review (sol): accept — The change correctly implements the requested GFM table subset in YccKit, with a public value model, look-ahead parser, alignment handling, escaped-pipe splitting, row normalization, and focused XCTes
…[truncated]
- 2026-08-08 usage: 507,996 tok (in 296,860, out 24,000, cache_r 494,214, cache_w 23,730) · cost n/a (unpriced)
  implementer: 347,256 tok (in 183,306, out 11,374, cache_r 152,576, cache_w 0) · cost n/a (unpriced)
  reviewer:sol: 152,419 tok (in 113,538, out 4,321, cache_r 34,560, cache_w 0) · cost n/a (unpriced)
  coordinator: 8,321 tok (in 16, out 8,305, cache_r 307,078, cache_w 23,730) · cost n/a (unpriced)
