// Pure session-list logic: merge per-project ListSessionHistory pages into the
// daemon-wide Recent feed (or one project's list), section it with needs-answer
// sessions pinned first, and derive row labels. Mirrors YccKit's
// SessionListModel so the web and iOS clients order and label rows alike.
import type { ProjectInfo, SessionSummary } from "../../gen/ycc/v1/ycc_pb";

/** One project's loaded history pages. */
export interface HistoryLoad {
  project: string;
  sessions: SessionSummary[];
  pinned: SessionSummary[];
  nextCursor: string;
  /** Load failure message; the previous rows (if any) are kept. */
  error?: string;
}

export interface FeedRow {
  session: SessionSummary;
  /** Project argument for routing/RPCs ("" resolves server-side). */
  project: string;
}

export interface FeedSection {
  kind: "needsAnswer" | "all";
  title: string | null;
  rows: FeedRow[];
}

export interface Feed {
  rows: FeedRow[];
  hasMore: boolean;
  warning?: string;
  error?: string;
}

/**
 * The history queries a project list implies: one per distinct workspace
 * (aliases of the same path return the same logs). With no registered project
 * the daemon may still own its startup workspace; "" resolves it server-side.
 */
export function historyTargets(projects: ProjectInfo[]): string[] {
  if (projects.length === 0) return [""];
  const seen = new Set<string>();
  const out: string[] = [];
  for (const p of projects) {
    const path = p.path.trim();
    const identity = path ? `path:${path}` : `name:${p.name}`;
    if (seen.has(identity)) continue;
    seen.add(identity);
    out.push(p.name);
  }
  return out;
}

/** Merge a cursor page into a load by session id (pinned live rows dedupe). */
export function mergePage(load: HistoryLoad, sessions: SessionSummary[], nextCursor: string): HistoryLoad {
  const merged = load.sessions.slice();
  const index = new Map(merged.map((s, i) => [s.sessionId, i]));
  for (const row of sessions) {
    const i = index.get(row.sessionId);
    if (i === undefined) {
      index.set(row.sessionId, merged.length);
      merged.push(row);
    } else {
      merged[i] = row;
    }
  }
  return { ...load, sessions: merged, nextCursor, error: undefined };
}

/** Update a bookmark without changing recency, paging, or other sessions. */
export function withFollowUp(loads: HistoryLoad[], project: string, sessionId: string, followUp: boolean, followUpAt: string): HistoryLoad[] {
  const patch = (s: SessionSummary) => s.sessionId === sessionId ? { ...s, followUp, followUpAt } : s;
  return loads.map((load) => load.project === project
    ? { ...load, sessions: load.sessions.map(patch), pinned: load.pinned.map(patch) }
    : load);
}

export function onlyFollowUp(rows: FeedRow[]): FeedRow[] {
  return rows.filter((row) => row.session.followUp);
}

/**
 * Build the visible feed. `scope` null is the daemon-wide Recent feed; a name
 * restricts it to that project's loaded pages. In the aggregate, rows older
 * than the newest truncated project's oldest loaded row are held back until
 * that project pages past them, so the global order never has holes.
 */
export function buildFeed(loads: HistoryLoad[], scope: string | null): Feed {
  const failed = loads.filter((l) => l.error);
  const error =
    loads.length > 0 && failed.length === loads.length ? failed[0].error ?? "Couldn’t load sessions." : undefined;
  const warning =
    failed.length > 0 && failed.length < loads.length
      ? `Some projects couldn’t be loaded: ${failed.map((l) => l.project || "(default)").join(", ")}.`
      : undefined;

  if (scope !== null) {
    const load = loads.find((l) => l.project === scope);
    if (!load) return { rows: [], hasMore: false, warning, error };
    const seen = new Set(load.sessions.map((s) => s.sessionId));
    const all = [...load.sessions, ...load.pinned.filter((s) => !seen.has(s.sessionId))];
    return {
      rows: sortedByRecency(all).map((session) => ({ session, project: load.project })),
      hasMore: load.nextCursor !== "",
      warning,
      error: load.error && load.sessions.length === 0 ? load.error : undefined,
    };
  }

  const frontiers = loads
    .filter((l) => l.nextCursor !== "" && l.sessions.length > 0)
    .map((l) => sortedByRecency(l.sessions)[l.sessions.length - 1]);
  const frontier = sortedByRecency(frontiers)[0];
  const seen = new Set<string>();
  const merged: FeedRow[] = [];
  for (const load of loads) {
    for (const session of load.sessions) {
      if (frontier && precedes(frontier, session)) continue;
      if (seen.has(session.sessionId)) continue;
      seen.add(session.sessionId);
      merged.push({ session, project: load.project });
    }
    for (const session of load.pinned) {
      if (seen.has(session.sessionId)) continue;
      seen.add(session.sessionId);
      merged.push({ session, project: load.project });
    }
  }
  const order = new Map(sortedByRecency(merged.map((r) => r.session)).map((s, i) => [s.sessionId, i]));
  merged.sort((a, b) => order.get(a.session.sessionId)! - order.get(b.session.sessionId)!);
  return { rows: merged, hasMore: loads.some((l) => l.nextCursor !== ""), warning, error };
}

export function needsAnswer(s: SessionSummary): boolean {
  return s.waitingInput;
}

/** Needs-answer rows pinned in their own section, the rest most-recent first. */
export function sections(rows: FeedRow[]): FeedSection[] {
  const pinned = rows.filter((r) => needsAnswer(r.session));
  const rest = rows.filter((r) => !needsAnswer(r.session));
  const out: FeedSection[] = [];
  if (pinned.length) out.push({ kind: "needsAnswer", title: "Needs answer", rows: pinned });
  if (rest.length) out.push({ kind: "all", title: pinned.length ? "Recent" : null, rows: rest });
  return out;
}

function ms(value: string): number {
  if (!value) return Number.NEGATIVE_INFINITY;
  const t = Date.parse(value);
  return Number.isNaN(t) ? Number.NEGATIVE_INFINITY : t;
}

export function recencyMs(s: SessionSummary): number {
  const a = ms(s.lastActivity);
  return a === Number.NEGATIVE_INFINITY ? ms(s.startedAt) : a;
}

/** The daemon's keyset order: last activity desc, start desc, id asc. */
export function precedes(a: SessionSummary, b: SessionSummary): boolean {
  const ra = recencyMs(a);
  const rb = recencyMs(b);
  if (ra !== rb) return ra > rb;
  const sa = ms(a.startedAt);
  const sb = ms(b.startedAt);
  if (sa !== sb) return sa > sb;
  return a.sessionId < b.sessionId;
}

export function sortedByRecency(sessions: SessionSummary[]): SessionSummary[] {
  return sessions.slice().sort((a, b) => (precedes(a, b) ? -1 : precedes(b, a) ? 1 : 0));
}

export function taskIds(s: SessionSummary): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw of s.focusTasks) {
    const id = raw.trim();
    if (!id || seen.has(id)) continue;
    seen.add(id);
    out.push(id);
  }
  return out;
}

export function taskChipLabels(s: SessionSummary): string[] {
  const ids = taskIds(s);
  return ids.length > 2 ? [...ids.slice(0, 2), `+${ids.length - 2}`] : ids;
}

function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/** Row title without leading task boilerplate; falls back to mode + short id. */
export function displayTitle(s: SessionSummary): string {
  let title = s.title.trim();
  const ids = taskIds(s);
  const idSet = new Set(ids);
  if (title.startsWith("[")) {
    const close = title.indexOf("]");
    if (close > 0) {
      const inside = title.slice(1, close).split(",").map((x) => x.trim());
      if (inside.length && inside.every((x) => x && idSet.has(x))) title = title.slice(close + 1).trim();
    }
  }
  for (const id of ids) {
    const e = escapeRegExp(id);
    for (const re of [new RegExp(`^work\\s+on\\s+task\\s+${e}\\s*[:\\-–—]\\s*`, "i"), new RegExp(`^${e}\\s*[:\\-–—]\\s*`)]) {
      if (re.test(title)) {
        title = title.replace(re, "").trim();
        break;
      }
    }
  }
  if (title) return title;
  const mode = s.mode.trim() || "session";
  const short = s.sessionId.slice(0, 8);
  return short ? `${mode} · ${short}` : mode;
}

/** User-facing state, independent of whether the daemon still holds the session. */
export function lifecycleLabel(s: SessionSummary): string | null {
  if (s.waitingInput) return "Needs your answer";
  if (s.live && s.awaitingJobs) return "Waiting on background jobs";
  switch (s.status.toLowerCase()) {
    case "running":
      return "Working";
    case "paused":
      return "Paused";
    case "error":
      return "Error";
    case "stopped":
      return "Stopped";
    default:
      return s.live ? "Ready for your message" : null;
  }
}

export function lifecycleTone(s: SessionSummary): string {
  if (s.waitingInput) return "waiting";
  if (s.live && s.awaitingJobs) return "background";
  return ["running", "paused", "error", "stopped"].includes(s.status.toLowerCase()) ? s.status.toLowerCase() : "idle";
}

export function compactTokenCount(tokens: number): string | null {
  if (!(tokens > 0)) return null;
  const dec = (v: number) => {
    if (v >= 100 || Math.round(v) === v) return v.toFixed(0);
    const r = v.toFixed(1);
    return r.endsWith(".0") ? r.slice(0, -2) : r;
  };
  if (tokens < 1_000) return String(tokens);
  if (tokens < 999_500) return dec(tokens / 1_000) + "K";
  if (tokens < 999_500_000) return dec(tokens / 1_000_000) + "M";
  return dec(tokens / 1_000_000_000) + "B";
}

export function modelSummary(s: SessionSummary): string | null {
  const models = s.modelUsage
    .filter((m) => m.model.trim() && m.tokens > 0n)
    .sort((a, b) => (a.tokens !== b.tokens ? (b.tokens > a.tokens ? 1 : -1) : a.model < b.model ? -1 : 1));
  if (!models.length) return null;
  const name = models[0].model.trim();
  return models.length === 1 ? name : `${name} +${models.length - 1}`;
}

export function metadataItems(s: SessionSummary): string[] {
  const items: string[] = [];
  if (s.mode.trim()) items.push(s.mode.trim());
  const model = modelSummary(s);
  if (model) items.push(model);
  const ctx = compactTokenCount(Number(s.contextTokens));
  if (ctx) items.push(`${ctx} ctx`);
  const turns = Number(s.turns);
  items.push(`${turns} ${turns === 1 ? "turn" : "turns"}`);
  return items;
}

/** Display name for a row's project, falling back to the workspace folder. */
export function displayProject(row: FeedRow): string {
  if (row.project) return row.project;
  const ws = row.session.workspace.trim().replace(/\/+$/, "");
  const base = ws.slice(ws.lastIndexOf("/") + 1);
  return base;
}

/** Compact relative time ("now", "5m", "3h", "2d", or a date). */
export function relativeTime(iso: string, now: number = Date.now()): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "";
  const s = Math.max(0, Math.round((now - t) / 1000));
  if (s < 45) return "now";
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.round(h / 24);
  if (d < 14) return `${d}d ago`;
  return new Date(t).toLocaleDateString();
}
