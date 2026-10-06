---
id: "0432"
title: Isolate agent execution under a separate uid or container
status: proposed
priority: 2
created: "2026-10-06"
updated: "2026-10-06"
depends_on: ["0430"]
spec_refs: ["8"]
---

## Description
Environment scrubbing and file-tool credential-path denies are defense-in-depth, not isolation. Agent shells still run as the daemon uid and can read its credential files or inspect its process state. Run agent execution under a separate uid or container with only the workspace and explicitly approved tool credentials exposed.

## Acceptance criteria
- Foreground, background, reviewer, and repository-configured agent commands cannot read daemon config/secrets, its token file, or daemon process credentials.
- Workspace access, cancellation, output capture, and operator-approved development tools continue to work with an explicit execution identity and credential policy.
- Document deployment requirements and test the isolation boundary against file and process-state credential reads.
