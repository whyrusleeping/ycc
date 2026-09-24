---
id: "0398"
title: 'Daemon: project-confined ListFiles/ReadFile RPCs for remote file browsing'
status: done
priority: 2
created: "2026-09-24"
updated: "2026-09-24"
depends_on: []
spec_refs:
    - 12. RPC protocol
    - 14. Persistence, remote access, and workstreams
    - docs/remote-api.md#AddProject / ListDir
---

## Description
Remote clients (iOS first) can't view project files. Two uses: browsing a project's tree, and following the file references agents put in transcript markdown. A sample of recent agent output had ~100 markdown file links (`[x](bench/…/FINDINGS.md)`, `…/index.rs#L291`, `…:42`) and ~1250 backtick-quoted paths (`internal/server/listdir.go:42`). Links are always relative to the workspace root and never absolute. Right now tapping one on iOS does nothing.

The existing `ListDir` only lists directories under $HOME, for the add-project picker. It explicitly never returns files or contents, and that should stay true. Add separate read-only RPCs that are **confined to a project**:

- `ListFiles{project, session_id?, path}` → `{root, path, entries[{name, is_dir, size, mtime, is_symlink, ignored}]}`. `path` is relative to the root, and empty means the root itself. Directories sort first, then by name. Dotfiles and dot-directories (including `.git` and `.ycc`) are omitted from listings (user decision, 2026-09-24). `ignored` marks gitignored entries, which the client dims. ReadFile of an explicitly linked dotfile path (for example `.github/…`) is still allowed; paths inside `.git/` are refused. Strictly read-only: no write/rename/delete RPCs. The listing is capped with a `truncated` flag.
- `ReadFile{project, session_id?, path, max_bytes?}` → `{root, path, data, size, media_type, is_binary, truncated, mtime}`. Text is capped at about 1 MiB by default and still returns a prefix plus `truncated` when over the cap. Binary detection uses the NUL-in-first-8KiB check plus the extension/`http.DetectContentType`. Images (png/jpeg/gif/webp/svg?) come back as bytes under the same cap, so clients can render them inline.

Choosing the root: if `session_id` is set and that session's workspace still exists (for example, a live workstream worktree), use it. Otherwise use the project's registered path. Report the chosen root so clients can say "showing main checkout; worktree reclaimed" when it fell back. The worktree case matters because agents in workstreams link to files that don't exist on the base branch yet.

Path handling: reject absolute paths, and reject any `..` escape after `filepath.Clean` with InvalidArgument. Resolve symlinks and require the real path to stay inside the root; otherwise return PermissionDenied. Missing files → NotFound, and a directory passed to ReadFile → InvalidArgument. Tolerate a trailing `:N`, `:N-M` or `#LN` line suffix: strip it server-side OR in the client, but pick one and document it. The preference is to do it in the client and keep the RPC path pure.

Trust note: the bearer token already allows StartSession, which means arbitrary bash in any workspace. Project-confined reads therefore don't widen the trust surface. Record this in docs/remote-api.md next to the ListDir note, and update the ListDir wording so it doesn't read as a blanket "never file contents" guarantee.

## Acceptance criteria
- proto + Go and Swift regen (both committed, per 0254).
- Handlers in internal/server with tests covering: root vs. session-worktree resolution and the fallback when the worktree is gone; `..`/absolute rejection; a symlink escaping the root being denied; NotFound; the directory-vs-file errors; the truncation cap; binary/image detection; `.git` omitted; the ignored flag.
- docs/remote-api.md documents both RPCs, with curl examples and the trust note. spec §12/§14 gets a short mention.

## Work log

- 2026-09-24: Implemented. `internal/projectfs` holds the pure, root-confined logic:
  - `CleanRel` rejects absolute paths and `..` escapes with ErrInvalidPath, and rejects `.git` components with ErrDenied.
  - `resolve` runs EvalSymlinks on the root and the target, requires the target to stay inside the root, and re-checks for `.git` after resolution.
  - `List` hides dotfiles, sorts dirs first and then names case-insensitively, caps at 5000 entries, and marks ignored entries with one `git check-ignore -z --stdin` call (best effort, 5s timeout; outside a repo nothing is marked).
  - `Read` only accepts regular files, so a FIFO can't block it. Binary detection is a NUL in the first 8 KiB or an `http.DetectContentType` image type. Text over the cap is cut at a line boundary, or at a rune boundary for a single huge line. Images are returned only when within the cap; other binaries return metadata only. Default cap 1 MiB, clamped to 8 MiB.
  - `session.Manager.FileRoot(project, sessionID)` tries the live session's workspace first, then the registry's in-flight workstream worktree (VerifyUnderRoot + exists), then the project root; `fellBack` is set when a session's workspace is gone.
  - Handlers are in internal/server/files.go, with error→code mapping in `fileError`. Line suffixes are stripped client-side (documented).
  - Tests: internal/projectfs/projectfs_test.go and internal/server/files_test.go, end to end over h2c with a real workstream: live, then stopped, then discarded (fallback).
  - Docs: remote-api.md "ListFiles / ReadFile" (plus the catalog row and a ListDir cross-reference) and spec §12. Go and Swift protos regenerated (additive only).
  - Pre-existing unrelated failure: `internal/docs` TestRepositoryDocsConfig (committed `.ycc/config.toml` globs `docs/*.md` but the test expects only docs/design).
