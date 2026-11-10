---
id: "0481"
title: Retire the terminal TUI client
status: done
priority: 4
created: "2026-10-26"
updated: "2026-11-10"
depends_on: []
spec_refs: []
---

## Description

The README no longer covers the TUI, but the code is still there: `internal/tui` (plus `internal/tui/snapshot`), `ycc` with no subcommand launching `tui.Run`, `ycc --background`, the first-run setup wizard path in `cmd/ycc/main.go` (`runTUI`/`internal/setup`), the PTY end-to-end tests in `internal/e2e` (which CI requires to PASS), `docs/tui.png`, `docs/tui-components.md`, `docs/e2e-tui.md`, and spec mentions (§1 diagram, §18 "The TUI is the primary local surface", session browser, analytics clients).

## Acceptance criteria

- Decide what `ycc` with no arguments does once the TUI is gone (help text, or open/print the web UI URL), and where first-run config setup lives (`ycc login` only, or a CLI wizard).
- Remove the TUI package, its tests and docs, and the `--background` flag (or repoint it at a detached daemon only).
- Replace or drop the PTY e2e harness so CI still has end-to-end coverage of the daemon/CLI path.
- spec.md and docs no longer describe the TUI; `ycc spec-check` is clean.

## Outcome

TUI retired. `ycc` with no arguments prints help pointing at `ycc daemon --web` and `ycc login`. First-run setup is `ycc login` plus the web Get started checklist. Removed: internal/tui, the internal/setup wizard (its ConfigPath moved to daemon.UserConfigPath), the --background flag and EnsureBackgroundDaemon, and the TUI docs and image; go mod tidy dropped the Charm, pty and vt dependencies. internal/e2e now drives the built binary as a persistent daemon (chat tool round trip plus `ycc list`; ask_user answered over the API), the one-shot in-process path, and the no-argument help, with CI still requiring PASS and no SKIP. spec.md, docs and plans no longer describe the TUI; 4 earlier spec-check drifts were fixed, so `ycc spec-check` exits 0. `go test ./...` passes; reviewer (sol) accepted.

Commit: Retire the terminal TUI: remove internal/tui, internal/setup wizard and --background; plain ycc prints help; daemon/CLI e2e harness replaces the PTY suite; spec/docs updated, spec-check clean (0481)
