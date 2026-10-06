// Pure model-registry editing logic (mirrors YccKit ModelEditorDraft): the
// editable draft, its validation, and the exact UpsertModel / TestModel /
// DiscoverModels payloads. Credentials are only ever referenced by key_env —
// the NAME of an environment variable or `ycc token` secrets entry that the
// daemon resolves locally. No secret value has a field here, and payloads are
// built from an allowlist so nothing else can ride along.
import type { ModelConfig, ModelInfo } from "../../gen/ycc/v1/ycc_pb";

export const BACKENDS = [
  { value: "anthropic", title: "Anthropic" },
  { value: "openai", title: "OpenAI" },
  { value: "openai-compatible", title: "OpenAI-compatible" },
  { value: "glm", title: "GLM" },
  { value: "ollama", title: "Ollama" },
] as const;

export const AUTH_MODES = [
  { value: "api-key", title: "API key (key_env)" },
  { value: "oauth", title: "OAuth / subscription login" },
  { value: "", title: "None / provider default" },
] as const;

export const THINKING_MODES = [
  { value: "", title: "Provider default" },
  { value: "adaptive", title: "Adaptive" },
  { value: "off", title: "Off" },
] as const;

export const EFFORTS = [
  { value: "", title: "Provider default" },
  { value: "low", title: "Low" },
  { value: "medium", title: "Medium" },
  { value: "high", title: "High" },
  { value: "xhigh", title: "X-High" },
  { value: "max", title: "Max" },
] as const;

export const THINKING_DISPLAYS = [
  { value: "", title: "Provider default" },
  { value: "summarized", title: "Summarized" },
  { value: "omitted", title: "Omitted" },
] as const;

export interface ModelDraft {
  name: string;
  enabled: boolean;
  backend: string;
  auth: string;
  baseUrl: string;
  modelId: string;
  /** The NAME of the env var / secrets entry holding the API key — never the key. */
  keyEnv: string;
  thinking: string;
  effort: string;
  thinkingDisplay: string;
  priceInput: string;
  priceOutput: string;
  priceCacheRead: string;
  priceCacheWrite: string;
}

export function emptyDraft(): ModelDraft {
  return {
    name: "",
    enabled: true,
    backend: "anthropic",
    auth: "api-key",
    baseUrl: "",
    modelId: "",
    keyEnv: "",
    thinking: "",
    effort: "",
    thinkingDisplay: "",
    priceInput: "",
    priceOutput: "",
    priceCacheRead: "",
    priceCacheWrite: "",
  };
}

const price = (v: number | undefined) => (v === undefined ? "" : String(v));

/** Seed an editor from GetModelConfig (duplicate = a copy under a new name). */
export function draftFromConfig(c: ModelConfig, duplicate = false): ModelDraft {
  return {
    name: duplicate ? `${c.name}-copy` : c.name,
    enabled: !c.disabled,
    backend: c.backend,
    auth: c.auth,
    baseUrl: c.baseUrl,
    modelId: c.model,
    keyEnv: c.keyEnv,
    thinking: c.thinking,
    effort: c.effort,
    thinkingDisplay: c.thinkingDisplay,
    priceInput: price(c.priceInput),
    priceOutput: price(c.priceOutput),
    priceCacheRead: price(c.priceCacheRead),
    priceCacheWrite: price(c.priceCacheWrite),
  };
}

/** Environment-variable name syntax (as `ycc token set` requires). */
export function isEnvName(s: string): boolean {
  return /^[A-Za-z_][A-Za-z0-9_]*$/.test(s);
}

const SECRET_PREFIXES = /^(sk-|sk_|sk-ant-|xai-|gsk_|ghp_|gho_|github_pat_|hf_|AIza|pplx-|fw_|r8_|nvapi-|glpat-|eyJ)/;

/**
 * Whether a key_env value looks like a pasted secret rather than a name:
 * a known provider key prefix, or a long mixed-case/digit token that no
 * one would choose as a variable name.
 */
export function looksLikeSecret(s: string): boolean {
  const v = s.trim();
  if (!v) return false;
  if (SECRET_PREFIXES.test(v)) return true;
  if (v.length >= 32 && /[a-z]/.test(v) && /[A-Z]/.test(v) && /\d/.test(v)) return true;
  if (v.length >= 40 && /\d/.test(v) && !/^[A-Z0-9_]+$/.test(v)) return true;
  return false;
}

export function parsePrice(s: string): number | undefined | null {
  const v = s.trim();
  if (!v) return undefined;
  const n = Number(v);
  if (!Number.isFinite(n) || n < 0) return null;
  return n;
}

export type DraftField = "name" | "backend" | "auth" | "baseUrl" | "modelId" | "keyEnv" | "price";

export interface DraftCheck {
  errors: Partial<Record<DraftField, string>>;
  warnings: string[];
}

const FIREWORKS_PREFIX = "accounts/fireworks/models/";

/** Client-side checks; the daemon stays the authority. */
export function validateDraft(d: ModelDraft, existingNames: readonly string[] = [], isNew = true): DraftCheck {
  const errors: DraftCheck["errors"] = {};
  const warnings: string[] = [];
  const name = d.name.trim();
  if (!name) errors.name = "A logical name is required.";
  else if (/\s/.test(name)) errors.name = "Use a name without spaces (e.g. claude-opus).";
  else if (isNew && existingNames.includes(name)) errors.name = `A model named “${name}” already exists.`;
  if (!BACKENDS.some((b) => b.value === d.backend)) errors.backend = "Choose a backend.";
  if (!AUTH_MODES.some((a) => a.value === d.auth)) errors.auth = "Choose an authentication mode.";
  else if (d.auth === "oauth" && d.backend !== "anthropic" && d.backend !== "openai") {
    errors.auth = "OAuth login is only supported by the Anthropic and OpenAI backends.";
  }
  if (!d.modelId.trim()) errors.modelId = "A model id is required.";
  const base = d.baseUrl.trim();
  if (base) {
    let ok = false;
    try {
      const u = new URL(base);
      ok = (u.protocol === "http:" || u.protocol === "https:") && !u.username && !u.password;
    } catch {
      ok = false;
    }
    if (!ok) errors.baseUrl = "Enter an http(s) URL without credentials in it.";
  } else if (d.backend === "openai-compatible") {
    errors.baseUrl = "An OpenAI-compatible backend needs a base URL.";
  }
  const key = d.keyEnv.trim();
  if (key) {
    if (looksLikeSecret(key)) {
      errors.keyEnv =
        "This looks like an API key. Enter the NAME of the environment variable or `ycc token` entry that holds it (e.g. ANTHROPIC_API_KEY), never the key itself.";
    } else if (!isEnvName(key)) {
      errors.keyEnv = "Use an environment-variable name: letters, digits, and underscores (e.g. OPENAI_API_KEY).";
    } else if (d.auth === "oauth") {
      warnings.push("key_env is ignored for OAuth login; the daemon uses its stored subscription credentials.");
    }
  }
  for (const p of [d.priceInput, d.priceOutput, d.priceCacheRead, d.priceCacheWrite]) {
    if (parsePrice(p) === null) errors.price = "Prices are non-negative numbers ($ per million tokens), or blank.";
  }
  const id = d.modelId.trim();
  if (/fireworks\.ai/i.test(base) && id && !id.startsWith(FIREWORKS_PREFIX)) {
    warnings.push(`Fireworks model ids usually need the ${FIREWORKS_PREFIX} prefix.`);
  }
  return { errors, warnings };
}

export function canSubmit(check: DraftCheck): boolean {
  return Object.keys(check.errors).length === 0;
}

/** Fields that change the provider request; a test result is stale once these change. */
export function probeFingerprint(d: ModelDraft): string {
  return [d.backend, d.auth, d.baseUrl.trim(), d.modelId.trim(), d.keyEnv.trim(), d.thinking, d.effort, d.thinkingDisplay].join("\u0000");
}

/** The fields a ModelConfig payload may carry (anything else is dropped). */
export const MODEL_CONFIG_FIELDS = [
  "name",
  "backend",
  "baseUrl",
  "model",
  "keyEnv",
  "thinking",
  "effort",
  "thinkingDisplay",
  "priceInput",
  "priceOutput",
  "priceCacheRead",
  "priceCacheWrite",
  "auth",
  "disabled",
] as const;

export interface ModelConfigPayload {
  name: string;
  backend: string;
  baseUrl: string;
  model: string;
  keyEnv: string;
  thinking: string;
  effort: string;
  thinkingDisplay: string;
  priceInput?: number;
  priceOutput?: number;
  priceCacheRead?: number;
  priceCacheWrite?: number;
  auth: string;
  disabled: boolean;
}

/** The ModelConfig sent by Save (UpsertModel) and Test (TestModel). */
export function modelConfigPayload(d: ModelDraft): ModelConfigPayload {
  const out: ModelConfigPayload = {
    name: d.name.trim(),
    backend: d.backend,
    baseUrl: d.baseUrl.trim(),
    model: d.modelId.trim(),
    keyEnv: d.keyEnv.trim(),
    thinking: d.thinking,
    effort: d.effort,
    thinkingDisplay: d.thinkingDisplay,
    auth: d.auth,
    disabled: !d.enabled,
  };
  const prices = [
    ["priceInput", d.priceInput],
    ["priceOutput", d.priceOutput],
    ["priceCacheRead", d.priceCacheRead],
    ["priceCacheWrite", d.priceCacheWrite],
  ] as const;
  for (const [k, v] of prices) {
    const n = parsePrice(v);
    if (typeof n === "number") out[k] = n;
  }
  return out;
}

/** UpsertModel: registry mutations always persist to ycc.toml (persist kept for older daemons). */
export function upsertModelRequest(d: ModelDraft) {
  return { model: modelConfigPayload(d), persist: true };
}

export function testModelRequest(d: ModelDraft) {
  return { model: modelConfigPayload(d) };
}

export function discoverRequest(d: ModelDraft) {
  return { backend: d.backend, baseUrl: d.baseUrl.trim(), keyEnv: d.auth === "oauth" ? "" : d.keyEnv.trim() };
}

/** A logical-name suggestion for a discovered model id ("accounts/x/models/foo-1" → "foo-1"). */
export function suggestName(modelId: string, existing: readonly string[]): string {
  const base =
    modelId
      .trim()
      .split("/")
      .pop()
      ?.toLowerCase()
      .replace(/[^a-z0-9._-]+/g, "-")
      .replace(/^-+|-+$/g, "") || "model";
  if (!existing.includes(base)) return base;
  for (let i = 2; ; i++) if (!existing.includes(`${base}-${i}`)) return `${base}-${i}`;
}

export interface RoleAssignment {
  coordinator: string;
  implementer: string;
  reviewers: readonly string[];
}

/** Roles a model fills by default ("coordinator", "implementer", "reviewer"). */
export function rolesOf(name: string, roles: RoleAssignment): string[] {
  const out: string[] = [];
  if (roles.coordinator === name) out.push("coordinator");
  if (roles.implementer === name) out.push("implementer");
  if (roles.reviewers.includes(name)) out.push("reviewer");
  return out;
}

/** "$3 in · $15 out" from ModelInfo prices, or null when unpriced. */
export function pricingLine(m: ModelInfo): string | null {
  if (!m.priced) return null;
  const f = (v: number | undefined) => (v === undefined ? "—" : `$${Number(v.toFixed(4))}`);
  const parts = [`${f(m.priceInput)} in`, `${f(m.priceOutput)} out`];
  if (m.priceCacheRead !== undefined) parts.push(`${f(m.priceCacheRead)} cache read`);
  if (m.priceCacheWrite !== undefined) parts.push(`${f(m.priceCacheWrite)} cache write`);
  return `${parts.join(" · ")} /Mtok`;
}

/** Models sorted case-insensitively by name. */
export function sortedModels(models: readonly ModelInfo[]): ModelInfo[] {
  return [...models].sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: "base" }));
}

/**
 * Toggle a default reviewer. The final reviewer cannot be removed: an empty
 * list means "leave unchanged" on the wire.
 */
export function toggleReviewer(current: readonly string[], name: string): { next: string[]; error: string | null } {
  if (current.includes(name)) {
    if (current.length <= 1) return { next: [...current], error: "At least one reviewer must remain selected." };
    return { next: current.filter((r) => r !== name), error: null };
  }
  return { next: [...current, name], error: null };
}
