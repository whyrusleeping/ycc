---
id: "0259"
title: Refresh Anthropic OAuth token per turn instead of snapshotting it at Build
status: done
priority: 1
created: "2026-08-06"
updated: "2026-08-06"
depends_on: []
spec_refs: []
---

## Description
## Problem

`config.Registry.Build` resolves a Claude subscription access token **once**
(`anthropicauth.AccessToken` → `c.SetBearerToken(tok)`, internal/config/config.go ~1150)
and bakes it into the gollama client. A session's coordinator loop keeps that client for
its whole life (`session.buildLoop` builds it at start/mode-transition only), so a long
session sends the same bearer token for hours.

Anthropic invalidates the previous access token when the refresh token is redeemed, so any
*other* refresh — the subusage poller, `ycc doctor`, the setup wizard, or a ycc process in
another workspace — silently kills every live session's credential. The failure surfaces as
a non-retryable `kind: auth` 401 `"OAuth access token has been revoked."`, which ends the
session and loses its context.

Observed: `/home/why/code/valstore/.ycc/sessions/s_48f210c2a3df61d5` (started 2026-08-05
23:00:19Z, opus/oauth) idled ~2h55m on an `ask_user`, resumed 2026-08-06 04:11:37Z and
immediately took that 401. `~/.config/ycc/secrets.json` had been rewritten with a fresh 8h
token at 02:58:22Z — i.e. a background refresh 73 minutes before the death.

The codex/OpenAI path does not have this bug: `codex.New(base, openaiauth.AccessToken)`
receives the token *function* and resolves it per request. Anthropic is the asymmetric one.

Related: task 0200 (unlocked, non-atomic secrets read-modify-write) can produce the same
symptom by a different route — a clobbered rotated refresh token later gets redeemed twice
and Anthropic revokes the whole token family.

## Acceptance criteria

- [x] An `auth = "oauth"` anthropic client resolves a live access token per turn (cheap when
      unexpired), not once at `Build`; `anthropicauth.Turner` is the natural place (it already
      wraps the client) and must serialize header mutation.
- [x] A 401 whose body indicates a revoked/expired OAuth access token triggers one forced
      refresh + retry of the turn before the session is failed.
- [x] A background refresh by another process (or another session) no longer kills a live
      session: covered by a test that rotates the stored credential mid-loop.
- [x] Long-lived subagent/worker loops get the same treatment (they build through the same
      registry path).
- [x] Spec §13 credential-mechanism text updated: the token is live per request, not
      per `Build`.
- [x] `go test ./...` and `go test -race ./internal/anthropicauth/... ./internal/config/...` pass.


## Work log

- 2026-08-06: `anthropicauth.Turner` gained an optional `TokenSource` (`NewOAuthTurner`):
  it resolves the access token before every turn, installs it as the transport bearer, and
  on a rejection matching `IsRevokedCredential` (401 + `authentication_error` +
  revoked/expired) does one forced refresh and one retry. Credential-carrying turns hold the
  Turner's mutex so installing a token cannot race a request reading the client's header map.
- `anthropicauth.ForceRefresh(ctx, stale)` added alongside `AccessToken`, sharing one
  `accessToken` implementation under the package refresh mutex. It returns the stored
  credential without a network call when the store no longer holds the rejected token —
  another process had already refreshed, and redeeming an already-rotated refresh token is
  what makes Anthropic revoke the whole family (see task 0200 for the store-level race).
- `config.Registry.Build` still resolves a token eagerly (a missing/superseded login stays a
  Build-time error) but now passes `TokenSource{AccessToken, ForceRefresh, c.SetBearerToken}`
  to `NewOAuthTurner`. A static `sk-ant-oat` key_env credential keeps the plain `NewTurner`.
- Tests: per-turn resolution, retry-once on revoked (turn + stream), unrecoverable rejection
  reported as the provider error, no refresh burned on non-auth failures,
  `IsRevokedCredential` matrix, `ForceRefresh` store-preference; plus two end-to-end
  `internal/config` tests driving a real gollama client against an httptest `/v1/messages`
  that rejects the pre-rotation bearer.
- Not addressed here: the engine loop still classifies a surviving auth 401 as
  non-retryable, which is correct once the credential itself is recoverable.

- 2026-08-07: Closed done in a backlog audit — implemented and committed; on-device use is the verification the Linux box could not provide.
