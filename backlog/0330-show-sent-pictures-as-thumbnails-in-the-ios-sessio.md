---
id: "0330"
title: Show sent pictures as thumbnails in the iOS session transcript
status: in_review
priority: 3
created: "2026-08-13"
updated: "2026-08-13"
depends_on: []
spec_refs:
    - 18.1 Session input
    - docs/design/ios-client.md#Session transcript and interaction
---

## Description
The iOS transcript currently turns `user_input.images` metadata into a “Picture attached” text label. Render the actual pictures sent with a message in that user-message bubble instead.

Use durable, bounded session attachment storage and an authenticated daemon retrieval API so thumbnails remain available after leaving/reopening the session without placing base64 payloads in `events.jsonl`. Preserve a clear metadata-only fallback for legacy, missing, or reclaimed attachments.

## Acceptance criteria

- A picture sent with an opening prompt or in-session message appears as an image thumbnail in that user transcript row, alongside any text.
- Pictures remain visible after reopening the transcript while its session data is retained.
- Event JSON remains metadata-only; image bytes are stored separately with owner-only permissions and retrieved through an authenticated session-scoped RPC.
- Legacy/missing attachment data renders a non-broken “Picture attached” fallback.
- Attachment count/type/size validation remains unchanged, and daemon plus YccKit projection/client tests cover the new behavior.

## Work log
