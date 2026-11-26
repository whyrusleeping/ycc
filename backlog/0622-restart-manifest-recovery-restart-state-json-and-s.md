---
id: "0622"
title: Restart manifest recovery (restart-state.json) and supervised bash launches on by default
status: done
priority: 2
created: "2026-11-26"
updated: "2026-11-26"
depends_on:
    - "0616"
    - "0621"
spec_refs:
    - docs/design/daemon-restart.md#5. Passive resource ownership, restart manifest, leases, and crash policy
---

## Description

Split from 0616 (Slice 4 of restart-safe daemon sessions, `docs/design/daemon-restart.md` §5) on 2026-11-26 by the work-loop session that took 0616; this is the authoring agent's split, not a user decision. It delivers the parent's restart-manifest and "supervised launches on by default" criteria, which need both the persisted `wait` (0616, now scoped to that) and crash-safe job-bound leases (0621, passive lease ownership and `leases.json`) to exist first.

Status: todo. Depends on 0616 and 0621.

### Acceptance criteria
- Graceful shutdown writes generation-bound `<state-dir>/restart-state.json` (`0600`, atomic rename, `restart_id`, per-session `last_seq`, `waiting_sessions` for any unresolved `wait_started` or `StatusWithJobContinuation() == (StatusIdle, true)`, non-loop `interrupted_sessions`, and unexpired `unbound_leases`). If writing it fails, the daemon logs the error and still finishes clean detach.
- Startup consumes it idempotently across repeated crashes using `(restart_id, last_seq)` authorization (an entry whose log already recorded `session_reopened(restart_id)` or advanced past `last_seq` is skipped), writes each prepared session's `session_reopened(restart_id)` before execution is released, removes the file once preparation completes, and quarantines a corrupt entry rather than looping.
- Waiting sessions reopen through the 0616 paths (restored `wait`, wake-eligible idle). Interrupted sessions reopen and continue when history already owes a model response; otherwise one durable synthetic restart-context event (never an apparent human `user_input`) is recorded. `ask_user` sessions stay closed on disk until answered, their leases held passively.
- No model turn starts automatically after an unplanned crash without a manifest.
- Supervised bash launches are enabled by default for persistent `ycc daemon` (`Options.Persist == true`); `OriginWorkLoop` sessions stay excluded from automatic restart reopening (`spec.md` §9.1 preserved until 0617, Slice 5).
- `spec.md` §3.1, §5.1, §11 and `docs/design/async-jobs.md` / `docs/design/daemon-restart.md` match the implemented behavior.

## Outcome

Graceful shutdown writes <state-dir>/restart-state.json (0600, atomic, restart_id, per-session last_seq, waiting_sessions for unresolved wait or awaiting idle, non-loop interrupted_sessions, unbound_leases bound to their exact lease_acquired seq). Suspension keeps unexpired unbound leases (live() honours the restart cause); if the manifest is not written they are recorded as ended. Startup RecoverRestart (after RestoreLeases, execution held) restores carried leases only if the holder log still holds that acquisition, decodes entries one by one (malformed/over-attempted ones quarantined), authorizes by restart_id + last_seq before counting attempts, reopens via the 0616 paths, writes restart_context (synthetic, replayed as context) before session_reopened(restart_id) for interrupted sessions owing no response, and removes the file (or keeps only failed entries, without leases). Excludes ask_user, errored/paused turns, work-loop, watch, groom and integration sessions; subagent session_error no longer holds/disarms the coordinator. ycc daemon requires and sets a jobshim launcher; ExpectWatches gives recovered chats watch tools. No crash manifest means no automatic turns. Docs: spec §3.1/§5.1/§7.3/§11, async-jobs.md, daemon-restart.md. go test ./... passes; opus+sol accepted round 6. Not deployed.

Commit: session: restart manifest (restart-state.json) reopens waiting and interrupted sessions after a graceful restart, carries unbound leases; supervised bash launches on by default in ycc daemon (0622)
