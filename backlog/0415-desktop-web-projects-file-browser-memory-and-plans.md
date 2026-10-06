---
id: "0415"
title: 'Desktop web: projects, file browser, memory and plans'
status: done
priority: 3
created: "2026-10-02"
updated: "2026-10-06"
depends_on:
    - "0410"
spec_refs:
    - Daemon lifecycle and projects
    - Plans and memory
    - Project onboarding
---

## Description
Sixth phase of docs/design/web-client.md. Reference iOS AddProjectView/DirectoryBrowserView, FileBrowsing/FileBrowserModel/FileReference, and MemoryView.

- Projects: AddProject with a server-side directory picker (ListDir), RenameProject, and RemoveProject (confirmed).
- File browser: a tree via ListFiles plus a highlighted viewer via ReadFile, resolved against a session's live worktree when opened from a session. Transcript file references (path[:line]) open the viewer at that line in the inspector.
- Memory viewer (GetMemory, showing classification and provenance) and a plans library (ListPlans/GetPlan) with markdown rendering.

## Acceptance criteria
- [ ] Add a project by browsing directories, rename it, and remove it from the browser.
- [ ] Clicking a file reference in a transcript opens the right file and line from that session's worktree.

## Work log
