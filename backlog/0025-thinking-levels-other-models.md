---
id: "0025"
title: Verify thinking levels (effort) across backends as models are added
status: done
priority: 3
created: "2026-06-26"
updated: "2026-07-06"
depends_on:
    - "0005"
    - "0120"
spec_refs:
    - Backends & model registry
    - Agent engine
---

## Description
gollama now supports Anthropic extended thinking + effort (done outside the ycc backlog, in
/home/why/code/gollama): `RequestOptions.Thinking` ("adaptive"), `Effort` ("low".."max"),
`ThinkingDisplay` map to the Anthropic `thinking` + `output_config.effort` fields; response
`thinking`/`redacted_thinking` blocks are parsed into `Message.Thinking` +
`Message.ThinkingBlocks` and replayed verbatim (signatures intact) so tool-using turns
round-trip. Verified offline + live against claude-opus-4-8 (gollama `thinking_test.go`,
`anthropic_live_test.go`).

**The translation is Anthropic-only.** Each backend expresses reasoning differently, so as we
wire more models into the registry we need to verify (and, where missing, implement) the
mapping per backend:
- **OpenAI / GPT** — `reasoning_effort` (low/medium/high) as a request field; gollama's OpenAI
  path does NOT send `Effort`/`Thinking` yet.
- **GLM** (OpenAI-compatible) — provider-specific thinking parameter; confirm shape.
- **Ollama** — `think` bool (the existing `RequestOptions.Think`); on/off only, no levels.

This task is the cross-backend verification pass; it pairs with the (separate) ycc-side work
to plumb per-role `effort`/`thinking` config (beside `max_tokens`) through config → session →
engine and to surface returned thinking in the event log/TUI.

## Acceptance criteria
- [ ] for each configured backend (anthropic ✓, openai/gpt, glm, ollama): confirm how it
      expresses thinking levels and that gollama translates `Effort`/`Thinking` into the right
      request shape (e.g. OpenAI `reasoning_effort`) — implement the missing translations
- [ ] live smoke test per backend: a reasoning prompt returns reasoning content and the
      request is accepted; a tool round-trip with thinking on does not error
- [ ] document the per-backend mapping (what "effort" means where; what's unsupported — e.g.
      Ollama is on/off only)
- [ ] decide + implement ycc behavior when a model doesn't support a requested level (silently
      ignore vs. error) so a per-role effort setting degrades gracefully across mixed backends
      — **decided 2026-07-08 (pm, with user): ignore, but emit a one-time warning in the
      session log** (per session/role, not per request) when a role's effort/thinking setting
      hits a backend that can't express it

## Plan

Goal (narrowed scope per work log): implement the missing OpenAI-compatible + Ollama thinking/effort translations in gollama, live-smoke what we can (Ollama locally; Anthropic already done), document the per-backend mapping, and implement the decided ycc degrade behavior (ignore + one-time session-log warning). The OpenAI live smoke requires OPENAI_API_KEY (absent) — write it key-guarded and split its execution into a follow-on task.

VERIFIED ENVIRONMENT FACTS (this session):
- gollama working repo exists at /home/why/code/gollama (HEAD 4140920, clean).
- Local Ollama at localhost:11434 is live; its OpenAI-compatible /v1/chat/completions endpoint HONORS `"think": true` and returns the reasoning text in `message.reasoning`; native /api/chat returns it as `message.thinking`. Model gemma4:26b produces thinking. No OPENAI_API_KEY anywhere (env, ~/.config/ycc/secrets.json).
- OpenAI's documented `reasoning_effort` values: none|minimal|low|medium|high|xhigh (model-dependent). gollama Effort levels: low|medium|high|xhigh|max.

WORKSPACE MECHANICS (same as task 0120): git clone /home/why/code/gollama .gollama-work (inside the ycc workspace; check .git/info/exclude already lists it, add if not). Edit only in .gollama-work; build/test there. When done: commit in .gollama-work, then `git -C /home/why/code/gollama pull --ff-only /home/why/code/ycc/.gollama-work main` (branch name may be master — check), then `git -C /home/why/code/gollama push origin main`. Record the sha. Then ycc: `GOPRIVATE=github.com/whyrusleeping go get github.com/whyrusleeping/gollama@<sha> && go mod tidy`. rm -rf .gollama-work at the very end.

PART A — gollama:
1. openai.go: add to openaiRequest: `ReasoningEffort string `json:"reasoning_effort,omitempty"`` and `Think bool `json:"think,omitempty"``. In ChatCompletion's OpenAI-compatible branch, branch on c.Backend():
   - BackendOpenAI (incl. GLM-style endpoints): if opts.Effort != "" set req.ReasoningEffort = mapOpenAIEffort(opts.Effort): low/medium/high/xhigh pass through; "max" → "xhigh" (closest OpenAI level). opts.Thinking has no OpenAI request equivalent (effort IS the knob) — not sent. Never send `think`.
   - BackendOllama: if opts.Think || opts.Thinking != "" set req.Think = true (on/off only). Never send reasoning_effort (levels inexpressible on Ollama — that's the documented degrade).
2. Response normalization: add `Reasoning string `json:"reasoning,omitempty"`` to Message (types.go) so Ollama /v1's `message.reasoning` is captured; after decode in ChatCompletion, if Choices[0].Message.Thinking == "" and .Reasoning != "", copy Reasoning into Thinking and clear Reasoning — callers keep the single normalized Message.Thinking field (matches Anthropic behavior; ycc's thinking event path lights up for free). Check Message's custom MarshalJSON (types.go ~line 215) so assistant-turn replay serialization is unchanged (do not start emitting a `reasoning` key on replay).
3. Documentation: update the RequestOptions doc comments (Thinking/Effort/Think in types.go) into the authoritative per-backend mapping: anthropic → thinking{type:adaptive}+output_config.effort (low..max); openai-compatible → reasoning_effort (low|medium|high|xhigh; max clamps to xhigh; model-dependent per OpenAI docs); ollama → think bool (on iff Think or Thinking set; effort levels ignored); bedrock → not translated (ignored). Also fix the now-stale comment in anthropic.go (~line 183) saying "Other backends ignore opts.Thinking/Effort; they are only translated here".
4. Offline tests (httptest pattern like anthropic_test.go / turn_test.go), e.g. openai_thinking_test.go:
   - OpenAI backend: Effort="high" → request body contains "reasoning_effort":"high"; Effort="max" → "xhigh"; Effort="" → key absent; `think` key never present.
   - Ollama backend (client baseURL containing :11434, path /v1): Thinking="adaptive" → body has "think":true and NO "reasoning_effort"; Effort set → still no reasoning_effort; response JSON with message.reasoning → returned Message.Thinking populated, Reasoning empty.
5. Live smokes:
   - New ollama_live_test.go: skip with t.Skip unless localhost:11434 is reachable (quick GET /api/tags with short timeout). Model "gemma4:26b". (a) Turn with Thinking="adaptive", Effort="high" (effort must be harmlessly ignored): assert no error, non-empty Choices[0].Message.Thinking, non-empty or empty Content tolerated but request accepted. (b) tool round-trip with thinking on: define a trivial tool (e.g. get_weather), prompt that invites a call; if the model returns a tool call, append the assistant msg + tool result and run a second Turn — assert no error end-to-end. If gemma4:26b rejects tools (Ollama 400 "does not support tools"), note it in the test and fall back to asserting the thinking-on plain round-trip (2 sequential turns with history replay) does not error — document which path ran.
   - New openai_live_test.go (or extend an existing pattern): guarded by OPENAI_API_KEY (t.Skip when absent) — Turn against https://api.openai.com/v1 with a reasoning model (e.g. "gpt-5.1") and Effort="low"; assert accepted + answer returned. It will be exercised by the follow-on task when a key is available.
6. cd .gollama-work && go build ./... && go vet ./... && go test ./... (Anthropic live tests run with ANTHROPIC_API_KEY; Ollama live tests run against local daemon). Remove the stray untracked .edit-test.txt? — NO: that's in /home/why/code/gollama itself, leave it alone (don't commit it; a fresh clone won't include it).

PART B — ycc (after the go.mod bump):
1. Degrade behavior (decided 2026-07-08 with user: ignore + ONE-TIME warning in the session log, per session/role): in internal/engine/loop.go, before/around the turn request in Run(), when think.Thinking != "" emit at most one event.Narration per Loop describing what the backend cannot express, guarded by a private bool (e.g. l.thinkingWarned) that resets in SetBackend and SetThinking (so a backend/level change may warn once more — still per role/session in practice):
   - backend "ollama": msg like `thinking: backend ollama supports on/off only; effort "<X>" is ignored (thinking stays enabled)`.
   - backend "anthropic" and "openai": no warning (fully expressible / levels expressible).
   - any other backend (e.g. bedrock, unknown): msg `thinking: backend <b> does not support thinking/effort; settings ignored`.
   Use l.Backend (already on the Loop; compare case-insensitively like the existing strings.EqualFold(l.Backend, "anthropic") at loop.go:360). Payload shape: map[string]any{"msg": ...} like existing Narration emits.
2. Unit test in internal/engine/loop_test.go (existing fake Turner + emitter patterns): ollama backend + thinking enabled → exactly one Narration with the warning across a multi-turn run; anthropic/openai backends → zero warnings; thinking disabled (empty Thinking) → zero warnings.
3. spec.md: update §7.4 (reasoning/thinking settings) and §13 where it mentions ignored-harmlessly semantics: add the concise per-backend mapping (anthropic thinking+effort; openai reasoning_effort with max→xhigh clamp; ollama think on/off, levels ignored; bedrock/others ignored) and the degrade rule: unsupported settings are ignored, with a one-time per-session/role warning in the session log.
4. go.mod bump as above; then run the repo runbook: go build ./... && go vet ./... && go test ./... — all green.
5. rm -rf .gollama-work.

ACCEPTANCE MAPPING: criterion 1 (translations) → Part A 1–3; criterion 2 (live smokes) → Ollama done live locally, Anthropic previously done, OpenAI test written but key-guarded (execution split to follow-on task — coordinator will create it); criterion 3 (mapping doc) → A3 + B3; criterion 4 (degrade decision implemented) → B1–B2. GLM remains deferred per the 2026-07-05 grooming (treated as generic OpenAI-compatible: it receives reasoning_effort; provider-specific thinking param verification deferred).

## Work log
- 2026-07-08 pm grooming (with user): unblocked to todo — the gollama working repo now
  exists at /home/why/code/gollama and the user can attend live smokes / supply keys.
  Sequenced **after 0120** (added as a dependency): both touch gollama's turn/request
  paths, so doing effort translation after TurnStream lands avoids churn. Degrade
  decision recorded: ignore + one-time session-log warning (see acceptance criteria).
  Scope remains OpenAI + Ollama (GLM deferred).
- 2026-07-05 re-blocked (autonomous coordinator): this session cannot complete the
  narrowed scope — the gollama working repo (/home/why/code/gollama) is still absent
  (only the read-only module cache exists) and no OPENAI_API_KEY is available, so the
  missing OpenAI translation cannot be implemented or smoke-tested. Verification done
  meanwhile against the pinned gollama (v0.0.0-20260628184513):
  - anthropic: `Thinking`/`Effort`/`ThinkingDisplay` → `thinking` + `output_config.effort`
    confirmed in anthropic.go (done, as recorded).
  - openai/openai-compatible/glm: `ChatCompletion`'s openaiRequest has NO
    `reasoning_effort` field; `Thinking`/`Effort` are `json:"-"` so they are silently
    dropped → degrade today is "silently ignore" (safe, but no reasoning control).
  - ollama: `Turn` routes Ollama through the OpenAI-compatible /v1 path, so the
    `Think` bool is also dropped (native /api/chat would carry it, but Turn never uses
    it). Local live check: Ollama /api/chat with `think:true` on gemma4:26b succeeds and
    returns a `thinking` field — the live smoke is ready to run once gollama plumbs it.
  Remaining (needs gollama repo + OpenAI key, user present): implement OpenAI
  `reasoning_effort` + Ollama `think` translation in gollama Turn path, live smokes,
  then the mapping doc + explicit degrade decision here. Note ycc already passes
  Effort/Thinking on every request regardless of backend, so gollama-side translation
  lights up without further ycc plumbing (spec §7.4, §13 already document
  ignored-harmlessly semantics).
- 2026-07-05 unblocked (pm grooming with user): scope narrowed to the backends the user
  has live access to right now — **OpenAI + Ollama** (Anthropic already done). GLM is
  deferred: keep its bullet in the description but treat it as out of scope for this
  pass; verify it later when an endpoint is available. Live smoke tests for OpenAI and
  Ollama are in scope since the user can attend/supply keys.
- 2026-07-02 blocked: parked for the overnight autonomous run — requires live smoke tests against OpenAI/GLM/Ollama backends (keys/endpoints not available unattended) and edits in the separate gollama repo; user wants to be present. Unblock when the user can supply/verify backend access.
- 2026-07-06 plan: Goal (narrowed scope per work log): implement the missing OpenAI-compatible + Ollama thinking/effort translations in gollama, live-smoke what we can (Ollama locally; Anthropic already done), document 
…[truncated]
- 2026-07-06 context hints: gollama repo: /home/why/code/gollama (HEAD 4140920, clean; branch main) — clone to .gollama-work, do not edit the real repo directly; gollama openai.go:47 ChatCompletion, :102 openaiRequest literal;
…[truncated]
- 2026-07-06 implementer report: Completed task 0025: cross-backend thinking/effort verification + missing translations + graceful degrade.  ## PART A — gollama (pushed to origin/main, sha c92bd8b4201ff472d4e8d916ce03c6020d9e05b6) 
…[truncated]
- 2026-07-06 review tier: single-opus — reviewers: Claude
- 2026-07-06 review (Claude): accept — The change correctly implements the ycc-side portion of the cross-backend thinking/effort verification. The gollama translations (OpenAI reasoning_effort with max→xhigh clamp, Ollama think bool, rea
…[truncated]
- 2026-07-06 decision: accept — commit: Cross-backend thinking/effort: OpenAI + Ollama translations, graceful degrade (task 0025)  gollama c92bd8b adds the missing reasoning translations on the OpenAI-compatible path: Effort → reasoning_e
…[truncated]
- 2026-07-06 usage: 54,192 tok (in 222, out 53,970, cache_r 5,074,064, cache_w 205,346) · cost n/a (unpriced)
  implementer: 29,539 tok (in 136, out 29,403, cache_r 3,539,015, cache_w 82,195) · cost n/a (unpriced)
  coordinator: 19,358 tok (in 50, out 19,308, cache_r 1,225,055, cache_w 95,407) · cost n/a (unpriced)
  reviewer:Claude: 5,295 tok (in 36, out 5,259, cache_r 309,994, cache_w 27,744) · cost n/a (unpriced)
