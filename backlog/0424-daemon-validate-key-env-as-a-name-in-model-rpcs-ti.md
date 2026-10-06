---
id: "0424"
title: 'Daemon: validate key_env as a NAME in model RPCs; tidy ycc.toml writes'
status: proposed
priority: 3
created: "2026-10-06"
updated: "2026-10-06"
depends_on: []
spec_refs: []
---

## Description
Found during web 0416: UpsertModel, TestModel and DiscoverModels accept any `key_env` string, so a client that lets a user paste an API key there would persist the secret into ycc.toml. The web form refuses this client-side, but the daemon should enforce it for every client.

Validate `key_env` server-side with the same name rule `ycc token set` uses (env-var-style identifier; reject values that look like keys, e.g. `sk-…`, long high-entropy strings) and return InvalidArgument without writing.

Also seen (cosmetic, same area): settings writes rewrite ycc.toml with many blank lines and empty-string fields (`base_url = ''`, `thinking = ''`); RemoveModel on a default-role model reports "still referenced by running session" before the role reference.

## Acceptance criteria
- [ ] UpsertModel/TestModel/DiscoverModels with a key-like or invalid key_env fail with InvalidArgument and write nothing.
- [ ] Saving a model omits empty optional fields from ycc.toml.

## Work log
