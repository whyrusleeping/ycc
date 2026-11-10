---
id: "0509"
title: 'Web: show permanent session-stream errors instead of retrying them forever'
status: todo
priority: 2
created: "2026-11-09"
updated: "2026-11-09"
depends_on: []
spec_refs: []
---

## Description
Follow-up from PR #1 (session log holes). `classify()` in `clients/web/src/features/session/controller.ts` treats every code not in an explicit list as `"transient"`, including `Code.Internal`. A SessionView/SubscribeSessionView that fails every time (e.g. the pre-PR #1 sequence-discontinuity error) therefore sits in "reconnecting" forever with backoff. To the user it looks like an endless spinner, and the error text never shows.

## Acceptance criteria
- A session stream that keeps failing with the same non-network error (e.g. `internal`) ends in a visible failed state showing the server's error message. It does not reconnect without limit.
- Real transient failures (network drop, daemon restart, `unavailable`) still reconnect automatically and resume from the cursor, as they do today.
- The failed state offers a way to retry by hand (or reopening the session retries).
- Check whether iOS (YccKit session stream) has the same problem. Fix it there or file it separately.

## Work log
