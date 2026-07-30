---
id: "0219"
title: Add iOS project removal affordance
status: done
priority: 2
created: "2026-07-19"
updated: "2026-07-19"
depends_on: []
spec_refs:
    - docs/design/ios-client.md#RPC coverage map
---

## Description
Add a destructive, confirmed removal action to the iOS home project picker using the existing RemoveProject RPC. Removing a registration must not imply deleting workspace files; refresh the picker/session list and fall back to Default if the selected project is removed.

## Acceptance criteria
- Any registered project can be selected for removal from the home project menu.
- The app asks for destructive confirmation and explains that workspace files are not deleted.
- Successful removal refreshes projects and resets a removed active selection to Default.
- Unauthorized and RPC errors are surfaced consistently.
- YccKit tests cover the RemoveProject client/model-facing behavior.

## Work log
- 2026-07-19: Added `YccClient.removeProject`, model behavior/tests, and a destructive removal submenu + confirmation on the iOS home project picker. Successful removal falls back to Default when necessary and refreshes the list; failures and unauthorized responses are surfaced. Updated iOS design docs. `git diff --check` passes; Swift tests and app build could not run in this Linux environment (`swift`, `xcodegen`, and `xcodebuild` unavailable).
