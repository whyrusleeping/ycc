# Remote access smoke test (manual tailnet runbook)

Verifies the rescoped M5 remote-access path (spec §12/§14): a workspace daemon
dialed directly over a private network (Tailscale/VPN) with a bearer token; TLS
optional. There is **no** daemon-to-daemon log sync — a remote observer/prodder
just dials the workspace daemon's Connect endpoint.

Run this when you have two machines on the same tailnet (or a phone on it). The
automated tests in `internal/server/remote_e2e_test.go` cover the same shape over
loopback; this runbook replays it over a real network, plus the interactive TUI
attach that a test can't drive.

## Prerequisites

- Two machines (A = workspace host, B = remote client / phone) joined to the same
  Tailscale tailnet (or any private network where B can reach A).
- `ycc` built on both, or at least on A; B can also use `curl`.
- A model API key available on A (e.g. `ANTHROPIC_API_KEY`) so sessions can run.
- Note machine A's tailnet IP (e.g. `tailscale ip -4`, something like `100.64.0.1`).

Pick a token once and export it on both machines:

```
export YCC_TOKEN=$(head -c 32 /dev/urandom | base64)   # A: generate
export YCC_TOKEN=<paste the same value>                # B: reuse
```

## Steps

1. **Confirm the guardrail on A.** Try to bind the tailnet address without a
   token — it must be refused before it opens the port:

   ```
   ycc daemon --addr 100.64.0.1:8787            # no --token / YCC_TOKEN unset
   ```

   Expected: it exits with an error like
   `refusing to bind non-loopback address 100.64.0.1:8787 without a token`.

2. **Start the daemon on A** with the token (TLS omitted — the tailnet is already
   encrypted):

   ```
   YCC_TOKEN=$YCC_TOKEN ycc daemon --addr 100.64.0.1:8787
   ```

   Expected: it logs a cleartext warning
   (`warning: binding non-loopback address 100.64.0.1:8787 without TLS; traffic is cleartext`)
   and then `ycc daemon listening on 100.64.0.1:8787 (... tls=false)`. Leave it
   running.

3. **Attach from B and drive a session** end to end:

   ```
   ycc --addr http://100.64.0.1:8787 --token $YCC_TOKEN start "add a hello.txt file"
   ```

   Expected: it prints `session s_...` and streams live events. While it runs:
   - type a line and press enter to **prod** (SendInput);
   - if the coordinator asks a question, answer it (the answer round-trips via
     AnswerQuestion / SendInput);
   - Ctrl-C detaches the client without stopping the session (the daemon on A
     keeps running it).

   Re-attach and replay from the start to confirm the offset replay:

   ```
   ycc --addr http://100.64.0.1:8787 --token $YCC_TOKEN attach s_... --from 0
   ```

   Also exercise `list`, and interrupt/resume/stop from B:

   ```
   ycc --addr http://100.64.0.1:8787 --token $YCC_TOKEN list
   ```

4. **Curl the Connect HTTP/JSON unary path from B** (phone-friendly, no ycc
   binary needed):

   ```
   curl -sS \
     -H "Authorization: Bearer $YCC_TOKEN" \
     -H 'Content-Type: application/json' \
     -d '{}' \
     http://100.64.0.1:8787/ycc.v1.SessionService/ListSessions
   ```

   Expected: HTTP 200 and a JSON body like
   `{"sessions":[{"sessionId":"s_...","mode":"work","status":"running", ...}]}`.

   Confirm rejection without the token:

   ```
   curl -sS -o /dev/null -w '%{http_code}\n' \
     -H 'Content-Type: application/json' -d '{}' \
     http://100.64.0.1:8787/ycc.v1.SessionService/ListSessions
   ```

   Expected: `401` (Unauthenticated).

5. **Curl the Connect server-streaming path (`Subscribe`) from B.** The Connect
   streaming protocol frames each message in a 5-byte envelope: 1 flag byte
   (`0x00`) + 4-byte big-endian length + the JSON payload. Build the request
   frame for the JSON message `{"sessionId":"<id>","fromSeq":0}`:

   ```
   SID=s_...                                   # a running session id
   MSG="{\"sessionId\":\"$SID\",\"fromSeq\":0}"
   LEN=$(printf '%s' "$MSG" | wc -c)
   # 5-byte header: flag 0x00 + big-endian uint32 length, then the message.
   { printf '\x00'; printf "$(printf '%08x' "$LEN" | sed 's/../\\x&/g')"; printf '%s' "$MSG"; } \
     | curl -sS --http2-prior-knowledge \
         -H "Authorization: Bearer $YCC_TOKEN" \
         -H 'Content-Type: application/connect+json' \
         --data-binary @- \
         http://100.64.0.1:8787/ycc.v1.SessionService/Subscribe | xxd | head
   ```

   Expected: a stream of enveloped response frames. Data frames (flag byte
   `0x00`) carry `Event` JSON (`{"seq":"1","ts":"...","actor":"user","type":"user_input", ...}`);
   the stream ends with an end-of-stream frame (flag byte `0x02`) whose payload is
   the trailer JSON (`{}` on clean close, or `{"error":{...}}`). Persisted events
   replay first (because `fromSeq:0`), then live events tail until the session's
   log closes or the connection is dropped.

   Confirm rejection without the token returns an Unauthenticated Connect error
   (HTTP 401 with a JSON `{"code":"unauthenticated", ...}` body) by dropping the
   `Authorization` header.

## Expected outcome

- Step 1: the daemon refuses the token-free non-loopback bind (guardrail holds).
- Step 2: the daemon starts, warns about cleartext, and listens on the tailnet IP.
- Step 3: from B, `ycc --addr ... --token ...` attaches, streams, prods, answers,
  re-attaches with `--from 0` replay, and can interrupt/resume/stop — all remote.
- Step 4: authenticated JSON `ListSessions` returns 200 + JSON; unauthenticated
  returns 401.
- Step 5: authenticated `Subscribe` streams enveloped `connect+json` frames
  (replay + live + end-of-stream); unauthenticated is rejected.

If any step deviates, capture the command and its output. Single-writer is
unaffected: remote clients only issue RPCs against the one workspace daemon, which
remains the sole writer of each session's log.
