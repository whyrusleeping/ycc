---
id: "0399"
title: 'iOS: project file browser/viewer + tappable file links in agent markdown'
status: in_review
priority: 2
created: "2026-09-24"
updated: "2026-09-24"
depends_on:
    - "0398"
spec_refs:
    - 18. Client interaction model
    - docs/design/ios-client.md#Navigation and interaction
---

## Description
Build an iOS file browser and viewer on top of the 0398 `ListFiles`/`ReadFile` RPCs, and make the file references in agent markdown tappable.

**Browse**: add a "Files" entry per project (drawer row and/or project toolbar) and a new `HomeRouter` route `.files(project, path)`. The directory list shows folder/file icons, size, and relative mtime, with gitignored entries dimmed. Tapping pushes into a folder or opens a file. A breadcrumb or title shows the relative path. Headless model in YccKit (`FileBrowserModel`), mirroring `DirectoryBrowserModel`, with tests.

**View** (`FileViewerView`):
- Text: monospaced with a line-number gutter, horizontal scroll, and selectable text. Supports a target line/range: scroll to it and highlight it.
- `.md`: rendered with `MarkdownText` by default, with a Raw toggle. Relative links inside resolve against the **file's directory**.
- Images: rendered inline.
- Binary files: a "binary, N bytes" placeholder.
- Truncated files: a notice.
- NotFound: a friendly "file not found (may have been moved, or lived in a reclaimed worktree)". If the daemon fell back from a session worktree to the project root, show a small note.
- Toolbar: copy path, copy contents, share.
- Syntax highlighting is IN scope (user decision, 2026-09-24): a lightweight in-YccKit tokenizer (keywords/strings/comments/numbers per language family — Go, Swift, Rust, Python, JS/TS, shell, proto, TOML/YAML/JSON), testable headlessly, no new heavy dependency. Unknown languages fall back to plain monospace.
- Strictly read-only (user decision): no editing affordances.

**Links from transcripts**:
- `MarkdownText` gets an injected link handler: `.environment(\.openURL, OpenURLAction { … })`. http(s)/mailto go to the system. Anything else that parses as a relative path (optionally with `#L12`, `#L12-L20`, `:12` or `:12-20`) opens the viewer. The link context is (project, session_id, base dir): the workspace root for transcripts, the file's dir for rendered markdown files, and `backlog/` for task bodies (task bodies link siblings like `0378-….md`).
- Auto-link inline code spans that look like repo paths, since this is the dominant form in agent output. Rule: contains a `/` or ends in a known source/doc extension, has no spaces, and takes an optional `:line[-line]` suffix. Keep the monospace styling and add a link tint. Keep the matcher in YccKit as a pure function with tests: `internal/server/listdir.go:42` ✓, `spec.md` ✓, `go test ./...` ✗, `ycc://x` ✗, `--flag` ✗, `a/b` with spaces ✗.
- Presentation: open the viewer in a **sheet** with its own NavigationStack. That keeps transcript scroll position, respects the hub-and-spoke rule (no stack growth from lateral hops), and lets file→file links push inside the sheet.

Update docs/design/ios-client.md (Navigation and interaction) with the files route and the link-handling rule.

## Acceptance criteria
- YccKit: `FileBrowserModel` and link/path-reference parsing (markdown URL + code-span matcher + line-suffix parsing + base-dir resolution), with unit tests.
- App: Files route, browser, viewer, and link interception in MarkdownText (transcript, task bodies, rendered md files).
- plans/ios-client-smoke.md gets on-device checks: tap a transcript link → sheet opens at the highlighted line; browse the project tree; a markdown file's relative link; a workstream-session link after reclaim shows the fallback note.
- Status goes to in_review until it's been used on a device (no Swift toolchain here).

## Work log

- 2026-09-24: Implemented; awaiting on-device use (uncommitted).
  - **YccKit:**
    - `FileReference.swift`: `FileReference`/`FileRoute`/`FileLinkContext`. `classify(url:)` treats http(s)/mailto/ycc as external and everything else, including `foo.go:12` (which parses with URL scheme `foo.go`), as a path. It handles `#L12[-L20]` fragments and `:12`/`:12-20`/`:12:5` suffixes, percent-decoding, stripping known absolute roots, `..` normalization and escape rejection. `fromCodeSpan` is conservative (known extensions or well-known names, no whitespace, resolves from the root). Recognized code spans get a private `ycc-file:///` link URL so the base dir isn't applied twice.
    - `SyntaxHighlighter.swift`: a line scanner with carried block-comment/multi-line-string state, 13 language families. It takes ~20 ms for the 130 KB session.go.
    - `FileBrowserModel.swift`: `ProjectFileSource`, `FileBrowserModel`, and `FileViewerModel`. Highlighting runs detached. On invalid_argument, the viewer falls back to a listing, which handles links to directories without a trailing `/`. CRLF splitting accounts for `\r\n` being one Character.
    - `YccClient.listFiles/readFile`, and tests in `FileBrowsingTests.swift`.
  - **App:**
    - `App/FileBrowsing.swift`: environment `fileLinkContext`/`fileNavigator`, the `.fileLinks(_:presentation:)` modifier (an `OpenURLAction` that uses a sheet from transcripts and pushes inside files), `FileSheet` (own typed stack), `FileScreen`, `FileBrowserView`, `FileViewerView` (raw/wrap toggles, copy path/contents, share, fallback and truncation notes), and the lazy `CodeTextView` (line gutter, target-line highlight and scroll).
    - `MarkdownText` autolinks code-span paths when a context is set.
    - `HomeDestination.file(FileRoute)`, a Files entry in the landing overflow, "Browse files" in the session overflow (session worktree), and link contexts on SessionView, TaskDetailView (base `backlog`) and MemoryView.
  - **Docs:** ios-client.md Navigation section; plans/ios-client-smoke.md §6.
  - **Verification:** the YccKit pieces (FileReference, SyntaxHighlighter, the models and the real YccError, plus the generated protos with a stub YccClient) compile and all 19 tests pass on Linux in a `swift:6.2-noble` Docker scratch harness (/tmp/ycchk). The SwiftUI App layer and the `AttributedString(markdown:)` code-span post-processing (Linux Foundation has no markdown parser) were NOT compiled here and need an Xcode build.
  - `absoluteRoots` is not populated yet. Absolute-path links appeared in 0 of the sampled transcripts; wiring in the project path from ListProjects is a small follow-up if needed.
