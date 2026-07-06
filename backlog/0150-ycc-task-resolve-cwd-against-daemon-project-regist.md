---
id: "0150"
title: 'ycc task: resolve cwd against daemon project registry (avoid misfiled adds)'
status: proposed
priority: 4
created: "2026-07-06"
updated: "2026-07-06"
depends_on:
    - "0143"
spec_refs: []
---

## Description
`ycc task` (task 0143) prefers a reachable local persistent daemon; without `--project` the RPCs then target the daemon's *default workspace*. If the user runs `ycc task add` from a different repo than the daemon's default, the task is filed into the wrong backlog. Flagged as a nit in review of 0143.

Improve resolution: when using a local daemon without `--project`, resolve the CLI's cwd/--workspace against the daemon's project registry (ListProjects) and pass the matching project name; if the cwd matches no registered project, fall back to the direct docs.Store on the cwd (with a note), rather than silently writing to the daemon's default workspace.

## Acceptance criteria
- [ ] `ycc task add` from a registered project's directory files the task into that project via the daemon.
- [ ] `ycc task add` from an unregistered directory (local daemon running) writes to the cwd backlog directly instead of the daemon default workspace.
- [ ] `--project` and `--addr` behavior unchanged.


## Work log
