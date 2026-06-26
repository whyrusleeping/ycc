---
id: "0014"
title: Daemon lifecycle — one-shot in-process default, opt-in persistence
status: todo
priority: 2
created: 2026-06-26
updated: 2026-06-26
depends_on: ["0003"]
spec_refs: ["System architecture", "Package layout"]
---

## Description
Persistence is now opt-in (spec §3.1). Today `ycc` always spawns a *detached* `ycc daemon`
(setsid) on a fixed port 8787 that survives exit. That default caused real footguns: it
orphaned a daemon running a stale (deleted, in-tree) binary with the OLD tool surface
(`read_file` reappearing after the Read/Write/Edit rename), and it captured a stale
environment (a keyless first launch → every later session 401s). Replace the default.

New behavior for plain `ycc` (no `-addr`, no `daemon` subcommand):
1. If a persistent local daemon is reachable at the well-known address, attach to it
   (→ project picker, task 0015).
2. Otherwise start the daemon **in-process** on an ephemeral loopback address (port :0 or
   a unix socket), tied to this process and torn down on exit. cwd is the single project;
   skip the picker.

`ycc daemon` stays the explicit, persistent, foreground service. Add `ycc --background`
(opt-in) to spawn today's detached persistent daemon and attach the TUI to it. `-addr`
still attaches to a remote/explicit daemon.

Make the trade explicit in the UX: closing a one-shot `ycc` ends in-flight agent work
(no persistence). That is intended; `ycc daemon` / `--background` is the escape hatch.

## Acceptance criteria
- [ ] plain `ycc` no longer leaves a daemon running after exit unless persistence was
      explicitly requested (`ycc daemon` or `ycc --background`)
- [ ] the one-shot daemon runs in-process on an ephemeral address (no fixed :8787, no
      setsid) and is reliably torn down on exit, including ctrl-c / panic
- [ ] plain `ycc` attaches to an already-running persistent daemon at the well-known
      address when present
- [ ] `ycc daemon` (foreground persistent) and `ycc --background` (detached persistent +
      attach) both work
- [ ] in-process one-shot demonstrably removes the stale-binary, keyless-env, and
      port-collision footguns (no detached survivor left to go stale)
- [ ] spec §3.1 / §15 match the implemented behavior; remove the old auto-start memory note

## Work log
