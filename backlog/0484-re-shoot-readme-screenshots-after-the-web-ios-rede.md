---
id: "0484"
title: Re-shoot README screenshots after the web/iOS redesign
status: done
priority: 3
created: "2026-10-28"
updated: "2026-11-12"
depends_on:
    - "0482"
    - "0483"
spec_refs: []
---

## Description

After the web (0482) and iOS (0483) redesigns land, regenerate docs/images/web/*.png and docs/images/ios/*.png with the existing recipes (.ycc/tmp/readme-shots/README.txt) so the README matches.

## Outcome

Regenerated all 9 README images (docs/images/ios/*.png 540x1174, docs/images/web/*.png 1600x1000) on HEAD's binary and iOS build, replaying the 2026-10-26 demo data with time-shifted logs and a stall mock so the same scenes (running loops, pending 3-question ask, unread work, pixelpad fix/commit, b4a60422 diff, finished feedr loop) match the existing captions. Recipe in untracked .ycc/tmp/readme-shots/README.txt.

Commit: docs: re-shoot README screenshots after the web/iOS redesign (0484)
