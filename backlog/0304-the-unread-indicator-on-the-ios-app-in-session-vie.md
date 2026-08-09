---
id: "0304"
title: The unread indicator on the ios app in session view feels a bit odd. It only shows one or two dots instead of one on each one thats actually unread
status: in_review
priority: 3
created: "2026-08-08"
updated: "2026-08-09"
depends_on: []
spec_refs: []
---

## Description

## Acceptance criteria

## Plan

Root cause: SessionReadStore's "first sighting is read" rule baselines EVERY unknown session at its current activity. Sessions created after install (e.g. everything a work loop produced while the phone was in a pocket) are baselined read on the very next refresh and never show an unread dot; only sessions known before AND with later activity dot up. Hence "one or two dots instead of one per actually-unread session".

Fix (YccKit only, no proto/daemon changes):
1. Add a persisted daemon-clock **watermark** to SessionReadStore: the newest activity timestamp this device has ever been shown in a session list. Persist under `key + ".watermark"` in the same UserDefaults.
2. In `noteSeen(_:)`, classify unknown sessions against the PRE-refresh watermark:
   - No watermark yet (fresh install) → baseline at the session's own stamp (read) — unchanged behavior.
   - Session's recency stamp ≤ watermark → back-catalogue (e.g. a newly registered project's history) → baseline read.
   - Stamp > watermark → genuinely new session → baseline its mark AT the watermark, so it reads as unread (isUnread compares activity > mark). A still-running one stays quiet until it stops (existing live/running guard), which is the desired behavior.
   After classifying, advance the watermark to the max parseable recency stamp across ALL listed sessions (never move backwards), and persist.
3. Legacy upgrade path: on init, when the watermark key is absent but marks exist, derive the watermark from the max parseable stored mark so the fix takes effect immediately after app upgrade instead of after one more silent baseline round.
4. Update the class doc comment (the "First sighting is read" paragraph) to describe the watermark rule.
5. Tests in SessionReadStoreTests: (a) a session first seen with activity after the watermark is unread; (b) it clears via markRead; (c) a still-running new session is not unread until it stops; (d) back-catalogue (stamp ≤ watermark) stays read; (e) fresh-install first load stays all-read; (f) watermark persists across store instances; (g) legacy stores (marks, no watermark) classify a subsequently-appearing new session as unread.

No Swift toolchain in this environment — verification is by careful review; the user runs `swift test` in clients/ios/YccKit on their Mac (task will end in_review per iOS convention, uncommitted).

### Starting points
- clients/ios/YccKit/Sources/YccKit/SessionReadStore.swift — the whole fix lives here (noteSeen, isUnread, persist)
- clients/ios/YccKit/Tests/YccKitTests/SessionReadStoreTests.swift — extend; helper session(id:lastActivity:startedAt:status:live:)
- SessionListModel.recencyDate/parseTimestamp are the shared timestamp helpers
- SessionListModel.apply calls readMarks.noteSeen(allSessions) once per refresh — no model changes needed
- Do not disturb unrelated uncommitted work in the tree (other in_review iOS tasks); touch only these two files

## Work log
- 2026-08-09 plan: Root cause: SessionReadStore's "first sighting is read" rule baselines EVERY unknown session at its current activity. Sessions created after install (e.g. everything a work loop produced while the pho
…[truncated]
- 2026-08-09 context hints: 5 recorded with plan
- 2026-08-09 context hints: SessionListModel.recencyDate(_:) and parseTimestamp(_:) are internal static helpers in the same module — usable from SessionReadStore; isUnread already guards live+running sessions; mark-at-watermar
…[truncated]
- 2026-08-09 preload: 2 file(s), ~16 KiB seeded into implementer context
- 2026-08-09 implementer report: Implemented Task 0304 in the two requested YccKit files only.  - Added a daemon-clock session-list watermark persisted at `<marks key>.watermark`. - Updated `noteSeen(_:)` to classify unknown sessions
…[truncated]
- 2026-08-09 review tier: single-opus — reviewers: sol
- 2026-08-09 review (sol): accept — The change correctly implements the persisted daemon-clock watermark and classifies newly discovered sessions against the pre-refresh watermark while preserving fresh-install and back-catalogue behavi
…[truncated]
- 2026-08-09 usage: 578,090 tok (in 314,948, out 23,526, cache_r 518,780, cache_w 82,627) · cost n/a (unpriced)
  implementer: 434,437 tok (in 211,113, out 11,100, cache_r 212,224, cache_w 0) · cost n/a (unpriced)
  reviewer:sol: 134,592 tok (in 103,819, out 3,381, cache_r 27,392, cache_w 0) · cost n/a (unpriced)
  coordinator: 9,061 tok (in 16, out 9,045, cache_r 279,164, cache_w 82,627) · cost n/a (unpriced)
