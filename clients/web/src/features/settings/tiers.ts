// Pure review-tier editing logic (mirrors YccKit ReviewTiersModel): a draft
// seeded from a wire tier (expanding the `models` shorthand into slots), the
// UpsertReviewTier payload (restoring the shorthand when every slot is
// generic, so simple tiers keep their compact ycc.toml shape), validity, and
// list presentation.
import type { ReviewTierInfo } from "../../gen/ycc/v1/ycc_pb";

export type ReviewStrategy = "agents" | "self";

export const STRATEGIES = [
  { value: "agents", title: "Reviewer agents", detail: "Spawns one reviewer subagent per slot." },
  { value: "self", title: "Coordinator self-review", detail: "The coordinator reviews the change itself; no reviewer agent." },
] as const;

export const SLOT_THINKING = [
  { value: "", title: "Inherit" },
  { value: "off", title: "Off" },
  { value: "low", title: "Low" },
  { value: "medium", title: "Medium" },
  { value: "high", title: "High" },
  { value: "xhigh", title: "X-High" },
  { value: "max", title: "Max" },
] as const;

export function parseStrategy(v: string): ReviewStrategy {
  return ["coordinator", "self", "self-review"].includes(v.trim().toLowerCase()) ? "self" : "agents";
}

/** The tier's `strategy` field ("" means reviewer agents). */
export function strategyWire(s: ReviewStrategy): string {
  return s === "self" ? "coordinator" : "";
}

let slotSeq = 0;

export interface SlotDraft {
  /** Local identity for list rendering. */
  key: number;
  name: string;
  model: string;
  prompt: string;
  /** "" = inherit the reviewers role level / model default. */
  thinking: string;
}

export function newSlot(model = ""): SlotDraft {
  return { key: ++slotSeq, name: "", model, prompt: "", thinking: "" };
}

export function isGenericSlot(s: SlotDraft): boolean {
  return !s.name.trim() && !s.prompt.trim() && !s.thinking;
}

export interface TierDraft {
  name: string;
  strategy: ReviewStrategy;
  description: string;
  prompt: string;
  slots: SlotDraft[];
  /** Editing an existing tier (its name is fixed; renaming is remove + add). */
  existing: boolean;
  builtin: boolean;
}

export function newTierDraft(firstModel = ""): TierDraft {
  return { name: "", strategy: "agents", description: "", prompt: "", slots: [newSlot(firstModel)], existing: false, builtin: false };
}

export function draftFromTier(t: ReviewTierInfo): TierDraft {
  const slots = t.reviewers.length
    ? t.reviewers.map((r) => ({ key: ++slotSeq, name: r.name, model: r.model, prompt: r.prompt, thinking: r.thinking.trim().toLowerCase() }))
    : t.models.map((m) => newSlot(m));
  return {
    name: t.name,
    strategy: parseStrategy(t.strategy),
    description: t.description,
    prompt: t.prompt,
    slots,
    existing: true,
    builtin: t.builtin,
  };
}

export interface TierPayload {
  name: string;
  strategy: string;
  description: string;
  prompt: string;
  models: string[];
  reviewers: { name: string; model: string; prompt: string; thinking: string }[];
}

/**
 * The UpsertReviewTier tier. Slots without a model are dropped; when every
 * remaining slot is generic the `models` shorthand is sent instead of the
 * long form. Self-review tiers carry no reviewers.
 */
export function tierPayload(d: TierDraft): TierPayload {
  const out: TierPayload = {
    name: d.name.trim(),
    strategy: strategyWire(d.strategy),
    description: d.description.trim(),
    prompt: d.strategy === "self" ? "" : d.prompt.trim(),
    models: [],
    reviewers: [],
  };
  if (d.strategy === "self") return out;
  const kept = d.slots.filter((s) => s.model.trim());
  if (kept.every(isGenericSlot)) out.models = kept.map((s) => s.model.trim());
  else
    out.reviewers = kept.map((s) => ({
      name: s.name.trim(),
      model: s.model.trim(),
      prompt: s.prompt.trim(),
      thinking: s.thinking,
    }));
  return out;
}

/** Why the draft can't be saved yet (null when it can). The daemon stays the authority. */
export function tierProblem(d: TierDraft, existingNames: readonly string[] = []): string | null {
  const name = d.name.trim();
  if (!name) return "Name the tier.";
  if (/\s/.test(name)) return "Use a tier name without spaces.";
  if (!d.existing && existingNames.includes(name)) return `A tier named “${name}” already exists; edit it instead.`;
  if (d.strategy === "agents" && !d.slots.some((s) => s.model.trim())) return "Add at least one reviewer with a model.";
  return null;
}

/** Only configured entries can be removed; the default custom tier is refused by the daemon. */
export function isRemovable(t: ReviewTierInfo, defaultTier: string): boolean {
  if (!t.configured) return false;
  return t.builtin || t.name !== defaultTier;
}

export function tierBadge(t: ReviewTierInfo): { label: string; tone: "warn" | "muted" | "accent" } {
  if (t.builtin && t.configured) return { label: "overridden", tone: "warn" };
  if (t.builtin) return { label: "built-in", tone: "muted" };
  return { label: "custom", tone: "accent" };
}

/** One-line reviewer line-up: "readability (claude · high), performance (gpt)". */
export function tierSummary(t: ReviewTierInfo): string {
  if (parseStrategy(t.strategy) === "self") return "Coordinator self-review — no reviewer agent";
  if (t.reviewers.length) {
    return t.reviewers
      .map((r) => {
        const label = r.name || r.model;
        const bits = [r.name ? r.model : "", r.thinking, r.prompt.trim() ? "focused" : ""].filter(Boolean);
        return bits.length ? `${label} (${bits.join(" · ")})` : label;
      })
      .join(", ");
  }
  if (t.models.length) return t.models.join(", ");
  return "Uses the session’s reviewer assignment";
}

export function moveSlot(slots: readonly SlotDraft[], index: number, delta: -1 | 1): SlotDraft[] {
  const to = index + delta;
  if (to < 0 || to >= slots.length) return [...slots];
  const out = [...slots];
  [out[index], out[to]] = [out[to], out[index]];
  return out;
}
