# Remote access smoke test

Run this when releasing changes to daemon binding, authentication, Connect transport, or remote
client attachment. Loopback tests cover protocol behavior; this checks a real private-network path
between two machines.

## Prerequisites

- Host A has a workspace, usable model config, and a reachable private-network address.
- Host B is on the same private network and has the release-candidate `ycc` client.
- Both use the same high-entropy `YCC_TOKEN`.

## Steps

1. On A, attempt a non-loopback daemon bind with no token. Confirm it exits before listening.
2. Start the daemon on A with the token and no ycc TLS when the private network supplies transport
   encryption. Confirm the daemon warns that its own HTTP transport is cleartext.
3. From B, run an authenticated project/session listing, then start a session and observe live
   events. Send input and answer a question from B.
4. Disconnect B without stopping the session. Let A produce more durable events, then reattach B
   from its last known sequence. Confirm replay followed by live tail has no gap or duplicate.
5. From B, interrupt, steer, resume, and stop a disposable session. Confirm each state is visible
   from a client on A.
6. Repeat one unary RPC without the token and with a wrong token. Confirm both return
   unauthenticated and reveal no project/session data.

Example attachment shape (addresses and ids are environment-specific):

```
YCC_TOKEN="$YCC_TOKEN" ycc daemon --addr <host-a-private-ip>:8787
ycc --addr http://<host-a-private-ip>:8787 --token "$YCC_TOKEN" list
ycc --addr http://<host-a-private-ip>:8787 --token "$YCC_TOKEN" attach <session-id> --from 0
```

## Pass condition

The token-free bind is refused; authenticated commands cross the real network; an unattended
session survives client disconnect; replay resumes cleanly; control commands round-trip; and
unauthenticated callers receive no data. Capture the two ycc versions, network type, commands, and
output for any failure.
