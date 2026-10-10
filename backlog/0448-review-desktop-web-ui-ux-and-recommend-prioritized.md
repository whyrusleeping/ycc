---
id: "0448"
title: Review desktop web UI/UX and recommend prioritized improvements
status: done
priority: 3
created: "2026-10-10"
updated: "2026-10-10"
depends_on: []
spec_refs: []
---

## Description
Inspect the current web client's rendered UI and interaction flows. Deliver a concise, evidence-grounded set of prioritized recommendations with concrete proposed changes. This is an audit, not authorization to implement a redesign. Preserve existing in-flight UI work.

## Outcome
Reviewed current working UI against the live daemon, with desktop screenshots at 1440×900, a 1024×768 inspector check, light/dark samples, and an independent read-only interaction/accessibility inspection. No application source changes or commits; existing UI work preserved.

Browser-confirmed defects: new-session prompt disappears after leaving and returning; clicking a preset replaces an existing prompt without confirmation; default inspector at 1024px leaves a roughly 324px main pane with the session title effectively lost. Small secondary-text tokens have contrast below 4.5:1 (light 3.21:1, dark card 3.82:1).

Recommendations delivered for approval: protect drafts; adapt inspector/layout to available width; improve session discovery and state labels; reduce transcript lifecycle noise; improve secondary-text contrast; align menu/radio keyboard interactions and form error feedback. Suggested first implementation slice is draft protection, inspector sizing, and session-list clarity. These remain recommendations, not accepted implementation scope.

Visual evidence: `/tmp/ycc-ux-audit/` (ephemeral local screenshots). No test suite/build run because no application source was changed.
