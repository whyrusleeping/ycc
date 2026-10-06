// Settings: model drafts (validation, key_env-as-a-name, request building
// from an allowlist so no secret can ride along), review-tier drafts, and the
// Anthropic login guards.
import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { ModelConfigSchema, ModelInfoSchema, ReviewTierInfoSchema, WorkLoopInfoSchema } from "../src/gen/ycc/v1/ycc_pb";
import {
  MODEL_CONFIG_FIELDS,
  canSubmit,
  discoverRequest,
  draftFromConfig,
  emptyDraft,
  isEnvName,
  looksLikeSecret,
  modelConfigPayload,
  pricingLine,
  probeFingerprint,
  rolesOf,
  suggestName,
  testModelRequest,
  toggleReviewer,
  upsertModelRequest,
  validateDraft,
  type ModelDraft,
} from "../src/features/settings/models";
import {
  draftFromTier,
  isRemovable,
  moveSlot,
  newSlot,
  newTierDraft,
  parseStrategy,
  tierBadge,
  tierPayload,
  tierProblem,
  tierSummary,
} from "../src/features/settings/tiers";
import { codeProblem, loopNeedsAnthropicReconnect, needsAnthropicReconnect, validLoginStart } from "../src/features/settings/anthropic";

const draft = (o: Partial<ModelDraft>): ModelDraft => ({ ...emptyDraft(), name: "stub3", backend: "openai", modelId: "stub-model-3", keyEnv: "YCC_E2E_KEY", ...o });

describe("model drafts", () => {
  it("accepts a complete draft and requires name, backend, and model id", () => {
    expect(canSubmit(validateDraft(draft({})))).toBe(true);
    const bad = validateDraft(draft({ name: " ", modelId: "", backend: "bogus" }));
    expect(Object.keys(bad.errors).sort()).toEqual(["backend", "modelId", "name"]);
    expect(validateDraft(draft({ name: "stub" }), ["stub"]).errors.name).toMatch(/already exists/);
    // Editing keeps its own name.
    expect(validateDraft(draft({ name: "stub" }), ["stub"], false).errors.name).toBeUndefined();
  });

  it("insists key_env is a variable NAME and refuses pasted secrets", () => {
    expect(isEnvName("ANTHROPIC_API_KEY")).toBe(true);
    expect(isEnvName("9KEY")).toBe(false);
    for (const secret of ["sk-ant-api03-AbCdEf0123456789", "sk_live_abc", "xai-0123abcd", "AIzaSyD-abcdefgh", "Abcdefghijklmnop1234567890QRSTUVWX"]) {
      expect(looksLikeSecret(secret)).toBe(true);
      expect(validateDraft(draft({ keyEnv: secret })).errors.keyEnv).toMatch(/NAME/);
    }
    for (const name of ["OPENAI_API_KEY", "FIREWORKS_API_KEY", "YCC_E2E_KEY", "MY_VERY_LONG_ENVIRONMENT_VARIABLE_NAME_2026"]) {
      expect(looksLikeSecret(name)).toBe(false);
      expect(validateDraft(draft({ keyEnv: name })).errors.keyEnv).toBeUndefined();
    }
    expect(validateDraft(draft({ keyEnv: "has space" })).errors.keyEnv).toMatch(/environment-variable name/);
  });

  it("checks auth, base URL, and prices; warns about Fireworks ids and OAuth key_env", () => {
    expect(validateDraft(draft({ auth: "oauth", backend: "glm" })).errors.auth).toMatch(/only supported/);
    expect(validateDraft(draft({ baseUrl: "ftp://x" })).errors.baseUrl).toBeDefined();
    expect(validateDraft(draft({ baseUrl: "https://user:pw@host/v1" })).errors.baseUrl).toBeDefined();
    expect(validateDraft(draft({ backend: "openai-compatible", baseUrl: "" })).errors.baseUrl).toMatch(/needs a base URL/);
    expect(validateDraft(draft({ priceInput: "-1" })).errors.price).toBeDefined();
    expect(validateDraft(draft({ priceInput: "abc" })).errors.price).toBeDefined();
    expect(validateDraft(draft({ priceInput: "3", priceOutput: "15.5" })).errors.price).toBeUndefined();
    const fw = validateDraft(draft({ backend: "openai-compatible", baseUrl: "https://api.fireworks.ai/inference/v1", modelId: "kimi-k2" }));
    expect(fw.warnings.join(" ")).toMatch(/accounts\/fireworks\/models\//);
    expect(validateDraft(draft({ baseUrl: "https://api.fireworks.ai/inference/v1", modelId: "accounts/fireworks/models/kimi-k2" })).warnings).toEqual([]);
    expect(validateDraft(draft({ auth: "oauth", backend: "anthropic", keyEnv: "ANTHROPIC_API_KEY" })).warnings.join(" ")).toMatch(/ignored/);
  });

  it("builds payloads from an allowlist: no secret field, prices only when set", () => {
    const d = { ...draft({ priceInput: "3", priceCacheRead: "" , baseUrl: " http://127.0.0.1:18790/v1 " }), apiKey: "sk-should-never-leave", secret: "x" } as ModelDraft;
    const p = modelConfigPayload(d);
    expect(Object.keys(p).every((k) => (MODEL_CONFIG_FIELDS as readonly string[]).includes(k))).toBe(true);
    expect(JSON.stringify(upsertModelRequest(d))).not.toContain("sk-should-never-leave");
    expect(JSON.stringify(testModelRequest(d))).not.toContain("sk-should-never-leave");
    expect(p).toEqual({
      name: "stub3",
      backend: "openai",
      baseUrl: "http://127.0.0.1:18790/v1",
      model: "stub-model-3",
      keyEnv: "YCC_E2E_KEY",
      thinking: "",
      effort: "",
      thinkingDisplay: "",
      auth: "api-key",
      disabled: false,
      priceInput: 3,
    });
    expect(upsertModelRequest(d).persist).toBe(true);
    expect(discoverRequest(d)).toEqual({ backend: "openai", baseUrl: "http://127.0.0.1:18790/v1", keyEnv: "YCC_E2E_KEY" });
    expect(discoverRequest({ ...d, auth: "oauth" }).keyEnv).toBe("");
    // Explicit availability is always sent (omission would preserve the old state).
    expect(modelConfigPayload({ ...d, enabled: false }).disabled).toBe(true);
  });

  it("round-trips GetModelConfig records, including duplicates", () => {
    const c = create(ModelConfigSchema, { name: "claude", backend: "anthropic", model: "claude-x", keyEnv: "ANTHROPIC_API_KEY", priceOutput: 15, disabled: true, auth: "api-key" });
    const d = draftFromConfig(c);
    expect(d).toMatchObject({ name: "claude", enabled: false, priceOutput: "15", priceInput: "" });
    expect(modelConfigPayload(d)).toMatchObject({ name: "claude", model: "claude-x", priceOutput: 15, disabled: true });
    expect(modelConfigPayload(d)).not.toHaveProperty("priceInput");
    expect(draftFromConfig(c, true).name).toBe("claude-copy");
  });

  it("invalidates a test result only for request-shaping fields", () => {
    const d = draft({});
    expect(probeFingerprint({ ...d, priceInput: "9", enabled: false })).toBe(probeFingerprint(d));
    expect(probeFingerprint({ ...d, modelId: "other" })).not.toBe(probeFingerprint(d));
  });

  it("suggests names for discovered ids and reports role use", () => {
    expect(suggestName("accounts/fireworks/models/Kimi-K2", [])).toBe("kimi-k2");
    expect(suggestName("stub-model", ["stub-model"])).toBe("stub-model-2");
    expect(rolesOf("stub", { coordinator: "stub", implementer: "x", reviewers: ["stub", "y"] })).toEqual(["coordinator", "reviewer"]);
    expect(pricingLine(create(ModelInfoSchema, { priced: false }))).toBeNull();
    expect(pricingLine(create(ModelInfoSchema, { priced: true, priceInput: 3, priceOutput: 15 }))).toBe("$3 in · $15 out /Mtok");
  });

  it("never removes the final default reviewer", () => {
    expect(toggleReviewer(["a"], "a")).toEqual({ next: ["a"], error: expect.stringMatching(/At least one/) });
    expect(toggleReviewer(["a", "b"], "a")).toEqual({ next: ["b"], error: null });
    expect(toggleReviewer(["a"], "b")).toEqual({ next: ["a", "b"], error: null });
  });
});

describe("review tiers", () => {
  const tier = (o: Record<string, unknown>) => create(ReviewTierInfoSchema, { name: "standard", ...o });

  it("expands the models shorthand into slots and restores it when slots stay generic", () => {
    const d = draftFromTier(tier({ models: ["claude", "gpt"], builtin: true, configured: true }));
    expect(d.slots.map((s) => s.model)).toEqual(["claude", "gpt"]);
    expect(d.existing).toBe(true);
    const p = tierPayload(d);
    expect(p.models).toEqual(["claude", "gpt"]);
    expect(p.reviewers).toEqual([]);
  });

  it("emits the long form once any slot has a label, focus, or thinking", () => {
    const d = draftFromTier(tier({ models: ["claude", "gpt"] }));
    d.slots[1] = { ...d.slots[1], name: "security", prompt: "  Look for injection.  ", thinking: "high" };
    d.slots.push(newSlot("")); // a slot without a model is dropped
    const p = tierPayload(d);
    expect(p.models).toEqual([]);
    expect(p.reviewers).toEqual([
      { name: "", model: "claude", prompt: "", thinking: "" },
      { name: "security", model: "gpt", prompt: "Look for injection.", thinking: "high" },
    ]);
    // And the long form round-trips.
    const back = draftFromTier(tier({ reviewers: p.reviewers }));
    expect(back.slots.map((s) => [s.name, s.model, s.prompt, s.thinking])).toEqual([
      ["", "claude", "", ""],
      ["security", "gpt", "Look for injection.", "high"],
    ]);
  });

  it("maps strategies and drops reviewers for self-review", () => {
    expect(parseStrategy("self-review")).toBe("self");
    expect(parseStrategy("coordinator")).toBe("self");
    expect(parseStrategy("")).toBe("agents");
    const d = { ...newTierDraft("claude"), name: "quick", strategy: "self" as const, prompt: "ignored" };
    expect(tierPayload(d)).toEqual({ name: "quick", strategy: "coordinator", description: "", prompt: "", models: [], reviewers: [] });
  });

  it("validates names and reviewers", () => {
    expect(tierProblem(newTierDraft("claude"))).toMatch(/Name/);
    expect(tierProblem({ ...newTierDraft(""), name: "x" })).toMatch(/at least one reviewer/);
    expect(tierProblem({ ...newTierDraft("claude"), name: "standard" }, ["standard"])).toMatch(/already exists/);
    expect(tierProblem({ ...newTierDraft("claude"), name: "deep review" })).toMatch(/spaces/);
    expect(tierProblem({ ...newTierDraft(""), name: "x", strategy: "self" })).toBeNull();
  });

  it("offers removal only for configured entries (never the default custom tier)", () => {
    expect(isRemovable(tier({ builtin: true, configured: false }), "standard")).toBe(false);
    expect(isRemovable(tier({ builtin: true, configured: true }), "standard")).toBe(true);
    expect(isRemovable(tier({ name: "mine", configured: true }), "mine")).toBe(false);
    expect(isRemovable(tier({ name: "mine", configured: true }), "standard")).toBe(true);
    expect(tierBadge(tier({ builtin: true, configured: true })).label).toBe("overridden");
    expect(tierBadge(tier({ name: "mine", configured: true })).label).toBe("custom");
  });

  it("summarizes line-ups and reorders slots", () => {
    expect(tierSummary(tier({ strategy: "coordinator" }))).toMatch(/self-review/);
    expect(tierSummary(tier({ models: ["a", "b"] }))).toBe("a, b");
    expect(tierSummary(tier({ reviewers: [{ name: "perf", model: "gpt", thinking: "high", prompt: "speed" }, { model: "claude" }] }))).toBe(
      "perf (gpt · high · focused), claude",
    );
    expect(tierSummary(tier({}))).toMatch(/session/);
    const slots = [newSlot("a"), newSlot("b"), newSlot("c")];
    expect(moveSlot(slots, 0, 1).map((s) => s.model)).toEqual(["b", "a", "c"]);
    expect(moveSlot(slots, 0, -1).map((s) => s.model)).toEqual(["a", "b", "c"]);
  });
});

describe("Anthropic login", () => {
  const now = Date.parse("2026-10-06T12:00:00Z");
  const start = (o: Partial<{ attemptId: string; authorizationUrl: string; expiresAtUnix: bigint }>) => ({
    attemptId: "att",
    authorizationUrl: "https://claude.com/cai/oauth/authorize?code=true&client_id=x",
    expiresAtUnix: BigInt(now / 1000 + 600),
    ...o,
  });

  it("only offers Anthropic's own unexpired authorize page", () => {
    expect(validLoginStart(start({}), now)).toBe(true);
    expect(validLoginStart(start({ authorizationUrl: "http://claude.com/cai/oauth/authorize" }), now)).toBe(false);
    expect(validLoginStart(start({ authorizationUrl: "https://evil.example/cai/oauth/authorize" }), now)).toBe(false);
    expect(validLoginStart(start({ authorizationUrl: "https://u:p@claude.com/cai/oauth/authorize" }), now)).toBe(false);
    expect(validLoginStart(start({ authorizationUrl: "javascript:alert(1)" }), now)).toBe(false);
    expect(validLoginStart(start({ attemptId: "" }), now)).toBe(false);
    expect(validLoginStart(start({ expiresAtUnix: BigInt(now / 1000 - 1) }), now)).toBe(false);
  });

  it("checks the pasted code#state shape", () => {
    expect(codeProblem("")).toBeNull();
    expect(codeProblem("abc#def")).toBeNull();
    expect(codeProblem("abc")).toMatch(/code#state/);
    expect(codeProblem("#def")).toMatch(/code#state/);
    expect(codeProblem("ab c#def")).toMatch(/spaces/);
  });

  it("offers a reconnect only for Anthropic login problems", () => {
    expect(needsAnthropicReconnect("no Anthropic subscription credentials stored; run `ycc login anthropic`")).toBe(true);
    expect(needsAnthropicReconnect("refreshing Anthropic subscription token (re-run …)")).toBe(true);
    expect(needsAnthropicReconnect("anthropic: overloaded_error")).toBe(false);
    expect(needsAnthropicReconnect("anthropic: max_tokens exceeded")).toBe(false);
    expect(needsAnthropicReconnect("openai: 401 unauthorized")).toBe(false);
    const loop = create(WorkLoopInfoSchema, { outcome: "stopped: session failed", sessions: [{ errorKind: "auth", errorMessage: "anthropic: 401" }] });
    expect(loopNeedsAnthropicReconnect(loop, true)).toBe(true);
    expect(loopNeedsAnthropicReconnect(loop, false)).toBe(false);
    expect(loopNeedsAnthropicReconnect(create(WorkLoopInfoSchema, { outcome: "Completed 2 tasks" }), true)).toBe(false);
  });
});
