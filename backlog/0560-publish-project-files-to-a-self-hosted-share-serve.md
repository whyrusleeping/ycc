---
id: "0560"
title: Publish project files to a self-hosted share server (ycc-share server, daemon RPCs, web)
status: done
priority: 2
created: "2026-11-18"
updated: "2026-11-18"
depends_on: []
spec_refs:
    - docs/design/publishing.md
---

## Description

User request (2026-11-18, this session): share artifacts from ycc projects publicly from a self-hosted server on a configurable domain (the owner's deployment uses paste.why.earth, but nothing is hard-coded). Choices the user made in this session: rendered viewer pages plus raw/download; links can be revoked from ycc; links expire after a server-configured default (30 days); re-publishing unchanged content reuses its link, while changed content gets a new link; deploy to the owner's VPS (deployment details belong in the private infra project).

## Acceptance criteria

- `ycc-share` (cmd/ycc-share) is a standalone server. It accepts token-authenticated uploads and deletes. It serves each item at an unguessable secret URL as a rendered viewer page (markdown, text, images, PDF, sandboxed HTML), with raw and download variants. It also handles expiry and the safe-serving headers described in docs/design/publishing.md.
- The daemon `[publish]` config (url, name, token_env) works without hard-coded hosts. The token resolves from the environment and then from the `ycc token` store.
- `GetPublications`, `PublishFile` and `UnpublishFile` RPCs are confined to the project or session root like ReadFile. Publications persist in an owner-only daemon state file. Unchanged content reuses its link.
- The web file viewer offers "Publish to <name>" when a target is configured. It shows the resulting link with copy/open, marks whether the current contents are published, and can unpublish.

## Outcome

ycc-share (cmd/ycc-share, internal/shareserver) serves token-uploaded files at secret 128-bit URLs as rendered viewer pages, with raw and download variants, sandboxed HTML, safe-serving headers and expiry. Daemon `[publish]` (url, name, token_env resolved from env then `ycc token`) backs GetPublications/PublishFile/UnpublishFile, which are root-confined like ReadFile, dedupe identical bytes, persist owner-only records and rekey on project rename. The web file viewer gained a Publish/Published control. iOS client changes land in the same commit and are tracked by 0561. Deployed at https://paste.why.earth; verified end to end against production.

Commit: Publish project files to a self-hosted share server: ycc-share, [publish] daemon RPCs, web and iOS file viewer actions
