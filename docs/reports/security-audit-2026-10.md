# Security audit: internet-exposed daemon (task 0427, 2026-10-06)

Deployment audited: `ycc daemon --web` bound to `127.0.0.1:8787` with `YCC_TOKEN` set, published
by `ngrok http 8787 --url https://<reserved>.ngrok.dev`. The host is multi-user (other accounts
have login shells) and runs host-networked containers, so loopback is **not** private.

Trust model: a bearer-token holder can start agent sessions that run arbitrary shell commands,
so token = shell as the daemon user. Findings therefore concern (a) unauthenticated internet
clients, (b) other local users / malicious web pages, and (c) ways the token or provider
credentials leak.

Method: code review (Go daemon, web client, tools/sandbox, credential paths), local reproduction
tests against `buildHandler`, and black-box probes of the public URL (no token, harmless sizes).

## Verified OK

- Every RPC (unary and streaming) requires the bearer token; `/debug/latency` too. Wrong/missing
  token → 401 with a generic message. Constant-time compare (only the length leaks).
- No Connect GET (no method is side-effect-free), no CORS headers, header-only auth (no cookies):
  ordinary cross-site CSRF is impossible.
- Web assets: strict CSP (`script-src 'self'`, `frame-ancestors 'none'`, `object-src 'none'`),
  `nosniff`, `no-referrer`; embedded FS, no path traversal (verified live).
- Web client: no HTML sinks (`dangerouslySetInnerHTML`/`innerHTML`/eval are absent). Markdown is
  lexer-only and raw HTML renders as text. Links are allow-listed to http/https/mailto with
  `rel=noopener`. Blob URLs are only used as `<img src>`. The token lives in `localStorage` and is
  never placed in URLs.
- Anthropic login RPCs never return tokens; latency diagnostics record no payloads/headers.
- The ngrok inspector (:4040) rejects foreign `Host` headers (no DNS rebinding).

## Findings

### F1 — HIGH — Unauthenticated memory exhaustion (live-confirmed)
connect-go v1.20.0 reads and decompresses the whole unary request body (`handler.go:69`) before
the interceptor chain runs auth (`handler.go:82`), with gzip enabled and no `ReadMaxBytes`
(`internal/daemon/serve.go:127-130`). A 1 MiB gzip body sent without a token to the public URL
came back as `invalid_argument` (it had been parsed) instead of `unauthenticated`. Locally, a
~0.5 MB gzip body (512 MiB expanded) drove the heap to ~1.8 GiB. A few small requests can OOM the
daemon and kill all running agent work. `x/net/h2c` has the same problem: an HTTP/1 `Upgrade: h2c`
request gets `io.ReadAll`'d before routing (`h2c.go:186`).
**Fix:** check the bearer token in plain `net/http` middleware before the Connect handler (keep
the interceptor as well); `connect.WithReadMaxBytes(~32 MiB)`, which also bounds decompressed
size; replace `x/net/h2c` with Go's native unencrypted HTTP/2 (`http.Server.Protocols`).

### F2 — HIGH (operational) — ngrok inspector stores the bearer token for every local user
ngrok's request inspector on `127.0.0.1:4040` had captured 100 requests, 93 of them with the
`Authorization: Bearer` header in clear, along with response bodies (transcripts). Every local
account and every host-networked container can `curl` it, so any of them gets the token, and with
it a shell as the daemon user.
**Fix:** run `ngrok http 8787 --inspect=false` (or `inspect: false` / `web_addr: false` in
ngrok.yml), then **rotate the token**. If ngrok's cloud Traffic Inspector with full capture is
enabled, turn it off.

### F3 — HIGH (operational) — The token looks hand-chosen; there is no rate limiting
The live token is 22 characters, lowercase+digits, with only 12 distinct characters and a common
word in it. The daemon applies no brute-force throttling.
**Fix:** use `head -c 32 /dev/urandom | base64` as `docs/remote-api.md` recommends. Optionally
add failed-auth rate limiting, and/or put ngrok edge auth in front (OAuth/IP restriction traffic
policy) as defense in depth.

### F4 — HIGH (local) — Tokenless loopback daemons: other local users and DNS rebinding
The one-shot in-process daemon is **always** tokenless (`cmd/ycc/main.go:768-778`).
`ycc --background` and loopback `ycc daemon` are tokenless whenever `YCC_TOKEN` is unset. On this
host, any other local user or host-networked container can drive them, which is a shell as `why`.
No Host/Origin check exists, so a web page that DNS-rebinds to `127.0.0.1:8787` can drive a
tokenless daemon same-origin (confirmed locally: foreign `Host`/`Origin` → 200 with data).
**Fix:** generate a random per-process token for the in-process daemon (the client is the same
process). Have `--background`/loopback daemons auto-generate a token persisted 0600 in the state
dir, which clients read. Reject non-loopback `Host` headers when no token is configured. Longer
term, consider a 0600 unix socket for local use.

### F5 — MEDIUM-HIGH — Nothing prevents publishing a tokenless daemon via ngrok
The "token required" check looks only at the bind address. ngrok connects from loopback, so
restarting `ycc daemon --web` in a shell without `YCC_TOKEN` would give anyone on the internet a
shell. **Fix:** require a token whenever `--web` is set. The F4 Host allowlist also blocks this,
because ngrok forwards the public `Host`.

### F6 — HIGH (design) — Prompt-injected agents can read the daemon token and provider keys
Bash tools (foreground, background, and reviewer) inherit the daemon environment, including
`YCC_TOKEN` and `ANTHROPIC_API_KEY` (`internal/tools/worker.go:1451-1468,1576-1579`). Read accepts
any absolute path, including `~/.config/ycc/secrets.json`, which holds OAuth refresh tokens in
plaintext. Reviewer confinement is read-only but not secret-free and keeps network access. Tool
output flows unfiltered into events.jsonl, clients, and later LLM requests. An agent that is
prompt-injected by a fetched page or repo file can exfiltrate the token and hand an outside
attacker persistent access to the public daemon.
**Fix (cheap first step):** scrub `YCC_TOKEN` and provider credential variables from
model-triggered subprocess environments. Real isolation requires running agents under a separate
uid or container that cannot read the daemon's config, secrets, or `/proc`.

### F7 — MEDIUM — `x-api-key` follows cross-origin redirects
Go strips `Authorization` on cross-domain redirects but not `x-api-key`. Anthropic discovery
(`internal/config/discover.go`), Exa (`internal/tools/web.go:94-97`), and inference clients
(`internal/llmhttp/policy.go`) use redirect-following clients. **Fix:** a `CheckRedirect` policy
that rejects cross-origin redirects and HTTPS downgrades on credential-bearing clients.

### F8 — MEDIUM — Raw provider/OAuth error bodies are persisted and forwarded
Codex/gollama `APIError` and the OAuth token-endpoint errors embed raw response bodies
(`internal/codex/codex.go:469-471`, `anthropicauth.go:203-205`, `openaiauth.go:363-365`). These
flow into `session_error` events, clients, and the notification webhook without redaction. A
provider or proxy that reflects credentials would persist them. **Fix:** redact the request's own
credentials (API key, access/refresh token) from upstream error text before emitting it.

### F9 — LOW — Model RPCs forward any credential reference to any URL
`DiscoverModels`/`TestModel`/`UpsertModel` accept an arbitrary `base_url` together with any
`key_env` (any environment variable or secrets entry, including `YCC_TOKEN` and the serialized
OAuth records). Within the trust model this is not an escalation. It is still a one-call
exfiltration primitive once a token leaks. **Fix:** deny `YCC_TOKEN` and the OAuth record names as
API-key references, and require HTTPS for credential-bearing custom URLs.

### F10 — LOW — Server hygiene
- No `ReadHeaderTimeout`/`IdleTimeout`/`MaxHeaderBytes` and no HTTP/2 idle timeout or stream
  limit (`serve.go:164,199`), which allows slowloris attacks by local clients.
- Notifier failures log the full webhook URL, including any secret query (`internal/notify/notify.go:136,141`).
- The startup probe sends `YCC_TOKEN` to whatever process holds `127.0.0.1:8787`, which on a
  shared host could be another user's listener.
- The web UI has no "forget token" action, and the token persists in `localStorage` indefinitely.
- The web client trusts the server's `image/*` mediaType when building blob URLs. That is safe
  today because the server never emits SVG, but an allow-list of raster types would be
  defense-in-depth.

## Recommended order
1. Operational, now: restart ngrok with `--inspect=false` and rotate to a random 32-byte token (F2, F3).
2. Pre-auth hardening: HTTP auth middleware, read limits, native h2c, timeouts (F1, F10).
3. Local daemons: a token for every daemon, a Host allowlist, and a required token with `--web` (F4, F5).
4. Agent credential isolation: scrub the subprocess environment, then a uid/container split (F6, F9).
5. Credential transport: redirect policy and error redaction (F7, F8).
