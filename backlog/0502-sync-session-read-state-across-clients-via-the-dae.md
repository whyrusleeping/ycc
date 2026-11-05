---
id: "0502"
title: Sync session read state across clients via the daemon
status: in_review
priority: 2
created: "2026-11-05"
updated: "2026-11-05"
depends_on: []
spec_refs:
    - docs/design/web-client.md
    - docs/design/ios-client.md
---

## Description
## Problem
Unread state is a client-side, per-device watermark: iOS keeps it in `UserDefaults` (`YccKit/SessionReadStore.swift`), the web in per-origin `localStorage` (`clients/web/src/features/sessions/readStore.ts`). The daemon has no read state (see 0283; web-client.md: "Unread state is a client-side watermark per session kept in browser storage"). So reading a session on one client never clears it on another. Every browser origin (localhost or tunnel) and every reinstall also starts its own baseline.

Ruled out as causes: summary and event timestamps share the same millisecond RFC3339 format. The idle reaper and daemon shutdown (`ReclaimAll`) write no trailing events, so on a single device marks do hold.

## Proposed design
- **One shared mark per session**, stored on the daemon in `.ycc/session-read-marks.json` per workspace, following the `session-follow-ups.json` sidecar pattern (atomic temp+rename, mutex). The design assumes a single-user harness, so the mark belongs to the user, not to a device.
- **Wire:** `SessionSummary.read_through` (RFC3339, daemon clock) on both live and persisted paths, plus `MarkSessionsRead(project, repeated {session_id, through})`. The mark is monotonic and never moves back, and the RPC returns the resulting marks. A batch form covers "mark all read".
- **Baseline for new sessions:** the daemon records `baseline_at` when the file is first created. A session with no mark is unread only if its `last_activity` is after `baseline_at`. This replaces the per-device watermark.
- **Clients:** web and iOS read `read_through` from summaries and write through `MarkSessionsRead`, keeping local marks only as an optimistic cache (max-merge with the server). On their first sync they upload existing local marks once, so current read state is kept. The existing rules stay client-side: running or awaiting-jobs sessions are never unread, automatic sessions are excluded from the inbox, and iOS still opens unread sessions at the newest reply.
- Changes reach other clients on their next list refresh (web poll, iOS foreground/refresh).
- Optional: the TUI marks read through what it displayed, so sessions driven from the terminal don't show as unread elsewhere.

## Acceptance
- Reading a session (opening it, row "mark read", or "mark all read") on iOS clears its unread marker on the web after the next list refresh, and vice versa.
- Marks survive daemon restarts, browser origin changes and app reinstalls. Existing local marks migrate without a flood of unread rows.
- Go protobuf and Swift protobuf+Connect outputs are regenerated together. Go tests cover monotonic merge, baseline and file corruption handling. Web unit tests cover merge and migration. YccKit tests run on the configured Mac, along with an iOS simulator check.
- The web/iOS design docs and spec are updated to describe daemon-held read state.

## Acceptance criteria

## Work log
- 2026-11-05: Implemented as designed, built in a separate worktree because other sessions had uncommitted proto, server and sidebar work. The daemon side is `internal/session/readmarks.go` (sidecar file, monotonic, clamped to now, unknown ids skipped, a corrupt file is replaced) plus the `MarkSessionsRead` RPC and `SessionSummary.read_through`, with Go, web and Swift regenerated. An unmarked session reports the baseline, and before any mark the field is empty, which clients treat as read.
  - Web (`readStore.ts`) and iOS (`SessionReadStore`) drop the per-device watermark. Their effective mark is the later of the local and daemon marks. Listing never creates marks; local marks ahead of the daemon are re-pushed, which also migrates old local marks. Web pushes are coalesced over 1.5s and flushed on unmount, pagehide or hidden.
  - Verification: Go suite passes. Web typecheck and tests pass (423). YccKit `swift test` passes on the Mac (560) and the app builds and runs its test target. The scratch-daemon Playwright e2e (`.ycc/tmp/readsync-e2e/e2e.mjs`) passes 15/15, using two browser contexts as two devices and RPC as iOS: no marks from listing, daemon→web, RPC read clears the web on the next refresh, the web dot and opening a session reach the daemon, and a second browser follows.
  - Not done: an iOS simulator or device check against a live daemon (in_review per iOS convention). The optional TUI marking was left out; sessions driven only from the terminal still show unread elsewhere until read there.
