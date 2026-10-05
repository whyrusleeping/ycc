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

/** Scope a reasoning change targets; "all" is the empty wire role. */
export const THINKING_ROLES = [
  { value: "all", title: "All roles" },
  { value: "coordinator", title: "Coordinator" },
  { value: "implementer", title: "Implementer" },
  { value: "reviewers", title: "Reviewers" },
] as const;
export type ThinkingRole = (typeof THINKING_ROLES)[number]["value"];

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

export function thinkingRoleWire(role: ThinkingRole): string {
  return role === "all" ? "" : role;
}

/** The known level for a scope ("all" shows the coordinator's as representative). */
export function thinkingFor(role: ThinkingRole, levels: RoleThinking): ThinkingLevel {
  return role === "implementer" ? levels.implementer : role === "reviewers" ? levels.reviewers : levels.coordinator;
}

/**
 * Whether applying `level` at `role` changes anything. For "all" any divergent
 * role counts, so unifying the roles at the coordinator's level still applies.
 */
export function needsThinkingApply(role: ThinkingRole, level: ThinkingLevel, levels: RoleThinking): boolean {
  if (role === "all") return level !== levels.coordinator || level !== levels.implementer || level !== levels.reviewers;
  return level !== thinkingFor(role, levels);
}

/** Levels after a successful SetThinking. */
export function withThinking(role: ThinkingRole, level: ThinkingLevel, levels: RoleThinking): RoleThinking {
  if (role === "all") return { coordinator: level, implementer: level, reviewers: level };
  return { ...levels, [role]: level };
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
