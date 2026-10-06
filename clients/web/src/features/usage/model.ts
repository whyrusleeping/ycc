// Pure usage-dashboard logic (mirrors YccKit UsageModel and the CLI's `ycc
// cost`): the URL-held query (grouping, date range, task filter), the GetUsage
// request it maps to, CLI-identical cell formatting (so the browser and the
// CLI read the same), a gap-filled spend timeline, row sorting and shares,
// subscription-window and budget-cap presentation.
import type { GetBudgetResponse, SubscriptionUsageAccount, SubscriptionUsageWindow, UsageRow } from "../../gen/ycc/v1/ycc_pb";

/** GetUsage group_by dimensions, in the CLI's vocabulary. */
export const USAGE_DIMS = [
  { value: "task", title: "Task" },
  { value: "model", title: "Model" },
  { value: "agent", title: "Role" },
  { value: "session", title: "Session" },
  { value: "day", title: "Day" },
] as const;
export type UsageDim = (typeof USAGE_DIMS)[number]["value"];

export function isUsageDim(v: string): v is UsageDim {
  return USAGE_DIMS.some((d) => d.value === v);
}

export function dimTitle(d: UsageDim): string {
  return USAGE_DIMS.find((x) => x.value === d)?.title ?? d;
}

/** Date-range presets; days are UTC (the daemon's day keys are UTC). */
export const RANGE_PRESETS = [
  { value: "all", title: "All time" },
  { value: "today", title: "Today" },
  { value: "7d", title: "Last 7 days" },
  { value: "30d", title: "Last 30 days" },
  { value: "month", title: "This month" },
  { value: "custom", title: "Custom…" },
] as const;
export type RangePreset = (typeof RANGE_PRESETS)[number]["value"];

export interface UsageQuery {
  /** Ordered grouping (at least one dimension, no repeats). */
  by: UsageDim[];
  range: RangePreset;
  /** Custom range bounds (YYYY-MM-DD, inclusive; "" = open). */
  since: string;
  until: string;
  /** Restrict to one backlog task id ("" = every task). */
  task: string;
}

export const DEFAULT_QUERY: UsageQuery = { by: ["task"], range: "all", since: "", until: "", task: "" };

const DAY_RE = /^\d{4}-\d{2}-\d{2}$/;

export function isWireDay(s: string): boolean {
  if (!DAY_RE.test(s)) return false;
  const t = Date.parse(`${s}T00:00:00Z`);
  return !Number.isNaN(t) && new Date(t).toISOString().slice(0, 10) === s;
}

/** Read the dashboard query from the page URL (unknown values fall back). */
export function parseUsageQuery(search: string | URLSearchParams): UsageQuery {
  const p = typeof search === "string" ? new URLSearchParams(search) : search;
  const by: UsageDim[] = [];
  for (const raw of (p.get("by") ?? "").split(",")) {
    const v = raw.trim();
    if (isUsageDim(v) && !by.includes(v)) by.push(v);
  }
  const since = p.get("since") ?? "";
  const until = p.get("until") ?? "";
  const rawRange = p.get("range") ?? "";
  let range: RangePreset = RANGE_PRESETS.some((r) => r.value === rawRange) ? (rawRange as RangePreset) : "all";
  // Explicit bounds without a preset mean a custom range.
  if (!rawRange && (since || until)) range = "custom";
  return {
    by: by.length ? by : [...DEFAULT_QUERY.by],
    range,
    since: isWireDay(since) ? since : "",
    until: isWireDay(until) ? until : "",
    task: (p.get("task") ?? "").trim(),
  };
}

/** The URL search string for a query ("" for the defaults). */
export function usageSearch(q: Partial<UsageQuery>): string {
  const p = new URLSearchParams();
  const by = q.by?.length ? q.by : DEFAULT_QUERY.by;
  if (by.join(",") !== DEFAULT_QUERY.by.join(",")) p.set("by", by.join(","));
  const range = q.range ?? "all";
  if (range === "custom") {
    if (q.since) p.set("since", q.since);
    if (q.until) p.set("until", q.until);
    if (!q.since && !q.until) p.set("range", "custom");
  } else if (range !== "all") {
    p.set("range", range);
  }
  if (q.task) p.set("task", q.task);
  const s = p.toString();
  return s ? `?${s}` : "";
}

export function wireDay(d: Date): string {
  return d.toISOString().slice(0, 10);
}

function addDays(day: string, n: number): string {
  const t = Date.parse(`${day}T00:00:00Z`) + n * 86_400_000;
  return new Date(t).toISOString().slice(0, 10);
}

/** The inclusive since/until a query sends ("" = unbounded). */
export function resolveRange(q: UsageQuery, now: Date = new Date()): { since: string; until: string } {
  const today = wireDay(now);
  switch (q.range) {
    case "today":
      return { since: today, until: today };
    case "7d":
      return { since: addDays(today, -6), until: today };
    case "30d":
      return { since: addDays(today, -29), until: today };
    case "month":
      return { since: `${today.slice(0, 8)}01`, until: today };
    case "custom":
      return { since: q.since, until: q.until };
    default:
      return { since: "", until: "" };
  }
}

/** A custom range whose start is after its end can only return nothing. */
export function rangeError(q: UsageQuery): string | null {
  if (q.range === "custom" && q.since && q.until && q.since > q.until) return "The start date is after the end date.";
  return null;
}

export interface UsageRequest {
  project: string;
  groupBy: string[];
  since: string;
  until: string;
  task: string;
}

/** GetUsage for a scope ("" = all projects, or the sole/default one). */
export function usageRequest(project: string, q: UsageQuery, now: Date = new Date(), groupBy: UsageDim[] = q.by): UsageRequest {
  const { since, until } = resolveRange(q, now);
  return { project, groupBy: [...groupBy], since, until, task: q.task };
}

/** One grouped cell, labelled exactly as `ycc cost` prints it. */
export function cellLabel(row: UsageRow, dim: UsageDim): string {
  switch (dim) {
    case "task":
      return row.task || "(unattributed)";
    case "model":
      return row.model || "(unknown)";
    case "agent":
      return row.agent || "(unknown)";
    case "session":
      return row.session;
    case "day":
      return row.day;
  }
}

/** Thousands separators, as `ycc cost` prints counts. */
export function commas(n: bigint | number): string {
  const v = typeof n === "bigint" ? n : BigInt(Math.trunc(n));
  const neg = v < 0n;
  const s = (neg ? -v : v).toString();
  let out = "";
  for (let i = 0; i < s.length; i++) {
    if (i > 0 && (s.length - i) % 3 === 0) out += ",";
    out += s[i];
  }
  return neg ? `-${out}` : out;
}

/** The CLI's cost cell: "—" unpriced, "$0.1234*" partial, "$0.1234" priced. */
export function costCell(cost: number, status: string): string {
  switch (status.trim().toLowerCase()) {
    case "unpriced":
      return "—";
    case "partial":
      return `$${cost.toFixed(4)}*`;
    default:
      return `$${cost.toFixed(4)}`;
  }
}

/** A headline amount: "$12.34", or "$0.0042" for sub-cent spend. */
export function headlineCost(cost: number, status: string): string {
  if (status.trim().toLowerCase() === "unpriced") return "Unpriced";
  const v = cost > 0 && cost < 0.01 ? cost.toFixed(4) : cost.toFixed(2);
  return `$${v}${status === "partial" ? "*" : ""}`;
}

/** "842", "12.3K", "1.2M" (headline token counts). */
export function shortTokens(n: bigint | number): string {
  const v = Math.abs(Number(n));
  const sign = Number(n) < 0 ? "-" : "";
  if (v >= 1_000_000_000) return `${sign}${(v / 1_000_000_000).toFixed(1)}B`;
  if (v >= 1_000_000) return `${sign}${(v / 1_000_000).toFixed(1)}M`;
  if (v >= 1_000) return `${sign}${(v / 1_000).toFixed(1)}K`;
  return `${sign}${v}`;
}

export type PriceStatus = "priced" | "unpriced" | "partial";

/** Merge price statuses like the daemon: all priced, all unpriced, else partial. */
export function mergePriceStatus(statuses: readonly string[]): PriceStatus {
  let priced = false;
  let unpriced = false;
  for (const raw of statuses) {
    const s = raw.trim().toLowerCase();
    if (s === "partial") return "partial";
    if (s === "unpriced") unpriced = true;
    else priced = true;
  }
  if (priced && unpriced) return "partial";
  return unpriced ? "unpriced" : "priced";
}

export function anyPartial(rows: readonly UsageRow[], total?: UsageRow | null): boolean {
  return rows.some((r) => r.priceStatus === "partial") || total?.priceStatus === "partial";
}

export type SortKey = "default" | "label" | "input" | "output" | "cache" | "total" | "cost";

/** Rows in the requested order; "default" keeps the daemon's (tokens desc). */
export function sortRows(rows: readonly UsageRow[], by: readonly UsageDim[], key: SortKey, desc: boolean): UsageRow[] {
  const out = [...rows];
  if (key === "default") return out;
  const num = (r: UsageRow): number => {
    switch (key) {
      case "input":
        return Number(r.input);
      case "output":
        return Number(r.output);
      case "cache":
        return Number(r.cacheRead + r.cacheWrite);
      case "total":
        return Number(r.total);
      case "cost":
        // Unpriced rows sort below every priced amount.
        return r.priceStatus === "unpriced" ? -1 : r.cost;
      default:
        return 0;
    }
  };
  out.sort((a, b) => {
    if (key === "label") {
      const la = by.map((d) => cellLabel(a, d)).join("\u0000");
      const lb = by.map((d) => cellLabel(b, d)).join("\u0000");
      return desc ? lb.localeCompare(la) : la.localeCompare(lb);
    }
    const d = num(a) - num(b);
    return desc ? -d : d;
  });
  return out;
}

/** A row's share of the total (0..1) by tokens, or by cost when priced. */
export function rowShare(row: UsageRow, total: UsageRow | null | undefined, metric: "tokens" | "cost"): number {
  if (!total) return 0;
  if (metric === "cost") {
    if (!(total.cost > 0) || row.priceStatus === "unpriced") return 0;
    return Math.min(1, Math.max(0, row.cost / total.cost));
  }
  const t = Number(total.total);
  return t > 0 ? Math.min(1, Math.max(0, Number(row.total) / t)) : 0;
}

export type BucketSize = "day" | "week" | "month";

export interface TimelineBucket {
  /** First day of the bucket (YYYY-MM-DD). */
  start: string;
  /** Last day of the bucket (inclusive). */
  end: string;
  label: string;
  tokens: bigint;
  cost: number;
  status: PriceStatus;
  /** Whether any usage fell in the bucket. */
  hasUsage: boolean;
}

function dayIndex(day: string): number {
  return Math.round(Date.parse(`${day}T00:00:00Z`) / 86_400_000);
}

function weekStart(day: string): string {
  const dow = new Date(`${day}T00:00:00Z`).getUTCDay(); // 0 = Sunday
  return addDays(day, -((dow + 6) % 7)); // weeks start on Monday
}

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

function shortDay(day: string): string {
  return `${MONTHS[Number(day.slice(5, 7)) - 1]} ${Number(day.slice(8, 10))}`;
}

/** Day buckets up to two months, weeks up to a year, then months. */
export function bucketSizeFor(spanDays: number): BucketSize {
  if (spanDays <= 62) return "day";
  if (spanDays <= 371) return "week";
  return "month";
}

/**
 * Bucket day-grouped usage rows into a gap-filled timeline from `since` (or
 * the first usage day) to `until` (or the last usage day, capped at today).
 */
export function buildTimeline(
  rows: readonly UsageRow[],
  bounds: { since: string; until: string },
  now: Date = new Date(),
): { size: BucketSize; buckets: TimelineBucket[] } {
  const days = rows.map((r) => r.day).filter(isWireDay).sort();
  if (!days.length) return { size: "day", buckets: [] };
  const today = wireDay(now);
  const first = bounds.since && bounds.since < days[0] ? bounds.since : days[0];
  let last = bounds.until && bounds.until > days[days.length - 1] ? bounds.until : days[days.length - 1];
  if (last > today && days[days.length - 1] <= today) last = today;
  const size = bucketSizeFor(dayIndex(last) - dayIndex(first) + 1);
  const keyOf = (day: string) => (size === "day" ? day : size === "week" ? weekStart(day) : `${day.slice(0, 8)}01`);
  const nextStart = (start: string) => {
    if (size === "day") return addDays(start, 1);
    if (size === "week") return addDays(start, 7);
    const y = Number(start.slice(0, 4));
    const m = Number(start.slice(5, 7));
    return m === 12 ? `${y + 1}-01-01` : `${y}-${String(m + 1).padStart(2, "0")}-01`;
  };
  const buckets: TimelineBucket[] = [];
  const byKey = new Map<string, { b: TimelineBucket; statuses: string[] }>();
  for (let start = keyOf(first); start <= last; start = nextStart(start)) {
    const end = addDays(nextStart(start), -1);
    const label =
      size === "day" ? shortDay(start) : size === "week" ? `Week of ${shortDay(start)}` : `${MONTHS[Number(start.slice(5, 7)) - 1]} ${start.slice(0, 4)}`;
    const b: TimelineBucket = { start, end, label, tokens: 0n, cost: 0, status: "priced", hasUsage: false };
    buckets.push(b);
    byKey.set(start, { b, statuses: [] });
  }
  for (const r of rows) {
    if (!isWireDay(r.day)) continue;
    const slot = byKey.get(keyOf(r.day));
    if (!slot) continue;
    slot.b.tokens += r.total;
    slot.b.cost += r.cost;
    slot.b.hasUsage = true;
    slot.statuses.push(r.priceStatus);
  }
  for (const { b, statuses } of byKey.values()) b.status = statuses.length ? mergePriceStatus(statuses) : "priced";
  return { size, buckets };
}

// ---- Subscription allowance -------------------------------------------------

/** "resets in 2h 14m" (or "resets Oct 7, 14:00" past a day); null when unknown. */
export function resetText(resetsAtUnix: bigint | number, now: number = Date.now()): string | null {
  const at = Number(resetsAtUnix) * 1000;
  if (!(at > 0)) return null;
  const secs = Math.round((at - now) / 1000);
  if (secs <= 0) return "resets now";
  const m = Math.round(secs / 60);
  if (m < 60) return `resets in ${Math.max(1, m)}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return `resets in ${h}h ${m % 60}m`;
  const d = new Date(at);
  return `resets ${MONTHS[d.getMonth()]} ${d.getDate()}, ${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}

export function windowPercent(w: SubscriptionUsageWindow): number {
  return Math.min(100, Math.max(0, Number.isFinite(w.usedPercent) ? w.usedPercent : 0));
}

export function windowTone(w: SubscriptionUsageWindow): "ok" | "warn" | "over" {
  const p = windowPercent(w);
  return p >= 100 ? "over" : p >= 90 ? "warn" : "ok";
}

export function capitalize(s: string): string {
  return s ? s[0].toUpperCase() + s.slice(1) : s;
}

/** The account's state badge (none when fresh). */
export function accountBadge(a: SubscriptionUsageAccount): { label: string; tone: "warn" | "muted" } | null {
  const s = a.state.trim().toLowerCase();
  if (!s || s === "fresh") return null;
  return { label: s, tone: s === "stale" ? "warn" : "muted" };
}

// ---- Budget (spend guard) ----------------------------------------------------

export function costCap(cost: number): string {
  return cost > 0 ? `$${cost.toFixed(2)}` : "Unlimited";
}

export function tokenCap(tokens: bigint): string {
  return tokens > 0n ? `${commas(tokens)} tokens` : "Unlimited";
}

export function budgetRows(b: GetBudgetResponse): { label: string; value: string; unlimited: boolean }[] {
  return [
    { label: "Per session · cost", value: costCap(b.sessionCost), unlimited: !(b.sessionCost > 0) },
    { label: "Per session · tokens", value: tokenCap(b.sessionTokens), unlimited: !(b.sessionTokens > 0n) },
    { label: "Per loop run · cost", value: costCap(b.loopCost), unlimited: !(b.loopCost > 0) },
    { label: "Per loop run · tokens", value: tokenCap(b.loopTokens), unlimited: !(b.loopTokens > 0n) },
  ];
}
