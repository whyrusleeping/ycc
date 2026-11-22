// Pure per-session settings logic (mirrors YccKit SessionSettingsModel and
// SessionUsageModel): reasoning levels and scopes, role-model choices, and a
// session-scoped usage breakdown from GetUsage.
import type { ModelInfo, UsageRow } from "../../gen/ycc/v1/ycc_pb";

export const THINKING_LEVELS = [
  { value: "off", title: "Off" },
  { value: "low", title: "Low" },
  { value: "medium", title: "Medium" },
  { value: "high", title: "High" },
  { value: "xhigh", title: "X-High" },
  { value: "max", title: "Max" },
] as const;
export type ThinkingLevel = (typeof THINKING_LEVELS)[number]["value"];

/** A role with its own thinking picker (SetThinking's wire role). */
export type ThinkingRole = "coordinator" | "implementer" | "reviewers";

export interface RoleThinking {
  coordinator: ThinkingLevel;
  implementer: ThinkingLevel;
  reviewers: ThinkingLevel;
}

/** A wire level, defaulting to medium so a picker always has a selection. */
export function parseThinking(value: string): ThinkingLevel {
  const v = value.trim().toLowerCase();
  return (THINKING_LEVELS.find((l) => l.value === v)?.value ?? "medium") as ThinkingLevel;
}

const rank = (level: string) => THINKING_LEVELS.findIndex((l) => l.value === level);

/** The display title of a level ("X-High"); an unknown value shows as is. */
export function thinkingTitle(level: string): string {
  return THINKING_LEVELS.find((l) => l.value === level)?.title ?? level;
}

/**
 * The levels a model supports (ModelInfo.thinking_levels), lowest first.
 * Unknown levels are dropped; an empty list (an older daemon, or a model not in
 * the list) offers every level.
 */
export function supportedLevels(info: ModelInfo | undefined): ThinkingLevel[] {
  const wanted = new Set((info?.thinkingLevels ?? []).map((l) => l.trim().toLowerCase()));
  const out = THINKING_LEVELS.map((l) => l.value).filter((l) => wanted.has(l));
  return out.length ? out : THINKING_LEVELS.map((l) => l.value);
}

/** The levels supported by the model named `name`. */
export function levelsNamed(name: string, models: readonly ModelInfo[]): ThinkingLevel[] {
  return supportedLevels(models.find((m) => m.name === name));
}

/** The levels any of `names` supports: a role with several models (the reviewers); the daemon clamps per model. */
export function levelsAnyOf(names: readonly string[], models: readonly ModelInfo[]): ThinkingLevel[] {
  if (!names.length) return THINKING_LEVELS.map((l) => l.value);
  const any = new Set(names.flatMap((n) => levelsNamed(n, models)));
  return THINKING_LEVELS.map((l) => l.value).filter((l) => any.has(l));
}

/**
 * A picker's choices: the supported levels plus `current` when it is a known
 * level outside them, so a saved level is never rewritten just by showing it.
 */
export function levelChoices(supported: readonly ThinkingLevel[], current = ""): ThinkingLevel[] {
  if (rank(current) < 0 || supported.includes(current as ThinkingLevel)) return [...supported];
  return THINKING_LEVELS.map((l) => l.value).filter((l) => l === current || supported.includes(l));
}

/**
 * `level` mapped onto `supported` as the daemon clamps a new choice (spec §7.4):
 * a supported level is kept, "off" becomes the lowest supported level, and any
 * other level the nearest supported level above "off" (ties go to the higher
 * one). "" (the model's default), an unknown level, or an empty list is kept.
 */
export function clampThinking(level: string, supported: readonly string[]): string {
  const want = rank(level);
  if (want < 0 || !supported.length || supported.includes(level)) return level;
  const ranked = supported.filter((s) => rank(s) >= 0).sort((a, b) => rank(a) - rank(b));
  if (!ranked.length) return level;
  if (level === "off") return ranked[0];
  let best = "";
  for (const s of ranked) {
    if (s === "off") continue;
    if (!best || Math.abs(rank(s) - want) <= Math.abs(rank(best) - want)) best = s;
  }
  return best || ranked[0];
}

/**
 * Models offered for a role: enabled ones, plus any disabled model the role
 * currently holds (so the picker shows reality and can migrate off it).
 */
export function roleModelChoices(models: readonly ModelInfo[], assigned: readonly string[]): ModelInfo[] {
  return models.filter((m) => !m.disabled || assigned.includes(m.name));
}

export type PriceStatus = "priced" | "unpriced" | "partial";

export interface UsageTotals {
  input: bigint;
  output: bigint;
  cacheRead: bigint;
  cacheWrite: bigint;
  total: bigint;
  cost: number;
  priceStatus: PriceStatus;
}

/**
 * Keep one session's rows of a ["session","model"]-grouped GetUsage response
 * and sum a total (GetUsage has no session filter). Price status merges like
 * the daemon: all priced → priced, all unpriced → unpriced, else partial.
 */
export function sessionUsage(rows: readonly UsageRow[], sessionId: string): { rows: UsageRow[]; total: UsageTotals | null } {
  const kept = rows.filter((r) => r.session === sessionId);
  if (!kept.length) return { rows: [], total: null };
  const total: UsageTotals = { input: 0n, output: 0n, cacheRead: 0n, cacheWrite: 0n, total: 0n, cost: 0, priceStatus: "priced" };
  let priced = false;
  let unpriced = false;
  let partial = false;
  for (const r of kept) {
    total.input += r.input;
    total.output += r.output;
    total.cacheRead += r.cacheRead;
    total.cacheWrite += r.cacheWrite;
    total.total += r.total;
    total.cost += r.cost;
    const s = r.priceStatus.trim().toLowerCase();
    if (s === "unpriced") unpriced = true;
    else if (s === "partial") partial = true;
    else priced = true;
  }
  total.priceStatus = partial || (priced && unpriced) ? "partial" : unpriced ? "unpriced" : "priced";
  return { rows: kept, total };
}

/** "$1.23" for priced usage; "—" when no rate is configured. */
export function formatCost(cost: number, status: string): string {
  if (status.trim().toLowerCase() === "unpriced") return "—";
  const v = cost < 0.01 && cost > 0 ? cost.toFixed(4) : cost.toFixed(2);
  return `$${v}${status === "partial" ? "+" : ""}`;
}
