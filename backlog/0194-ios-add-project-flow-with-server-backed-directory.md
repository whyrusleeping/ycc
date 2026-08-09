---
id: "0194"
title: 'iOS: Add-project flow with server-backed directory picker'
status: in_review
priority: 3
created: "2026-07-10"
updated: "2026-08-08"
depends_on:
    - "0264"
    - "0193"
spec_refs: []
---

## Description
Upgrade the iOS add-project flow (task 0264 ships the entry affordance + manual path entry) with a server-backed directory picker.

UI: the add-project sheet from 0264 gains:
1. **Suggestions** — likely projects (git-repo siblings of registered projects, from the daemon's ListDir suggestions) as a one-tap list.
2. **Browse** — drill-down directory browser over the new `ListDir` RPC (NavigationStack, breadcrumb/up navigation, dirs only, git repos highlighted, registered projects marked), with a "Use this folder" confirm.
The manual path text field from 0264 remains as a fallback.

Acceptance:
- New `DirectoryBrowserModel` (or similar) in YccKit behind a Source protocol with headless unit tests (navigation, annotations, error/unauthorized handling), matching the existing model patterns.
- Swift proto regen for the new RPC (BSR remote plugins, network required).
- `YccClient` gains listDir (addProject arrives with 0264); unauthorized routes to connect screen like other models.
- Works end-to-end against a local daemon.

## Acceptance criteria

## Plan

Goal: upgrade the iOS add-project sheet (from 0264) with a server-backed directory picker over the existing `ListDir` RPC (0193). Swift protos are ALREADY regenerated (Ycc_V1_ListDirRequest/Response, listDir in ycc.connect.swift) — no regen needed.

1. YccKit — `YccClient.listDir(path:suggest:)`:
   - New wrapper method next to `addProject` in YccClient.swift: builds Ycc_V1_ListDirRequest {path, suggest}, calls `generated.listDir`, returns `Ycc_V1_ListDirResponse`, maps failures via `Self.map(error)` like the other unary wrappers.

2. YccKit — `DirectoryBrowserModel.swift` (new file), following the AddProjectModel/SessionListModel pattern:
   - `public protocol DirectoryBrowserSource: Sendable { func listDir(path: String, suggest: Bool) async throws -> Ycc_V1_ListDirResponse }`; `extension YccClient: DirectoryBrowserSource {}`.
   - `@MainActor @Observable public final class DirectoryBrowserModel`:
     - State: `path` (resolved current dir), `parent`, `entries: [Ycc_V1_DirEntry]`, `suggestions: [String]`, `isLoading`, `errorMessage: String?`, `unauthorized` (private(set), like AddProjectModel).
     - `loadInitial()` — listDir(path: "", suggest: true): lands on the daemon home dir and populates suggestions for the one-tap list.
     - `open(_ path: String)` — listDir(path:, suggest: false) drills into a subdirectory; on error keep the current listing and set errorMessage (don't wipe state on a failed drill-down).
     - `navigateUp()` — open(parent); `canGoUp` = !parent.isEmpty.
     - Unauthorized (`YccError.unauthorized`) sets `unauthorized` for the view to route to connect; other errors set `errorMessage` via `displayMessage`.

3. YccKit tests — `DirectoryBrowserModelTests.swift`:
   - Mock source keyed by path returning canned responses / errors. Cover: initial load resolves home + suggestions; drill-down updates path/parent/entries; navigateUp; annotations preserved (is_git_repo / is_registered flow through); error on drill-down keeps prior listing + sets message; unauthorized flag set; isLoading toggles / reentrancy guard.

4. App UI — AddProjectView.swift (+ new DirectoryBrowserView, either same file or its own in clients/ios/App):
   - Suggestions section: on sheet appear, fire a lightweight listDir(suggest: true); render suggested repo paths (abbreviated, monospaced) as one-tap rows that fill `model.path` (user still confirms with Add — keeps the existing error path and optional name field).
   - "Browse server…" row pushes DirectoryBrowserView inside the sheet's existing NavigationStack: shows current path (breadcrumb-ish title or header), an Up row when canGoUp, dir rows (folder icon; git repos highlighted e.g. accent color/badge; registered projects marked and shown dimmed/checkmarked), tapping a row drills in, and a prominent "Use this folder" button that writes the browsed path back into `model.path` and pops back to the form.
   - Unauthorized from the browser routes like the existing sheet (dismiss + app.handleUnauthorized()).
   - Manual path text field stays as-is (fallback).
   - Update the stale doc comment in AddProjectView ("path is typed manually for now").

5. Docs: touch docs/design/ios-client.md if it tracks the add-project flow status (small note that the picker landed).

Verification: no Swift toolchain on this machine — `swift test`/xcodebuild run on the user's Mac. Verify by careful review; task ends at status `in_review` (project convention: implemented, awaiting on-device use) and stays uncommitted alongside the other in_review iOS work (tree already holds 0294/0296/0297 uncommitted; the commit tool's `git add -A` must NOT be used).

### Starting points
- clients/ios/YccKit/Sources/YccKit/AddProjectModel.swift — pattern to mirror (Source protocol, @MainActor @Observable, unauthorized flag)
- clients/ios/YccKit/Sources/YccKit/YccClient.swift — addProject/renameProject wrappers show the unary + Self.map(error) pattern
- clients/ios/App/AddProjectView.swift — the sheet to extend (already inside a NavigationStack)
- Ycc_V1_ListDirRequest{path, suggest} / Ycc_V1_ListDirResponse{path, parent, entries:[Ycc_V1_DirEntry], suggestions:[String]} — already in YccProto; DirEntry has name/isGitRepo/isRegistered
- clients/ios/YccKit/Tests/YccKitTests/AddProjectModelTests.swift — headless test pattern with mock source
- daemon semantics (internal/server/listdir.go): empty path ⇒ home; parent=="" at root; entries dir-only, hidden skipped; suggestions are absolute paths of unregistered git-repo siblings

## Work log
- 2026-08-06 dep repointed 0192 → 0264 after duplicate-id renumbering (the add-project affordance task, not the all-projects list).
- 2026-08-08 plan: Goal: upgrade the iOS add-project sheet (from 0264) with a server-backed directory picker over the existing `ListDir` RPC (0193). Swift protos are ALREADY regenerated (Ycc_V1_ListDirRequest/Response, 
…[truncated]
- 2026-08-08 context hints: 6 recorded with plan
- 2026-08-08 context hints: clients/ios/YccKit/Sources/YccProto/ycc/v1/ycc.pb.swift — verify exact Ycc_V1_DirEntry field names before using them; clients/ios/YccKit/Sources/YccKit/YccClient.swift:54-99 — unary wrapper + Self
…[truncated]
- 2026-08-08 preload: 3 file(s), ~13 KiB seeded into implementer context
- 2026-08-08 implementer report: Implemented task 0194 and moved it to `in_review`.  Changes: - Added `YccClient.listDir(path:suggest:)` using the generated unary RPC and existing error mapping. - Added `DirectoryBrowserModel` behind
…[truncated]
- 2026-08-08 review tier: single-opus — reviewers: sol
- 2026-08-08 review (sol): accept — Task 0194 is implemented completely and consistently with the existing iOS model patterns. `YccClient.listDir` wraps the generated RPC with standard error mapping; `DirectoryBrowserModel` provides gua
…[truncated]
- 2026-08-08 usage: 1,551,401 tok (in 765,342, out 24,715, cache_r 1,059,150, cache_w 60,593) · cost n/a (unpriced)
  implementer: 939,969 tok (in 363,506, out 14,799, cache_r 561,664, cache_w 0) · cost n/a (unpriced)
  reviewer:sol: 605,475 tok (in 401,822, out 3,973, cache_r 199,680, cache_w 0) · cost n/a (unpriced)
  coordinator: 5,957 tok (in 14, out 5,943, cache_r 297,806, cache_w 60,593) · cost n/a (unpriced)
