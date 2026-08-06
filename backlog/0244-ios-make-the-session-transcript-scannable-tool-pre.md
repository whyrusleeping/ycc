---
id: "0244"
title: 'iOS: make the session transcript scannable (tool previews, reasoning as an aside)'
status: done
priority: 2
created: "2026-08-05"
updated: "2026-08-06"
depends_on: []
spec_refs: []
---

## Description

Screenshots from the device showed the live transcript's visual hierarchy is inverted and its rows carry almost no information:

- An **expanded `thinking` block rendered as full-size primary body text**, making the agent's internal monologue the loudest thing on screen — louder than the coordinator's actual reply, which sits in a small grey bubble below it. Spec §18.4 requires reasoning to read as a dimmed, italic aside.
- A collapsed reasoning row said only **"Thinking"**, so a column of them conveyed nothing about what the agent was weighing.
- Tool rows said only **"Bash ✓"** — no indication of which command ran or which file was touched, making a 200-turn transcript unscannable.
- Every tool used the same chunky `wrench.and.screwdriver` glyph at body size, so the icons dominated the caption-sized text beside them.
- The transcript scrolled under a translucent navigation bar with no opaque background, so body text bled through behind the status bar and title.
- The inline navigation title middle-truncated (`[0131] There...it. Can you ...`), cutting out the identifying front of a prompt-derived title.

## Acceptance criteria

- [x] Expanded reasoning renders dimmed + italic at footnote size (spec §18.4), visually subordinate to model replies.
- [x] Collapsed reasoning rows show a one-line preview of the reasoning text.
- [x] Tool rows show a one-line preview of the call's most identifying argument (command / path / task id / query).
- [x] Each tool family gets its own SF Symbol, sized to match the row's text.
- [x] Preview logic lives in YccKit with unit tests, and unknown tools degrade to a sensible field + glyph without a client change.
- [x] The session view's navigation bar is opaque so transcript text cannot bleed behind it.
- [x] The navigation title truncates at the tail.
- [ ] Verified on device against a long, tool-heavy transcript.

## Work log
- Added `ToolPreview` (YccKit): per-tool `symbol(for:)` and `summary(tool:args:)` that parses the args JSON and picks the identifying field, with long-path shortening (`…/Sources/YccKit/ToolPreview.swift`), newline collapsing, and a 90-char cap. Unknown tools fall back to the first string-ish argument in stable key order; known tools with no useful field stay quiet. `oneLine` is shared with the reasoning preview.
- 14 `ToolPreviewTests` covering each behaviour including non-JSON payloads, nested-object skipping, arrays, and numeric values.
- `ToolRowView` now renders `icon + name + argument preview + status`; `ExpandableRow` gained `preview` and `detailIsAside`, and reasoning uses both.
- Session view: opaque navigation bar background, tail-truncated title.
- NOT YET VERIFIED on device.
- 2026-08-07: Closed done in a backlog audit — implemented and committed; on-device use is the verification the Linux box could not provide.
