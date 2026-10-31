---
id: "0494"
title: Remove deployment-specific sensitive material from the open-source workspace
status: done
priority: 1
created: "2026-10-31"
updated: "2026-10-31"
depends_on:
    - "0493"
spec_refs: []
---

## Description
After preserving operational material in the private infrastructure workspace, scrub the shareable working tree and generated work files of personal hostnames, addresses, access/deployment details and incident artifacts. Preserve generic security/development lessons and unrelated work. Audit current tracked/untracked content and distinguish published Git history and active private runtime logs, which must not be silently rewritten. Verify privacy cleanup and report remaining history/runtime boundaries.

## Acceptance criteria

## Work log
- Preserved operational evidence privately, removed the new deployment-specific reports/tasks from the shareable tree, relocated inactive scratch work, and sanitized older reports, memory, backlog examples and test fixtures while retaining generic technical lessons.
- Targeted identifier scans and all nine README screenshot OCR checks passed; focused credential/daemon/server tests passed. Swift edit was a string-only fixture change; no platform build was needed for that edit.
- This is current-tree cleanup, not historical erasure. Older committed copies and active ignored session/database/attribution state were not rewritten; further history/retention review is separately gated in the private workspace.
