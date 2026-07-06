---
id: "0136"
title: 'First-run wizard: capture, store & verify API keys (close the post-wizard 401 pit)'
status: done
priority: 2
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 19.1 First-run setup (global — model providers & roles)
---

## Description
The first-run wizard (internal/setup) collects only a `key_env` **name** — never the key value. A brand-new user finishes the wizard, starts their first session, and hits a 401, because nothing told them to `export ANTHROPIC_API_KEY` or run `ycc token set`. The machine-local secrets store (`internal/secrets`) already exists; the wizard just doesn't use it. This is the single biggest onboarding pit.

Also: `setup.NeedsSetup` checks only `os.Getenv("ANTHROPIC_API_KEY")`, not the secrets store, and the wizard's model-id defaults are stale/hand-rolled (`gpt-4o`) instead of reusing the curated defaults + `DiscoverModels` seeding the TUI backend manager already has (spec §18.2/§13).

## Acceptance criteria
- [ ] Per provider, the wizard offers an optional "paste the API key now" field (masked input); a pasted key is stored via `secrets.Set(key_env, value)`, never written into `ycc.toml`.
- [ ] A "verify" step tests the connection per provider (e.g. `DiscoverModels` or a minimal completion) and shows pass/fail inline before finishing; failures can be edited and retried, or accepted anyway.
- [ ] Model-id defaults are seeded from the same curated per-backend defaults the TUI backend manager uses, with discovery (ctrl+f equivalent) available once a key is entered.
- [ ] `NeedsSetup` treats a key present in the secrets store like an env key (no spurious wizard re-run).
- [ ] Skipping the wizard prints concrete next steps ("set ANTHROPIC_API_KEY, or run `ycc token set ANTHROPIC_API_KEY`, or re-run setup from esc → settings") instead of proceeding silently toward a keyless 401.

## Plan

Goal: close the post-wizard 401 pit — the first-run wizard (internal/setup) should capture the API key value (optionally), verify connections, seed curated model ids, and NeedsSetup must respect the secrets store; skipping must print concrete next steps.

Changes, all in internal/setup (plus a test file), no daemon involvement (the wizard runs pre-daemon, so use config.DiscoverModels directly, NOT the TUI's RPC path):

1. API key capture (provider editor, wizard.go):
   - Add a new masked field `fieldKey` ("api key (optional)") to the provider form, textinput with EchoMode=EchoPassword. Store the pasted value on the `provider` struct (`key string`).
   - Keys are NEVER written into ycc.toml. In setup.Run, after config.Save succeeds, persist each provider's non-empty pasted key via secrets.Set(p.keyEnv, p.key) — only when keyEnv is non-empty (ollama has none). Persist at completion, not mid-wizard, so a skipped wizard stores nothing. Best-effort: on secrets.Set error print a warning to stderr but don't fail setup.

2. Verify step (new step between provider entry and stepAddMore):
   - After Enter on a valid provider form, go to a new `stepVerify` that runs a connection check for that provider asynchronously (tea.Cmd → verifyMsg) and shows a "verifying…" line then pass/fail inline.
   - The check: config.DiscoverModels(ctx, backend, baseURL, resolvedKey) with a short timeout (~10s). resolvedKey order: pasted key > os.Getenv(keyEnv) > secrets.Lookup(keyEnv) > "" (ollama keyless is fine).
   - On pass: show ok + let Enter continue to stepAddMore (or auto-advance; implementer's choice, but show the result at least until a keypress).
   - On fail: show the error and offer: [e]dit (return to provider editor with all values retained, including the key), [r]etry, [enter] accept anyway (continue to stepAddMore). Esc still skips the whole wizard.
   - Make the verifier injectable on the model (e.g. `verify func(p provider) error` field defaulting to the real DiscoverModels-based check) so tests can drive stepVerify without network.

3. Curated model defaults + discovery:
   - Replace the hand-rolled defaultModel() values with config.CuratedModelIDs(backend): default the model field to the first curated id; keep the existing applyBackendDefaults "don't clobber user edits" behavior (compare against the previous backend's curated default).
   - In the provider editor, add ctrl+f: fetch ids via config.DiscoverModels using the same key resolution as verification; on success, store the id list on the model and let the user cycle it into the model field (ctrl+n/ctrl+p, mirroring the TUI backend manager's preset cycling) with an inline info line ("N models fetched"); on failure show the error inline (editErr or a separate info line) and keep the curated list as the cycle source. Update the help footer.

4. NeedsSetup (setup.go): also treat a key present in the secrets store as usable: return false when secrets.Lookup("ANTHROPIC_API_KEY") hits. Update the doc comment.

5. Skip guidance: in setup.Run, when the user skips (esc/ctrl+c) print concrete next steps to stderr, e.g.:
   "ycc: setup skipped — to get a working model either:\n  export ANTHROPIC_API_KEY=sk-...\n  or run: ycc token set ANTHROPIC_API_KEY\n  or re-run setup later from esc → settings"
   (verify the exact `ycc token` subcommand syntax in cmd/ycc/main.go tokenCommand and match it).

6. Tests (internal/setup/setup_test.go / new wizard_test.go):
   - NeedsSetup: with no config and no env key but a secrets-store token (point os.UserConfigDir at a temp dir via t.Setenv("XDG_CONFIG_HOME", ...), write secrets.json via secrets.Set) → false; with none → true. Mirror how internal/secrets/secrets_test.go isolates the store.
   - Wizard: drive the model with synthetic tea.KeyMsg through the new key field and stepVerify with a stubbed verifier (pass, fail→retry, fail→edit retains values, fail→accept-anyway); assert the pasted key lands on the provider struct and that buildConfig output never contains the key value.
   - Keep existing tests passing; `go build ./... && go test ./...` clean; `go vet ./...` clean.

Constraints: don't change the config schema; don't touch the TUI backend manager; keep the wizard dependency-light (it must run before any daemon exists).

### Starting points
- internal/setup/wizard.go — provider form, steps, key handling
- internal/setup/setup.go — NeedsSetup, Run, defaultModel to replace
- internal/secrets/secrets.go — Set/Lookup; secrets_test.go shows XDG_CONFIG_HOME isolation
- internal/config/discover.go — CuratedModelIDs, DiscoverModels(ctx, backend, baseURL, key)
- cmd/ycc/main.go:461 tokenCommand — exact `ycc token set` syntax for the skip message
- internal/tui/tui.go ~5508-5760 — backend manager's ctrl+f/ctrl+n/p pattern for reference only

## Work log
- 2026-07-06 plan: Goal: close the post-wizard 401 pit — the first-run wizard (internal/setup) should capture the API key value (optionally), verify connections, seed curated model ids, and NeedsSetup must respect the
…[truncated]
- 2026-07-06 context hints: 6 recorded with plan
- 2026-07-06 context hints: internal/setup/wizard.go — provider form, steps, focus handling; internal/setup/setup.go — NeedsSetup, Run, defaultModel to replace; internal/secrets/secrets.go — Set/Lookup; secrets_test.go sho
…[truncated]
- 2026-07-06 implementer report: Implemented Task 0136 — first-run wizard now captures, stores & verifies API keys, closing the post-wizard 401 pit. All changes confined to internal/setup (plus tests); no daemon/RPC involvement (wi
…[truncated]
- 2026-07-06 review tier: single-opus — reviewers: Claude
- 2026-07-06 review (Claude): accept — The change fully satisfies Task 0136. The first-run wizard now captures an optional masked API key, persists it to the machine-local secrets store (never into ycc.toml) only at completion, adds a step
…[truncated]
- 2026-07-06 decision: accept — commit: First-run wizard: capture, store & verify API keys; curated model defaults; secrets-aware NeedsSetup; skip guidance (task 0136)
- 2026-07-06 usage: 38,013 tok (in 114, out 37,899, cache_r 2,210,113, cache_w 147,199) · cost n/a (unpriced)
  implementer: 29,390 tok (in 68, out 29,322, cache_r 1,505,214, cache_w 63,220) · cost n/a (unpriced)
  coordinator: 5,020 tok (in 24, out 4,996, cache_r 557,614, cache_w 60,584) · cost n/a (unpriced)
  reviewer:Claude: 3,603 tok (in 22, out 3,581, cache_r 147,285, cache_w 23,395) · cost n/a (unpriced)
