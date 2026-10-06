// Pure work-loop logic: lifecycle state, digest sections, summary/totals
// lines, the captured resource envelope, completion announcements, and the
// sessions a loop owns. Mirrors YccKit's WorkLoopModel so the web and iOS
// clients label a loop alike.
import type { GetBudgetResponse, WorkLoopDigestTask, WorkLoopInfo } from "../../gen/ycc/v1/ycc_pb";
import { compactTokenCount } from "../sessions/feed";

export type LoopState = "running" | "waiting" | "stopping" | "finished" | "none" | "unknown";

/** The daemon-side lifecycle; no snapshot is "none", an unknown value "unknown". */
export function loopState(loop: WorkLoopInfo | null | undefined): LoopState {
  if (!loop) return "none";
  const s = loop.state.trim().toLowerCase();
  switch (s) {
    case "running":
    case "waiting":
    case "stopping":
    case "finished":
      return s;
    default:
      return "unknown";
  }
}

export function stateTitle(state: LoopState): string {
  switch (state) {
    case "running":
      return "Running";
    case "waiting":
      return "Waiting";
    case "stopping":
      return "Stopping";
    case "finished":
      return "Finished";
    case "none":
      return "Not running";
    case "unknown":
      return "Unknown";
  }
}

export const canStart = (s: LoopState) => s === "none" || s === "finished";
export const canStop = (s: LoopState) => s === "running" || s === "waiting";
export const isActive = (s: LoopState) => s === "running" || s === "waiting" || s === "stopping";

/** Poll quickly while a loop is live; otherwise refresh slowly (another client may start one). */
export function pollInterval(loop: WorkLoopInfo | null | undefined): number {
  return isActive(loopState(loop)) ? 3_000 : 30_000;
}

export function currentSessionId(loop: WorkLoopInfo | null | undefined): string {
  return loop?.currentSessionId.trim() ?? "";
}

/** Every session a loop ran or is running (for "via loop" markers). */
export function loopSessionIds(loop: WorkLoopInfo | null | undefined): Set<string> {
  const ids = new Set<string>();
  if (!loop) return ids;
  for (const s of loop.sessions) if (s.sessionId) ids.add(s.sessionId);
  const current = currentSessionId(loop);
  if (current) ids.add(current);
  return ids;
}

export interface DigestSection {
  key: "completed" | "blocked" | "inReview" | "unfinished" | "created";
  title: string;
  rows: WorkLoopDigestTask[];
}

/** Digest groups in product order; empty groups disappear. */
export function digestSections(loop: WorkLoopInfo): DigestSection[] {
  const all: DigestSection[] = [
    { key: "completed", title: "Completed", rows: loop.completed },
    { key: "blocked", title: "Blocked", rows: loop.blocked },
    { key: "inReview", title: "In review", rows: loop.inReview },
    { key: "unfinished", title: "Unfinished", rows: loop.unfinished },
    { key: "created", title: "Created", rows: loop.created },
  ];
  return all.filter((s) => s.rows.length > 0);
}

const plural = (n: number, one: string, many = one + "s") => `${n} ${n === 1 ? one : many}`;

/** "3 sessions · 2 completed, 1 blocked". */
export function summaryLine(loop: WorkLoopInfo): string {
  const sessions = plural(loop.sessionsRun, "session");
  const counts = [
    [loop.completed.length, "completed"],
    [loop.blocked.length, "blocked"],
    [loop.inReview.length, "in review"],
    [loop.unfinished.length, "unfinished"],
    [loop.created.length, "created"],
  ] as const;
  const digest = counts
    .filter(([n]) => n > 0)
    .map(([n, label]) => `${n} ${label}`)
    .join(", ");
  return digest ? `${sessions} · ${digest}` : sessions;
}

export function formatTokens(tokens: bigint | number): string {
  const n = Number(tokens);
  return compactTokenCount(n) ?? "0";
}

/** Cost text never presents incomplete pricing as exact. */
export function formatCost(cost: number, status: string): string {
  switch (status.trim().toLowerCase()) {
    case "priced":
      return `$${cost.toFixed(4)}`;
    case "partial":
      return `≈$${cost.toFixed(4)} (partial)`;
    default:
      return "unpriced";
  }
}

export function totalsLine(tokens: bigint | number, cost: number, priceStatus: string): string {
  return `${formatTokens(tokens)} tokens · ${formatCost(cost, priceStatus)}`;
}

export function loopTotalsLine(loop: WorkLoopInfo): string {
  return totalsLine(loop.totalTokens, loop.totalCost, loop.costStatus);
}

/** Compact wall-clock duration ("45s", "3m 20s", "2h 5m"); null when unknown. */
export function durationText(secs: bigint | number): string | null {
  const s = Number(secs);
  if (!(s > 0)) return null;
  if (s < 60) return `${s}s`;
  if (s < 3_600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  return `${Math.floor(s / 3_600)}h ${Math.floor((s % 3_600) / 60)}m`;
}

function limitLine(scope: string, tokens: bigint | number, cost: number, timeSecs: bigint | number): string {
  const tokenText = Number(tokens) > 0 ? `${formatTokens(tokens)} tokens` : "tokens unbounded";
  const costText = cost > 0 ? `$${cost.toFixed(2)} priced cost` : "cost unbounded";
  const time = durationText(timeSecs);
  const timeText = time ? `${time} wall time` : "wall time unbounded";
  return `${scope}: ${tokenText} · ${costText} · ${timeText}`;
}

/** The resource envelope captured when the loop started; zero limits are explicitly unbounded. */
export function resourceEnvelopeLines(loop: WorkLoopInfo): string[] {
  if (!loop.resourceEnvelopeCaptured) return ["Resource envelope unavailable for this pre-upgrade snapshot."];
  return [
    limitLine("Session", loop.sessionTokenLimit, loop.sessionCostLimit, loop.sessionTimeLimitSecs),
    limitLine("Loop", loop.loopTokenLimit, loop.loopCostLimit, loop.loopTimeLimitSecs),
    loop.costLimitsPricedOnly
      ? "Cost caps and totals count priced models only; tokens count all models."
      : "Cost-limit pricing semantics unavailable.",
    "Attempts: no fixed limit; ready work continues until stopped, budget-limited, or blocked.",
  ];
}

/** The configured caps a new loop would capture (GetBudget), before starting. */
export function budgetLines(b: GetBudgetResponse): string[] {
  const line = (scope: string, tokens: bigint, cost: number) =>
    `${scope}: ${Number(tokens) > 0 ? `${formatTokens(tokens)} tokens` : "tokens unbounded"} · ${
      cost > 0 ? `$${cost.toFixed(2)} priced cost` : "cost unbounded"
    }`;
  return [line("Per session", b.sessionTokens, b.sessionCost), line("Whole loop", b.loopTokens, b.loopCost)];
}

export function formatClock(iso: string): string | null {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return null;
  return new Date(t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

/** "Waiting for provider (rate_limit) — resumes 03:15". */
export function waitingLine(loop: WorkLoopInfo): string {
  let line = "Waiting for provider";
  const kind = loop.waitKind.trim();
  if (kind) line += ` (${kind})`;
  const at = formatClock(loop.resumeAt);
  if (at) line += ` — resumes ${at}`;
  return line;
}

/** A compact status line for banners and the sidebar tooltip. */
export function bannerLine(loop: WorkLoopInfo | null | undefined): string {
  const state = loopState(loop);
  if (!loop) return stateTitle(state);
  switch (state) {
    case "running":
      return `Running · ${summaryLine(loop)}`;
    case "waiting":
      return waitingLine(loop);
    case "stopping":
      return "Stopping…";
    case "finished":
      return loop.outcome.trim() || `Finished · ${summaryLine(loop)}`;
    default:
      return stateTitle(state);
  }
}

/**
 * Whether a finished outcome reports a failure rather than a normal end
 * (drained backlog, requested stop, or budget reached).
 */
export function isFailureOutcome(outcome: string): boolean {
  const o = outcome.trim().toLowerCase();
  if (!o) return false;
  if (o.startsWith("loop complete")) return false;
  if (o === "loop stopped: requested") return false;
  if (o.includes("budget reached")) return false;
  return true;
}

/**
 * The announcement for a loop observed going from active to finished (or a
 * Start that returned an already-finished loop), else null. A historical
 * completion seen on first load is not announced.
 */
export function finishAnnouncement(
  prev: WorkLoopInfo | null | undefined,
  next: WorkLoopInfo | null | undefined,
  fromStart = false,
): { text: string; failure: boolean } | null {
  if (loopState(next) !== "finished" || !next) return null;
  const sameLoop = prev && prev.loopId === next.loopId;
  if (!fromStart && !(sameLoop && isActive(loopState(prev)))) return null;
  const outcome = next.outcome.trim();
  const label = next.project ? `Work loop in ${next.project} finished` : "Work loop finished";
  return { text: outcome ? `${label}: ${outcome}` : `${label} · ${summaryLine(next)}`, failure: isFailureOutcome(outcome) };
}

/** The sidebar's loop indicator: null when nothing is live. */
export function loopIndicator(loops: readonly (WorkLoopInfo | null | undefined)[]): { label: string; title: string; tone: "live" | "warn" } | null {
  const active = loops.filter((l): l is WorkLoopInfo => !!l && isActive(loopState(l)));
  if (!active.length) return null;
  const waiting = active.some((l) => loopState(l) === "waiting");
  const title = active.map((l) => (l.project ? `${l.project}: ${bannerLine(l)}` : bannerLine(l))).join("\n");
  const label = active.length === 1 ? stateTitle(loopState(active[0])).toLowerCase() : `${active.length} running`;
  return { label, title, tone: waiting ? "warn" : "live" };
}

export const WORK_IMPLEMENTATIONS = [
  { value: "delegate", label: "Delegate", detail: "The coordinator hands code changes to an implementer subagent." },
  { value: "direct", label: "Direct", detail: "The coordinator edits the code itself." },
] as const;
