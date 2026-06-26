---
id: "0014"
title: Daemon lifecycle — one-shot in-process default, opt-in persistence
status: done
priority: 2
created: "2026-06-26"
updated: "2026-06-26"
depends_on:
    - "0003"
spec_refs:
    - System architecture
    - Package layout
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
- [x] plain `ycc` no longer leaves a daemon running after exit unless persistence was
      explicitly requested (`ycc daemon` or `ycc --background`)
- [x] the one-shot daemon runs in-process on an ephemeral address (no fixed :8787, no
      setsid) and is reliably torn down on exit, including ctrl-c / panic
- [x] plain `ycc` attaches to an already-running persistent daemon at the well-known
      address when present
- [x] `ycc daemon` (foreground persistent) and `ycc --background` (detached persistent +
      attach) both work
- [x] in-process one-shot demonstrably removes the stale-binary, keyless-env, and
      port-collision footguns (no detached survivor left to go stale)
- [x] spec §3.1 / §15 match the implemented behavior; remove the old auto-start memory note

## Work log
- 2026-06-26 plan: Rework the `ycc` launch path so persistence is opt-in.  1. CLI surface (cmd/ycc main / root command):    - Default `ycc` (no `-addr`, no `daemon`, no `--background`): probe the well-known      persist
…[truncated]
- 2026-06-26 done (finished by hand after the implementer agent self-terminated):
  Implemented in cmd/ycc/main.go (resolveDaemon: -addr / --background / default
  attach-or-in-process; installSignalShutdown for ctrl-c/SIGTERM teardown),
  internal/daemon/serve.go (shared buildHandler; StartInProcess on 127.0.0.1:0 with
  Shutdown/Close), internal/daemon/client.go (EnsureLocalDaemon -> EnsureBackgroundDaemon;
  DiscoverConfig exported). Added internal/daemon/inprocess_test.go (ephemeral addr,
  reachable while up, gone after Shutdown). go test -race ./... green; go vet clean.
  Verified the live binary end to end: (A) plain `ycc` with no daemon -> in-process
  ephemeral, leaves no survivor; (B) plain `ycc` with a persistent daemon up -> attaches
  (no one-shot message); (C) `ycc --background` -> detached persistent survivor + attach;
  (D) `ycc -addr` -> explicit attach. spec §3.1/§15 already match; project memory's
  auto-start note updated to the new opt-in behavior.
- NOTE/incident: the implementer agent (running under the OLD persistent :8787 daemon)
  ran a smoke test, found that daemon, judged it "a stale orphan this task describes," and
  `kill`ed it — which was its own host, ending the session at seq 94. This is exactly the
  footgun 0014 removes; the in-process one-shot default makes it unrepeatable. A guardrail
  (don't let an agent kill the daemon hosting it) is worth considering — see 0009.
