// Pure work-loop logic: lifecycle state, digest sections, summary/totals
// lines, the captured resource envelope, completion announcements, and the
// sessions a loop owns. Mirrors YccKit's WorkLoopModel so the web and iOS
// clients label a loop alike.
import type { GetBudgetResponse, WorkLoopDigestTask, WorkLoopInfo, WorkLoopSlot } from "../../gen/ycc/v1/ycc_pb";
import { shortSessionId } from "../coordination/model";
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
export const canResize = (s: LoopState) => s === "running" || s === "waiting";
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
  for (const s of loop.slots) if (s.sessionId) ids.add(s.sessionId);
  const current = currentSessionId(loop);
  if (current) ids.add(current);
  return ids;
}

export interface DigestSection {
  key: "completed" | "blocked" | "inReview" | "unfinished" | "created" | "uncommittedInReview";
  title: string;
  rows: WorkLoopDigestTask[];
}

const isDone = (t: WorkLoopDigestTask) => t.status.trim().toLowerCase() === "done";

/** Every task that reached done during the loop: ones that existed at start, then ones it created. */
export function completedTasks(loop: WorkLoopInfo): WorkLoopDigestTask[] {
  return [...loop.completed, ...loop.created.filter(isDone)];
}

/** Tasks created during the loop that are not done. */
export function createdOpenTasks(loop: WorkLoopInfo): WorkLoopDigestTask[] {
  return loop.created.filter((t) => !isDone(t));
}

/** Digest groups in product order; empty groups disappear. */
export function digestSections(loop: WorkLoopInfo): DigestSection[] {
  const all: DigestSection[] = [
    { key: "completed", title: "Completed", rows: completedTasks(loop) },
    { key: "blocked", title: "Blocked", rows: loop.blocked },
    { key: "inReview", title: "In review", rows: loop.inReview },
    { key: "unfinished", title: "Unfinished", rows: loop.unfinished },
    { key: "created", title: "Created, still open", rows: createdOpenTasks(loop) },
    { key: "uncommittedInReview", title: "Uncommitted in review at start", rows: loop.uncommittedInReview },
  ];
  return all.filter((s) => s.rows.length > 0);
}

const plural = (n: number, one: string, many = one + "s") => `${n} ${n === 1 ? one : many}`;

/**
 * "2 in-review tasks have uncommitted changes in 5 files", or "" when none:
 * loop sessions leave that work to its owner and route around those files.
 */
export function uncommittedInReviewLine(tasks: WorkLoopDigestTask[], paths: number): string {
  if (tasks.length === 0) return "";
  const subject = tasks.length === 1 ? "1 in-review task has" : `${tasks.length} in-review tasks have`;
  return `${subject} uncommitted changes in ${plural(paths, "file")}`;
}

/**
 * Loop sessions running now: an active loop's slots that have a session. A
 * slot still starting has none yet, and a finished loop's slots are history
 * (a restored loop keeps its interrupted waiters).
 */
function liveSessionCount(loop: WorkLoopInfo): number {
  if (!isActive(loopState(loop))) return 0;
  return loop.slots.filter((s) => s.sessionId.trim() !== "").length;
}

/**
 * "3 sessions", or with live ones "3 sessions finished, 2 live". Without
 * `withLive` (the slots line beside it names them) live ones are left out.
 */
function sessionsPart(loop: WorkLoopInfo, withLive: boolean): string {
  const finished = loop.sessionsRun;
  const live = liveSessionCount(loop);
  if (live === 0) return plural(finished, "session");
  if (!withLive) return finished > 0 ? `${plural(finished, "session")} finished` : "";
  return finished > 0 ? `${plural(finished, "session")} finished, ${live} live` : plural(live, "live session");
}

/**
 * "3 sessions finished, 1 live · 4 completed, 1 blocked, 2 created still open, 1 other commit".
 * Completed counts every task that reached done, including ones the loop created.
 */
export function summaryLine(loop: WorkLoopInfo, withLive = true): string {
  const sessions = sessionsPart(loop, withLive);
  const counts = [
    [completedTasks(loop).length, "completed"],
    [loop.blocked.length, "blocked"],
    [loop.inReview.length, "in review"],
    [loop.unfinished.length, "unfinished"],
    [createdOpenTasks(loop).length, "created still open"],
  ] as const;
  const parts = counts.filter(([n]) => n > 0).map(([n, label]) => `${n} ${label}`);
  if (loop.manualLandings.length > 0) parts.push(plural(loop.manualLandings.length, "other commit"));
  return [sessions, parts.join(", ")].filter(Boolean).join(" · ");
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

/** A digest row's usage, or null for a task no loop session worked (nothing to price). */
export function digestTotalsLine(t: WorkLoopDigestTask): string | null {
  if (t.attempts === 0 && Number(t.tokens) === 0) return null;
  return totalsLine(t.tokens, t.cost, t.priceStatus);
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
    "Attempts: no fixed limit; sessions without progress back off, and 12h without progress stops the loop.",
  ];
}

/** The configured caps a new loop would capture (GetBudget), before starting. */
/** "Caps: $5.00 or 2.0M tokens per session · none per loop.", or that no caps are set. */
export function budgetLine(b: GetBudgetResponse): string {
  const cap = (tokens: bigint, cost: number) =>
    [cost > 0 ? `$${cost.toFixed(2)}` : "", Number(tokens) > 0 ? `${formatTokens(tokens)} tokens` : ""].filter(Boolean).join(" or ") ||
    "none";
  const session = cap(b.sessionTokens, b.sessionCost);
  const loop = cap(b.loopTokens, b.loopCost);
  if (session === "none" && loop === "none") return "No budget caps set.";
  return `Caps: ${session} per session · ${loop} per loop.`;
}

export function formatClock(iso: string): string | null {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return null;
  return new Date(t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

/**
 * "Waiting for provider (rate_limit) — resumes 03:15", the no-progress backoff,
 * or a wait on loop sessions that wait on their background jobs.
 */
export function waitingLine(loop: WorkLoopInfo): string {
  let line = "Waiting for provider";
  const kind = loop.waitKind.trim();
  if (kind === "no_progress") line = "Waiting: last session made no progress";
  else if (kind === "sessions_waiting") line = "Waiting: loop sessions are waiting on background jobs";
  else if (kind) line += ` (${kind})`;
  const at = formatClock(loop.resumeAt);
  if (at) line += ` — resumes ${at}`;
  return line;
}

/**
 * "2/3 working, 1 waiting": actual working slots and the worker target,
 * plus sessions waiting on background jobs. The target is not a hard cap:
 * lowering it leaves existing sessions working. Empty for pre-slot snapshots.
 */
export function slotsLine(loop: WorkLoopInfo): string {
  if (loop.parallel <= 0) return "";
  const working = loop.slots.filter((s) => s.state === "working").length;
  const waiting = loop.slots.filter((s) => s.state === "waiting").length;
  const workers = working > loop.parallel ? `${working} working (target ${loop.parallel})` : `${working}/${loop.parallel} working`;
  return `${workers}, ${waiting} waiting`;
}

/**
 * Whether the loop may run more than one session at once. Its single
 * "now running" session then does not describe it; the slots do.
 */
export function isParallel(loop: WorkLoopInfo): boolean {
  return loop.parallel > 1 || loop.slots.length > 1;
}

/** "working 12m 3s", "waiting 3h 20m": a slot's state and how long it has been in it. */
export function slotStateText(slot: WorkLoopSlot, now: number = Date.now()): string {
  const state = slot.state.trim() || "working";
  const since = Date.parse(slot.since);
  if (Number.isNaN(since)) return state;
  const secs = Math.floor((now - since) / 1000);
  return secs < 1 ? state : `${state} ${durationText(secs)}`;
}

/**
 * The Stop confirmation: how many working sessions finish, and that sessions
 * the loop let wait on their background jobs keep running, even ones a job
 * has woken. Snapshots without slots (older daemons) keep the one-session wording.
 */
export function stopConfirmBody(loop: WorkLoopInfo | null | undefined): string {
  const providerWait = loopState(loop) === "waiting" && loop?.waitKind.trim() !== "sessions_waiting";
  const noMore = "The daemon will not pick another backlog task.";
  if (!loop || loop.parallel <= 0) {
    return providerWait
      ? `The loop is waiting for the provider; stopping ends the wait at once. ${noMore}`
      : "The current session finishes its work; the daemon will not pick another backlog task.";
  }
  const detached = loop.slots.filter((s) => s.detached);
  const foreground = loop.slots.length - detached.length;
  const woken = detached.filter((s) => s.state === "working").length;
  const parts: string[] = [];
  if (providerWait) parts.push("The loop is waiting for the provider; stopping ends the wait at once.");
  if (foreground > 0) parts.push(`${foreground === 1 ? "1 foreground session finishes its" : `${foreground} foreground sessions finish their`} work.`);
  const n = detached.length;
  if (n > 0 && woken === 0) {
    parts.push(n === 1 ? "1 session waiting on its background jobs keeps running." : `${n} sessions waiting on their background jobs keep running.`);
  } else if (n > 0) {
    parts.push(`${n === 1 ? "1 session" : `${n} sessions`} let wait on background jobs ${n === 1 ? "keeps" : "keep"} running (${woken} working again).`);
  }
  parts.push(noMore);
  return parts.join(" ");
}

const WAITER_CLAUSE_RE =
  /;\s*(still waiting on background jobs|still working after their jobs woke them|sessions were waiting on background jobs):\s*([^;]+)/gi;
const SESSION_ID_RE = /\bs_(?:[a-z]+-[a-z]+-[0-9a-f]{4}|[0-9a-f]{16})\b/g;

function countWaiterEntries(list: string): number {
  return list
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean).length;
}

/**
 * Count of live detached sessions noted in a finished outcome (still waiting
 * on background jobs or woken and still working).
 */
export function outcomeWaitingCount(outcome: string): number {
  let total = 0;
  for (const m of outcome.matchAll(WAITER_CLAUSE_RE)) {
    const kind = m[1].toLowerCase();
    if (kind === "sessions were waiting on background jobs") continue;
    total += countWaiterEntries(m[2]);
  }
  return total;
}

/**
 * A readable one-line outcome for banners and the sidebar: replaces raw
 * semicolon lists of full session IDs ("still waiting on background jobs:
 * s_4228... (0394), ...") with concise counts, and shortens any remaining
 * session IDs.
 */
export function compactOutcome(outcome: string): string {
  const trimmed = outcome.trim();
  if (!trimmed) return "";
  const clauses: string[] = [];
  let hadClauses = false;
  const base = trimmed
    .replace(WAITER_CLAUSE_RE, (_, kind: string, list: string) => {
      hadClauses = true;
      const n = countWaiterEntries(list);
      const k = kind.toLowerCase();
      if (k === "still waiting on background jobs") {
        clauses.push(`${n} waiting on background jobs`);
      } else if (k === "still working after their jobs woke them") {
        clauses.push(`${n} still working`);
      } else {
        clauses.push(`${n} were waiting on background jobs`);
      }
      return "";
    })
    .trim();
  const cleanBase =
    hadClauses && base.toLowerCase() === "loop stopped: requested"
      ? "Stopped by request"
      : base.replace(SESSION_ID_RE, (id) => shortSessionId(id));
  return [cleanBase, ...clauses].filter(Boolean).join(" · ");
}

/** A compact status line for banners and the sidebar tooltip. */
export function bannerLine(loop: WorkLoopInfo | null | undefined): string {
  const state = loopState(loop);
  if (!loop) return stateTitle(state);
  switch (state) {
    case "running":
      return ["Running", slotsLine(loop), summaryLine(loop, !slotsLine(loop))].filter(Boolean).join(" · ");
    case "waiting":
      return [waitingLine(loop), slotsLine(loop)].filter(Boolean).join(" · ");
    case "stopping":
      return "Stopping…";
    case "finished":
      return compactOutcome(loop.outcome) || `Finished · ${summaryLine(loop)}`;
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
  if (o.startsWith("loop stopped: requested")) return false;
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

/** How long a finished loop stays in the sidebar's ambient status. */
export const RECENT_LOOP_MS = 24 * 60 * 60 * 1000;

/**
 * Loops worth ambient sidebar status: live ones, and ones that ran sessions and
 * finished within RECENT_LOOP_MS unless dismissed. A finished snapshot without
 * finishedAt (pre-upgrade daemon) has no known age and stays hidden.
 */
export function ambientLoops(
  loops: readonly { project: string; loop: WorkLoopInfo | null }[],
  now: number,
  dismissed: ReadonlySet<string>,
): { project: string; loop: WorkLoopInfo }[] {
  const out: { project: string; loop: WorkLoopInfo }[] = [];
  for (const { project, loop } of loops) {
    if (!loop) continue;
    const state = loopState(loop);
    if (isActive(state)) out.push({ project, loop });
    else if (state === "finished" && loop.sessionsRun > 0 && !dismissed.has(loop.loopId)) {
      const at = Date.parse(loop.finishedAt);
      if (!Number.isNaN(at) && now - at < RECENT_LOOP_MS) out.push({ project, loop });
    }
  }
  return out;
}

/** Completed tasks, most recently worked first (by the last session focused on each). */
export function recentCompleted(loop: WorkLoopInfo): WorkLoopDigestTask[] {
  const last = new Map<string, number>();
  loop.sessions.forEach((s, i) => s.focus && last.set(s.focus, i));
  return completedTasks(loop).sort((a, b) => (last.get(b.id) ?? -1) - (last.get(a.id) ?? -1));
}

/** The highest worker target accepted by StartWorkLoop and ResizeWorkLoop. */
export const MAX_LOOP_PARALLEL = 16;

/** Unlike Start, Resize requires an explicit worker target; 0 is not a default. */
export const isResizeTarget = (n: number) => Number.isInteger(n) && n >= 1 && n <= MAX_LOOP_PARALLEL;

/**
 * The slot count Start sends as StartWorkLoopRequest.parallel: 0 (the
 * project's [work_loop] parallel applies) unless the user picked a count
 * other than the configured one. `configured` is 0 when the daemon did not
 * report it.
 */
export function parallelToSend(choice: number | null, configured: number): number {
  if (choice === null || choice === configured) return 0;
  return Math.min(Math.max(Math.trunc(choice), 1), MAX_LOOP_PARALLEL);
}

/** How the slots fill: one at a time, or staggered by task claims. */
export function parallelExplanation(parallel: number): string {
  return parallel <= 1
    ? "One session at a time."
    : "Each session starts once the previous one has picked its task. They share one working tree (experimental).";
}

/** Who writes the code in work sessions (the daemon-wide work implementation). */
export const WORK_IMPLEMENTATIONS = [
  { value: "direct", label: "Main agent", detail: "The main agent writes the code; separate agents review it." },
  { value: "delegate", label: "Implementer", detail: "The main agent plans and checks; the implementer model writes the code." },
] as const;
