# Design: publishing files to a share server

> Status: accepted. The server (`cmd/ycc-share`), daemon config and RPCs, and the web and iOS file
> viewers are implemented.

## Context

Users want to hand someone outside ycc a file an agent produced (a report, a chart, a markdown
write-up, an HTML page) without committing it anywhere or copying it to another host by hand.
The daemon usually runs on a private network and its bearer token grants shell access, so its own
file RPCs can never be the public surface. A separate, small public server is needed. Clients
should be able to reach it from the file viewer in one action.

## Decision

`ycc-share` is a standalone server in this repository that stores uploaded files and serves each
one publicly at an unguessable URL. The daemon is its only writer: it reads a project file under
the same confinement as `ReadFile`, uploads the bytes with a bearer token, and remembers what it
published so clients can show, reuse, and revoke links. Clients never see the share server's
token and never upload directly.

Nothing names a particular host. The operator chooses the public URL in the server flags and in
the daemon's `[publish]` config, and the clients label the action with the configured name
("Publish to paste.example.com").

## Share server

### Running

```
ycc-share --data /var/lib/ycc-share --public-url https://paste.example.com \
  --token-file /path/to/token [--listen 127.0.0.1:8095] [--ttl 720h] [--max-bytes 50MiB]
```

- `--public-url` is the externally visible base URL used to build links; the server normally sits
  behind a TLS-terminating reverse proxy.
- The upload token comes from `--token-file` (one line, surrounding whitespace trimmed) or
  `YCC_SHARE_TOKEN`; the server refuses to start without one. Tokens are compared in constant time.
- `--ttl` is the lifetime of each upload (default 30 days); `0` keeps uploads until deleted.
- `--max-bytes` caps one upload (default 50 MiB); larger bodies get 413.

### Storage

Each item is `<data>/<id>.blob` plus `<data>/<id>.json` metadata (name, media type, size, sha256,
created and expiry times). The blob is written first and the metadata last, each via a temporary
file and rename, so an item exists exactly when its metadata does. An hourly sweep and every
lookup treat expired items as absent; the sweep deletes them. Ids are 16 bytes from
`crypto/rand`, base64url without padding (22 characters); anything else in an id position is 404.

### API

- `POST /api/items` with `Authorization: Bearer <token>`. The body is the file's bytes and the
  `X-Ycc-Filename` header carries the base name, percent-encoded. Names are cut to their last
  path component, cleaned of control characters, and cut to 200 bytes keeping the extension; an
  empty result is `file`. The response is
  201 with JSON `{"id","url","raw_url","size","sha256","expires_at"}` (`expires_at` is RFC 3339,
  empty for no expiry). The server determines the media type itself, by extension first and
  content sniffing second; a client-supplied type is ignored.
- `DELETE /api/items/{id}` with the bearer token: 204, or 404 for an unknown or expired id. A
  storage error is 500, never 404, because the daemon forgets a link on 404.
- `GET /{id}/{name}`: the viewer page. The name segment is cosmetic and only makes links
  readable; `/{id}` serves the same page. `?raw=1` returns the bytes and `?download=1` returns
  them as an attachment.
- `GET /robots.txt` disallows everything; `GET /healthz` returns `ok`; `GET /` is a minimal page
  that lists nothing.

Missing, expired, and deleted items all return the same 404, so a link does not reveal whether it
ever existed.

### Viewer pages

Each viewer page has a header with the file name, size, publish date, expiry, and Raw/Download
links, and it follows the reader's light or dark preference. The body depends on the kind of file:

| Kind | Body |
| --- | --- |
| Markdown (`.md`, `.markdown`) | Rendered GitHub-flavoured Markdown; raw HTML in the source is not rendered. Over 2 MiB it is shown as text, because every view renders afresh |
| HTML (`.html`, `.htm`) | A full-height `<iframe sandbox="allow-scripts allow-popups allow-popups-to-escape-sandbox allow-modals">` of `?render=1` |
| SVG and raster images | `<img src="?raw=1">` |
| PDF | An embedded frame of `?raw=1` plus an open link |
| Audio / video | Native `<audio>` / `<video controls>` |
| Other UTF-8 text | Line-numbered monospace text; at most 2 MiB and 50,000 lines are shown, with a note pointing at Raw |
| Other binary | A download button |

### Safe serving

The share domain serves owner-published but otherwise arbitrary content, so it holds no cookies
or other state, and every response carries `X-Content-Type-Options: nosniff`,
`Referrer-Policy: no-referrer` (secret URLs must not leak through links in shared documents), and
`X-Robots-Tag: noindex, nofollow`.

- Viewer pages use `Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline';
  img-src 'self' data: https:; media-src 'self'; frame-src 'self'; base-uri 'none';
  form-action 'none'; frame-ancestors 'none'`.
- `?render=1` (HTML only) serves the document as `text/html` under `Content-Security-Policy:
  sandbox allow-scripts allow-popups allow-popups-to-escape-sandbox allow-modals;
  frame-ancestors 'self'`, so its scripts run in an opaque origin even when opened directly. They
  may still load and contact other origins, as agent reports load chart libraries from CDNs. A
  published page's own scripts can therefore read and send its URL, so a link to HTML is only as
  secret as the scripts it includes.
- `?raw=1` serves every `text/*` type (including HTML and JavaScript), XHTML, and XML other than
  SVG as `text/plain; charset=utf-8`, so Raw shows their source instead of running or
  downloading it. Other types keep their own media type under `Content-Security-Policy: sandbox;
  default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; media-src 'self';
  frame-ancestors 'self'`. PDF omits that header because browser PDF viewers do not render under
  a CSP sandbox; it keeps `frame-ancestors 'self'`.
- Item responses are `Cache-Control: private, no-cache` with the sha256 as ETag, so an unpublished
  or expired link stops serving at once.

## Daemon

### Configuration

```toml
[publish]
url = "https://paste.example.com"  # share server base URL; absent disables publishing
name = "paste.example.com"         # label in clients; defaults to the URL's host
token_env = "YCC_PUBLISH_TOKEN"    # upload token name: environment first, then `ycc token set`
```

The URL must be HTTPS, or HTTP on loopback, because it carries a credential. `token_env` is
required when `url` is set, follows the model `key_env` naming rules, and is added to the set of
credentials scrubbed from agent subprocess environments. Requests do not follow redirects; a
redirect is reported as an error naming its target. A loopback HTTP target bypasses environment
proxies, and share server error text is redacted of the token before it reaches clients.

### RPCs

- `GetPublications(project, session_id, path)` returns the target (empty when unconfigured) and
  the project's unexpired publications of that path, newest first, each with `current` set when
  its sha256 matches the file's present contents. An empty path lists every publication in the
  project and leaves `current` unset.
- `PublishFile(project, session_id, path)` resolves the file exactly as `ReadFile` does (the
  project root or the session's live worktree, with the same confinement and `.git` refusal),
  reads it whole up to 64 MiB (opening it through the root, so a symlink swapped in after the check
  cannot escape), and uploads it. If an unexpired publication of the same project,
  path, and sha256 on the same server exists, it is returned with `reused` set and nothing is
  uploaded. Unconfigured publishing and a missing token are `failed_precondition`; an
  unreachable server is `unavailable`; a rejected token or an oversized file is
  `failed_precondition` with the server's reason. If the upload succeeds but the record cannot be
  saved, the daemon deletes the item again rather than leave a link it cannot show or revoke.
- `UnpublishFile(project, id)` deletes the item on the server and forgets it. A server 404 counts
  as success because the item is already gone; other server failures keep the record and return
  an error.

Publication records (id, project, root-relative path, URL, sha256, size, publish and expiry
times, server base URL) live in the owner-only `publications.json` beside the daemon's other
state files, or in memory for a one-shot daemon. Expired records are dropped when read. Records
are keyed by project name and relabelled when the project is renamed.

## Clients

The file viewer shows a publish action only when `GetPublications` reports a target:

- When no current publication exists, it shows "Publish to <name>". Publishing copies the new link
  (web) or offers copy and share (iOS) and shows the link.
- When the current contents are published, it shows the link with copy and open actions, its
  expiry, and Unpublish.
- When only older versions are published, the viewer still offers to publish, and lists the
  earlier links with their dates so each can be copied or unpublished.

## Rejected alternatives

- **Serving shares from the daemon.** The daemon's token authorizes arbitrary shell, and its
  listener is meant for private networks; exposing an unauthenticated public route on it would
  widen that boundary.
- **Uploading from clients.** Clients would need the share token and a second network path, and
  iOS would have to fetch whole files that `ReadFile` caps at 8 MiB.
- **Content-addressed links.** Hash-derived URLs are guessable by anyone holding the file, and
  revoking one would revoke every share of identical bytes. Random ids keep each publication
  independently secret and revocable.
