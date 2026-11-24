---
id: "0602"
title: Choose allowed spawn_agent models in new-session settings
status: done
priority: 3
created: "2026-11-24"
updated: "2026-11-24"
depends_on: []
spec_refs: []
---

## Description

Add a session-only subagent model choice alongside mode in the web and iOS new-session menus. Unrestricted sessions retain the existing enabled catalog. An explicit selection, including none, constrains the models the generic spawn tool advertises and can actually use, including when starting a separate workstream. Other sessions and global settings stay unchanged.

## Outcome

The web and iOS New Session menus offer all enabled models, a selected subset, or no generic subagents. The daemon enforces and restores the choice for that session, including workstream starts, without changing role assignments or defaults. Verified with Go, web and YccKit tests, a browser against a mock-backed daemon, and an iOS simulator.

Commit: Let new sessions choose which models spawn_agent can use
