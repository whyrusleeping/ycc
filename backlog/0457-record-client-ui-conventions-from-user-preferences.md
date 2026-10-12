---
id: "0457"
title: Record client UI conventions from user preferences in the design docs
status: proposed
priority: 5
created: "2026-10-10"
updated: "2026-10-10"
depends_on: []
spec_refs: []
---

## Description
Two user-stated UI preferences exist only in project memory and appear nowhere in the spec or the client design docs. The memory-groom pass on 2026-10-10 found no duplicate task.

- legacy-62aef247d2ed: "Home-menu action affordances must be ctrl-chords, never naked letter keys." (TUI home menu)
- legacy-c20c46c22190: "iOS project rename/removal live only in drawer rows' long-press context menu — intentional." (current code: clients/ios/App/LandingView.swift `drawer.rename_project` via contextMenu)

Proposal: once the user approves, add each rule as one sentence: the TUI rule in the spec's TUI/client section (or docs/tui-components.md) and the iOS rule in docs/design/ios-client.md. Then retire both memory notes.

Acceptance:
- [ ] The user confirms both preferences are still current, and where each should be recorded.
- [ ] Each rule is stated once in the chosen doc, following docs/design/doc-style.md.
- [ ] legacy-62aef247d2ed and legacy-c20c46c22190 are retired with a pointer to the doc.

## Acceptance criteria

## Work log
