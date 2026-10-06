---
id: "0430"
title: Isolate agent tool subprocesses from daemon credentials
status: todo
priority: 2
created: "2026-10-06"
updated: "2026-10-06"
depends_on: []
spec_refs: []
---

## Description
From security audit 0427 (F6, F9). Bash tools (foreground, background, and reviewer) inherit the daemon env (YCC_TOKEN, ANTHROPIC_API_KEY, ...) — internal/tools/worker.go:1451-1468,1576-1579. Read can open ~/.config/ycc/secrets.json, which holds OAuth refresh tokens. A prompt-injected agent can exfiltrate the token of an internet-facing daemon. Model RPCs also accept any key_env plus any base_url, which gives a one-call way to forward a credential to an arbitrary endpoint.

## Acceptance criteria
- Model-triggered subprocesses get a scrubbed environment: at minimum YCC_TOKEN and configured provider key_env names are removed, and the policy is documented. Consider an allowlist.
- Read/Edit tools deny the ycc config/secrets directory (defense-in-depth; not a boundary against Bash).
- key_env validation rejects YCC_TOKEN and the internal OAuth record names; credential-bearing custom base URLs must be https (loopback excepted).
- Spec notes that real isolation needs a separate uid/container. File that as its own proposed task if it is not done here.

## Work log
