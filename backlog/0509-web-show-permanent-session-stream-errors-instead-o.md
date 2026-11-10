---
id: "0509"
title: 'Web: show permanent session-stream errors instead of retrying them forever'
status: done
priority: 2
created: "2026-11-09"
updated: "2026-11-10"
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

## Outcome

Web SessionController now classifies Internal/DataLoss and other daemon-side codes as "server" errors. After 3 consecutive identical errors with no durable update in between, it ends in the failed state with the server message, shown in the existing banner with a Retry button. Network codes (Unknown fetch failures, Unavailable, DeadlineExceeded, ResourceExhausted, Aborted) still reconnect without limit from the cursor. Retry or reopening restarts the attempt count, and a failed read-only reload now reloads its snapshot. vitest has 3 new tests (421 pass) and internal/web/dist was rebuilt (bundle committed separately). The iOS has the same problem: filed as 0536.

Commit: Web: a session stream that keeps failing with the same server error shows it with Retry instead of reconnecting forever (0509)
