---
id: "0142"
title: 'Push notifications: daemon-side webhook/ntfy channel for questions, idle, errors, digests'
status: done
priority: 3
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 14. Persistence & remote sync
    - docs/remote-api.md#Overview
---

## Description
Notifications today are terminal-local (BEL + OSC 9, task 0108) — they only help if the terminal is visible. The signature workflow ("kick off autonomous work, walk away, answer questions from the phone") needs the *reach-out* half: the daemon pushing "agent needs you" to wherever the user is. A plain webhook POST (ntfy.sh-compatible: title/body/priority/click-URL) covers phones, Slack, and home-automation with zero vendor lock-in, and pairs with the documented remote API (the click-through target).

## Acceptance criteria
- [ ] Daemon-side notifier configured in ycc.toml (e.g. `[notify] url = "https://ntfy.sh/mytopic"`, optional auth header); absent = disabled.
- [ ] Fires on: `question_asked` (incl. Confirm gates), `session_idle` (with the final-report first line), `session_error`, work-loop completion digest, and a blocked implementer.
- [ ] Payload includes project, session id, event kind, and a short human line; delivery is best-effort/async (never blocks or fails a session).
- [ ] Per-event-kind enable/disable so autonomous loop users can pick "questions + digest only".

## Plan

Add a daemon-side, best-effort webhook notifier (ntfy.sh-compatible) that pushes "agent needs you" events, configured in ycc.toml.

1) Config (internal/config/config.go)
- New `Notify` struct + `Notify Notify `toml:"notify,omitempty"`` on Config:
  - `URL string` — webhook endpoint (e.g. https://ntfy.sh/mytopic); empty = disabled.
  - `Auth string` — optional Authorization header value (e.g. "Bearer tk_...").
  - `Events []string` — per-kind enable list; empty = all kinds. Valid kinds: question, idle, error, digest, blocked.
- validate(): reject unknown Events entries. Add Registry accessor `Notify() config.Notify` (mirror the GC() accessor pattern).

2) New package internal/notify
- Kind constants: KindQuestion="question", KindIdle="idle", KindError="error", KindDigest="digest", KindBlocked="blocked".
- `Notifier` built via `New(cfg config.Notify) *Notifier` (nil when URL empty; all methods nil-safe).
- `Send(kind, project, sessionID, line string)`: no-op when kind muted; otherwise async goroutine POST (10–15s timeout, never blocks/fails the caller; failures log.Printf only). ntfy-compatible: request body = short human line + a "project · session <id>" context line; headers: Title ("ycc <project>: <kind>"), Priority (high for question/error/blocked, default for idle/digest), Tags (kind), Authorization when Auth set.
- `Enabled(kind) bool` helper; a `Flush()`/waitgroup so tests can deterministically await in-flight sends.

3) Session watcher (internal/session/session.go)
- Manager: `notifier *notify.Notifier` field + `SetNotifier`; `Notify(kind, project, session, line) bool` method (delivered=false when disabled/muted) for the RPC below.
- In newSession (shared by Start, SpawnWorkstream, Reopen): when notifier != nil, spawn a watcher goroutine: `ch, cancel := log.Subscribe(log.LastSeq())` (LastSeq at attach so reopen replay never re-fires), loop over ch (exits when the log closes), skip Transient events, and map:
  - question_asked with data["auto"] != true (auto-answered autonomous asks notify nobody; Confirm gates DO flow here) → kind question, line = first line of data["question"], or first batch question + "(+N more)".
  - session_idle → kind idle, line = first line of data["report"].
  - session_error → kind error, line = data["msg"].
  - subagent_finished with data["blocked"]==true → kind blocked, line like "implementer blocked — decision needed".
- Project label resolved once at attach: match session workspace against m.projects.List() paths, fallback filepath.Base(workspace).

4) Work-loop digest (client-driven, so add a tiny RPC)
- proto/ycc/v1/ycc.proto: `rpc Notify(NotifyRequest) returns (NotifyResponse)`; NotifyRequest{kind, line, project, session_id}, NotifyResponse{delivered bool}. Regenerate with `buf generate`.
- internal/server/server.go: implement Notify → validate kind, call mgr.Notify.
- internal/tui/tui.go: where the loop ends and buildLoopDigest result is stored (m.digest set true, ~L1294–1306), fire a fire-and-forget cmd calling client.Notify with kind "digest" and a one-line summary ("work loop finished: N completed, M blocked, K in review", project m.project). Errors ignored (daemon may have notify disabled).

5) Wiring (internal/daemon/serve.go buildHandler): `mgr.SetNotifier(notify.New(cfg.Notify))` right after NewManager.

6) Docs: spec.md §14 — short bullet on daemon-side push notifications ([notify] block: url/auth/events, best-effort, kinds); docs/remote-api.md — mention the Notify RPC. Include a small TOML example like the [budget] one (~spec L1563).

7) Tests
- internal/notify: httptest server — POST body/headers (Title/Priority/Authorization/Tags), kind muting via Events, nil-notifier safety.
- internal/session: watcher test — real Notifier pointed at httptest server, event.Log in t.TempDir(), emit question_asked (auto + real), session_idle, session_error, subagent_finished{blocked:true}; assert delivered kinds/lines; assert reopen-style attach (fromSeq=LastSeq) doesn't refire history.
- internal/server: Notify RPC delivered=true with notifier set, false when disabled.
- internal/config: validate() rejects bad event kind; Save/Load round-trips [notify].
Verify: buf generate clean, go build ./... && go test ./...

### Starting points
- internal/config/config.go — Config struct ~L290, GC/Budget structs as pattern; Registry ~L462, mirror reg.GC() accessor
- internal/event/log.go — Log.Subscribe(fromSeq)/LastSeq; channel closes on log Close
- internal/session/session.go — Manager ~L936, newSession ~L1260 (shared by start/Reopen), session_idle emit ~L820, session_error ~L799
- internal/session/interaction.go — askData/askManyData (data: question/questions, auto:true = auto-answered, skip those)
- internal/orchestrator/orchestrator.go:520 — SubagentFinished {role: implementer, blocked: true}
- internal/daemon/serve.go buildHandler — wire SetNotifier after session.NewManager
- proto/ycc/v1/ycc.proto service ~L519; regen: buf generate (buf + protoc-gen-go + protoc-gen-connect-go on PATH)
- internal/tui/tui.go ~L1294-1306 — loop end builds digest (buildLoopDigest), m.digest=true; client is m.client (yccv1connect.SessionServiceClient)
- ntfy HTTP: POST body=message; headers Title, Priority, Tags, Click; auth via Authorization header

## Work log
- 2026-07-06 plan: Add a daemon-side, best-effort webhook notifier (ntfy.sh-compatible) that pushes "agent needs you" events, configured in ycc.toml.  1) Config (internal/config/config.go) - New `Notify` struct + `Notif
…[truncated]
- 2026-07-06 context hints: 9 recorded with plan
- 2026-07-06 context hints: internal/config/config.go — Config struct ~L290, GC/Budget structs as pattern; Registry ~L462, mirror the reg.GC() accessor; internal/event/log.go — Log.Subscribe(fromSeq)/LastSeq; subscriber chan
…[truncated]
- 2026-07-06 implementer report: Implemented task 0142: daemon-side webhook/ntfy push notifications for questions, idle, errors, digests, and blocked implementers.  ## What changed  **Config (internal/config/config.go)** - New `Notif
…[truncated]
- 2026-07-06 review tier: single-opus — reviewers: Claude
- 2026-07-06 review (Claude): accept — The change implements a daemon-side, best-effort webhook (ntfy.sh-compatible) push notifier exactly as planned. Config gains a `[notify]` block (url/auth/events) with validation and a Registry accesso
…[truncated]
- 2026-07-06 decision: accept — commit: daemon: push notifications via ntfy-compatible webhook for questions, idle, errors, digests, blocked (task 0142)
