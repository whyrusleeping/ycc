---
id: "0283"
title: 'iOS: unread agent activity badges + always ask which project a new chat starts in'
status: in_review
priority: 2
created: "2026-08-06"
updated: "2026-08-06"
depends_on: []
spec_refs:
    - docs/design/ios-client.md#Navigation shell — workspace drawer + recent-session feed
---

## Description
Two user-reported gaps on the iOS client.

**1. No "unread agent messages" indication.** When a session finished while the
phone was away, nothing on the session list said so — the row looked identical to
one the user had already read. Added client-side unread tracking (the daemon
holds no per-device read state):

- `YccKit/SessionReadStore` — per-session "seen up to here" watermark, persisted
  in `UserDefaults`, capped at 600 marks (most-recently-active kept).
- Marks are recorded from **daemon** timestamps only (`SessionProjection.
  lastEventTimestamp`, which advances on every durable event and never on a
  transient `turn_delta`), so device clock skew cannot fake unread state.
- First sighting of a session is baselined as read (no wall of false unread on
  install / after registering a project with history); a still-**running**
  session is never unread (its row already says live, and its log grows
  constantly) — it goes unread once it stops producing, i.e. when it finishes.
- `SessionView` marks read on disappear / backgrounding, through the newest
  folded event.
- UI: leading accent dot + bolder title + "new" pill on the row, `ProjectActivity
  .unread` badges in the workspace drawer and on the closed menu button, plus
  "Mark read" (row swipe + context menu) and "Mark all read" (list overflow menu).

**2. New chat no longer asked which project.** Since d39ae30 the unscoped Recent
sessions feed silently started chats in the last-*viewed* project. Restored the
chooser: a project-scoped list still starts straight away, the unscoped feed
always asks (last-viewed project listed first), and only a single-project daemon
skips the prompt.

## Acceptance criteria

- [x] Unread state is durable, daemon-clock based, and baselines first sightings.
- [x] Row, drawer and menu-button badges; mark read / mark all read affordances.
- [x] New chat from Recent sessions presents the project chooser again.
- [x] YccKit unit tests: `SessionReadStoreTests`, unread cases in
      `SessionListModelTests`, `lastEventTimestamp` in `SessionProjectionTests`.
- [x] `docs/design/ios-client.md` §6 updated (unread rules + new-chat chooser).
- [ ] Verified on device: a session that finishes while the app is elsewhere
      shows the unread badge, opening it clears it, and New chat asks.


## Work log
