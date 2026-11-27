---
id: "0633"
title: Reserve space for subscription allowance while Usage loads on web and iOS
status: done
priority: 3
created: "2026-11-27"
updated: "2026-11-27"
depends_on: []
spec_refs: []
---

## Description

On a cold open, Usage should show a quota-sized placeholder with a small loading indicator instead of inserting the allowance section several seconds later. Keep existing readings visible during refresh; a completed empty result should not keep spinning.

## Outcome

Both Usage screens reserve the space of a typical account and show a small spinner on the first fetch. Existing readings stay visible on refresh; empty or failed initial requests clear the placeholder. Verified with delayed responses in Chromium and the iOS simulator; web, embedded-bundle and iOS tests passed.

Commit: Reserve space for subscription allowance while Usage loads on web and iOS
