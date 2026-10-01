---
id: "0409"
title: 'Memory: surface memory pressure + one-tap groom in TUI and iOS'
status: in_review
priority: 3
created: "2026-09-30"
updated: "2026-09-30"
depends_on: []
spec_refs: []
---

## Description
GetMemory reports active bytes, soft/hard budgets, and any running auto-groom session. iOS MemoryView shows a size gauge vs budget and a "Groom now" action (starts memory-groom preset session) / link to the running groom. TUI shows the memory pressure on the memory-groom home preset entry. Proto change: commit both Go and Swift regens.

## Acceptance criteria

## Work log
