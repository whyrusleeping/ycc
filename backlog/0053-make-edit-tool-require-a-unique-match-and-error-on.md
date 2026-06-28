---
id: "0053"
title: Make edit tool require a unique match and error on multiple matches
status: todo
priority: 3
created: "2026-06-28"
updated: "2026-06-28"
depends_on: []
spec_refs: []
---

## Description
The edit tool currently performs find/replace and reports how many replacements were made, allowing multiple matches to be replaced in one call. This is error-prone: an ambiguous match can silently edit places the caller didn't intend.

Change the edit tool to enforce a single, unique match:

- The search string must match exactly once in the target file.
- If it matches zero times, error as it does today (no match found).
- If it matches more than once, error out (e.g. "found N matches; the search text must be unique — add more surrounding context") instead of replacing all of them.
- Drop / simplify the "N replacements made" reporting, since a successful edit now always means exactly one replacement.

## Acceptance criteria
- Editing with a search string that matches exactly once succeeds and applies the replacement.
- Editing with a search string that matches multiple times returns an error and does NOT modify the file.
- Editing with a search string that matches zero times returns a clear "no match" error.
- The multi-match error message guides the caller to add more surrounding context to disambiguate.
- Existing tests updated and new tests cover the multi-match error case.

## Acceptance criteria

## Work log
